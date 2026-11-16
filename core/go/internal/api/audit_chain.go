package api

import (
	"net/http"

	"aura.local/core/internal/policy"
)

// auditVerify re-computes the audit hash chain (ODI A3, ADR-0072). "head" is the value to anchor outside this database: a row deleted from the tail of
// the log cannot be seen by the chain itself.
func (s *Server) auditVerify(w http.ResponseWriter, r *http.Request) {
	st, err := policy.VerifyAuditChain(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	code := 200
	if !st.Valid {
		code = 409
	}
	writeJSON(w, code, st)
}
