package gateway

import (
	"context"
	"database/sql"
	"fmt"

	"aura.local/core/internal/config"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/store"
)

// RouteCheck is the verification result of one configured route.
type RouteCheck struct {
	Tier, RouteID string
	Health        string // ok | missing | no_structured_output | price_changed | unknown
	Detail        string
}

// VerifyRoutes compares configured routes with the provider catalog. A nil
// catalog (provider unreachable) yields health "unknown" rather than a guess.
func VerifyRoutes(models map[string][]config.ModelRoute, catalog map[string]ModelInfo) []RouteCheck {
	var out []RouteCheck
	for _, tier := range config.Tiers {
		for _, r := range models[tier] {
			c := RouteCheck{Tier: tier, RouteID: r.RouteID, Health: "ok"}
			mi, ok := catalog[r.RouteID]
			switch {
			case catalog == nil:
				c.Health, c.Detail = "unknown", "catalog unavailable"
			case !ok:
				c.Health, c.Detail = "missing", "route not in provider catalog"
			case !mi.StructuredOutput:
				c.Health, c.Detail = "no_structured_output", "route does not advertise structured_outputs"
			case mi.PriceInPerMTok > r.PriceInPerMTok*1.0001 || mi.PriceOutPerMTok > r.PriceOutPerMTok*1.0001:
				c.Health = "price_changed"
				c.Detail = fmt.Sprintf("catalog price %.4g/%.4g per Mtok exceeds configured %.4g/%.4g",
					mi.PriceInPerMTok, mi.PriceOutPerMTok, r.PriceInPerMTok, r.PriceOutPerMTok)
			}
			out = append(out, c)
		}
	}
	return out
}

// SyncRegistry records the provider and tier routes with their verified
// health in providers/model_tiers. keyRef is the key file path, never the key.
func SyncRegistry(ctx context.Context, st *store.Store, baseURL, keyRef string, models map[string][]config.ModelRoute, checks []RouteCheck) error {
	health := map[string]string{}
	for _, c := range checks {
		health[c.Tier+"|"+c.RouteID] = c.Health
	}
	now := ids.NowMs()
	return st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into providers(id, kind, base_url, key_ref, health, updated_at) values('openrouter','openrouter',?,?,?,?)
			on conflict(id) do update set base_url=excluded.base_url, key_ref=excluded.key_ref, health=excluded.health, updated_at=excluded.updated_at`,
			baseURL, keyRef, "configured", now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "delete from model_tiers"); err != nil {
			return err
		}
		for tier, routes := range models {
			for i, r := range routes {
				h := health[tier+"|"+r.RouteID]
				if h == "" {
					h = "unknown"
				}
				var verified any
				if h == "ok" {
					verified = now
				}
				if _, err := tx.ExecContext(ctx, `insert into model_tiers(tier, priority, provider_id, display_name, api_model_id, route_id,
					structured_output, price_in_per_mtok, price_out_per_mtok, reasoning_effort, health, verified_at)
					values(?,?,?,?,?,?,?,?,?,?,?,?)`, tier, i, "openrouter", r.DisplayName, r.APIModelID, r.RouteID,
					boolInt(h == "ok"), r.PriceInPerMTok, r.PriceOutPerMTok, r.ReasoningEffort, h, verified); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
