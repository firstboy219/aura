// Package app wires the Core together for `aura serve` (and for the crash
// test, which runs the same wiring in a child process).
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"aura.local/core/internal/api"
	"aura.local/core/internal/budget"
	"aura.local/core/internal/config"
	"aura.local/core/internal/engine"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/mcp"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/worker"
)

// MinFreeBytes is the free-space floor below which new tasks are refused (ADR-0001).
const MinFreeBytes = 1 << 30

// Options let tests replace the provider and the fetch guard.
type Options struct {
	Provider gateway.Provider
	Guard    *tools.Guard
	Searcher tools.Searcher // tests; nil = from config
	// ExtraProviders replace or add providers by id (tests).
	ExtraProviders map[string]gateway.Provider
	Log            *slog.Logger
	Version        string
}

// App is a running Core.
type App struct {
	Cfg      *config.Config
	Store    *store.Store
	Engine   *engine.Engine
	Worker   *worker.Supervisor
	Governor *budget.Governor
	API      *api.Server
	MCP      map[string]*mcp.Client
	Log      *slog.Logger
	keyOK    bool
	srv      *http.Server
}

// unconfigured is the provider used when no key file is configured: every call
// fails permanently with a clear reason instead of pretending to work.
type unconfigured struct{}

func (unconfigured) ID() string { return "openrouter" }
func (unconfigured) Complete(context.Context, config.ModelRoute, gateway.Request) (*gateway.Attempt, error) {
	return nil, &gateway.ProviderError{Status: 0, Transient: false, Msg: "provider_not_configured"}
}

// NewLogger returns the redacting JSON logger.
func NewLogger() *slog.Logger {
	return slog.New(obs.Handler{Handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})})
}

// Build opens the store and constructs every component (nothing runs yet).
func Build(ctx context.Context, cfg *config.Config, o Options) (*App, error) {
	log := o.Log
	if log == nil {
		log = NewLogger()
	}
	a := &App{Cfg: cfg, Log: log}
	st, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BackupDir)
	if err != nil {
		return nil, err
	}
	a.Store = st
	log.Info("store open", "path", cfg.Database.Path, "sqlite", st.SQLiteVersion())

	token, err := EnsureToken(cfg.Server.TokenFile)
	if err != nil {
		st.Close()
		return nil, err
	}
	obs.RegisterSecret(token)

	prov := o.Provider
	if prov == nil {
		if cfg.Providers.OpenRouter.KeyFile == "" {
			log.Warn("no provider key file configured; model calls will fail with provider_not_configured")
			prov = unconfigured{}
		} else {
			key, err := config.ReadSecret(cfg.Providers.OpenRouter.KeyFile)
			if err != nil {
				st.Close()
				return nil, err
			}
			obs.RegisterSecret(key)
			prov = gateway.NewOpenRouter(cfg.Providers.OpenRouter.BaseURL, key,
				time.Duration(cfg.Providers.OpenRouter.TimeoutSeconds)*time.Second)
			a.keyOK = true
		}
	} else {
		a.keyOK = true
	}
	if len(cfg.Models) == 0 {
		log.Warn("no model routes configured; model calls will fail with no_route_for_tier")
	}
	a.Governor = budget.New(st, cfg.Budgets.Global.DailyCostUSD)
	a.Governor.SetLocation(cfg.Location())
	provs := map[string]gateway.Provider{"openrouter": prov}
	for _, p := range cfg.Providers.OpenAICompatible {
		key, err := config.ReadSecret(p.KeyFile)
		if err != nil {
			st.Close()
			return nil, err
		}
		obs.RegisterSecret(key)
		timeout := time.Duration(p.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		provs[p.ID] = gateway.NewOpenAICompatible(p.ID, p.BaseURL, key, timeout)
	}
	for id, p := range o.ExtraProviders {
		provs[id] = p
	}
	gw := gateway.NewMulti(provs, cfg.Models, a.Governor)
	gw.Notify = func(event string, data map[string]any) { // R0 self-healing is always recorded (Blueprint §8A.5)
		st.Write(context.Background(), func(tx *sql.Tx) error { return policy.Audit(context.Background(), tx, event, "core", "", "", data) })
		log.Warn("self-healing", "event", event, "data", data)
	}
	ws, err := tools.NewWorkspace(cfg.Workspace.Root)
	if err != nil {
		st.Close()
		return nil, err
	}
	guard := o.Guard
	if guard == nil {
		guard = tools.NewGuard()
	}
	srch := o.Searcher
	if srch == nil {
		switch cfg.Tools.WebSearch.Provider {
		case "wikipedia":
			srch = tools.NewWikipedia(tools.NewGuard(), cfg.Tools.WebSearch.WikipediaLang)
		case "brave":
			key, err := config.ReadSecret(cfg.Tools.WebSearch.BraveKeyFile)
			if err != nil {
				st.Close()
				return nil, err
			}
			obs.RegisterSecret(key)
			srch = tools.NewBrave(tools.NewGuard(), key)
		}
	}
	charter, err := LoadCharter(cfg.Charter.File, cfg.Charter.SHA256)
	if err != nil {
		// Charter §5: on a hash mismatch Charter-dependent features are disabled and a
		// security event is raised; the Core keeps running with the Blueprint baseline rules.
		log.Error("charter not loaded", "err", err)
		st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "CHARTER_HASH_MISMATCH", "core", "", "", map[string]any{"file": cfg.Charter.File})
		})
		charter = ""
	}
	clients, docs, err := buildMCP(cfg, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.MCP = clients
	a.Worker = worker.New(worker.Options{Command: cfg.Worker.Command, ScratchRoot: cfg.Worker.ScratchRoot,
		IdleTimeout: time.Duration(cfg.Worker.IdleTimeoutSeconds) * time.Second, Log: log})
	a.Engine = engine.New(engine.Deps{Config: cfg, Store: st, Gateway: gw, Governor: a.Governor, Worker: a.Worker,
		Fetcher:   tools.NewFetcher(guard, cfg.Tools.WebFetch.MaxBytes, time.Duration(cfg.Tools.WebFetch.TimeoutSeconds)*time.Second),
		Workspace: ws, Searcher: srch, MCP: clients, ToolDocs: docs, Charter: charter, Version: o.Version, Log: log})
	a.Engine.Backfill(ctx)
	a.API = &api.Server{Engine: a.Engine, Store: st, Governor: a.Governor, Worker: a.Worker, Token: token, Version: o.Version,
		Log: log, RatePerSec: cfg.Server.RatePerS, Ready: a.ready, AcceptTasks: a.acceptTasks}
	return a, nil
}

func (a *App) ready(ctx context.Context) map[string]string {
	c := map[string]string{"database": "ok", "provider_key": "ok", "disk": "ok", "kill_switch": "ok"}
	var one int
	if err := a.Store.R.QueryRowContext(ctx, "select 1").Scan(&one); err != nil {
		c["database"] = "error"
	}
	if !a.keyOK {
		c["provider_key"] = "missing"
	}
	if FreeBytes(filepath.Dir(a.Cfg.Database.Path)) < MinFreeBytes {
		c["disk"] = "low"
	}
	if a.Governor.Halted() {
		c["kill_switch"] = "active"
	}
	return c
}

func (a *App) acceptTasks() string {
	if FreeBytes(filepath.Dir(a.Cfg.Database.Path)) < MinFreeBytes {
		return "disk_pressure"
	}
	return ""
}

// FreeBytes is the space available to unprivileged users on the filesystem of dir.
func FreeBytes(dir string) uint64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0
	}
	return s.Bavail * uint64(s.Bsize)
}

// EnsureToken reads the API token file or creates it (256-bit, mode 0400).
func EnsureToken(path string) (string, error) {
	if tok, err := config.ReadSecret(path); err == nil {
		return tok, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", fmt.Errorf("token file %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(tok + "\n"); err != nil {
		return "", err
	}
	return tok, nil
}

// SyncModels verifies configured routes against the provider catalog (no
// spend) and records them in the registry. Failures are logged, not fatal.
func (a *App) SyncModels(ctx context.Context) []gateway.RouteCheck {
	var catalog map[string]gateway.ModelInfo
	or := gateway.NewOpenRouter(a.Cfg.Providers.OpenRouter.BaseURL, "", 15*time.Second)
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if c, err := or.ListModels(cctx); err != nil {
		a.Log.Warn("model catalog unavailable", "err", err)
	} else {
		catalog = c
	}
	checks := gateway.VerifyRoutes(a.Cfg.Models, catalog)
	for _, c := range checks {
		a.Log.Info("model route", "tier", c.Tier, "route", c.RouteID, "health", c.Health, "detail", c.Detail)
	}
	if err := gateway.SyncRegistry(ctx, a.Store, a.Cfg.Providers.OpenRouter.BaseURL, a.Cfg.Providers.OpenRouter.KeyFile, a.Cfg.Models, checks); err != nil {
		a.Log.Error("registry sync", "err", err)
	}
	return checks
}

// maintain checkpoints the WAL, backs up daily and prunes old backups.
func (a *App) maintain(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	lastBackup := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		mode := "PASSIVE"
		if a.Store.WALBytes() > 64<<20 {
			mode = "TRUNCATE"
		}
		if err := a.Store.Checkpoint(ctx, mode); err != nil {
			a.Log.Warn("wal checkpoint", "mode", mode, "err", err)
		}
		if time.Since(lastBackup) > 24*time.Hour {
			if p, err := a.Store.Backup(ctx, "daily"); err != nil {
				a.Log.Error("daily backup", "err", err)
			} else if n, err := store.RestoreDrill(ctx, p); err != nil {
				a.Log.Error("daily restore drill failed", "path", p, "err", err)
			} else {
				a.Log.Info("daily backup verified by restore drill", "path", p, "tasks", n)
				lastBackup = time.Now()
				pruneBackups(a.Cfg.Database.BackupDir, 7)
			}
			if n, err := a.Engine.PurgeContent(ctx); err != nil {
				a.Log.Error("content purge", "err", err)
			} else if n > 0 {
				a.Log.Info("expired content purged", "checkpoints", n)
			}
		}
	}
}

func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "aura-") && strings.HasSuffix(e.Name(), "-daily.db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// Serve runs the engine, maintenance and the HTTP server until ctx ends, then
// shuts down gracefully (in-flight tasks are left for recovery).
func (a *App) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.Cfg.Server.Bind)
	if err != nil {
		return err
	}
	return a.ServeListener(ctx, ln)
}

// ServeListener is Serve on an existing listener.
func (a *App) ServeListener(ctx context.Context, ln net.Listener) error {
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	engineDone := make(chan error, 1)
	go func() { engineDone <- a.Engine.Run(ectx) }()
	go a.maintain(ectx)
	a.srv = &http.Server{Handler: a.API.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	srvErr := make(chan error, 1)
	go func() { srvErr <- a.srv.Serve(ln) }()
	a.Log.Info("aura serving", "addr", ln.Addr().String())
	var err error
	select {
	case <-ctx.Done():
	case err = <-srvErr:
	case err = <-engineDone:
	}
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	a.srv.Shutdown(sctx)
	cancel()
	a.Worker.Close()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// Close releases the store and stops MCP servers.
func (a *App) Close() {
	for _, c := range a.MCP {
		c.Close()
	}
	a.Store.Close()
}

// LoadCharter reads the owner's Charter read-only and verifies its pinned
// sha256 (ADR-0024). A mismatch refuses startup; no file means not provided.
func LoadCharter(path, sha string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("charter: %w", err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, sha) {
		return "", fmt.Errorf("charter: %s has sha256 %s, config pins %s — refusing to start with an unapproved Charter", path, got, sha)
	}
	if len(b) > 64<<10 {
		return "", fmt.Errorf("charter: %s is larger than 64 KiB", path)
	}
	return string(b), nil
}

// buildMCP starts nothing yet: it registers the owner-declared MCP tools in the
// trusted manifest and prepares one client per server (started on first use).
func buildMCP(cfg *config.Config, log *slog.Logger) (map[string]*mcp.Client, map[string]string, error) {
	clients := map[string]*mcp.Client{}
	docs := map[string]string{}
	for _, s := range cfg.MCP.Servers {
		env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
		if s.KeyFile != "" {
			key, err := config.ReadSecret(s.KeyFile)
			if err != nil {
				return nil, nil, err
			}
			obs.RegisterSecret(key)
			env = append(env, s.KeyEnv+"="+key)
		}
		clients[s.Name] = mcp.New(s.Name, s.Command, env, time.Duration(s.TimeoutSeconds)*time.Second, log)
		for name, tl := range s.Tools {
			id := "mcp." + s.Name + "." + name
			policy.RegisterMCPTool(id, tl.SideEffect, policy.Tier(tl.Tier))
			docs[id] = tl.Description
		}
	}
	return clients, docs, nil
}
