package nexus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
)

// Teaching (owner direction 2026-11-17, ADR-0078): when an external provider has answered a PUBLIC task, AURA keeps the question and the answer as a teaching example
// and sends it to NEXUS, which quarantines, validates and (if it passes) publishes it as a lesson of the lowest trust tier. Private tasks are never captured; an example
// that looks personal or secret is not kept; examples are bounded in size and number. This is the Teacher Gate's learning path, driven by AURA instead of by a question.

const maxExampleChars = 12000

// lessonMaxChars is what NEXUS accepts as an in-context lesson (its context is small); longer examples (whole agent prompts) are kept here as fine-tuning candidates and not sent.
const lessonMaxChars = 9000

// Capture records one external answer as a teaching example when the policy allows it. It never fails the call it is attached to.
func (r *Router) Capture(ctx context.Context, pol Policy, req gateway.Request, res *gateway.Result) {
	if !pol.TeachEnabled || pol.TeachDailyMax <= 0 {
		return
	}
	switch {
	case req.DataClass != "public":
		r.skip("not_public") // the Core marks the data class; private tasks are never captured
		return
	case res == nil || res.Content == "":
		r.skip("empty_answer")
		return
	case res.Provider == "nexus":
		r.skip("answered_by_nexus")
		return
	}
	text := ""
	for _, m := range req.Messages {
		text += m.Content + "\n"
	}
	if len(text)+len(res.Content) > maxExampleChars {
		r.skip("too_large") // could never sit in a small model's context
		return
	}
	if SanitizeForTeaching(text) != nil || SanitizeForTeaching(res.Content) != nil {
		r.skip("looks_personal_or_secret")
		return
	}
	mj, _ := json.Marshal(req.Messages)
	sum := sha256.Sum256([]byte(string(mj) + "\x00" + string(req.ResponseSchema) + "\x00" + res.Content))
	day := r.now().UTC().Truncate(24 * time.Hour).UnixMilli()
	paid := 0
	if !res.Free {
		paid = 1 // the daily cap is a limit on paid routes only; free-route answers are kept without a cap (storage is pruned below)
		var n int
		r.St.R.QueryRowContext(ctx, "select count(*) from teaching_examples where paid=1 and created_at>=?", day).Scan(&n)
		if n >= pol.TeachDailyMax {
			r.skip("daily_cap")
			return
		}
	}
	r.skip("kept")
	r.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert or ignore into teaching_examples(id, purpose, tier, messages_json, schema_json, answer, provider, route, content_sha256, tokens_est, status, created_at, paid)
			values(?,?,?,?,?,?,?,?,?,?, 'new', ?, ?)`, ids.New(), nullable(req.Purpose), nullable(req.Tier), string(mj), nullable(string(req.ResponseSchema)), res.Content, nullable(res.Provider), nullable(res.RouteID),
			hex.EncodeToString(sum[:]), (len(text)+len(res.Content))/3, r.now().UnixMilli(), paid)
		if err != nil {
			return err
		}
		// storage stays bounded without limiting free answers: beyond 20,000 rows the oldest ones that were already sent or rejected go
		_, err = tx.ExecContext(ctx, "delete from teaching_examples where status!='new' and id in (select id from teaching_examples where status!='new' order by created_at limit max(0,(select count(*) from teaching_examples)-20000))")
		return err
	})
}

// skip counts why an external answer was (not) kept; in memory since the last start, shown in the admin status so the owner can see what teaching really does.
func (r *Router) skip(reason string) {
	r.mu.Lock()
	if r.skips == nil {
		r.skips = map[string]int{}
	}
	r.skips[reason]++
	r.mu.Unlock()
}

// TeachingCounts returns the counters (reason -> count; "kept" counts accepted candidates before de-duplication).
func (r *Router) TeachingCounts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.skips {
		out[k] = v
	}
	return out
}

type lessonOut struct {
	SourceID string          `json:"source_id"`
	Purpose  string          `json:"purpose,omitempty"`
	Messages json.RawMessage `json:"messages"`
	Schema   json.RawMessage `json:"response_schema,omitempty"`
	Answer   string          `json:"answer"`
	Source   map[string]any  `json:"source"`
	Class    string          `json:"data_class"`
}

// PushLessons sends up to 20 new examples to NEXUS and records what it accepted or rejected (and why). Called by the service loop.
func (s *Service) PushLessons(ctx context.Context) (sent, accepted int, err error) {
	pol, err := LoadPolicy(ctx, s.St.R)
	if err != nil || !pol.TeachEnabled || pol.Mode == ModeOff || pol.BaseURL == "" {
		return 0, 0, err
	}
	rows, err := s.St.R.QueryContext(ctx, `select id, coalesce(purpose,''), messages_json, coalesce(schema_json,''), answer, coalesce(provider,''), coalesce(route,'') from teaching_examples where status='new' order by created_at limit 20`)
	if err != nil {
		return 0, 0, err
	}
	var batch []lessonOut
	var tooLong []string
	for rows.Next() {
		var l lessonOut
		var msgs, schema, prov, route string
		rows.Scan(&l.SourceID, &l.Purpose, &msgs, &schema, &l.Answer, &prov, &route)
		if len(msgs)+len(l.Answer) > lessonMaxChars {
			tooLong = append(tooLong, l.SourceID)
			continue
		}
		l.Messages = json.RawMessage(msgs)
		if schema != "" {
			l.Schema = json.RawMessage(schema)
		}
		l.Source, l.Class = map[string]any{"type": "external-ai", "provider": prov, "route": route}, "public"
		batch = append(batch, l)
	}
	rows.Close()
	if len(tooLong) > 0 {
		s.St.Write(ctx, func(tx *sql.Tx) error {
			for _, id := range tooLong {
				if _, err := tx.ExecContext(ctx, "update teaching_examples set status='rejected', reject_reason=?, sent_at=? where id=? and status='new'", "kept here, not sent: too long for NEXUS's context (fine-tuning candidate)", time.Now().UnixMilli(), id); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if len(batch) == 0 {
		return 0, 0, nil
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out struct {
		Results []struct {
			SourceID string `json:"source_id"`
			Accepted bool   `json:"accepted"`
			Reason   string `json:"reason"`
		} `json:"results"`
	}
	if _, err := s.Client.do(pctx, "POST", "/api/v1/knowledge/lessons", map[string]any{"lessons": batch}, &out); err != nil {
		return 0, 0, err
	}
	now := time.Now().UnixMilli()
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		for _, r := range out.Results {
			st := "sent"
			if !r.Accepted {
				st = "rejected"
			} else {
				accepted++
			}
			if _, err := tx.ExecContext(ctx, "update teaching_examples set status=?, reject_reason=?, sent_at=? where id=? and status='new'", st, nullable(r.Reason), now, r.SourceID); err != nil {
				return err
			}
		}
		return policy.Audit(ctx, tx, "NEXUS_LESSONS_PUSHED", "core", "", "", map[string]any{"sent": len(out.Results), "accepted": accepted})
	})
	return len(out.Results), accepted, err
}
