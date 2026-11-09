package store

import (
	"context"
	"database/sql"
	"fmt"
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
