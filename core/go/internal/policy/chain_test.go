package policy

import (
	"context"
	"database/sql"
	"testing"
)

func audit(t *testing.T, s interface {
	Write(context.Context, func(*sql.Tx) error) error
}, typ string, data map[string]any) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *sql.Tx) error { return Audit(context.Background(), tx, typ, "core", "", "", data) }); err != nil {
		t.Fatal(err)
	}
}

func TestAuditChainVerifiesAndDetectsTampering(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	st, err := VerifyAuditChain(ctx, s.R)
	if err != nil || !st.Valid || st.Chained != 0 || st.Head != auditGenesis {
		t.Fatalf("empty chain: %+v %v", st, err)
	}
	for i := 0; i < 5; i++ {
		audit(t, s, "TEST_EVENT", map[string]any{"i": i})
	}
	st, _ = VerifyAuditChain(ctx, s.R)
	if !st.Valid || st.Chained != 5 || st.Head == auditGenesis {
		t.Fatalf("intact chain: %+v", st)
	}
	head := st.Head

	// The append-only triggers refuse an edit or a delete through the normal path...
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "update audit_events set data_json='{}' where type='TEST_EVENT'")
		return err
	}); err == nil {
		t.Fatal("audit row edited through the append-only trigger")
	}
	// ...and a privileged actor who drops the trigger is caught by the chain.
	tamper := func(stmts ...string) {
		t.Helper()
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			for _, q := range stmts {
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	tamper("drop trigger audit_no_update", "update audit_events set data_json='{\"i\":99}' where rowid=(select rowid from audit_events order by rowid limit 1 offset 2)")
	st, _ = VerifyAuditChain(ctx, s.R)
	if st.Valid || st.BrokenWhy == "" {
		t.Fatalf("edited row not detected: %+v", st)
	}
	tamper("update audit_events set data_json='{\"i\":2}' where rowid=(select rowid from audit_events order by rowid limit 1 offset 2)")
	if st, _ = VerifyAuditChain(ctx, s.R); !st.Valid || st.Head != head {
		t.Fatalf("restored chain should verify again: %+v", st)
	}

	tamper("drop trigger audit_no_delete", "delete from audit_events where rowid=(select rowid from audit_events order by rowid limit 1 offset 1)")
	if st, _ = VerifyAuditChain(ctx, s.R); st.Valid {
		t.Fatalf("a row deleted inside the chain was not detected: %+v", st)
	}
}

func TestAuditChainLegacyRowsAreCountedNotTrusted(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	// A row written before the chain existed (hash NULL), then chained rows.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into audit_events(id, type, actor, data_json, at) values('old1','OLD','core','{}',1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	audit(t, s, "NEW", map[string]any{})
	audit(t, s, "NEW", map[string]any{})
	st, _ := VerifyAuditChain(ctx, s.R)
	if !st.Valid || st.Legacy != 1 || st.Chained != 2 {
		t.Fatalf("%+v", st)
	}
	// An unchained row after the chain started is a break.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into audit_events(id, type, actor, data_json, at) values('sneak','X','core','{}',2)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if st, _ = VerifyAuditChain(ctx, s.R); st.Valid || st.BrokenAt != "sneak" {
		t.Fatalf("%+v", st)
	}
}

func TestChainHashFieldsCannotBeShifted(t *testing.T) {
	a := ChainHash("p", "id", "ab", "c", "", "", "d", 1)
	b := ChainHash("p", "id", "a", "bc", "", "", "d", 1)
	if a == b {
		t.Fatal("moving a character between fields must change the hash")
	}
}
