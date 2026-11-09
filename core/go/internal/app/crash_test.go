package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/tools"
	_ "modernc.org/sqlite"
)

type loopbackResolver struct{}

func (loopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func crashConfig(dir string) *config.Config {
	cfg, _ := config.Parse([]byte("config_version: 1\n"), func(string) string { return "" })
	cfg.Server.TokenFile = filepath.Join(dir, "api_token")
	cfg.Server.RatePerS = 1000
	cfg.Database.Path = filepath.Join(dir, "aura.db")
	cfg.Database.BackupDir = filepath.Join(dir, "backups")
	cfg.Workspace.Root = filepath.Join(dir, "ws")
	cfg.Worker.ScratchRoot = filepath.Join(dir, "scratch")
	cfg.Models = map[string][]config.ModelRoute{"standard": {{DisplayName: "S", APIModelID: "s", RouteID: "fake/s",
		PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 4000}}}
	return cfg
}

// TestCrashChild is the server process the crash test kills. It only runs
// when re-executed by TestCrashRecovery.
func TestCrashChild(t *testing.T) {
	if os.Getenv("AURA_CRASH_CHILD") != "1" {
		t.Skip("helper process")
	}
	dir, addr, port := os.Getenv("AURA_CRASH_DIR"), os.Getenv("AURA_CRASH_ADDR"), os.Getenv("AURA_CRASH_FIXTURE_PORT")
	fake := &gatewaytest.Fake{Respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		rep := gatewaytest.RS1Model(r, req)
		rep.Delay = time.Duration(rand.Intn(150)) * time.Millisecond // widen the kill windows
		return rep
	}}
	guard := tools.NewTestGuard(loopbackResolver{})
	tools.SetTestPort(guard, port)
	a, err := Build(context.Background(), crashConfig(dir), Options{Provider: fake, Guard: guard,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child build:", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child listen:", err)
		os.Exit(1)
	}
	a.ServeListener(context.Background(), ln)
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestCrashRecovery (D.1 P1): 20 random SIGKILL/restart cycles of a real Core
// process while tasks run. Afterwards every task must be terminal, waiting, or
// recoverable, and no idempotent write may have been performed twice.
func TestCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("long test")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><head><title>%s</title></head><body><p>Content of %s.</p></body></html>", r.URL.Path, r.URL.Path)
	}))
	defer fixture.Close()
	fu, _ := url.Parse(fixture.URL)
	_, fport, _ := net.SplitHostPort(fu.Host)
	dir := t.TempDir()
	addr := freeAddr(t)
	var child *exec.Cmd
	start := func() {
		child = exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.v=false")
		child.Env = append(os.Environ(), "AURA_CRASH_CHILD=1", "AURA_CRASH_DIR="+dir, "AURA_CRASH_ADDR="+addr, "AURA_CRASH_FIXTURE_PORT="+fport)
		child.Stderr = os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if resp, err := http.Get("http://" + addr + "/api/v1/system/health"); err == nil {
				resp.Body.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("child did not start")
	}
	kill := func() {
		syscall.Kill(-child.Process.Pid, syscall.SIGKILL) // Core and its worker
		child.Wait()
	}
	defer func() {
		if child != nil && child.ProcessState == nil {
			kill()
		}
	}()
	start()
	token, err := config.ReadSecret(filepath.Join(dir, "api_token"))
	if err != nil {
		t.Fatal(err)
	}
	submit := func(i int) {
		body := fmt.Sprintf(`{"objective":"Fetch http://fixture.test/a%[1]d and http://fixture.test/b%[1]d and write reports/summary.md","allow_domains":["fixture.test"]}`, i)
		req, _ := http.NewRequest("POST", "http://"+addr+"/api/v1/tasks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", fmt.Sprintf("crash-%d", i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	submitted := 0
	for kills := 0; kills < 20; kills++ {
		submit(submitted)
		submitted++
		time.Sleep(time.Duration(30+rng.Intn(400)) * time.Millisecond)
		kill()
		start()
	}
	// Let everything settle without further kills.
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "aura.db")+"?mode=ro")
	defer db.Close()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var active int
		db.QueryRow("select count(*) from tasks where state in ('CREATED','PLANNING','RUNNING')").Scan(&active)
		if active == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	kill()

	states := map[string]int{}
	rows, _ := db.Query("select state, coalesce(reason,'') from tasks")
	var reasons []string
	for rows.Next() {
		var s, r string
		rows.Scan(&s, &r)
		states[s]++
		if s != "SUCCEEDED" {
			reasons = append(reasons, s+":"+r)
		}
	}
	rows.Close()
	t.Logf("tasks after 20 kill/restart cycles: %v (non-success: %v)", states, reasons)
	frows, _ := db.Query(`select e.task_id, e.type, e.data_json from task_events e join tasks t on t.id=e.task_id
		where t.state != 'SUCCEEDED' and e.type in ('STEP_FAILED','STEP_ATTEMPT_FAILED','TOOL_FAILED','STEP_RECOVERED','STATE_CHANGED') order by e.task_id, e.seq`)
	for frows.Next() {
		var id, typ, d string
		frows.Scan(&id, &typ, &d)
		t.Logf("  %s %s %s", id[len(id)-6:], typ, d)
	}
	frows.Close()
	total := 0
	for s, n := range states {
		total += n
		switch s {
		case "SUCCEEDED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED", "WAITING_APPROVAL", "PAUSED":
		default:
			t.Errorf("%d task(s) left in non-terminal, non-waiting state %s", n, s)
		}
	}
	if total != submitted {
		t.Fatalf("tasks %d, submitted %d (idempotency keys must prevent duplicates)", total, submitted)
	}
	// Zero duplicate idempotent writes: per step key, at most one performed write.
	rows, _ = db.Query("select data_json from task_events where type='TOOL_SUCCEEDED'")
	performed := map[string]int{}
	for rows.Next() {
		var d string
		rows.Scan(&d)
		var m map[string]any
		json.Unmarshal([]byte(d), &m)
		if m["tool_id"] == "workspace.write" && m["skipped"] != true {
			performed[m["idempotency_key"].(string)]++
		}
	}
	rows.Close()
	for k, n := range performed {
		if n > 1 {
			t.Errorf("idempotent write %s performed %d times", k, n)
		}
	}
	var recovered int
	db.QueryRow("select count(*) from task_events where type='TASK_RECOVERED'").Scan(&recovered)
	t.Logf("recoveries: %d, writes performed: %d", recovered, len(performed))
	if states["SUCCEEDED"] == 0 {
		t.Fatal("no task succeeded")
	}
}
