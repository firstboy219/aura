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
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"aura.local/core/internal/api"
	"aura.local/core/internal/app"
	apiclient "aura.local/core/internal/client"
	"aura.local/core/internal/config"
	"aura.local/core/internal/console"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/release"
	"aura.local/core/internal/rpc"
	"aura.local/core/internal/sandbox"
	"aura.local/core/internal/store"
	"aura.local/core/internal/vault"
	"aura.local/core/internal/worker"
	"aura.local/core/internal/workerhost"
	tea "github.com/charmbracelet/bubbletea"
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
  console                       interactive terminal console (80×24 is enough)
  doctor                        check config, secrets, database, worker and provider
  version                       print version
  task create "<objective>" [--tool T]... [--allow-domain D]... [--max-cost USD] [--public] [--tier T] [--attach ws/path=file]...
  task list [--state S] | show <id> | cancel <id> | pause <id> | resume <id>
  trace <id> [--follow]         print a task's event log
  approve <approval-id> [--yes] show the full action and approve its exact hash
  reject <approval-id>
  answer <task-id> "<answer>"   answer a task's clarification question
  event <key> [--dedup ID] [--payload JSON]   signal an external dependency (wakes waiting tasks)
  task priority <id> <n>        reprioritize (-100..100, higher runs first)
  task tree <id>                task with its subagents and rolled-up cost
  correct <id> "<text>"         owner correction; reaches every task in the tree
  memory list [--q TEXT] | add "<fact>" [--private] | correct <id> "<text>" | delete <id>
  skill list | show <id> [--diff FROM TO] | disable <id> | enable <id> | rollback <id> <version>
  trigger list | add-task|add-watch … | enable|disable|run|delete <id>   scheduled tasks and read-only watches
  notifications [--unread]      findings from watches
  self                          who I am, what I can do, my measured weaknesses and how I improve (no model writes this)
  limitations [--all]           the Limitation Ledger; limitation add|accept|dismiss|reopen
  profile list [--all] | add "<preference>" | confirm|disable|enable|delete <id> | edit <id> "<text>"
  approvals                     list pending approvals
  stop --all [--yes]            kill switch
  resources                     resource and worker state
  browser check <url> [--allow-domain D]   open a page through the isolated browser and egress proxy (no model)
  browser profiles | session import <profile> --file F | session delete <profile> | code <task-id> <code>
                                login profiles (ADR-0040); one-time codes go straight into the page
  tools list | show <id> | approve <id> | reject <id> | disable <id>   AURA-built L2 tools (WASM sandbox)
  release keygen <priv> | sign … | verify --pubkey B64   signed runtime artifacts (ADR-0037)
  web setup-link [--ttl 30m] | sessions-revoke   Workspace Web App owner access (ADR-0041)
  secrets keygen <file> | import <name> --from-file <p> | list | rotate   encrypted secrets (ADR-0036)
  worker-host [--socket P]      (isolated worker container) serve workers over a Unix socket
  worker-connect [--socket P]   (Core) bridge the worker supervisor to the worker host

Environment: AURA_CONFIG (default /etc/aura/config.yaml), AURA_URL (default http://127.0.0.1:7400),
AURA_TOKEN_FILE (default: server.token_file from the config).
`)
}

func vaultSecret(cfg *config.Config, name string) (string, error) {
	v, err := app.OpenVault(cfg)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", errors.New("secrets.master_key_file is not set")
	}
	db, err := sql.Open("sqlite", "file:"+cfg.Database.Path+"?mode=ro")
	if err != nil {
		return "", err
	}
	defer db.Close()
	return v.Get(context.Background(), db, name)
}

// webCmd manages Web App owner access (ADR-0041); it runs where the Core runs.
func webCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "setup-link" && args[0] != "sessions-revoke") {
		return usageErr("web setup-link [--ttl 30m] | sessions-revoke")
	}
	cfg, err := config.Load(configPath())
	if err != nil {
		return err
	}
	if !cfg.Web.Enabled {
		return errors.New("web.enabled is false in the config")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BackupDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if args[0] == "sessions-revoke" {
		err := st.Write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, "delete from web_sessions"); return err })
		if err == nil {
			fmt.Fprintln(stdout, "every Web App session is signed out")
		}
		return err
	}
	fs := flag.NewFlagSet("web setup-link", flag.ContinueOnError)
	ttl := fs.Duration("ttl", 30*time.Minute, "how long the link works (single use)")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	if *ttl < time.Minute || *ttl > 24*time.Hour {
		return usageErr("--ttl must be 1m..24h")
	}
	link, err := api.CreateSetupLink(ctx, st, cfg.Web.Origin, *ttl)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "one-time owner setup link (works once, for %s; it also resets the password and the authenticator):\n%s\n", *ttl, link)
	return nil
}

// secretsCmd manages the encrypted secret store (ADR-0036). It runs where the
// Core runs (same config and database) and never prints a secret value.
func secretsCmd(args []string, stdin io.Reader, stdout io.Writer) error {
	const use = "secrets keygen <file> | import <name> (--from-file <path> | --stdin) | list | rotate"
	if len(args) == 0 {
		return usageErr(use)
	}
	if args[0] == "keygen" {
		if len(args) != 2 {
			return usageErr("secrets keygen <file>")
		}
		id, err := vault.GenerateKeyFile(args[1])
		if err == nil {
			fmt.Fprintf(stdout, "new master key written to %s (mode 0400, key %s); keep a copy outside the server\n", args[1], id)
		}
		return err
	}
	cfg, err := config.Load(configPath())
	if err != nil {
		return err
	}
	v, err := app.OpenVault(cfg)
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("set secrets.master_key_file in the config first (create one with: aura secrets keygen <file>)")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BackupDir)
	if err != nil {
		return err
	}
	defer st.Close()
	switch args[0] {
	case "import":
		fs := flag.NewFlagSet("secrets import", flag.ContinueOnError)
		from := fs.String("from-file", "", "read the value from this 0400 file")
		useStdin := fs.Bool("stdin", false, "read the value from stdin")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || (*from == "") == !*useStdin {
			return usageErr("secrets import <name> (--from-file <path> | --stdin)")
		}
		var value string
		if *from != "" {
			value, err = config.ReadSecret(*from)
		} else {
			var b []byte
			b, err = io.ReadAll(io.LimitReader(stdin, 16<<10+1))
			value = strings.TrimSpace(string(b))
		}
		if err != nil {
			return err
		}
		if err := st.Write(ctx, func(tx *sql.Tx) error { return v.Put(ctx, tx, pos[0], value) }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stored %s encrypted (key %s); reference it as %s%s\n", pos[0], v.CurrentKeyID(), vault.RefPrefix, pos[0])
	case "list":
		infos, err := vault.List(ctx, st.R)
		if err != nil {
			return err
		}
		for _, i := range infos {
			mark := ""
			if i.KeyID != v.CurrentKeyID() {
				mark = "  (old key: run aura secrets rotate)"
			}
			fmt.Fprintf(stdout, "%-24s key %s  updated %s%s\n", i.Name, i.KeyID, time.UnixMilli(i.UpdatedAt).UTC().Format(time.RFC3339), mark)
		}
	case "rotate":
		var n int
		err := st.Write(ctx, func(tx *sql.Tx) error {
			var err error
			n, err = v.Rotate(ctx, tx)
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "re-encrypted %d secret(s) under key %s; secrets.previous_key_file can now be removed\n", n, v.CurrentKeyID())
	default:
		return usageErr(use)
	}
	return nil
}

// verifyRelease checks the signed release manifest (ADR-0037). Without a
// configured public key it does nothing; with one, a failure stops startup
// when required, and is logged loudly otherwise.
func verifyRelease(pubText, manifest string, required bool, log *slog.Logger) error {
	if pubText == "" {
		return nil
	}
	err := func() error {
		pub, err := release.ParsePublicKey(pubText)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(manifest)
		if err != nil {
			return fmt.Errorf("release manifest: %w", err)
		}
		m, err := release.Verify(raw, pub, "")
		if err == nil {
			log.Info("release verified", "version", m.Body.Version, "files", len(m.Body.Files), "key", m.Body.KeyID)
		}
		return err
	}()
	if err != nil {
		if required {
			return fmt.Errorf("refusing to start: %w", err)
		}
		log.Error("release verification failed (release.required is false, starting anyway)", "err", err)
	}
	return nil
}

// releaseCmd creates and checks signed release manifests (ADR-0037).
func releaseCmd(args []string, stdout io.Writer) error {
	const use = "release keygen <private-key-file> | sign --key <file> --version V [--root DIR] --out <file> <path>... | verify --pubkey B64 [--manifest F] [--root DIR]"
	if len(args) == 0 {
		return usageErr(use)
	}
	switch args[0] {
	case "keygen":
		if len(args) != 2 {
			return usageErr("release keygen <private-key-file>")
		}
		pub, err := release.Keygen(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "private key written to %s (0400; keep it off the server)\npublic key (put in release.public_key): %s\n", args[1], pub)
	case "sign":
		fs := flag.NewFlagSet("release sign", flag.ContinueOnError)
		keyFile := fs.String("key", "", "private key file")
		version := fs.String("version", "", "release version")
		root := fs.String("root", "", "directory holding the image files (default: this filesystem)")
		out := fs.String("out", "", "manifest output file")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if *keyFile == "" || *version == "" || *out == "" || len(pos) == 0 {
			return usageErr("release sign --key <file> --version V [--root DIR] --out <file> <path>...")
		}
		text, err := config.ReadSecret(*keyFile)
		if err != nil {
			return err
		}
		priv, err := release.ParsePrivateKey(text)
		if err != nil {
			return err
		}
		files, err := release.Collect(*root, pos)
		if err != nil {
			return err
		}
		var dirs []string
		for _, p := range pos {
			if fi, err := os.Stat(filepath.Join(*root, p)); err == nil && fi.IsDir() {
				dirs = append(dirs, p)
			}
		}
		m, err := release.Sign(priv, *version, files, dirs)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "signed %d files (key %s) → %s\n", len(files), m.Body.KeyID, *out)
	case "verify":
		fs := flag.NewFlagSet("release verify", flag.ContinueOnError)
		pubText := fs.String("pubkey", "", "base64 public key")
		manifest := fs.String("manifest", "/usr/local/share/aura/release.json", "manifest")
		root := fs.String("root", "", "directory holding the image files")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		pub, err := release.ParsePublicKey(*pubText)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(*manifest)
		if err != nil {
			return err
		}
		m, err := release.Verify(raw, pub, *root)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "ok: release %s, %d files, key %s\n", m.Body.Version, len(m.Body.Files), m.Body.KeyID)
	default:
		return usageErr(use)
	}
	return nil
}

// workerHost runs in the isolated worker container (ADR-0035).
func workerHost(args []string) error {
	fs := flag.NewFlagSet("worker-host", flag.ContinueOnError)
	sock := fs.String("socket", "/run/aura-worker/worker.sock", "Unix socket to listen on")
	scratch := fs.String("scratch", "/tmp/aura-scratch", "per-worker scratch root (tmpfs)")
	allow := fs.Int("allow-uid", 10001, "only this peer uid (the Core) may connect; -1 allows any")
	pub := fs.String("release-pubkey", "", "verify the signed release before serving (ADR-0037)")
	manifest := fs.String("release-manifest", "/usr/local/share/aura/release.json", "release manifest")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := verifyRelease(*pub, *manifest, true, slog.New(slog.NewJSONHandler(os.Stdout, nil))); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return workerhost.Serve(ctx, workerhost.Options{Socket: *sock, ScratchRoot: *scratch, AllowUID: *allow,
		Command: []string{"python3", "-I", "-m", "aura_worker"}, Log: log})
}

// workerConnect is the Core-side worker command when the worker is isolated:
// it bridges the supervisor's stdio to the worker host socket.
func workerConnect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("worker-connect", flag.ContinueOnError)
	sock := fs.String("socket", "/run/aura-worker/worker.sock", "worker host socket")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	return workerhost.Connect(*sock, stdin, stdout)
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
	case "console":
		err = runConsole()
	case "secrets":
		err = secretsCmd(args[1:], stdin, stdout)
	case "web":
		err = webCmd(args[1:], stdout)
	case "release":
		err = releaseCmd(args[1:], stdout)
	case "worker-host":
		err = workerHost(args[1:])
	case "worker-connect":
		err = workerConnect(args[1:], stdin, stdout)
	case "task", "trace", "approve", "reject", "approvals", "stop", "resources", "answer", "event", "correct", "memory", "skill", "profile",
		"self", "limitations", "limitation", "trigger", "notifications", "tools", "browser":
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
	if err := verifyRelease(cfg.Release.PublicKey, cfg.Release.Manifest, cfg.Release.Required, slog.Default()); err != nil {
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
	} else if name, ok := strings.CutPrefix(cfg.Providers.OpenRouter.KeyFile, vault.RefPrefix); ok {
		_, err := vaultSecret(cfg, name)
		check("provider_key", err, "encrypted in the vault, decrypts with the master key (value not shown)")
	} else {
		_, err := config.ReadSecret(cfg.Providers.OpenRouter.KeyFile)
		check("provider_key", err, "mode and owner ok (value not shown)")
	}
	if _, err := os.Stat(cfg.L2.PythonWASM); err == nil {
		start := time.Now()
		rt, err := sandbox.NewPython(context.Background(), cfg.L2.PythonWASM, cfg.L2.CacheDir, sandbox.Limits{})
		detail := ""
		if err == nil {
			var res *sandbox.Result
			if res, err = rt.RunPython(context.Background(), "print(6*7)", nil); err == nil && strings.TrimSpace(string(res.Stdout)) != "42" {
				err = fmt.Errorf("unexpected output %q", res.Stdout)
			}
			rt.Close(context.Background())
			detail = fmt.Sprintf("WASM sandbox ready, CPython 3.12 WASI pinned (%d ms incl. compile)", time.Since(start).Milliseconds())
		}
		check("l2_sandbox", err, detail)
	} else {
		warn("l2_sandbox", "python WASM module not found; L2 tools (R3 remedies) unavailable")
	}
	if cfg.Release.PublicKey != "" {
		err := verifyRelease(cfg.Release.PublicKey, cfg.Release.Manifest, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
		check("release", err, "signed manifest verified ("+cfg.Release.Manifest+")")
	} else {
		warn("release", "release.public_key not set; runtime files are not verified")
	}
	if cfg.Secrets.MasterKeyFile != "" {
		v, err := app.OpenVault(cfg)
		detail := ""
		if err == nil {
			detail = "loaded, key " + v.CurrentKeyID()
		}
		check("master_key", err, detail)
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
	sup := worker.New(worker.Options{Command: cfg.Worker.Command, ScratchRoot: cfg.Worker.ScratchRoot,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
			public := fs.Bool("public", false, "content is public: allow model routes that may log prompts (free models)")
			tier := fs.String("tier", "", "force a model tier for the whole task: fast | standard | deep")
			recovery := fs.String("recovery", "", "after a Core restart: auto (default) | manual (pause for me) | never (fail)")
			profile := fs.String("browser-profile", "", "login profile this task may use in the browser (see aura browser profiles)")
			var attach multiFlag
			fs.Var(&attach, "attach", "input file: workspace/path=local-file (repeatable)")
			pos, err := parse(fs, rest)
			if err != nil {
				return err
			}
			if len(pos) != 1 {
				return usageErr(`task create "<objective>"`)
			}
			body := map[string]any{"objective": pos[0], "tools": []string(toolsF), "allow_domains": []string(domains)}
			if *public {
				body["data_class"] = "public"
			}
			if *tier != "" {
				body["tier"] = *tier
			}
			if *recovery != "" {
				body["recovery"] = *recovery
			}
			if *profile != "" {
				body["browser_profile"] = *profile
			}
			if len(attach) > 0 {
				files := map[string]string{}
				for _, a := range attach {
					dst, src, ok := strings.Cut(a, "=")
					if !ok {
						return usageErr("--attach needs workspace/path=local-file")
					}
					b, err := os.ReadFile(src)
					if err != nil {
						return err
					}
					files[dst] = string(b)
				}
				body["attachments"] = files
			}
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
		case "tree":
			if len(rest) != 1 {
				return usageErr("task tree <id>")
			}
			var out treeJSON
			if err := c.do("GET", "/tasks/"+rest[0]+"/tree", nil, nil, &out); err != nil {
				return err
			}
			printTree(stdout, out, 0)
			return nil
		case "priority":
			if len(rest) != 2 {
				return usageErr("task priority <id> <-100..100>")
			}
			var p int
			if _, err := fmt.Sscan(rest[1], &p); err != nil {
				return usageErr("priority must be an integer")
			}
			var out map[string]any
			if err := c.do("POST", "/tasks/"+rest[0]+"/priority", map[string]int{"priority": p}, nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s priority=%v\n", out["id"], out["priority"])
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
			case "COMPLETED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED", "PAUSED", "WAITING_APPROVAL", "WAITING_INPUT", "WAITING_EXTERNAL", "SCHEDULED":
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
	case "tools":
		return toolsCmd(c, rest, stdin, stdout)
	case "browser":
		const buse = "browser check <url> [--allow-domain D]... | profiles | session import <profile> --file F | session delete <profile> | code <task-id> <code>"
		if len(rest) > 0 && rest[0] != "check" {
			return browserCmd(c, rest, stdout, buse)
		}
		fs := flag.NewFlagSet("browser check", flag.ContinueOnError)
		var domains multiFlag
		fs.Var(&domains, "allow-domain", "domain the page may load from (repeatable; default: the URL's host)")
		if len(rest) == 0 {
			return usageErr(buse)
		}
		pos, err := parse(fs, rest[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("browser check <url> [--allow-domain D]...")
		}
		if len(domains) == 0 {
			if u, err := url.Parse(pos[0]); err == nil {
				domains = append(domains, u.Hostname())
			}
		}
		var out map[string]any
		if err := c.do("POST", "/browser/check", map[string]any{"url": pos[0], "allow_domains": []string(domains)}, nil, &out); err != nil {
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
	case "memory", "skill", "profile":
		return ownerData(c, cmd, rest, stdout)
	case "trigger":
		return triggerCmd(c, rest, stdout)
	case "notifications":
		var out any
		path := "/notifications"
		if len(rest) > 0 && rest[0] == "--unread" {
			path += "?unread=true"
		}
		if err := c.do("GET", path, nil, nil, &out); err != nil {
			return err
		}
		printJSON(stdout, out)
		return nil
	case "self":
		var out struct {
			Summary string `json:"summary"`
		}
		if err := c.do("GET", "/self", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.Summary)
		return nil
	case "limitations":
		var out struct {
			Limitations []struct {
				ID, Status, Category, Title string
				TaskClass                   string `json:"task_class"`
				Remedies                    []struct{ Class, Status string }
			}
		}
		path := "/limitations"
		if len(rest) == 0 || rest[0] != "--all" {
			path += "?status=open"
		}
		if err := c.do("GET", path, nil, nil, &out); err != nil {
			return err
		}
		for _, l := range out.Limitations {
			rem := ""
			for _, r := range l.Remedies {
				rem += fmt.Sprintf(" [%s:%s]", r.Class, r.Status)
			}
			fmt.Fprintf(stdout, "%s  %-15s %-18s%s  %s\n", l.ID, l.Status, l.Category, rem, l.Title)
		}
		return nil
	case "limitation":
		if len(rest) < 1 {
			return usageErr("limitation add|accept|dismiss|reopen …")
		}
		switch rest[0] {
		case "add":
			if len(rest) != 4 {
				return usageErr(`limitation add <category> <task-class> "<title>"`)
			}
			var out map[string]any
			if err := c.do("POST", "/limitations", map[string]string{"category": rest[1], "task_class": rest[2], "title": rest[3]}, nil, &out); err != nil {
				return err
			}
			printJSON(stdout, out)
			return nil
		case "accept", "dismiss", "reopen":
			if len(rest) < 2 {
				return usageErr("limitation " + rest[0] + " <id> [note]")
			}
			note := ""
			if len(rest) > 2 {
				note = rest[2]
			}
			var out map[string]any
			if err := c.do("POST", "/limitations/"+rest[1]+"/"+rest[0], map[string]string{"note": note}, nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s %s\n", out["id"], out["status"])
			return nil
		}
		return usageErr("unknown limitation subcommand")
	case "correct":
		if len(rest) != 2 {
			return usageErr(`correct <task-id> "<correction or added instruction>"`)
		}
		var out map[string]any
		if err := c.do("POST", "/tasks/"+rest[0]+"/context", map[string]string{"content": rest[1]}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s noted (reaches every task in its tree)\n", out["id"])
		return nil
	case "event":
		fs := flag.NewFlagSet("event", flag.ContinueOnError)
		dedup := fs.String("dedup", "", "dedup id (repeated sends with the same id count once)")
		payload := fs.String("payload", "", "JSON payload")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("event <key> [--dedup ID] [--payload JSON]")
		}
		body := map[string]any{"key": pos[0], "dedup_id": *dedup}
		if *payload != "" {
			body["payload"] = json.RawMessage(*payload)
		}
		var out map[string]any
		if err := c.do("POST", "/events", body, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s duplicate=%v\n", out["id"], out["duplicate"])
		return nil
	case "answer":
		if len(rest) != 2 {
			return usageErr(`answer <task-id> "<answer>"`)
		}
		var out map[string]any
		if err := c.do("POST", "/tasks/"+rest[0]+"/input", map[string]string{"answer": rest[1]}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", out["id"], out["state"])
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

type treeJSON struct {
	Task struct {
		ID, State, Objective string
		AgentRole            string  `json:"agent_role"`
		CostUSD              float64 `json:"cost_usd"`
	} `json:"task"`
	TreeCostUSD float64    `json:"tree_cost_usd"`
	Children    []treeJSON `json:"children"`
}

func printTree(w io.Writer, n treeJSON, depth int) {
	obj := strings.ReplaceAll(n.Task.Objective, "\n", " ")
	if len(obj) > 60 {
		obj = obj[:60] + "…"
	}
	fmt.Fprintf(w, "%s%s %-16s %-10s $%.4f (tree $%.4f)  %s\n", strings.Repeat("  ", depth), n.Task.ID, n.Task.State, n.Task.AgentRole,
		n.Task.CostUSD, n.TreeCostUSD, obj)
	for _, c := range n.Children {
		printTree(w, c, depth+1)
	}
}

func runConsole() error {
	c, err := newClient()
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(c.base, "/api/v1")
	_, err = tea.NewProgram(console.New(apiclient.New(base, c.token)), tea.WithAltScreen()).Run()
	return err
}

// ownerData implements the memory, skill and profile commands.
// browserCmd manages login profiles' sessions and one-time codes (ADR-0040).
func browserCmd(c *apiClient, rest []string, stdout io.Writer, use string) error {
	var out map[string]any
	switch {
	case rest[0] == "profiles":
		if err := c.do("GET", "/browser/profiles", nil, nil, &out); err != nil {
			return err
		}
		printJSON(stdout, out)
		return nil
	case rest[0] == "code" && len(rest) == 3:
		if err := c.do("POST", "/tasks/"+rest[1]+"/browser-code", map[string]any{"code": rest[2]}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "code entered into the page (not stored)")
		return nil
	case rest[0] == "session" && len(rest) >= 3 && rest[1] == "delete":
		if err := c.do("DELETE", "/browser/profiles/"+rest[2]+"/session", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "saved session of %s deleted\n", rest[2])
		return nil
	case rest[0] == "session" && len(rest) >= 3 && rest[1] == "import":
		fs := flag.NewFlagSet("browser session import", flag.ContinueOnError)
		file := fs.String("file", "", "Playwright storage state, or a JSON cookie list exported from your browser")
		pos, err := parse(fs, rest[2:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *file == "" {
			return usageErr("browser session import <profile> --file F")
		}
		raw, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		if err := c.do("POST", "/browser/profiles/"+pos[0]+"/session", map[string]any{"state": json.RawMessage(raw)}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "imported %v cookies for %s (encrypted; other domains dropped). Delete the exported file now.\n", out["cookies_kept"], pos[0])
		return nil
	}
	return usageErr(use)
}

// toolsCmd reviews and decides AURA-built L2 tools (ADR-0038).
func toolsCmd(c *apiClient, rest []string, stdin io.Reader, stdout io.Writer) error {
	const use = "tools list | show <id> | approve <id> [--yes] | reject <id> | disable <id> [reason]"
	if len(rest) == 0 {
		return usageErr(use)
	}
	switch rest[0] {
	case "list":
		var out struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := c.do("GET", "/tools", nil, nil, &out); err != nil {
			return err
		}
		for _, t := range out.Tools {
			fmt.Fprintf(stdout, "%s  l2.%-28v v%-3v %-18v %v\n", t["id"], t["name"], t["version"], t["status"], t["description"])
		}
		return nil
	case "show", "approve":
		fs := flag.NewFlagSet("tools "+rest[0], flag.ContinueOnError)
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		pos, err := parse(fs, rest[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr("tools " + rest[0] + " <id>")
		}
		var t map[string]any
		if err := c.do("GET", "/tools/"+pos[0], nil, nil, &t); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "l2.%v version %v — %v\nstatus: %v\nsha256: %v\n\n--- source ---\n%v\n--- input schema ---\n", t["name"], t["version"],
			t["description"], t["status"], t["source_sha256"], t["source"])
		printJSON(stdout, t["input_schema"])
		fmt.Fprintln(stdout, "--- tests (input → expected) ---")
		printJSON(stdout, t["tests"])
		fmt.Fprintln(stdout, "--- sandbox report (static check, test results, benchmark) ---")
		printJSON(stdout, t["report"])
		if rest[0] == "show" {
			return nil
		}
		fmt.Fprintln(stdout, "The tool runs only in the WASM sandbox (no network, no files); approving lets tasks that list it call it.")
		if !*yes && !confirm(stdin, stdout, "Approve exactly this source?") {
			return errors.New("not approved")
		}
		var out map[string]any
		if err := c.do("POST", "/tools/"+pos[0]+"/approve", map[string]any{"sha256": t["source_sha256"]}, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "l2.%v v%v %v\n", out["name"], out["version"], out["status"])
		return nil
	case "reject", "disable":
		if len(rest) < 2 {
			return usageErr("tools " + rest[0] + " <id>")
		}
		body := map[string]any{}
		if rest[0] == "disable" && len(rest) > 2 {
			body["reason"] = strings.Join(rest[2:], " ")
		}
		var out map[string]any
		if err := c.do("POST", "/tools/"+rest[1]+"/"+rest[0], body, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "l2.%v v%v %v\n", out["name"], out["version"], out["status"])
		return nil
	}
	return usageErr(use)
}

func ownerData(c *apiClient, cmd string, rest []string, stdout io.Writer) error {
	if len(rest) == 0 {
		return usageErr(cmd + " needs a subcommand")
	}
	sub, args := rest[0], rest[1:]
	var out any
	var err error
	switch cmd + " " + sub {
	case "memory list":
		fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
		q := fs.String("q", "", "search text")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		path := "/memories"
		if *q != "" {
			path += "?q=" + url.QueryEscape(*q)
		}
		err = c.do("GET", path, nil, nil, &out)
	case "memory add":
		fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
		private := fs.Bool("private", false, "private fact")
		pos, perr := parse(fs, args)
		if perr != nil || len(pos) != 1 {
			return usageErr(`memory add "<fact>" [--private]`)
		}
		err = c.do("POST", "/memories", map[string]any{"content": pos[0], "private": *private}, nil, &out)
	case "memory correct":
		if len(args) != 2 {
			return usageErr(`memory correct <id> "<text>"`)
		}
		err = c.do("POST", "/memories/"+args[0]+"/correct", map[string]string{"content": args[1]}, nil, &out)
	case "memory delete":
		if len(args) != 1 {
			return usageErr("memory delete <id>")
		}
		err = c.do("DELETE", "/memories/"+args[0], nil, nil, &out)
	case "skill list":
		err = c.do("GET", "/skills", nil, nil, &out)
	case "skill show":
		fs := flag.NewFlagSet("skill show", flag.ContinueOnError)
		diff := fs.Bool("diff", false, "show a diff: --diff FROM TO")
		pos, perr := parse(fs, args)
		if perr != nil || len(pos) < 1 {
			return usageErr("skill show <id> [--diff FROM TO]")
		}
		path := "/skills/" + pos[0]
		if *diff && len(pos) == 3 {
			path += "?diff_from=" + pos[1] + "&diff_to=" + pos[2]
		}
		var res map[string]any
		if err = c.do("GET", path, nil, nil, &res); err == nil {
			if d, ok := res["diff"].(string); ok {
				fmt.Fprint(stdout, d)
				return nil
			}
			out = res
		}
	case "skill disable", "skill enable":
		if len(args) != 1 {
			return usageErr("skill " + sub + " <id>")
		}
		err = c.do("POST", "/skills/"+args[0]+"/"+sub, nil, nil, &out)
	case "skill rollback":
		if len(args) != 2 {
			return usageErr("skill rollback <id> <version>")
		}
		var v int
		fmt.Sscan(args[1], &v)
		err = c.do("POST", "/skills/"+args[0]+"/rollback", map[string]int{"version": v}, nil, &out)
	case "profile list":
		fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
		all := fs.Bool("all", false, "include deleted and superseded")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		path := "/profile"
		if *all {
			path += "?all=true"
		}
		err = c.do("GET", path, nil, nil, &out)
	case "profile add":
		if len(args) != 1 {
			return usageErr(`profile add "<preference>"`)
		}
		err = c.do("POST", "/profile", map[string]string{"statement": args[0]}, nil, &out)
	case "profile confirm", "profile disable", "profile enable", "profile delete":
		if len(args) != 1 {
			return usageErr("profile " + sub + " <id>")
		}
		err = c.do("POST", "/profile/"+args[0]+"/"+sub, nil, nil, &out)
	case "profile edit":
		if len(args) != 2 {
			return usageErr(`profile edit <id> "<text>"`)
		}
		err = c.do("POST", "/profile/"+args[0]+"/edit", map[string]string{"statement": args[1]}, nil, &out)
	default:
		return usageErr("unknown command " + cmd + " " + sub)
	}
	if err != nil {
		return err
	}
	printJSON(stdout, out)
	return nil
}

// triggerCmd implements `aura trigger`.
func triggerCmd(c *apiClient, rest []string, stdout io.Writer) error {
	if len(rest) == 0 {
		return usageErr("trigger list|add-task|add-watch|enable|disable|run|delete")
	}
	var out any
	switch rest[0] {
	case "list":
		if err := c.do("GET", "/triggers", nil, nil, &out); err != nil {
			return err
		}
	case "enable", "disable", "run", "delete":
		if len(rest) != 2 {
			return usageErr("trigger " + rest[0] + " <id>")
		}
		if err := c.do("POST", "/triggers/"+rest[1]+"/"+rest[0], nil, nil, &out); err != nil {
			return err
		}
	case "add-task", "add-watch":
		fs := flag.NewFlagSet(rest[0], flag.ContinueOnError)
		every := fs.Int64("every", 0, "interval in seconds (>= 60)")
		daily := fs.String("daily", "", "daily at HH:MM (workspace time zone) or HH:MM+07:00")
		var domains, sources multiFlag
		fs.Var(&domains, "allow-domain", "allowlisted domain (repeatable)")
		fs.Var(&sources, "source", "watched source URL (watch only, repeatable)")
		purpose := fs.String("purpose", "", "why you watch it (watch only)")
		pos, err := parse(fs, rest[1:])
		if err != nil {
			return err
		}
		if len(pos) < 1 {
			return usageErr(`trigger add-task|add-watch "<name>" [--objective ...] --every N | --daily HH:MM`)
		}
		body := map[string]any{"name": pos[0], "kind": strings.TrimPrefix(rest[0], "add-")}
		if *every > 0 {
			body["interval_seconds"] = *every
		}
		if *daily != "" {
			body["daily_at"] = *daily
		}
		if len(pos) > 1 {
			body["objective"] = pos[1]
		}
		spec := map[string]any{"allow_domains": []string(domains), "data_class": "public"}
		if rest[0] == "add-watch" {
			spec["sources"], spec["purpose"] = []string(sources), *purpose
		}
		body["spec"] = spec
		if err := c.do("POST", "/triggers", body, nil, &out); err != nil {
			return err
		}
	default:
		return usageErr("unknown trigger subcommand")
	}
	printJSON(stdout, out)
	return nil
}
