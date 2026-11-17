// Package egresslog records WHERE AURA's control plane talks to (ODI: "core egress is not allow-listed"). It is an observation step before any allowlist: after a few days of real use the owner sees
// every destination host (never a path, query, port, or credential) per purpose and can approve exactly that list. It blocks nothing and changes no request.
package egresslog

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type key struct{ host, purpose string }
type entry struct {
	n           int
	first, last int64
}

// Recorder counts requests per (host, purpose) in memory; Flush adds them to the table.
type Recorder struct {
	mu   sync.Mutex
	seen map[key]*entry
}

// Default is the process-wide recorder the wrapped clients report to.
var Default = &Recorder{seen: map[key]*entry{}}

// Host reduces a request URL host to the bare hostname: no port, no userinfo, lower-case.
func Host(h string) string {
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func (r *Recorder) Note(purpose, host string) {
	if host = Host(host); host == "" {
		return
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.seen[key{host, purpose}]
	if e == nil {
		e = &entry{first: now}
		r.seen[key{host, purpose}] = e
	}
	e.n++
	e.last = now
}

type writer interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// Flush moves the in-memory counts into egress_seen (upsert) and clears them. A failed write keeps the counts for the next flush.
func (r *Recorder) Flush(ctx context.Context, w writer) error {
	r.mu.Lock()
	batch := r.seen
	r.seen = map[key]*entry{}
	r.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	err := w.Write(ctx, func(tx *sql.Tx) error {
		for k, e := range batch {
			if _, err := tx.ExecContext(ctx, `insert into egress_seen(host, purpose, count, first_seen, last_seen) values(?,?,?,?,?)
				on conflict(host, purpose) do update set count=count+excluded.count, last_seen=excluded.last_seen`, k.host, k.purpose, e.n, e.first, e.last); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.mu.Lock()
		for k, e := range batch {
			if cur := r.seen[k]; cur != nil {
				cur.n += e.n
				if e.first < cur.first {
					cur.first = e.first
				}
			} else {
				r.seen[k] = e
			}
		}
		r.mu.Unlock()
	}
	return err
}

type transport struct {
	purpose string
	base    http.RoundTripper
	rec     *Recorder
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.rec.Note(t.purpose, req.URL.Host)
	return t.base.RoundTrip(req)
}

// Wrap makes rt report each request's destination host to the default recorder. nil rt means http.DefaultTransport.
func Wrap(purpose string, rt http.RoundTripper) http.RoundTripper {
	return WrapWith(Default, purpose, rt)
}

func WrapWith(rec *Recorder, purpose string, rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return transport{purpose: purpose, base: rt, rec: rec}
}
