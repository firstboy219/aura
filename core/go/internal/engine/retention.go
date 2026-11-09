package engine

import (
	"context"
	"database/sql"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
)

// PurgeContent deletes expired content blobs (checkpoints) of finished tasks
// (ADR-0008). Events and audit records are kept; each task gets a
// CONTENT_PURGED event. Workspace output files are the owner's results and are
// never purged here.
func (e *Engine) PurgeContent(ctx context.Context) (int, error) {
	purged := 0
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `select c.task_id, count(*) from checkpoints c join tasks t on t.id = c.task_id
			where c.purge_after is not null and c.purge_after < ?
			and t.state in ('SUCCEEDED','FAILED','CANCELLED','BUDGET_EXCEEDED') group by c.task_id`, ids.NowMs())
		if err != nil {
			return err
		}
		counts := map[string]int{}
		for rows.Next() {
			var id string
			var n int
			rows.Scan(&id, &n)
			counts[id] = n
		}
		rows.Close()
		for id, n := range counts {
			if _, err := tx.ExecContext(ctx, "delete from checkpoints where task_id=? and purge_after < ?", id, ids.NowMs()); err != nil {
				return err
			}
			if _, err := task.AppendInternalEvent(ctx, tx, id, nil, "CONTENT_PURGED", "core", map[string]any{"checkpoints": n}); err != nil {
				return err
			}
			purged += n
		}
		return nil
	})
	return purged, err
}
