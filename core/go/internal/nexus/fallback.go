package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
)

// Trigger codes: the ONLY two valid reasons for an external provider to serve cognition (Blueprint §7).
const (
	TriggerNexusUnavailable = "NEXUS_UNAVAILABLE"
	TriggerOwnerDisabled    = "OWNER_DISABLED"
)

// Authorization is one FallbackAuthorization: scoped by task, purpose, data classes, providers, budget, call count and expiry; it carries the evidence.
type Authorization struct {
	ID          string
	TaskID      string
	Purpose     string
	Trigger     string
	EvidenceRef string
	Providers   []string
	DataClasses []string
	BudgetUSD   float64
	MaxCalls    int
	UsedCalls   int
	ExpiresAt   int64
	AuditRef    string
}

// Authorizer is AURA's policy decision for fallback. NEXUS can report evidence but can never authorize its own replacement; if the policy cannot be
// evaluated the answer is "denied".
type Authorizer struct {
	St  *store.Store
	Mon *Monitor
	Now func() time.Time
}

func (a *Authorizer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authorizer) deny(ctx context.Context, err error, why map[string]any) error {
	why["denied_as"] = err.Error()
	a.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "FALLBACK_DENIED", "core", asString(why["task_id"]), "", why)
	})
	return err
}

func asString(v any) string { s, _ := v.(string); return s }

// Authorize issues (or reuses) a scoped authorization for one task and purpose, or denies with a stable error.
func (a *Authorizer) Authorize(ctx context.Context, taskID, purpose, dataClass string) (*Authorization, error) {
	info := map[string]any{"task_id": taskID, "purpose": purpose, "data_class": dataClass}
	pol, err := LoadPolicy(ctx, a.St.R)
	if err != nil {
		info["why"] = "policy_unavailable"
		return nil, a.deny(ctx, ErrFallbackNotAuthorized, info)
	}
	now := a.now()
	trigger, evidence := "", ""
	expires := now.Add(time.Duration(pol.FallbackTTLMinutes) * time.Minute).UnixMilli()
	if pol.OwnerDisabledUntil > now.UnixMilli() {
		trigger, evidence = TriggerOwnerDisabled, fmt.Sprintf("owner-disabled-until:%d", pol.OwnerDisabledUntil)
		expires = min(expires, pol.OwnerDisabledUntil)
	} else {
		st, ev := a.Mon.State()
		info["health"], info["health_evidence"] = string(st), ev
		switch st {
		case Unavailable:
			trigger, evidence = TriggerNexusUnavailable, ev
		case Unknown:
			return nil, a.deny(ctx, ErrNexusHealthUnknown, info)
		default:
			return nil, a.deny(ctx, ErrFallbackNotAuthorized, info) // healthy or merely degraded: slow, uncertain, missing knowledge, queued: never a trigger
		}
	}
	if len(pol.FallbackProviders) == 0 {
		info["why"] = "no fallback provider approved by the owner"
		return nil, a.deny(ctx, ErrFallbackNotAuthorized, info)
	}
	if !slices.Contains(pol.FallbackDataClass, dataClass) {
		return nil, a.deny(ctx, ErrDataClassNotAllowed, info)
	}
	// Reuse an active authorization for the same task and purpose, even when its PAID allowance is used up: free routes are unlimited (owner decision 2026-11-17), so
	// only the paid use is refused, later, by Use().
	var g Authorization
	var prov, classes string
	err = a.St.R.QueryRowContext(ctx, `select id, coalesce(task_id,''), purpose, trigger_code, evidence_ref, providers_json, data_classes_json, budget_cap_usd, max_calls,
		used_calls, expires_at, coalesce(audit_ref,'') from fallback_authorizations where state='active' and expires_at>? and coalesce(task_id,'')=? and purpose=?
		order by created_at desc limit 1`, now.UnixMilli(), taskID, purpose).
		Scan(&g.ID, &g.TaskID, &g.Purpose, &g.Trigger, &g.EvidenceRef, &prov, &classes, &g.BudgetUSD, &g.MaxCalls, &g.UsedCalls, &g.ExpiresAt, &g.AuditRef)
	if err == nil {
		json.Unmarshal([]byte(prov), &g.Providers)
		json.Unmarshal([]byte(classes), &g.DataClasses)
		return &g, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, a.deny(ctx, ErrFallbackNotAuthorized, info)
	}
	// A paid allowance that was used up is not renewed by asking again: otherwise a task could chain paid grants without limit (Blueprint §7: no blanket enablement).
	// A new authorization is still issued so that free routes keep working, but it carries NO paid calls.
	var spent int
	a.St.R.QueryRowContext(ctx, `select count(*) from fallback_authorizations where coalesce(task_id,'')=? and purpose=? and used_calls>=max_calls and max_calls>0 and revoked_at is null`, taskID, purpose).Scan(&spent)
	paidCalls := pol.FallbackMaxCalls
	if spent > 0 {
		paidCalls = 0
		info["why"] = "the paid allowance for this task and purpose is used up: free routes only"
	}
	g = Authorization{ID: ids.New(), TaskID: taskID, Purpose: purpose, Trigger: trigger, EvidenceRef: evidence, Providers: pol.FallbackProviders,
		DataClasses: pol.FallbackDataClass, BudgetUSD: pol.FallbackBudgetUSD, MaxCalls: paidCalls, ExpiresAt: expires}
	pj, _ := json.Marshal(g.Providers)
	cj, _ := json.Marshal(g.DataClasses)
	err = a.St.Write(ctx, func(tx *sql.Tx) error {
		g.AuditRef = ids.New()
		if _, err := tx.ExecContext(ctx, `insert into fallback_authorizations(id, task_id, purpose, trigger_code, evidence_ref, providers_json, data_classes_json,
			budget_cap_usd, max_calls, expires_at, state, audit_ref, created_at) values(?,?,?,?,?,?,?,?,?,?,'active',?,?)`,
			g.ID, nullable(taskID), purpose, trigger, evidence, string(pj), string(cj), g.BudgetUSD, g.MaxCalls, g.ExpiresAt, g.AuditRef, now.UnixMilli()); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "FALLBACK_AUTHORIZED", "core", taskID, g.AuditRef, map[string]any{"authorization": g.ID, "purpose": purpose, "trigger": trigger,
			"evidence": evidence, "providers": g.Providers, "data_classes": g.DataClasses, "budget_usd": g.BudgetUSD, "max_calls": g.MaxCalls, "expires_at": g.ExpiresAt})
	})
	if err != nil {
		return nil, a.deny(ctx, ErrFallbackNotAuthorized, info)
	}
	return &g, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Use spends one call of an authorization. It fails closed when the authorization is revoked, expired or used up.
func (a *Authorizer) Use(ctx context.Context, id string) error {
	var n int64
	err := a.St.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `update fallback_authorizations set used_calls=used_calls+1 where id=? and state='active' and expires_at>? and used_calls<max_calls`, id, a.now().UnixMilli())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil || n != 1 {
		return ErrFallbackNotAuthorized
	}
	return nil
}

// RevokeAll revokes every active authorization (NEXUS recovered, or the owner switched NEXUS back on). Calls already running may finish; no new call
// is allowed (Blueprint §7 "Recovery").
func (a *Authorizer) RevokeAll(ctx context.Context, reason string) (int, error) {
	var n int64
	err := a.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `update fallback_authorizations set state='revoked', revoke_reason=?, revoked_at=? where state='active'`, reason, a.now().UnixMilli())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		if n == 0 {
			return nil
		}
		return policy.Audit(ctx, tx, "FALLBACK_REVOKED", "core", "", "", map[string]any{"reason": reason, "count": n})
	})
	return int(n), err
}

// Active lists active authorizations (admin view).
func (a *Authorizer) Active(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.St.R.QueryContext(ctx, `select id, coalesce(task_id,''), purpose, trigger_code, evidence_ref, providers_json, budget_cap_usd, max_calls, used_calls, expires_at
		from fallback_authorizations where state='active' and expires_at>? order by created_at desc limit 100`, a.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, task, purpose, trig, ev, prov string
		var budget float64
		var max, used int
		var exp int64
		if err := rows.Scan(&id, &task, &purpose, &trig, &ev, &prov, &budget, &max, &used, &exp); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "task_id": task, "purpose": purpose, "trigger": trig, "evidence": ev, "providers": json.RawMessage(prov),
			"budget_usd": budget, "max_calls": max, "used_calls": used, "expires_at": exp})
	}
	return out, rows.Err()
}
