// Package config loads and validates the Core configuration (ADR-0002).
// Config holds paths to secrets, never secret values.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone database embedded: the container has none

	"gopkg.in/yaml.v3"
)

var identRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Tiers are the logical model tiers (Blueprint §8.5).
var Tiers = []string{"fast", "standard", "deep"}

type TaskBudget struct {
	MaxCostUSD     float64 `yaml:"max_cost_usd" json:"max_cost_usd"`
	MaxIterations  int     `yaml:"max_iterations" json:"max_iterations"`
	MaxWallSeconds int     `yaml:"max_wall_seconds" json:"max_wall_seconds"`
}

// ModelRoute is one OpenRouter route for a tier. Display name, provider API
// model ID and route ID are distinct values (Blueprint §13 model identity).
type ModelRoute struct {
	// Provider is "openrouter" (default) or the id of an openai_compatible provider.
	Provider        string  `yaml:"provider" json:"provider"`
	DisplayName     string  `yaml:"display_name" json:"display_name"`
	APIModelID      string  `yaml:"api_model_id" json:"api_model_id"`
	RouteID         string  `yaml:"route_id" json:"route_id"`
	ReasoningEffort string  `yaml:"reasoning_effort" json:"reasoning_effort"`
	PriceInPerMTok  float64 `yaml:"price_in_per_mtok" json:"price_in_per_mtok"`
	PriceOutPerMTok float64 `yaml:"price_out_per_mtok" json:"price_out_per_mtok"`
	MaxOutputTokens int     `yaml:"max_output_tokens" json:"max_output_tokens"`
	// Free marks a zero-price route. Only then may prices be 0; otherwise an
	// unknown price fails closed.
	Free bool `yaml:"free" json:"free"`
	// StructuredOutput is "native" (provider JSON-schema response format, the
	// default) or "prompt" (schema in the prompt; still validated in Core).
	StructuredOutput string `yaml:"structured_output" json:"structured_output"`
	// PromptLogging is "none" (default) or "may_log" when the endpoint may store
	// or train on prompts. may_log routes only serve tasks marked public.
	PromptLogging string `yaml:"prompt_logging" json:"prompt_logging"`
}

// MayLog reports whether the route may log prompts.
func (r ModelRoute) MayLog() bool { return r.PromptLogging == "may_log" }

type Config struct {
	ConfigVersion int `yaml:"config_version" json:"config_version"`
	// Timezone is the workspace time zone (Blueprint OL-08): every "today",
	// schedule and deadline uses it, never UTC by accident.
	Timezone string `yaml:"timezone" json:"timezone"`
	// PromptBudgetChars is the per-tier prompt budget (Blueprint OL-05): the
	// worker trims untrusted observations to fit; calls still over it are flagged.
	PromptBudgetChars map[string]int `yaml:"prompt_budget_chars" json:"prompt_budget_chars"`
	Server            struct {
		Bind      string `yaml:"bind" json:"bind"`
		TokenFile string `yaml:"token_file" json:"token_file"`
		RatePerS  int    `yaml:"rate_per_second" json:"rate_per_second"`
	} `yaml:"server" json:"server"`
	Database struct {
		Path      string `yaml:"path" json:"path"`
		BackupDir string `yaml:"backup_dir" json:"backup_dir"`
	} `yaml:"database" json:"database"`
	Workspace struct {
		Root string `yaml:"root" json:"root"`
	} `yaml:"workspace" json:"workspace"`
	Worker struct {
		Command            []string `yaml:"command" json:"command"`
		IdleTimeoutSeconds int      `yaml:"idle_timeout_seconds" json:"idle_timeout_seconds"`
		MaxWorkers         int      `yaml:"max_workers" json:"max_workers"`
		MaxConcurrentTasks int      `yaml:"max_concurrent_tasks" json:"max_concurrent_tasks"`
		ScratchRoot        string   `yaml:"scratch_root" json:"scratch_root"`
		CallTimeoutSeconds int      `yaml:"call_timeout_seconds" json:"call_timeout_seconds"`
	} `yaml:"worker" json:"worker"`
	Budgets struct {
		TaskDefault TaskBudget `yaml:"task_default" json:"task_default"`
		Global      struct {
			DailyCostUSD float64 `yaml:"daily_cost_usd" json:"daily_cost_usd"`
		} `yaml:"global" json:"global"`
	} `yaml:"budgets" json:"budgets"`
	Providers struct {
		OpenRouter struct {
			BaseURL        string `yaml:"base_url" json:"base_url"`
			KeyFile        string `yaml:"key_file" json:"key_file"`
			TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
		} `yaml:"openrouter" json:"openrouter"`
		// OpenAICompatible are additional providers (ADR-0021), e.g. a second
		// vendor's OpenAI-compatible endpoint, used as fallback routes.
		OpenAICompatible []struct {
			ID             string `yaml:"id" json:"id"`
			BaseURL        string `yaml:"base_url" json:"base_url"`
			KeyFile        string `yaml:"key_file" json:"key_file"`
			TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
		} `yaml:"openai_compatible" json:"openai_compatible"`
	} `yaml:"providers" json:"providers"`
	Models map[string][]ModelRoute `yaml:"models" json:"models"`
	Tools  struct {
		WebFetch struct {
			AllowDomains   []string `yaml:"allow_domains" json:"allow_domains"`
			MaxBytes       int64    `yaml:"max_bytes" json:"max_bytes"`
			TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
		} `yaml:"web.fetch" json:"web.fetch"`
		WebSearch struct {
			Provider      string `yaml:"provider" json:"provider"` // wikipedia | brave | none
			WikipediaLang string `yaml:"wikipedia_lang" json:"wikipedia_lang"`
			BraveKeyFile  string `yaml:"brave_key_file" json:"brave_key_file"`
			MaxResults    int    `yaml:"max_results" json:"max_results"`
		} `yaml:"web.search" json:"web.search"`
	} `yaml:"tools" json:"tools"`
	Approvals struct {
		TTLSeconds int `yaml:"ttl_seconds" json:"ttl_seconds"`
	} `yaml:"approvals" json:"approvals"`
	// Inputs is the timeout policy for unanswered clarification questions (Blueprint §8.2).
	Inputs struct {
		RemindAfterSeconds int    `yaml:"remind_after_seconds" json:"remind_after_seconds"` // 0 = no reminder
		TimeoutSeconds     int    `yaml:"timeout_seconds" json:"timeout_seconds"`
		OnTimeout          string `yaml:"on_timeout" json:"on_timeout"` // fail | continue (with stated assumptions)
	} `yaml:"inputs" json:"inputs"`
	Webhooks []Webhook `yaml:"webhooks" json:"webhooks"`
	MCP      struct {
		Servers []MCPServer `yaml:"servers" json:"servers"`
	} `yaml:"mcp" json:"mcp"`
	// Improvement configures the closed self-improvement loop (Blueprint §8A.5).
	Improvement struct {
		Enabled             bool    `yaml:"enabled" json:"enabled"`
		MinClassRuns        int     `yaml:"min_class_runs" json:"min_class_runs"`
		SuccessRateTarget   float64 `yaml:"success_rate_target" json:"success_rate_target"`
		MinRepeatedFailures int     `yaml:"min_repeated_failures" json:"min_repeated_failures"`
		LookbackDays        int     `yaml:"lookback_days" json:"lookback_days"`
		RetestRuns          int     `yaml:"retest_runs" json:"retest_runs"`
		NoiseMargin         float64 `yaml:"noise_margin" json:"noise_margin"`
		DailyCostUSD        float64 `yaml:"daily_cost_usd" json:"daily_cost_usd"`
	} `yaml:"improvement" json:"improvement"`
	// Charter is the owner-authored Charter (ADR-0024): read-only, pinned by sha256.
	Charter struct {
		File   string `yaml:"file" json:"file"`
		SHA256 string `yaml:"sha256" json:"sha256"`
	} `yaml:"charter" json:"charter"`
}

// MCPServer is an owner-declared MCP server (ADR-0020). Only the tools listed
// here are exposed, with the tier and side-effect class the owner set.
// Webhook is an authenticated inbound webhook (Blueprint §8.11, ADR-0034): a
// request signed with the secret raises the external event EventKey.
type Webhook struct {
	Name       string `yaml:"name" json:"name"`
	SecretFile string `yaml:"secret_file" json:"secret_file"` // >= 32 bytes, HMAC-SHA256 key
	EventKey   string `yaml:"event_key" json:"event_key"`
}

type MCPServer struct {
	Name           string             `yaml:"name" json:"name"`
	Command        []string           `yaml:"command" json:"command"`
	Env            map[string]string  `yaml:"env" json:"env"`           // non-secret values only
	KeyFile        string             `yaml:"key_file" json:"key_file"` // optional secret, passed as KeyEnv
	KeyEnv         string             `yaml:"key_env" json:"key_env"`
	TimeoutSeconds int                `yaml:"timeout_seconds" json:"timeout_seconds"`
	Tools          map[string]MCPTool `yaml:"tools" json:"tools"`
}

type MCPTool struct {
	Tier        string `yaml:"tier" json:"tier"`               // A0 | A2 | A3
	SideEffect  string `yaml:"side_effect" json:"side_effect"` // pure | read | write_idempotent | write_external
	Description string `yaml:"description" json:"description"` // owner-written; shown to the planner
}

// Default returns a config with every Blueprint default filled in.
func Default() *Config {
	c := &Config{ConfigVersion: 1, Timezone: "Asia/Jakarta"}
	c.Server.Bind = "127.0.0.1:7400"
	c.Server.TokenFile = "/var/lib/aura/api_token"
	c.Server.RatePerS = 20
	c.Database.Path = "/var/lib/aura/aura.db"
	c.Database.BackupDir = "/var/lib/aura/backups"
	c.Workspace.Root = "/var/lib/aura/workspace"
	c.Worker.Command = []string{"python3", "-I", "-m", "aura_worker"}
	c.Worker.IdleTimeoutSeconds = 60
	c.Worker.MaxWorkers = 1
	c.Worker.MaxConcurrentTasks = 1
	c.Worker.ScratchRoot = "/tmp/aura-scratch"
	c.Worker.CallTimeoutSeconds = 300
	c.Budgets.TaskDefault = TaskBudget{MaxCostUSD: 0.50, MaxIterations: 20, MaxWallSeconds: 900}
	c.Budgets.Global.DailyCostUSD = 5.00
	c.Providers.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	c.Providers.OpenRouter.TimeoutSeconds = 120
	c.Tools.WebFetch.MaxBytes = 2_000_000
	c.Tools.WebFetch.TimeoutSeconds = 20
	c.Tools.WebSearch.Provider = "wikipedia"
	c.Tools.WebSearch.WikipediaLang = "en"
	c.Tools.WebSearch.MaxResults = 8
	c.Approvals.TTLSeconds = 1800
	c.PromptBudgetChars = map[string]int{"fast": 24000, "standard": 60000, "deep": 120000}
	c.Inputs.RemindAfterSeconds, c.Inputs.TimeoutSeconds, c.Inputs.OnTimeout = 4*3600, 48*3600, "fail"
	c.Improvement.Enabled = true
	c.Improvement.MinClassRuns, c.Improvement.SuccessRateTarget, c.Improvement.MinRepeatedFailures = 10, 0.70, 3
	c.Improvement.LookbackDays, c.Improvement.RetestRuns, c.Improvement.NoiseMargin, c.Improvement.DailyCostUSD = 14, 5, 0.10, 0.25
	return c
}

// Load reads YAML strictly (unknown keys are errors), applies env overrides and validates.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(b, os.Getenv)
}

// Parse is Load without the file read; getenv is injectable for tests.
func Parse(b []byte, getenv func(string) string) (*Config, error) {
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := applyEnv(c, getenv); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// applyEnv supports AURA_<SECTION>_<KEY> overrides for scalar values only.
func applyEnv(c *Config, getenv func(string) string) error {
	str := map[string]*string{
		"AURA_SERVER_BIND":                   &c.Server.Bind,
		"AURA_SERVER_TOKEN_FILE":             &c.Server.TokenFile,
		"AURA_DATABASE_PATH":                 &c.Database.Path,
		"AURA_DATABASE_BACKUP_DIR":           &c.Database.BackupDir,
		"AURA_WORKSPACE_ROOT":                &c.Workspace.Root,
		"AURA_WORKER_SCRATCH_ROOT":           &c.Worker.ScratchRoot,
		"AURA_PROVIDERS_OPENROUTER_KEY_FILE": &c.Providers.OpenRouter.KeyFile,
		"AURA_PROVIDERS_OPENROUTER_BASE_URL": &c.Providers.OpenRouter.BaseURL,
	}
	for k, p := range str {
		if v := getenv(k); v != "" {
			*p = v
		}
	}
	num := map[string]*float64{
		"AURA_BUDGETS_GLOBAL_DAILY_COST_USD": &c.Budgets.Global.DailyCostUSD,
	}
	for k, p := range num {
		if v := getenv(k); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("config: %s: %w", k, err)
			}
			*p = f
		}
	}
	return nil
}

// Validate refuses configurations that would make the Core unsafe or ambiguous.
func (c *Config) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if c.ConfigVersion != 1 {
		add("config_version must be 1")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		add("timezone %q is not a valid IANA time zone", c.Timezone)
	}
	if c.Server.Bind == "" {
		add("server.bind is required")
	}
	if c.Database.Path == "" || c.Workspace.Root == "" {
		add("database.path and workspace.root are required")
	}
	if len(c.Worker.Command) == 0 {
		add("worker.command is required")
	}
	if c.Worker.MaxWorkers != 1 || c.Worker.MaxConcurrentTasks != 1 {
		add("worker.max_workers and worker.max_concurrent_tasks must be 1 in Phase 1 (ADR-0003)")
	}
	tb := c.Budgets.TaskDefault
	if tb.MaxCostUSD <= 0 || tb.MaxIterations <= 0 || tb.MaxWallSeconds <= 0 {
		add("budgets.task_default values must be positive")
	}
	if c.Budgets.Global.DailyCostUSD <= 0 {
		add("budgets.global.daily_cost_usd must be positive")
	}
	providers := map[string]bool{"openrouter": true}
	for i, p := range c.Providers.OpenAICompatible {
		if !identRe.MatchString(p.ID) || providers[p.ID] {
			add("providers.openai_compatible[%d]: id must be unique and match %s", i, identRe)
		}
		if !strings.HasPrefix(p.BaseURL, "https://") || p.KeyFile == "" {
			add("providers.openai_compatible[%d]: base_url must be https and key_file is required", i)
		}
		providers[p.ID] = true
	}
	for tier, routes := range c.Models {
		if !validTier(tier) {
			add("models: unknown tier %q", tier)
		}
		for i, r := range routes {
			if r.Provider != "" && !providers[r.Provider] {
				add("models.%s[%d]: unknown provider %q", tier, i, r.Provider)
			}
			if r.RouteID == "" || r.APIModelID == "" || r.DisplayName == "" {
				add("models.%s[%d]: display_name, api_model_id and route_id are required", tier, i)
			}
			switch {
			case r.Free && (r.PriceInPerMTok != 0 || r.PriceOutPerMTok != 0):
				add("models.%s[%d]: free routes must have price 0", tier, i)
			case r.Free && !r.MayLog():
				add("models.%s[%d]: free routes must declare prompt_logging: may_log", tier, i)
			case !r.Free && (r.PriceInPerMTok <= 0 || r.PriceOutPerMTok <= 0):
				add("models.%s[%d]: prices must be set (unknown cost fails closed)", tier, i)
			}
			switch r.StructuredOutput {
			case "", "native", "prompt":
			default:
				add("models.%s[%d]: structured_output must be native|prompt", tier, i)
			}
			switch r.PromptLogging {
			case "", "none", "may_log":
			default:
				add("models.%s[%d]: prompt_logging must be none|may_log", tier, i)
			}
			switch r.ReasoningEffort {
			case "", "low", "medium", "high":
			default:
				add("models.%s[%d]: reasoning_effort must be low|medium|high", tier, i)
			}
		}
	}
	switch c.Tools.WebSearch.Provider {
	case "wikipedia", "none":
	case "brave":
		if c.Tools.WebSearch.BraveKeyFile == "" {
			add("tools.web.search: provider brave needs brave_key_file")
		}
	default:
		add("tools.web.search.provider must be wikipedia, brave or none")
	}
	if c.Tools.WebSearch.MaxResults < 1 || c.Tools.WebSearch.MaxResults > 20 {
		add("tools.web.search.max_results must be 1..20")
	}
	if (c.Charter.File == "") != (c.Charter.SHA256 == "") {
		add("charter: file and sha256 go together (the Charter is pinned by hash)")
	}
	names := map[string]bool{}
	for i, s := range c.MCP.Servers {
		if !identRe.MatchString(s.Name) || names[s.Name] {
			add("mcp.servers[%d]: name must be unique and match %s", i, identRe)
		}
		names[s.Name] = true
		if len(s.Command) == 0 {
			add("mcp.servers[%d]: command is required", i)
		}
		if (s.KeyFile == "") != (s.KeyEnv == "") {
			add("mcp.servers[%d]: key_file and key_env go together", i)
		}
		for tn, tl := range s.Tools {
			if !identRe.MatchString(tn) {
				add("mcp.servers[%d].tools: bad tool name %q", i, tn)
			}
			switch tl.Tier {
			case "A0", "A2", "A3":
			default:
				add("mcp.servers[%d].tools.%s: tier must be A0, A2 or A3 (A1 is unsupported)", i, tn)
			}
			switch tl.SideEffect {
			case "pure", "read", "write_idempotent":
			case "write_external":
				if tl.Tier == "A0" {
					add("mcp.servers[%d].tools.%s: write_external tools need approval (A2) or are forbidden (A3)", i, tn)
				}
			default:
				add("mcp.servers[%d].tools.%s: side_effect must be pure, read, write_idempotent or write_external", i, tn)
			}
		}
	}
	if i := c.Improvement; i.MinClassRuns < 1 || i.MinRepeatedFailures < 1 || i.LookbackDays < 1 || i.RetestRuns < 1 ||
		i.SuccessRateTarget <= 0 || i.SuccessRateTarget > 1 || i.NoiseMargin < 0 || i.NoiseMargin > 1 || i.DailyCostUSD < 0 {
		add("improvement: thresholds must be positive; success_rate_target in (0,1]; noise_margin in [0,1]; daily_cost_usd >= 0")
	}
	for tier, n := range c.PromptBudgetChars {
		if !validTier(tier) || n < 4000 {
			add("prompt_budget_chars: keys fast|standard|deep, each at least 4000")
			break
		}
	}
	hookNames := map[string]bool{}
	for _, h := range c.Webhooks {
		if !webhookNameRe.MatchString(h.Name) || hookNames[h.Name] || h.SecretFile == "" || !eventKeyRe.MatchString(h.EventKey) {
			add("webhooks: each needs a unique name [a-z0-9-]{1,40}, a secret_file and an event_key [A-Za-z0-9._:-]{1,100}")
			break
		}
		hookNames[h.Name] = true
	}
	if c.Approvals.TTLSeconds <= 0 {
		add("approvals.ttl_seconds must be positive")
	}
	if in := c.Inputs; in.TimeoutSeconds <= 0 || in.RemindAfterSeconds < 0 || in.RemindAfterSeconds >= in.TimeoutSeconds ||
		(in.OnTimeout != "fail" && in.OnTimeout != "continue") {
		add("inputs: timeout_seconds must be positive, remind_after_seconds in [0, timeout_seconds), on_timeout fail|continue")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config invalid: %s", strings.Join(errs, "; "))
	}
	return nil
}

var (
	webhookNameRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	eventKeyRe    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)
)

func validTier(t string) bool {
	for _, x := range Tiers {
		if x == t {
			return true
		}
	}
	return false
}

// Location returns the workspace time zone (validated at load).
func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Hash is a stable hash of the effective config (secret paths, never values).
func (c *Config) Hash() string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReadSecret reads a secret file after checking it is a regular file (not a
// symlink), owned by the current user, with mode no wider than 0440.
// Errors name the path, never the content.
func ReadSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New("secret path is empty")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("secret %s: must be a regular file, not a symlink", path)
	}
	if perm := fi.Mode().Perm(); perm&^0o440 != 0 {
		return "", fmt.Errorf("secret %s: mode %04o is wider than 0440", path, perm)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return "", fmt.Errorf("secret %s: not owned by the Core user", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("secret %s: empty", path)
	}
	return s, nil
}
