package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"aura.local/core/internal/engine"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/odi"
	"aura.local/core/internal/policy"
)

// toolManifests lists the manifest of every registered tool (ODI A5, ADR-0074).
func (s *Server) toolManifests(w http.ResponseWriter, r *http.Request) {
	ms := odi.Manifests()
	gaps := 0
	for _, m := range ms {
		if len(m.Validate()) > 0 {
			gaps++
		}
	}
	writeJSON(w, 200, map[string]any{"tools": ms, "count": len(ms), "manifest_gaps": gaps})
}

func (s *Server) getToolQuotas(w http.ResponseWriter, r *http.Request) {
	q, err := engine.LoadToolQuotas(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"quotas": q})
}

// putToolQuotas replaces the per-tool daily quotas (tool id -> calls per day; 0 removes it). Tools must be registered; the change is audited.
func (s *Server) putToolQuotas(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Quotas map[string]int `json:"quotas"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	clean := map[string]int{}
	for id, n := range in.Quotas {
		if _, ok := policy.Lookup(id); !ok || n < 0 || n > 100000 {
			s.fail(w, r, 400, "invalid_input", "quotas: unknown tool or calls per day outside 0..100000: "+id)
			return
		}
		if n > 0 {
			clean[id] = n
		}
	}
	b, _ := json.Marshal(clean)
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `insert into settings(key, value, updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`,
			engine.ToolQuotaSetting, string(b), ids.NowMs()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "TOOL_QUOTAS_CHANGED", "owner", "", requestID(r), map[string]any{"quotas": clean})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"quotas": clean})
}
