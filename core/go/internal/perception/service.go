package perception

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
)

// Reasoner asks NEXUS (and only NEXUS: perception of the owner's surroundings is personal data, never sent to an external model) to answer from the evidence.
type Reasoner func(ctx context.Context, system, user string) (answer, model string, err error)

type Request struct {
	Modality   string
	Question   string
	ConsentRef string
	Locale     []string
	Media      []byte
	By         string
}

var ErrInvalid = errors.New("invalid perception request")

const MaxMedia = 12 << 20

// Validate refuses what the path cannot honestly serve. Pure.
func Validate(q Request) error {
	switch {
	case q.Modality != "audio" && q.Modality != "image":
		return fmt.Errorf("%w: modality must be audio or image", ErrInvalid)
	case len(strings.TrimSpace(q.Question)) < 2 || len([]rune(q.Question)) > 500:
		return fmt.Errorf("%w: question must be 2..500 characters", ErrInvalid)
	case len(q.ConsentRef) < 3 || len(q.ConsentRef) > 120:
		return fmt.Errorf("%w: a consent reference is required", ErrInvalid)
	case len(q.Media) == 0 || len(q.Media) > MaxMedia:
		return fmt.Errorf("%w: media must be 1 byte..12 MiB", ErrInvalid)
	}
	return nil
}

type Service struct {
	St     *store.Store
	AMPI   *AMPI
	Reason Reasoner
	// Warm, when set, asks NEXUS to read the fixed system prompt while AMPI is still perceiving (pipelining on a CPU: the prompt prefix is then cached when the evidence
	// arrives, so NEXUS reads only the evidence and the question). Its failure changes nothing.
	Warm  func(ctx context.Context, system string)
	Poll  time.Duration // AMPI job polling interval (default 150 ms)
	Limit time.Duration // whole-request limit (default 6 min)
	Log   func(msg string, kv ...any)
	wg    sync.WaitGroup
}

// Start records the request and runs it in the background; the caller polls Get. Returns the request id.
func (s *Service) Start(ctx context.Context, q Request) (string, error) {
	if err := Validate(q); err != nil {
		return "", err
	}
	id := "per-" + ids.New()
	now := time.Now().UnixMilli()
	err := s.St.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into perception_requests(id, modality, question, consent_ref, requested_by, state, step, media_bytes, created_at, updated_at)
			values(?,?,?,?,?, 'received', 'queued', ?, ?, ?)`, id, q.Modality, q.Question, q.ConsentRef, q.By, len(q.Media), now, now); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "PERCEPTION_REQUESTED", q.By, "", "", map[string]any{"request": id, "modality": q.Modality, "media_bytes": len(q.Media)}) // never the question or the media
	})
	if err != nil {
		return "", err
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(id, q) }()
	return id, nil
}

// Wait is for tests and shutdown: it returns when every started request has finished.
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) set(id string, cols map[string]any) {
	var sets []string
	var args []any
	for k, v := range cols {
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	args = append(args, time.Now().UnixMilli(), id)
	s.St.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("update perception_requests set "+strings.Join(sets, ", ")+", updated_at=? where id=?", args...)
		return err
	})
}

func (s *Service) run(id string, q Request) {
	limit := s.Limit
	if limit <= 0 {
		limit = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	t := map[string]int64{}
	start := time.Now()
	lap := func(k string, from time.Time) { t[k] = time.Since(from).Milliseconds() }
	timings := func() string {
		t["total_ms"] = time.Since(start).Milliseconds()
		b, _ := json.Marshal(t)
		return string(b)
	}
	fail := func(step string, err error) {
		s.set(id, map[string]any{"state": "failed", "step": step, "error": fmt.Sprintf("%.400s", err.Error()), "timings_json": timings()})
		if s.Log != nil {
			s.Log("perception failed", "request", id, "step", step, "err", err)
		}
	}

	s.set(id, map[string]any{"state": "perceiving", "step": "ampi_session"})
	if s.Warm != nil {
		sys, _ := Evidence{}.Messages("")
		go s.Warm(ctx, sys)
	}
	t0 := time.Now()
	sid, err := s.AMPI.Session(ctx, q.Modality, q.ConsentRef, q.Locale)
	if err != nil {
		fail("ampi_session", err)
		return
	}
	lap("ampi_session_ms", t0)
	defer func() {
		cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		s.AMPI.Close(cctx, sid)
		c()
	}()
	t0 = time.Now()
	media, err := s.AMPI.Upload(ctx, sid, q.Media)
	if err != nil {
		fail("ampi_upload", err)
		return
	}
	lap("ampi_upload_ms", t0)
	t0 = time.Now()
	job, err := s.AMPI.Job(ctx, sid, media, q.Modality)
	if err != nil {
		fail("ampi_job", err)
		return
	}
	s.set(id, map[string]any{"ampi_session": sid, "ampi_job": job, "step": "ampi_processing"})
	poll := s.Poll
	if poll <= 0 {
		poll = 150 * time.Millisecond
	}
	status := ""
	for {
		st, err := s.AMPI.JobStatus(ctx, job)
		if err != nil {
			fail("ampi_processing", err)
			return
		}
		if terminalJob(st) {
			status = st
			break
		}
		select {
		case <-ctx.Done():
			fail("ampi_processing", fmt.Errorf("AMPI did not finish within the request limit (last status %s)", st))
			return
		case <-time.After(poll):
		}
	}
	lap("ampi_processing_ms", t0)
	if status != "SUCCEEDED" && status != "PARTIAL" {
		s.set(id, map[string]any{"ampi_status": status})
		fail("ampi_processing", fmt.Errorf("AMPI job ended %s", status))
		return
	}
	t0 = time.Now()
	obs, err := s.AMPI.Observations(ctx, sid)
	if err != nil {
		fail("ampi_observations", err)
		return
	}
	ev := BuildEvidence(obs)
	evj, _ := json.Marshal(ev)
	lap("evidence_ms", t0)
	s.set(id, map[string]any{"state": "reasoning", "step": "nexus", "ampi_status": status, "evidence_json": string(evj)})
	t0 = time.Now()
	sys, user := ev.Messages(q.Question)
	answer, model, err := s.Reason(ctx, sys, user)
	if err != nil {
		lap("nexus_ms", t0)
		fail("nexus", err)
		return
	}
	lap("nexus_ms", t0)
	s.set(id, map[string]any{"state": "answered", "step": "done", "answer": answer, "model": model, "timings_json": timings()})
}

func terminalJob(s string) bool {
	switch s {
	case "SUCCEEDED", "PARTIAL", "FAILED", "CANCELLED", "EXPIRED", "DEAD_LETTERED":
		return true
	}
	return false
}

// Get returns the request as the owner may see it.
func (s *Service) Get(ctx context.Context, id string) (map[string]any, error) {
	var modality, question, state string
	var step, sess, job, ampiSt, evj, answer, model, errS sql.NullString
	var tj string
	var created, updated, mb int64
	err := s.St.R.QueryRowContext(ctx, `select modality, question, state, step, ampi_session, ampi_job, ampi_status, evidence_json, answer, model, timings_json, error, media_bytes, created_at, updated_at
		from perception_requests where id=?`, id).Scan(&modality, &question, &state, &step, &sess, &job, &ampiSt, &evj, &answer, &model, &tj, &errS, &mb, &created, &updated)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": id, "modality": modality, "question": question, "state": state, "step": step.String, "ampi": map[string]any{"session": sess.String, "job": job.String, "status": ampiSt.String},
		"answer": answer.String, "model": model.String, "error": errS.String, "media_bytes": mb, "created_at": created, "updated_at": updated,
		"authority": "AMPI observed; NEXUS reasoned over the evidence only; AURA authorized and recorded. Nothing was executed."}
	var t map[string]any
	json.Unmarshal([]byte(tj), &t)
	out["timings_ms"] = t
	if evj.Valid {
		var e any
		json.Unmarshal([]byte(evj.String), &e)
		out["evidence"] = e
	}
	return out, nil
}
