package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"aura.local/core/internal/store"
)

// SettingKey is the settings row that holds the owner's NEXUS policy (one JSON document, versioned by the audit trail).
const SettingKey = "odi.nexus.policy"

// Mode says who serves cognition.
const (
	ModeOff     = "off"     // legacy providers only (what AURA does today)
	ModeShadow  = "shadow"  // legacy serves; NEXUS runs beside it and is only measured
	ModePrimary = "primary" // NEXUS serves; an external provider only under a FallbackAuthorization
)

// Policy is the owner's decision set. Defaults deny: no provider is approved for fallback or for the Teacher Gate until the owner names one.
type Policy struct {
	Mode               string   `json:"mode"`
	BaseURL            string   `json:"base_url"`
	OwnerDisabledUntil int64    `json:"owner_disabled_until_ms"` // Unix ms; the owner switched NEXUS off until then
	FallbackProviders  []string `json:"fallback_providers"`
	FallbackDataClass  []string `json:"fallback_data_classes"`
	FallbackBudgetUSD  float64  `json:"fallback_budget_usd"`
	FallbackMaxCalls   int      `json:"fallback_max_calls"`
	FallbackTTLMinutes int      `json:"fallback_ttl_minutes"`
	TeacherEnabled     bool     `json:"teacher_enabled"`
	TeacherProviders   []string `json:"teacher_providers"`
	TeacherTier        string   `json:"teacher_tier"`
	TeacherBudgetUSD   float64  `json:"teacher_budget_usd"`
	TeacherMaxPerDay   int      `json:"teacher_max_per_day"`
}

func DefaultPolicy() Policy {
	return Policy{Mode: ModeOff, FallbackDataClass: []string{"public"}, FallbackBudgetUSD: 0.25, FallbackMaxCalls: 20, FallbackTTLMinutes: 10,
		TeacherTier: "standard", TeacherBudgetUSD: 0.05, TeacherMaxPerDay: 20}
}

func (p Policy) Validate() error {
	switch p.Mode {
	case ModeOff, ModeShadow, ModePrimary:
	default:
		return fmt.Errorf("mode must be off, shadow or primary")
	}
	if p.Mode != ModeOff && p.BaseURL == "" {
		return errors.New("base_url is required unless mode is off")
	}
	if p.FallbackBudgetUSD < 0 || p.FallbackBudgetUSD > 50 || p.FallbackMaxCalls < 0 || p.FallbackMaxCalls > 1000 || p.FallbackTTLMinutes < 1 || p.FallbackTTLMinutes > 1440 {
		return errors.New("fallback limits out of range")
	}
	if p.TeacherBudgetUSD < 0 || p.TeacherBudgetUSD > 5 || p.TeacherMaxPerDay < 0 || p.TeacherMaxPerDay > 500 {
		return errors.New("teacher limits out of range")
	}
	for _, c := range p.FallbackDataClass {
		if !slices.Contains([]string{"public", "internal", "confidential", "restricted"}, c) {
			return fmt.Errorf("unknown data class %q", c)
		}
	}
	return nil
}

// LoadPolicy reads the policy; a missing row is the default (mode off).
func LoadPolicy(ctx context.Context, q store.Querier) (Policy, error) {
	p := DefaultPolicy()
	var v string
	err := q.QueryRowContext(ctx, "select value from settings where key=?", SettingKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		return DefaultPolicy(), fmt.Errorf("unreadable NEXUS policy: %w", err)
	}
	return p, nil
}

// SavePolicy writes the policy inside the caller's transaction (so the audit row lands with it).
func SavePolicy(ctx context.Context, tx *sql.Tx, p Policy, now time.Time) error {
	b, _ := json.Marshal(p)
	_, err := tx.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?,?,?)
		on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, SettingKey, string(b), now.UnixMilli())
	return err
}
