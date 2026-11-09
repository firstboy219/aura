package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "aura.db"), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteVersionAndFTS5(t *testing.T) {
	s := open(t)
	if CompareVersions(s.SQLiteVersion(), MinSQLite) < 0 {
		t.Fatalf("SQLite %s < %s", s.SQLiteVersion(), MinSQLite)
	}
	err := s.Conn(context.Background(), func(ctx context.Context, c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "create virtual table temp.f using fts5(x)")
		return err
	})
	if err != nil {
		t.Fatalf("FTS5 unavailable: %v", err)
	}
}

func TestMigrateFromEmptyAndReopen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "aura.db")
	s, err := Open(context.Background(), p, filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := s.InstanceID(context.Background())
	s.Close()
	s, err = Open(context.Background(), p, filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id2, _ := s.InstanceID(context.Background())
	if id1 == "" || id1 != id2 {
		t.Fatalf("instance id changed: %q %q", id1, id2)
	}
	var n int
	s.R.QueryRow("select count(*) from schema_migrations").Scan(&n)
	ms, _ := loadMigrations()
	if n != len(ms) {
		t.Fatalf("applied %d of %d", n, len(ms))
	}
}

func TestReadPoolIsQueryOnly(t *testing.T) {
	s := open(t)
	if _, err := s.R.Exec("insert into audit_events values('x','t','core',null,null,'{}',0)"); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestAppendOnlyTriggers(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("insert into audit_events values('a','t','core',null,null,'{}',0)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("delete from audit_events")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete allowed: %v", err)
	}
}

func TestConcurrentWritersNoBusy(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec("insert into audit_events values(?,?,?,?,?,?,?)",
					fmt.Sprint("id", i), "t", "core", nil, nil, "{}", i)
				return err
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	s.R.QueryRow("select count(*) from audit_events").Scan(&n)
	if n != 200 {
		t.Fatalf("got %d rows", n)
	}
}

func TestBackupIsVerified(t *testing.T) {
	s := open(t)
	p, err := s.Backup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(context.Background(), "TRUNCATE"); err != nil {
		t.Fatal(err)
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("3.54.1", "3.51.3") != 1 || CompareVersions("3.51.2", "3.51.3") != -1 || CompareVersions("3.51.3", "3.51.3") != 0 {
		t.Fatal("compare")
	}
}

func TestRestoreDrill(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`insert into tasks(id, objective, state, root_task_id, budget_json, config_hash, created_at, updated_at)
			values('T1','o','CREATED','T1','{}','h',0,0)`)
		return err
	})
	p, err := s.Backup(ctx, "drill")
	if err != nil {
		t.Fatal(err)
	}
	n, err := RestoreDrill(ctx, p)
	if err != nil || n != 1 {
		t.Fatalf("restored %d tasks: %v", n, err)
	}
}

// Upgrading a database created with migration 0001 keeps its rows and passes
// the foreign key check (P2 table rebuild).
func TestUpgradeFrom0001KeepsData(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "aura.db")
	ms, _ := loadMigrations()
	// Build a 0001-only database by hand.
	db, err := sql.Open("sqlite", dsn(p, false))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL)`)
	if _, err := db.Exec(ms[0].sql); err != nil {
		t.Fatal(err)
	}
	db.Exec("insert into instance values(1,'I',0)")
	db.Exec("insert into schema_migrations values(?,?,?,0)", ms[0].version, ms[0].name, ms[0].checksum)
	db.Exec(`insert into tasks(id, objective, state, root_task_id, budget_json, config_hash, created_at, updated_at) values('T1','o','SUCCEEDED','T1','{}','h',0,0)`)
	db.Exec(`insert into task_steps(id, task_id, step_no, description, side_effect, state, idempotency_key, created_at, updated_at) values('S1','T1',1,'d','read','SUCCEEDED','k',0,0)`)
	db.Exec(`insert into task_events(id, task_id, seq, type, actor, data_json, at) values('E1','T1',1,'X','core','{}',0)`)
	db.Close()

	s, err := Open(context.Background(), p, filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	var prio, steps int
	s.R.QueryRow("select state, priority from tasks where id='T1'").Scan(&state, &prio)
	s.R.QueryRow("select count(*) from task_steps where task_id='T1'").Scan(&steps)
	if state != "SUCCEEDED" || prio != 0 || steps != 1 {
		t.Fatalf("state=%s prio=%d steps=%d", state, prio, steps)
	}
	var fk int
	s.R.QueryRow("select count(*) from pragma_foreign_key_check").Scan(&fk)
	if fk != 0 {
		t.Fatal("foreign key violations after upgrade")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "b"))
	if len(entries) == 0 {
		t.Fatal("no pre-migration backup")
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`insert into task_steps(id, task_id, step_no, description, side_effect, state, idempotency_key, created_at, updated_at) values('S2','NOPE',1,'d','read','PENDING','k2',0,0)`)
		return err
	})
	if err == nil {
		t.Fatal("foreign keys not re-enabled after migration")
	}
}
