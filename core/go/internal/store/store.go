// Package store owns the SQLite database (ADR-0001). The Core is the only
// writer: every write goes through one serialized writer goroutine holding the
// single write connection; reads use a separate query_only pool under WAL.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aura.local/core/internal/ids"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MinSQLite is the minimum embedded SQLite version (Blueprint §10, WAL-reset fix).
const MinSQLite = "3.51.3"

// Querier is satisfied by *sql.DB, *sql.Tx and *sql.Conn.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type job struct {
	ctx  context.Context
	fn   func(ctx context.Context, c *sql.Conn) error
	done chan error
}

// Store is the database handle.
type Store struct {
	path      string
	backupDir string
	w         *sql.DB
	R         *sql.DB // read pool (query_only)
	queue     chan job
	depth     atomic.Int64
	stop      chan struct{}
	stopped   chan struct{}
	version   string
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)", "foreign_keys(1)"} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if needed) the database, checks the SQLite version,
// backs up an existing database before pending migrations, and migrates.
func Open(ctx context.Context, path, backupDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	s := &Store{path: path, backupDir: backupDir, w: w, queue: make(chan job, 256),
		stop: make(chan struct{}), stopped: make(chan struct{})}
	if err := w.QueryRowContext(ctx, "select sqlite_version()").Scan(&s.version); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	if CompareVersions(s.version, MinSQLite) < 0 {
		w.Close()
		return nil, fmt.Errorf("store: embedded SQLite %s is older than required %s", s.version, MinSQLite)
	}
	var jm string
	if err := w.QueryRowContext(ctx, "pragma journal_mode").Scan(&jm); err != nil || jm != "wal" {
		w.Close()
		return nil, fmt.Errorf("store: journal_mode is %q, want wal (%v)", jm, err)
	}
	conn, err := w.Conn(ctx)
	if err != nil {
		w.Close()
		return nil, err
	}
	if err := s.migrate(ctx, conn); err != nil {
		conn.Close()
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		conn.Close()
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	s.R = r
	go s.writer(conn)
	return s, nil
}

func (s *Store) writer(conn *sql.Conn) {
	defer close(s.stopped)
	defer conn.Close()
	for {
		select {
		case j := <-s.queue:
			s.depth.Add(-1)
			if err := j.ctx.Err(); err != nil {
				j.done <- err
				continue
			}
			j.done <- j.fn(j.ctx, conn)
		case <-s.stop:
			return
		}
	}
}

// Conn runs fn on the single write connection, outside a transaction.
func (s *Store) Conn(ctx context.Context, fn func(ctx context.Context, c *sql.Conn) error) error {
	j := job{ctx: ctx, fn: fn, done: make(chan error, 1)}
	s.depth.Add(1)
	select {
	case s.queue <- j:
	case <-s.stop:
		s.depth.Add(-1)
		return errors.New("store closed")
	case <-ctx.Done():
		s.depth.Add(-1)
		return ctx.Err()
	}
	select {
	case err := <-j.done:
		return err
	case <-s.stopped:
		return errors.New("store closed")
	}
}

// Write runs fn in one short IMMEDIATE transaction on the writer.
// Note: once queued, the job completes even if ctx is cancelled mid-transaction.
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.Conn(ctx, func(_ context.Context, c *sql.Conn) error {
		tx, err := c.BeginTx(context.WithoutCancel(ctx), nil)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

// QueueDepth is the number of writes waiting for the writer.
func (s *Store) QueueDepth() int64 { return s.depth.Load() }

// SQLiteVersion is the embedded SQLite library version.
func (s *Store) SQLiteVersion() string { return s.version }

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// WALBytes is the current size of the -wal file.
func (s *Store) WALBytes() int64 {
	fi, err := os.Stat(s.path + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Checkpoint runs a WAL checkpoint (PASSIVE or TRUNCATE).
func (s *Store) Checkpoint(ctx context.Context, mode string) error {
	if mode != "PASSIVE" && mode != "TRUNCATE" {
		return fmt.Errorf("bad checkpoint mode %q", mode)
	}
	return s.Conn(ctx, func(ctx context.Context, c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "pragma wal_checkpoint("+mode+")")
		return err
	})
}

// Backup writes a consistent online snapshot with VACUUM INTO and verifies it
// with integrity_check (ADR-0001). It returns the backup path.
func (s *Store) Backup(ctx context.Context, label string) (string, error) {
	var dest string
	err := s.Conn(ctx, func(ctx context.Context, c *sql.Conn) error {
		var err error
		dest, err = vacuumInto(ctx, c, s.backupDir, label)
		return err
	})
	if err != nil {
		return "", err
	}
	return dest, VerifyBackup(ctx, dest)
}

func vacuumInto(ctx context.Context, c *sql.Conn, dir, label string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, fmt.Sprintf("aura-%s-%s.db", time.Now().UTC().Format("20060102T150405.000"), label))
	if strings.ContainsRune(dest, '\'') {
		return "", errors.New("backup path contains a quote")
	}
	_, err := c.ExecContext(ctx, "VACUUM INTO '"+dest+"'")
	return dest, err
}

// VerifyBackup opens a backup read-only and runs integrity_check.
func VerifyBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, "pragma integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("backup %s integrity_check: %s", path, res)
	}
	return nil
}

// Close stops the writer and closes both pools.
func (s *Store) Close() error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.stopped
	if s.R != nil {
		s.R.Close()
	}
	return s.w.Close()
}

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		n := e.Name()
		v, err := strconv.Atoi(strings.SplitN(n, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad name", n)
		}
		b, _ := migrationFS.ReadFile("migrations/" + n)
		h := sha256.Sum256(b)
		ms = append(ms, migration{v, n, string(b), hex.EncodeToString(h[:])})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

func (s *Store) migrate(ctx context.Context, c *sql.Conn) error {
	if _, err := c.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := c.QueryContext(ctx, "select version, checksum from schema_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		var sum string
		rows.Scan(&v, &sum)
		applied[v] = sum
	}
	rows.Close()
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var pending []migration
	for _, m := range ms {
		if sum, ok := applied[m.version]; ok {
			if sum != m.checksum {
				return fmt.Errorf("store: migration %s changed after being applied", m.name)
			}
			continue
		}
		pending = append(pending, m)
	}
	if len(pending) == 0 {
		return nil
	}
	if len(applied) > 0 {
		dest, err := vacuumInto(ctx, c, s.backupDir, "pre-migrate")
		if err != nil {
			return fmt.Errorf("store: backup before migration: %w", err)
		}
		if err := VerifyBackup(ctx, dest); err != nil {
			return err
		}
	}
	for _, m := range pending {
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if m.version == 1 {
			if _, err := tx.ExecContext(ctx, "insert into instance(id, instance_id, created_at) values(1, ?, ?)",
				ids.New(), ids.NowMs()); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "insert into schema_migrations values(?,?,?,?)",
			m.version, m.name, m.checksum, ids.NowMs()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// InstanceID returns this database's instance identity (ADR-0012).
func (s *Store) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.R.QueryRowContext(ctx, "select instance_id from instance where id=1").Scan(&id)
	return id, err
}

// CompareVersions compares dotted numeric versions.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
