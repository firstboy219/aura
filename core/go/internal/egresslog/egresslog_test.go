package egresslog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostKeepsOnlyTheHostname(t *testing.T) {
	for in, want := range map[string]string{"api.openrouter.ai": "api.openrouter.ai", "API.Telegram.org:443": "api.telegram.org", "user:secret@example.com:8080": "example.com", "example.com.": "example.com", "": ""} {
		if got := Host(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

type fakeW struct {
	rows map[string]int
	fail bool
}

func (f *fakeW) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if f.fail {
		return errors.New("disk full")
	}
	return nil
}

func TestTheWrapperRecordsTheHostNotThePathQueryOrToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	rec := &Recorder{seen: map[key]*entry{}}
	c := &http.Client{Transport: WrapWith(rec, "telegram", nil)}
	for i := 0; i < 3; i++ {
		resp, err := c.Get(srv.URL + "/bot123456:SECRET-TOKEN/getUpdates?offset=5")
		if err != nil || resp.StatusCode != 204 {
			t.Fatalf("the wrapped client must behave exactly like the original: %v", err)
		}
		resp.Body.Close()
	}
	if len(rec.seen) != 1 {
		t.Fatalf("%v", rec.seen)
	}
	for k, e := range rec.seen {
		if k.host != "127.0.0.1" || k.purpose != "telegram" || e.n != 3 {
			t.Fatalf("%+v %+v", k, e)
		}
	}
}

func TestAFailedFlushKeepsTheCountsForTheNextOne(t *testing.T) {
	rec := &Recorder{seen: map[key]*entry{}}
	rec.Note("provider", "api.openrouter.ai")
	rec.Note("provider", "api.openrouter.ai")
	if err := rec.Flush(context.Background(), &fakeW{fail: true}); err == nil {
		t.Fatal("the error must surface")
	}
	if e := rec.seen[key{"api.openrouter.ai", "provider"}]; e == nil || e.n != 2 {
		t.Fatalf("counts lost: %+v", rec.seen)
	}
	rec.Note("provider", "api.openrouter.ai")
	if err := rec.Flush(context.Background(), &fakeW{}); err != nil || len(rec.seen) != 0 {
		t.Fatalf("%v %v", err, rec.seen)
	}
}
