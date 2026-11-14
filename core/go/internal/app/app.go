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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"aura.local/core/internal/api"
	"aura.local/core/internal/aul"
	"aura.local/core/internal/browser"
	"aura.local/core/internal/budget"
	"aura.local/core/internal/channel"
	"aura.local/core/internal/config"
	"aura.local/core/internal/egress"
	"aura.local/core/internal/engine"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/mcp"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/sandbox"
	"aura.local/core/internal/store"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/vault"
	"aura.local/core/internal/worker"
)

// MinFreeBytes is the free-space floor below which new tasks are refused (ADR-0001).
const MinFreeBytes = 1 << 30

// Options let tests replace the provider and the fetch guard.
type Options struct {
	Provider gateway.Provider
	Guard    *tools.Guard
	Searcher tools.Searcher      // tests; nil = from config
	Pictures tools.PictureFinder // tests; nil = Wikipedia unless Searcher is set
	News     engine.NewsSource   // tests; nil = the default news feeds unless Searcher is set
	Info     engine.InfoSource   // tests; nil = the public info services unless Searcher is set
	// ExtraProviders replace or add providers by id (tests).
	ExtraProviders map[string]gateway.Provider
	Log            *slog.Logger
	Version        string
}

// App is a running Core.
type App struct {
	Cfg         *config.Config
	Store       *store.Store
	Engine      *engine.Engine
	Worker      *worker.Supervisor
	Governor    *budget.Governor
	API         *api.Server
	MCP         map[string]*mcp.Client
	Log         *slog.Logger
	keyOK       bool
	Vault       *vault.Vault // nil when secrets.master_key_file is not set
	Sandbox     *sandbox.Runtime
	AUL         *aul.Runtime
	Egress      *egress.Proxy // browser egress proxy (nil: browser automation off)
	MoreWorkers []*worker.Supervisor
	srv         *http.Server
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
	if a.Vault, err = OpenVault(cfg); err != nil {
		st.Close()
		return nil, err
	}

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
			key, err := a.secret(ctx, cfg.Providers.OpenRouter.KeyFile)
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
		key, err := a.secret(ctx, p.KeyFile)
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
	gw.Rescue = !cfg.Routing.NoTierRescue
	gw.Patience = map[string]time.Duration{}
	for tier, def := range map[string]int{"fast": 20, "standard": 45, "deep": 90} {
		if v, ok := cfg.Routing.FreePatienceSeconds[tier]; ok {
			def = v
		}
		if def > 0 {
			gw.Patience[tier] = time.Duration(def) * time.Second
		}
	}
	gw.Notify = func(event string, data map[string]any) { // R0 self-healing is always recorded (Blueprint §8A.5)
		st.Write(context.Background(), func(tx *sql.Tx) error { return policy.Audit(context.Background(), tx, event, "core", "", "", data) })
		log.Warn("self-healing", "event", event, "data", data)
		if event == "PROVIDER_REFUSED" {
			// Only the owner can replace a key or add credit, so the owner is told once
			// per change (the gateway reports the transition, not every refusal).
			body, _ := json.Marshal(data)
			st.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(context.Background(), "insert into notifications(id, kind, title, body_json, created_at) values(?,?,?,?,?)",
					ids.New(), "info", fmt.Sprintf("Model provider %v refuses AURA's key or credit (HTTP %v) — owner action needed", data["provider"], data["status"]),
					string(body), ids.NowMs())
				return err
			})
		}
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
	pics := o.Pictures
	if pics == nil && o.Searcher == nil {
		pics = tools.NewWikiPictures(tools.NewGuard(), "en")
	}
	var news engine.NewsSource = o.News
	if news == nil && o.Searcher == nil {
		news = tools.NewNews(tools.NewGuard(), nil)
	}
	var info engine.InfoSource = o.Info
	if info == nil && o.Searcher == nil {
		info = tools.NewInfo(tools.NewGuard(), cfg.Location(), cfg.HomeCity)
	}
	srch := o.Searcher
	if srch == nil {
		switch cfg.Tools.WebSearch.Provider {
		case "wikipedia":
			srch = tools.NewWikipedia(tools.NewGuard(), cfg.Tools.WebSearch.WikipediaLang)
		case "brave":
			key, err := a.secret(ctx, cfg.Tools.WebSearch.BraveKeyFile)
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
	clients, docs, err := buildMCP(ctx, cfg, log, a.secret)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.MCP = clients
	gens := &atomic.Int64{} // worker generations unique across the pool (grants are bound to them)
	newWorker := func() *worker.Supervisor {
		return worker.New(worker.Options{Command: cfg.Worker.Command, ScratchRoot: cfg.Worker.ScratchRoot,
			IdleTimeout: time.Duration(cfg.Worker.IdleTimeoutSeconds) * time.Second, Log: log, GenSource: gens})
	}
	a.Worker = newWorker()
	var moreWorkers []*worker.Supervisor
	for i := 1; i < cfg.Worker.MaxConcurrentTasks; i++ { // ADR-0047: one worker per concurrently driven task
		moreWorkers = append(moreWorkers, newWorker())
	}
	a.MoreWorkers = moreWorkers
	// L2 tools (ADR-0038): the WASM sandbox is optional; without the pinned
	// Python module, R3 remedies are escalated to the owner instead.
	var sbx *sandbox.Runtime
	if cfg.L2.PythonWASM != "" {
		if _, err := os.Stat(cfg.L2.PythonWASM); err == nil {
			sbx, err = sandbox.NewPython(ctx, cfg.L2.PythonWASM, cfg.L2.CacheDir, sandbox.Limits{})
			if err != nil {
				st.Close()
				return nil, fmt.Errorf("l2 sandbox: %w", err)
			}
			a.Sandbox = sbx
		} else {
			log.Info("L2 sandbox disabled: python module not found", "path", cfg.L2.PythonWASM)
		}
	}
	// AUL workflows (ADR-0050): the VM module is optional like the L2 sandbox.
	var aulRT *aul.Runtime
	if cfg.AUL.VMWasm != "" {
		if _, err := os.Stat(cfg.AUL.VMWasm); err == nil {
			aulRT, err = aul.NewRuntime(ctx, cfg.AUL.VMWasm, cfg.L2.CacheDir, aul.Limits{MaxCalls: cfg.AUL.MaxCalls,
				Timeout: time.Duration(cfg.AUL.TimeoutSeconds) * time.Second})
			if err != nil {
				st.Close()
				return nil, fmt.Errorf("aul vm: %w", err)
			}
			a.AUL = aulRT
		} else {
			log.Info("AUL workflows disabled: VM module not found", "path", cfg.AUL.VMWasm)
		}
	}
	a.Engine = engine.New(engine.Deps{Config: cfg, Store: st, Gateway: gw, Governor: a.Governor, Worker: a.Worker,
		Fetcher:   tools.NewFetcher(guard, cfg.Tools.WebFetch.MaxBytes, time.Duration(cfg.Tools.WebFetch.TimeoutSeconds)*time.Second),
		Workspace: ws, Searcher: srch, Pictures: pics, News: news, Info: info, MCP: clients, ToolDocs: docs, Charter: charter, Sandbox: sbx, AUL: aulRT, Secret: a.secret, Vault: a.Vault, MoreWorkers: moreWorkers, Replies: !cfg.Reply.Disabled, Version: o.Version, Log: log})
	a.Engine.Backfill(ctx)
	if gh := cfg.Connectors.GitHub; gh.Enabled {
		tok, err := a.secret(ctx, gh.TokenRef)
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("connectors.github: %w", err)
		}
		obs.RegisterSecret(tok)
		a.Engine.SetGitHubConnector(&engine.GitHubConnector{Token: tok, Repos: gh.Repos})
	}
	if cfg.Browser.Socket != "" || cfg.Shell.Socket != "" {
		a.Egress = egress.New(tools.NewGuard(), log)
		a.Egress.OnDeny = a.Engine.RecordEgressDenial
	}
	if cfg.Shell.Socket != "" {
		// The sandbox reaches the Core's egress proxy by the Core's container name on the shell network.
		a.Engine.SetShell(cfg.Shell.Socket, a.Egress, "aura-core"+cfg.Browser.ProxyListen, cfg.Shell.TimeoutSeconds)
	}
	if cfg.Browser.Socket != "" {
		bc := browser.New(cfg.Browser.Socket)
		a.Engine.SetBrowser(bc, a.Egress)
		if o.Searcher == nil && cfg.Tools.WebSearch.Provider == "chromium" {
			names := cfg.Tools.WebSearch.Engines
			if len(names) == 0 {
				names = browser.Engines()
			}
			langs := []string{cfg.Tools.WebSearch.WikipediaLang}
			for _, l := range []string{"id", "en"} {
				if !slices.Contains(langs, l) {
					langs = append(langs, l)
				}
			}
			var wiki tools.Multi
			for _, l := range langs {
				wiki = append(wiki, tools.NewWikipedia(tools.NewGuard(), l))
			}
			// the browser's engines first; when they find nothing (Bing serves unrelated results, DuckDuckGo a 403 to a headless browser) a plain HTTP request to
			// DuckDuckGo's HTML endpoint, and Wikipedia last (ADR-0068)
			a.Engine.SetSearcher(browser.NewSearcher(bc, a.Egress, names, tools.FirstWithResults{tools.NewDuckDuckGoHTML(tools.NewGuard()), wiki}))
		}
	}
	if n := a.Engine.RegisterActiveL2(ctx); n > 0 {
		log.Info("L2 tools registered", "count", n)
	}
	a.API = &api.Server{Engine: a.Engine, Store: st, Governor: a.Governor, Worker: a.Worker, Token: token, Version: o.Version,
		Log: log, RatePerSec: cfg.Server.RatePerS, Ready: a.ready, AcceptTasks: a.acceptTasks, Webhooks: map[string]api.Webhook{},
		Vault: a.Vault, Web: api.WebConfig{Enabled: cfg.Web.Enabled, Origin: cfg.Web.Origin, APIHosts: cfg.Web.APIHosts, SessionHours: cfg.Web.SessionHours}}
	// The owner's runtime daily budget overrides the config value (ADR-0043).
	var budgetSetting string
	if st.R.QueryRowContext(ctx, "select value from settings where key=?", api.SettingDailyBudget).Scan(&budgetSetting) == nil {
		if v, err := strconv.ParseFloat(budgetSetting, 64); err == nil {
			a.Governor.SetLimit(v)
		}
	}
	for _, c := range cfg.Web.TrustedProxyCIDRs {
		a.API.Web.TrustedProxy = append(a.API.Web.TrustedProxy, netip.MustParsePrefix(c))
	}
	for _, h := range cfg.Webhooks {
		secret, err := a.secret(ctx, h.SecretFile)
		if err != nil || len(secret) < 32 {
			return nil, fmt.Errorf("webhook %s: secret_file must hold at least 32 bytes", h.Name)
		}
		a.API.Webhooks[h.Name] = api.Webhook{Secret: []byte(secret), EventKey: h.EventKey}
	}
	return a, nil
}

// OpenVault loads the master key (and the previous one during a rotation).
func OpenVault(cfg *config.Config) (*vault.Vault, error) {
	if cfg.Secrets.MasterKeyFile == "" {
		return nil, nil
	}
	load := func(path string) ([]byte, error) {
		text, err := config.ReadSecret(path)
		if err != nil {
			return nil, err
		}
		return vault.ParseKey(text)
	}
	cur, err := load(cfg.Secrets.MasterKeyFile)
	if err != nil {
		return nil, fmt.Errorf("secrets.master_key_file: %w", err)
	}
	var prev []byte
	if cfg.Secrets.PreviousKeyFile != "" {
		if prev, err = load(cfg.Secrets.PreviousKeyFile); err != nil {
			return nil, fmt.Errorf("secrets.previous_key_file: %w", err)
		}
	}
	return vault.New(cur, prev)
}

// secret resolves a configured secret reference: "vault:<name>" from the
// encrypted store, anything else as a 0400 file (ADR-0036).
func (a *App) secret(ctx context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, vault.RefPrefix)
	if !ok {
		return config.ReadSecret(ref)
	}
	if a.Vault == nil {
		return "", fmt.Errorf("secret %q needs secrets.master_key_file", ref)
	}
	return a.Vault.Get(ctx, a.Store.R, name)
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
// denyListener closes connections from denied networks before any byte is read.
type denyListener struct {
	net.Listener
	deny []netip.Prefix
	log  *slog.Logger
}

func (l denyListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ap, perr := netip.ParseAddrPort(c.RemoteAddr().String())
		denied := false
		for _, p := range l.deny {
			if perr == nil && p.Contains(ap.Addr().Unmap()) {
				denied = true
			}
		}
		if !denied {
			return c, nil
		}
		l.log.Warn("API connection from a denied network closed", "remote", c.RemoteAddr().String())
		c.Close()
	}
}

func (a *App) ServeListener(ctx context.Context, ln net.Listener) error {
	if len(a.Cfg.Server.DenyCIDRs) > 0 {
		var deny []netip.Prefix
		for _, c := range a.Cfg.Server.DenyCIDRs {
			deny = append(deny, netip.MustParsePrefix(c))
		}
		ln = denyListener{Listener: ln, deny: deny, log: a.Log}
	}
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	engineDone := make(chan error, 1)
	go func() { engineDone <- a.Engine.Run(ectx) }()
	go a.maintain(ectx)
	if tg := a.Cfg.Channels.Telegram; tg.Enabled {
		if tok, err := a.secret(ectx, tg.TokenRef); err != nil {
			a.Log.Error("telegram channel disabled: token unavailable", "err", err)
		} else {
			bot := channel.NewTelegram(channel.TelegramConfig{Token: tok, AllowedUserIDs: tg.AllowedUserIDs, DefaultTools: tg.DefaultTools,
				DefaultDomains: tg.DefaultDomains, WebOrigin: a.Cfg.Web.Origin}, a.Engine, a.Store, a.Log)
			go bot.Run(ectx)
		}
	}
	if a.Egress != nil {
		go func() {
			if err := a.Egress.Serve(ectx, a.Cfg.Browser.ProxyListen); err != nil {
				a.Log.Error("browser egress proxy", "err", err)
			}
		}()
	}
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
	for _, w := range a.MoreWorkers {
		w.Close()
	}
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
func buildMCP(ctx context.Context, cfg *config.Config, log *slog.Logger, secret func(context.Context, string) (string, error)) (map[string]*mcp.Client, map[string]string, error) {
	clients := map[string]*mcp.Client{}
	docs := map[string]string{}
	for _, s := range cfg.MCP.Servers {
		env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
		if s.KeyFile != "" {
			key, err := secret(ctx, s.KeyFile)
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
