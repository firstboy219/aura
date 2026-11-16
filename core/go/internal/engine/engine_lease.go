package engine

import (
	"context"

	"aura.local/core/internal/task"
)

// PauseForLease pauses a task for a device takeover lease and says whether it did (a task that was already paused or finished is left alone, so handback does
// not resume something the owner paused for another reason).
func (e *Engine) PauseForLease(ctx context.Context, taskID string) (bool, error) {
	t, err := task.Get(ctx, e.st.R, taskID)
	if err != nil {
		return false, err
	}
	if t.State.Terminal() || t.State == task.Paused {
		return false, nil
	}
	if _, err := e.Pause(ctx, taskID); err != nil {
		return false, err
	}
	return true, nil
}

// ResumeAfterLease resumes a task that the lease paused, after a fresh observation was handed in.
func (e *Engine) ResumeAfterLease(ctx context.Context, taskID string) error {
	_, err := e.Resume(ctx, taskID)
	return err
}
