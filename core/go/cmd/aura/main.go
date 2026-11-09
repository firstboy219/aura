// Command aura is the AURA Go Core: `aura serve` runs the Core; the other
// commands are a CLI client of its REST API (Blueprint §11).
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"aura.local/core/internal/app"
	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/rpc"
	"aura.local/core/internal/store"
	"aura.local/core/internal/worker"
	_ "modernc.org/sqlite"
)

// Version is overridden at build time with -ldflags "-X main.Version=...".
var Version = "0.1.0-p1"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: aura <command> [flags]

  serve                         run the Core
  doctor                        check config, secrets, database, worker and provider
  version                       print version
  task create "<objective>" [--tool T]... [--allow-domain D]... [--max-cost USD]
  task list [--state S] | show <id> | cancel <id> | pause <id> | resume <id>
  trace <id> [--follow]         print a task's event log
  approve <approval-id> [--yes] show the full action and approve its exact hash
  reject <approval-id>
  approvals                     list pending approvals
  stop --all [--yes]            kill switch
  resources                     resource and worker state

Environment: AURA_CONFIG (default /etc/aura/config.yaml), AURA_URL (default http://127.0.0.1:7400),
AURA_TOKEN_FILE (default: server.token_file from the config).
`)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "aura %s (%s/%s, %s)\n", Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	case "serve":
		err = serve(args[1:])
	case "doctor":
		return doctor(stdout)
	case "task", "trace", "approve", "reject", "approvals", "stop", "resources":
		err = client(args, stdin, stdout)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		usage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "aura:", err)
		var ue usageErr
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	return 0
}

type usageErr string

func (u usageErr) Error() string { return string(u) }

func configPath() string {
	if p := os.Getenv("AURA_CONFIG"); p != "" {
		return p
	}
	return "/etc/aura/config.yaml"
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", configPath(), "config file")
	if err := fs.Parse(args); err != nil {
		return usageErr(err.Error())
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a, err := app.Build(ctx, cfg, app.Options{Version: Version})
	if err != nil {
		return err
	}
	defer a.Close()
	a.Log.Info("config loaded", "path", *cfgPath, "config_hash", cfg.Hash(), "version", Version)
	a.SyncModels(ctx)
	return a.Serve(ctx)
}

// doctor runs read-only checks and never spends provider credit.
func doctor(out io.Writer) int {
	failed := false
	check := func(name string, err error, okDetail string) {
		if err != nil {
			failed = true
			fmt.Fprintf(out, "FAIL  %-14s %v\n", name, err)
			return
		}
		fmt.Fprintf(out, "ok    %-14s %s\n", name, okDetail)
	}
	warn := func(name, msg string) { fmt.Fprintf(out, "warn  %-14s %s\n", name, msg) }
	fmt.Fprintf(out, "aura %s\n", Version)
	cfg, err := config.Load(configPath())
	check("config", err, configPath())
	if err != nil {
		return 1
	}
	if _, err := os.Stat(cfg.Server.TokenFile); err == nil {
		_, err := config.ReadSecret(cfg.Server.TokenFile)
		check("api_token", err, "mode and owner ok")
	} else {
		warn("api_token", "not created yet (aura serve creates it)")
	}
	if cfg.Providers.OpenRouter.KeyFile == "" {
		warn("provider_key", "providers.openrouter.key_file not set; model calls will fail")
	} else {
		_, err := config.ReadSecret(cfg.Providers.OpenRouter.KeyFile)
		check("provider_key", err, "mode and owner ok (value not shown)")
	}
	if _, err := os.Stat(cfg.Database.Path); err == nil {
		v, applied, err := dbInfo(cfg.Database.Path)
		if err == nil && store.CompareVersions(v, store.MinSQLite) < 0 {
			err = fmt.Errorf("SQLite %s < %s", v, store.MinSQLite)
		}
		check("database", err, fmt.Sprintf("SQLite %s, WAL, FTS5, %d/%d migrations applied", v, applied, store.MigrationCount()))
	} else {
		warn("database", "not created yet (aura serve creates it)")
	}
	free := app.FreeBytes(dirOf(cfg.Database.Path))
	if free < app.MinFreeBytes {
		check("disk", fmt.Errorf("%d MiB free, below 1 GiB", free>>20), "")
	} else {
		check("disk", nil, fmt.Sprintf("%d MiB free", free>>20))
	}
	sup := worker.New(worker.Options{Command: cfg.Worker.Command, ScratchRoot: cfg.Worker.ScratchRoot})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	start := time.Now()
	raw, err := sup.Call(ctx, "worker.health", rpc.Meta{TraceID: "doctor", CorrelationID: "doctor"}, map[string]any{}, nil)
	cancel()
	sup.Close()
	check("worker", err, fmt.Sprintf("health %s (cold start %d ms)", strings.TrimSpace(string(raw)), time.Since(start).Milliseconds()))
	or := gateway.NewOpenRouter(cfg.Providers.OpenRouter.BaseURL, "", 15*time.Second)
	cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
	catalog, err := or.ListModels(cctx)
	ccancel()
	if err != nil {
		warn("provider", "catalog unreachable: "+err.Error())
	}
	for _, c := range gateway.VerifyRoutes(cfg.Models, catalog) {
		if c.Health == "ok" {
			check("model:"+c.Tier, nil, c.RouteID)
		} else if c.Health == "unknown" {
			warn("model:"+c.Tier, c.RouteID+" (catalog unavailable)")
		} else {
			check("model:"+c.Tier, fmt.Errorf("%s: %s %s", c.RouteID, c.Health, c.Detail), "")
		}
	}
	if len(cfg.Models) == 0 {
		warn("models", "no model routes configured")
	}
	if failed {
		return 1
	}
	return 0
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

func dbInfo(path string) (version string, applied int, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return "", 0, err
	}
	defer db.Close()
	if err := db.QueryRow("select sqlite_version()").Scan(&version); err != nil {
		return "", 0, err
	}
	var jm string
	db.QueryRow("pragma journal_mode").Scan(&jm)
	if jm != "wal" {
		return version, 0, fmt.Errorf("journal_mode %q", jm)
	}
	if _, err := db.Exec("create virtual table temp.fts_probe using fts5(x)"); err != nil {
		return version, 0, fmt.Errorf("FTS5 unavailable: %v", err)
	}
	err = db.QueryRow("select count(*) from schema_migrations").Scan(&applied)
	return version, applied, err
}

// ---- REST client ----

type apiClient struct {
	base, token string
	hc          *http.Client
}

func newClient() (*apiClient, error) {
	base := os.Getenv("AURA_URL")
	if base == "" {
		base = "http://127.0.0.1:7400"
	}
	tf := os.Getenv("AURA_TOKEN_FILE")
	if tf == "" {
		if cfg, err := config.Load(configPath()); err == nil {
			tf = cfg.Server.TokenFile
		} else {
			tf = config.Default().Server.TokenFile
		}
	}
	tok, err := config.ReadSecret(tf)
	if err != nil {
		return nil, err
	}
	return &apiClient{base: strings.TrimRight(base, "/") + "/api/v1", token: tok, hc: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *apiClient) do(method, path string, body any, headers map[string]string, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message, RequestID string } `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return fmt.Errorf("%s: %s (HTTP %d, request %s)", e.Error.Code, e.Error.Message, resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func printJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}

func confirm(stdin io.Reader, stdout io.Writer, prompt string) bool {
	fmt.Fprint(stdout, prompt+" [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
}

// interleaved lets flags follow positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, usageErr(err.Error())
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos, nil
}

func client(args []string, stdin io.Reader, stdout io.Writer) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "task":
		if len(rest) == 0 {
			return usageErr("task needs a subcommand")
		}
		sub, rest := rest[0], rest[1:]
		switch sub {
		case "create":
			fs := flag.NewFlagSet("task create", flag.ContinueOnError)
			var toolsF, domains multiFlag
			fs.Var(&toolsF, "tool", "granted tool (repeatable)")
			fs.Var(&domains, "allow-domain", "allowlisted domain (repeatable)")
			maxCost := fs.Float64("max-cost", 0, "max cost in USD")
			idem := fs.String("idempotency-key", "", "Idempotency-Key header")
			pos, err := parse(fs, rest)
			if err != nil {
				return err
			}
			if len(pos) != 1 {
				return usageErr(`task create "<objective>"`)
			}
			body := map[string]any{"objective": pos[0], "tools": []string(toolsF), "allow_domains": []string(domains)}
			if *maxCost > 0 {
				body["budget"] = map[string]any{"max_cost_usd": *maxCost}
			}
			h := map[string]string{}
			if *idem != "" {
				h["Idempotency-Key"] = *idem
			}
			var out map[string]any
			if err := c.do("POST", "/tasks", body, h, &out); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s %s\n", out["id"], out["state"])
			return nil
		case "list":
			fs := flag.NewFlagSet("task list", flag.ContinueOnError)
			state := fs.String("state", "", "filter by state")
			if _, err := parse(fs, rest); err != nil {
				return err
			}
			path := "/tasks?limit=50"
			if *state != "" {
				path += "&state=" + *state
			}
			var out struct {
				Tasks []struct {
					ID, State, Objective string
					CostUSD              float64 `json:"cost_usd"`
				}
			}
			if err := c.do("GET", path, nil, nil, &out); err != nil {
				return err
			}
			for _, t := range out.Tasks {
				obj := t.Objective
				if len(obj) > 60 {
					obj = obj[:60] + "…"
				}
				fmt.Fprintf(stdout, "%s  %-16s $%.4f  %s\n", t.ID, t.State, t.CostUSD, obj)
			}
			return nil
		case "show", "cancel", "pause", "resume":
			if len(rest) != 1 {
				return usageErr("task " + sub + " <id>")
			}
			var out map[string]any
			method, path := "POST", "/tasks/"+rest[0]+"/"+sub
			if sub == "show" {
				method, path = "GET", "/tasks/"+rest[0]
			}
			if err := c.do(method, path, nil, nil, &out); err != nil {
				return err
			}
			printJSON(stdout, out)
			return nil
		}
		return usageErr("unknown task subcommand " + sub)
	case "trace":
		fs := flag.NewFlagSet("trace", flag.ContinueOnError)
		follow := fs.Bool("follow", false, "keep polling until the task stops")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("trace <id>")
		}
		var after int64
		for {
			var out struct {
				Events []struct {
					Seq             int64
					Type, Actor, At string
					Data            json.RawMessage
				}
			}
			if err := c.do("GET", fmt.Sprintf("/tasks/%s/events?after_seq=%d", pos[0], after), nil, nil, &out); err != nil {
				return err
			}
			for _, e := range out.Events {
				fmt.Fprintf(stdout, "%4d %s %-6s %-20s %s\n", e.Seq, e.At, e.Actor, e.Type, e.Data)
				after = e.Seq
			}
			if !*follow {
				return nil
			}
			var t struct{ State string }
			if err := c.do("GET", "/tasks/"+pos[0], nil, nil, &t); err != nil {
				return err
			}
			switch t.State {
			case "SUCCEEDED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED", "PAUSED", "WAITING_APPROVAL":
				if len(out.Events) == 0 {
					return nil
				}
			}
			time.Sleep(time.Second)
		}
	case "approvals":
		var out map[string]any
		if err := c.do("GET", "/approvals", nil, nil, &out); err != nil {
			return err
		}
		printJSON(stdout, out)
		return nil
	case "approve":
		fs := flag.NewFlagSet("approve", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("approve <approval-id>")
		}
		var list struct {
			Approvals []map[string]any `json:"approvals"`
		}
		if err := c.do("GET", "/approvals", nil, nil, &list); err != nil {
			return err
		}
		var ap map[string]any
		for _, a := range list.Approvals {
			if a["id"] == pos[0] {
				ap = a
			}
		}
		if ap == nil {
			return fmt.Errorf("no pending approval %s", pos[0])
		}
		fmt.Fprintln(stdout, "Action awaiting approval:")
		printJSON(stdout, ap)
		if !*yes && !confirm(stdin, stdout, "Approve exactly this action?") {
			return errors.New("not approved")
		}
		var out map[string]any
		if err := c.do("POST", "/approvals/"+pos[0]+"/approve", map[string]any{"action_hash": ap["action_hash"]}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", out["id"], out["status"])
		return nil
	case "reject":
		if len(rest) != 1 {
			return usageErr("reject <approval-id>")
		}
		var out map[string]any
		if err := c.do("POST", "/approvals/"+rest[0]+"/reject", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", out["id"], out["status"])
		return nil
	case "stop":
		fs := flag.NewFlagSet("stop", flag.ContinueOnError)
		all := fs.Bool("all", false, "stop all activity (required)")
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		if !*all {
			return usageErr("stop requires --all")
		}
		if !*yes && !confirm(stdin, stdout, "Kill switch: stop all AURA activity until the Core restarts?") {
			return errors.New("not stopped")
		}
		var out map[string]any
		if err := c.do("POST", "/system/stop", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stopped; %v task(s) paused\n", out["stopped_tasks"])
		return nil
	case "resources":
		var out map[string]any
		if err := c.do("GET", "/system/resources", nil, nil, &out); err != nil {
			return err
		}
		printJSON(stdout, out)
		return nil
	}
	return usageErr("unknown command " + cmd)
}
