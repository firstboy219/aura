package policy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"

	"aura.local/core/internal/store"
)

// auditGenesis is the prev_hash of the first chained audit row.
var auditGenesis = hex.EncodeToString(sha256Sum([]byte("aura-audit-genesis-v1")))

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

// ChainHash is the hash of one audit row (ODI Blueprint v1.1 §20: tamper-evidence). The fields are length-prefixed so no value can
// be moved into its neighbour without changing the hash.
func ChainHash(prev, id, typ, actor, taskID, requestID, data string, at int64) string {
	h := sha256.New()
	for _, f := range []string{prev, id, typ, actor, taskID, requestID, data, strconv.FormatInt(at, 10)} {
		h.Write([]byte(strconv.Itoa(len(f))))
		h.Write([]byte{':'})
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// lastChainHash is the hash of the newest chained row, or the genesis value when the chain has not started.
func lastChainHash(ctx context.Context, q store.Querier) (string, error) {
	var h sql.NullString
	err := q.QueryRowContext(ctx, "select hash from audit_events where hash is not null order by rowid desc limit 1").Scan(&h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !h.Valid) {
		return auditGenesis, nil
	}
	return h.String, err
}

// ChainStatus is the result of VerifyAuditChain.
type ChainStatus struct {
	Valid     bool   `json:"valid"`
	Chained   int    `json:"chained"`          // rows covered by the chain
	Legacy    int    `json:"legacy_unchained"` // rows from before the chain started (not verifiable)
	Head      string `json:"head"`             // hash of the newest chained row: anchor it outside this database
	BrokenAt  string `json:"broken_at,omitempty"`
	BrokenWhy string `json:"broken_why,omitempty"`
}

// VerifyAuditChain re-computes the whole chain. It detects an edited row, a deleted or inserted row inside the chain, and an unchained row
// written after the chain started. It cannot detect rows deleted from the tail: anchor Head outside the database (a periodic copy of ChainStatus.Head).
func VerifyAuditChain(ctx context.Context, q store.Querier) (ChainStatus, error) {
	st := ChainStatus{Valid: true, Head: auditGenesis}
	rows, err := q.QueryContext(ctx, `select id, type, actor, coalesce(task_id,''), coalesce(request_id,''), data_json, at, prev_hash, hash from audit_events order by rowid`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	prev, started := auditGenesis, false
	for rows.Next() {
		var id, typ, actor, taskID, reqID, data string
		var at int64
		var ph, h sql.NullString
		if err := rows.Scan(&id, &typ, &actor, &taskID, &reqID, &data, &at, &ph, &h); err != nil {
			return st, err
		}
		if !h.Valid {
			if started {
				st.Valid, st.BrokenAt, st.BrokenWhy = false, id, "unchained row after the chain started"
				return st, nil
			}
			st.Legacy++
			continue
		}
		started = true
		if !ph.Valid || ph.String != prev {
			st.Valid, st.BrokenAt, st.BrokenWhy = false, id, "prev_hash does not match the previous row (a row was removed, inserted or reordered)"
			return st, nil
		}
		if want := ChainHash(prev, id, typ, actor, taskID, reqID, data, at); want != h.String {
			st.Valid, st.BrokenAt, st.BrokenWhy = false, id, "row content does not match its hash (edited)"
			return st, nil
		}
		prev = h.String
		st.Chained++
	}
	st.Head = prev
	return st, rows.Err()
}
