package device

import (
	"context"
	"database/sql"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
)

// Lease is an owner takeover of a task through a device (Blueprint §12): automation on the task is paused while it is active; handing back needs a fresh
// observation, so AURA never assumes the screen or page stayed as it was.
type Lease struct {
	ID          string `json:"id"`
	DeviceID    string `json:"device_id"`
	TaskID      string `json:"task_id"`
	State       string `json:"state"`
	Paused      bool   `json:"paused"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
	ReleasedAt  *int64 `json:"released_at,omitempty"`
	Observation string `json:"observation_sha256,omitempty"`
}

// BeginLease starts a takeover. The device needs the capability that matches control (browser.control or input.*); the pause callback stops automation on
// the task and is called inside no database transaction (it may need its own).
func (s *Service) BeginLease(ctx context.Context, d *Device, taskID string, ttl time.Duration, pause func(context.Context, string) (bool, error)) (*Lease, error) {
	if !d.Has("browser.control") && !(d.Has("input.mouse") && d.Has("input.keyboard")) {
		return nil, ErrCapability
	}
	if ttl <= 0 || ttl > 4*time.Hour {
		ttl = 30 * time.Minute
	}
	now := s.now()
	l := &Lease{ID: ids.New(), DeviceID: d.ID, TaskID: taskID, State: "active", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(ttl).UnixMilli()}
	err := s.St.Write(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, "select count(*) from device_leases where task_id=? and state='active' and expires_at>?", taskID, now.UnixMilli()).Scan(&n)
		if n > 0 {
			return ErrLeaseConflict
		}
		if _, err := tx.ExecContext(ctx, "insert into device_leases(id, device_id, task_id, kind, state, created_at, expires_at) values(?,?,?, 'takeover', 'active', ?, ?)", l.ID, d.ID, taskID, l.CreatedAt, l.ExpiresAt); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "DEVICE_LEASE_BEGUN", "device", taskID, l.ID, map[string]any{"device": d.ID, "task": taskID, "expires_at": l.ExpiresAt})
	})
	if err != nil {
		return nil, err
	}
	if pause != nil {
		paused, err := pause(ctx, taskID)
		if err != nil {
			s.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, "update device_leases set state='revoked', released_at=? where id=?", now.UnixMilli(), l.ID)
				return e
			})
			return nil, err
		}
		if paused {
			l.Paused = true
			s.St.Write(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, "update device_leases set paused=1 where id=?", l.ID)
				return e
			})
		}
	}
	return l, nil
}

// ReleaseLease hands control back. A lease can only be released by its own device and only with a fresh observation (the hash of what AURA saw after the
// owner finished); without one, automation must not resume.
func (s *Service) ReleaseLease(ctx context.Context, d *Device, leaseID, observationSHA256 string) (*Lease, error) {
	if len(observationSHA256) != 64 {
		return nil, ErrNeedsObserve
	}
	var l Lease
	err := s.St.Write(ctx, func(tx *sql.Tx) error {
		var rel sql.NullInt64
		var obs sql.NullString
		var paused int
		err := tx.QueryRowContext(ctx, "select id, device_id, task_id, state, paused, created_at, expires_at, released_at, observation_sha256 from device_leases where id=?", leaseID).
			Scan(&l.ID, &l.DeviceID, &l.TaskID, &l.State, &paused, &l.CreatedAt, &l.ExpiresAt, &rel, &obs)
		l.Paused = paused == 1
		if err != nil || l.DeviceID != d.ID {
			return ErrLeaseConflict
		}
		if l.State != "active" {
			return ErrLeaseConflict
		}
		now := s.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, "update device_leases set state='released', released_at=?, observation_sha256=? where id=?", now, observationSHA256, leaseID); err != nil {
			return err
		}
		l.State, l.ReleasedAt, l.Observation = "released", &now, observationSHA256
		return policy.Audit(ctx, tx, "DEVICE_LEASE_RELEASED", "device", l.TaskID, l.ID, map[string]any{"device": d.ID, "observation": observationSHA256})
	})
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// ActiveLease reports whether an unexpired takeover lease holds the task. The engine must not run automation on such a task.
func (s *Service) ActiveLease(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := s.St.R.QueryRowContext(ctx, "select count(*) from device_leases where task_id=? and state='active' and expires_at>?", taskID, s.now().UnixMilli()).Scan(&n)
	return n > 0, err
}

// ExpireLeases marks overdue leases expired (their tasks stay paused until the owner decides).
func (s *Service) ExpireLeases(ctx context.Context) (int, error) {
	var n int64
	err := s.St.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update device_leases set state='expired', released_at=? where state='active' and expires_at<=?", s.now().UnixMilli(), s.now().UnixMilli())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}
