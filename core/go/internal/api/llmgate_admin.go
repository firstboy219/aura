package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"aura.local/core/internal/llmgate"
)

// getLLMGate shows JARVIS's monthly AI budget and this month's spend through the model gateway (ADR-0084).
func (s *Server) getLLMGate(w http.ResponseWriter, r *http.Request) {
	b := llmgate.LoadBudget(r.Context(), s.Store.R)
	var spent float64
	var calls int
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if s.Loc != nil {
		n := now.In(s.Loc)
		from = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, s.Loc)
	}
	s.Store.R.QueryRowContext(r.Context(), `select coalesce(sum(cost_actual_usd),0), count(*) from usage_records where tier='jarvis' and created_at>=?`,
		from.UnixMilli()).Scan(&spent, &calls)
	writeJSON(w, 200, map[string]any{"budget": b, "month_start": from.Format(time.RFC3339), "spent_usd": spent, "calls": calls})
}

// putLLMGate sets the cap (the owner; 0 = no cap). The change is audited.
func (s *Server) putLLMGate(w http.ResponseWriter, r *http.Request) {
	var b llmgate.Budget
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		s.fail(w, r, 400, "invalid_input", "body must be {\"monthly_usd\": number}")
		return
	}
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error { return llmgate.SaveBudget(r.Context(), tx, b, time.Now()) })
	if err != nil {
		s.fail(w, r, 400, "invalid_input", err.Error())
		return
	}
	s.audit(r, "LLMGATE_BUDGET_SET", map[string]any{"monthly_usd": b.MonthlyUSD})
	s.getLLMGate(w, r)
}
