package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/view"
)

// Workspace (ADR-0061): what a reply can put on the owner's workspace beyond
// text and figures — notes the Core keeps, and pictures of the people and
// things an explanation is about.

// Note is one note of the owner.
type Note struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	TaskID    string `json:"task_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const maxNotesListed = 200

func noteText(s string) (string, error) {
	s = view.CleanText(s, view.MaxNote)
	if s == "" {
		return "", fmt.Errorf("%w: text is required", ErrInvalid)
	}
	return s, nil
}

// Notes lists the owner's notes, newest first.
func (e *Engine) Notes(ctx context.Context, limit int) ([]Note, error) {
	if limit < 1 || limit > maxNotesListed {
		limit = maxNotesListed
	}
	rows, err := e.st.R.QueryContext(ctx, "select id, text, done, coalesce(task_id,''), created_at, updated_at from notes order by created_at desc, seq desc limit ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Text, &n.Done, &n.TaskID, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (e *Engine) note(ctx context.Context, id string) (*Note, error) {
	var n Note
	err := e.st.R.QueryRowContext(ctx, "select id, text, done, coalesce(task_id,''), created_at, updated_at from notes where id=?", id).
		Scan(&n.ID, &n.Text, &n.Done, &n.TaskID, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return &n, err
}

// AddNote stores a note the owner wrote.
func (e *Engine) AddNote(ctx context.Context, text string) (*Note, error) {
	text, err := noteText(text)
	if err != nil {
		return nil, err
	}
	now := ids.NowMs()
	n := &Note{ID: ids.New(), Text: text, CreatedAt: now, UpdatedAt: now}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into notes(id, text, created_at, updated_at) values(?,?,?,?)", n.ID, n.Text, now, now)
		return err
	})
	return n, err
}

// UpdateNote changes the text and/or the done mark of a note.
func (e *Engine) UpdateNote(ctx context.Context, id string, text *string, done *bool) (*Note, error) {
	n, err := e.note(ctx, id)
	if err != nil {
		return nil, err
	}
	if text != nil {
		if n.Text, err = noteText(*text); err != nil {
			return nil, err
		}
	}
	if done != nil {
		n.Done = *done
	}
	n.UpdatedAt = ids.NowMs()
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "update notes set text=?, done=?, updated_at=? where id=?", n.Text, n.Done, n.UpdatedAt, id)
		return err
	})
	return n, err
}

// DeleteNote removes a note.
func (e *Engine) DeleteNote(ctx context.Context, id string) error {
	if _, err := e.note(ctx, id); err != nil {
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "delete from notes where id=?", id)
		return err
	})
}

// saveNotes keeps the notes of a reply's note blocks, once per task: storing
// the reply again (recovery) adds nothing, and a note the owner deleted in
// between stays deleted only if the reply is not stored again.
func saveNotes(ctx context.Context, tx *sql.Tx, taskID string, blocks []view.Block) (int, error) {
	seq, now := 0, ids.NowMs()
	for _, b := range blocks {
		if b.Type != "note" {
			continue
		}
		for _, it := range b.Items {
			seq++
			if _, err := tx.ExecContext(ctx, `insert into notes(id, text, task_id, seq, created_at, updated_at) values(?,?,?,?,?,?)
				on conflict(task_id, seq) where task_id is not null do nothing`, ids.New(), it.Label, taskID, seq, now, now); err != nil {
				return 0, err
			}
		}
	}
	return seq, nil
}

// Media is a stored picture.
type Media struct {
	ID          string
	ContentType string
	Bytes       []byte
}

// GetMedia returns a stored picture by id.
func (e *Engine) GetMedia(ctx context.Context, id string) (*Media, error) {
	m := Media{ID: id}
	err := e.st.R.QueryRowContext(ctx, "select content_type, bytes from media where id=?", id).Scan(&m.ContentType, &m.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return &m, err
}

const (
	illustrateTimeout = 12 * time.Second
	lookupTTL         = 30 * 24 * time.Hour // a name is looked up again after this
)

// illustrate finds a picture for each figure of a reply. A lookup sends the
// figure's name to the picture source, so it happens only for a task whose
// grant already allows looking things up on the web; without that, or when
// nothing is found, the figure is shown without a picture. It never fails the reply.
func (e *Engine) illustrate(ctx context.Context, t *task.Task, blocks []view.Block) {
	if e.pics == nil || !(policy.Scopes{Tools: t.Scope.Tools}).HasTool("web.search") {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, illustrateTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for bi := range blocks {
		if blocks[bi].Type != "figures" {
			continue
		}
		for ii := range blocks[bi].Items {
			it := &blocks[bi].Items[ii]
			wg.Add(1)
			go func() {
				defer wg.Done()
				it.Image, it.URL = e.picture(ctx, it.Label)
			}()
		}
	}
	wg.Wait()
}

// picture resolves one name to a stored picture id and its source page.
func (e *Engine) picture(ctx context.Context, name string) (id, source string) {
	key := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if key == "" {
		return "", ""
	}
	var mid sql.NullString
	var at int64
	err := e.st.R.QueryRowContext(ctx, "select media_id, created_at from media_lookups where name=?", key).Scan(&mid, &at)
	if err == nil && time.Since(time.UnixMilli(at)) < lookupTTL {
		if !mid.Valid {
			return "", ""
		}
		e.st.R.QueryRowContext(ctx, "select source_url from media where id=?", mid.String).Scan(&source)
		return mid.String, source
	}
	p, err := e.pics.Find(ctx, name)
	if err != nil {
		e.log.Warn("picture lookup failed", "err", err)
		return "", "" // not remembered: the source may be back later
	}
	werr := e.st.Write(ctx, func(tx *sql.Tx) error {
		var ref any
		if p != nil {
			if _, err := tx.ExecContext(ctx, "insert into media(id, content_type, bytes, title, source_url, created_at) values(?,?,?,?,?,?) on conflict(id) do nothing",
				p.ID, p.ContentType, p.Bytes, p.Title, p.SourceURL, ids.NowMs()); err != nil {
				return err
			}
			ref = p.ID
		}
		_, err := tx.ExecContext(ctx, "insert into media_lookups(name, media_id, created_at) values(?,?,?) on conflict(name) do update set media_id=excluded.media_id, created_at=excluded.created_at",
			key, ref, ids.NowMs())
		return err
	})
	if werr != nil || p == nil {
		return "", ""
	}
	return p.ID, p.SourceURL
}

var _ tools.PictureFinder = (*tools.WikiPictures)(nil)
