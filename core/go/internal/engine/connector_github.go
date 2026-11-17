package engine

import (
	"aura.local/core/internal/egresslog"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// First connector pilot (Blueprint D.1 P5b, ADR-0045): GitHub, read-only.
// It proves an explicitly scoped service principal (a fine-grained, read-only
// token the owner creates for chosen repositories), Core-side authorization
// (the Core checks the repository against its own allowlist before any call,
// whatever the token could reach), audit of every call, and the workspace
// boundary (the token lives in this workspace's vault). Results are untrusted
// observations.

// GitHubConnector is the configured connector.
type GitHubConnector struct {
	Token   string
	Repos   []string // owner/name allowlist
	BaseURL string   // https://api.github.com (tests: a fake server)
	hc      *http.Client
}

// ConnectorGitHubTool is the tool id.
const ConnectorGitHubTool = "connector.github.read"

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// SetGitHubConnector enables the connector (app wiring).
func (e *Engine) SetGitHubConnector(c *GitHubConnector) {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.github.com"
	}
	c.hc = &http.Client{Timeout: 20 * time.Second, Transport: egresslog.Wrap("github", nil)}
	e.github = c
	policy.RegisterConnectorTool(ConnectorGitHubTool)
	if e.docs == nil {
		e.docs = map[string]string{}
	}
	e.docs[ConnectorGitHubTool] = "read GitHub (read-only) in the allowed repositories " + strings.Join(c.Repos, ", ") +
		`. Input: {"repo":"owner/name","kind":"issues|pulls|commits|file","path":"<file path, for kind file>","number":<issue or pull number, optional>}.`
}

func (c *GitHubConnector) allowedRepo(repo string) bool {
	for _, r := range c.Repos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

// executeGitHub runs one read call.
func (e *Engine) executeGitHub(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	fail := func(err error) (any, error) {
		e.toolFailed(ctx, t, st, spec.ID, err)
		return nil, err
	}
	c := e.github
	if c == nil {
		return fail(errors.New("tool_failed: the GitHub connector is not configured"))
	}
	in := args // the planned input object, already matched against the plan
	repo, _ := in["repo"].(string)
	kind, _ := in["kind"].(string)
	if !repoRe.MatchString(repo) || !c.allowedRepo(repo) {
		// Core-side authorization: the token's own reach does not matter.
		e.st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "CONNECTOR_DENIED", "core", t.ID, "", map[string]any{"connector": "github", "repo": repo})
		})
		return fail(fmt.Errorf("tool_failed: repository %q is not in the connector's allowlist", repo))
	}
	var p string
	switch kind {
	case "issues", "pulls", "commits":
		p = "/repos/" + repo + "/" + kind + "?per_page=20"
		if n, ok := in["number"].(float64); ok && n > 0 && kind != "commits" {
			p = "/repos/" + repo + "/" + kind + "/" + strconv.Itoa(int(n))
		}
	case "file":
		fp, _ := in["path"].(string)
		if fp == "" || strings.Contains(fp, "..") || len(fp) > 300 {
			return fail(errors.New("tool_failed: a file path is required"))
		}
		p = "/repos/" + repo + "/contents/" + (&url.URL{Path: strings.TrimPrefix(fp, "/")}).EscapedPath()
	default:
		return fail(errors.New("tool_failed: kind must be issues, pulls, commits or file"))
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+p, nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if kind == "file" {
		req.Header.Set("Accept", "application/vnd.github.raw")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fail(errors.New("tool_failed: GitHub unreachable"))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	e.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "CONNECTOR_CALL", "core", t.ID, "", map[string]any{"connector": "github", "repo": repo, "kind": kind,
			"status": resp.StatusCode, "bytes": len(body)})
	})
	if resp.StatusCode >= 400 {
		return fail(fmt.Errorf("tool_failed: GitHub returned HTTP %d", resp.StatusCode))
	}
	content := string(body)
	if kind != "file" {
		content = compactGitHub(body)
	}
	rec := obsRecord{Source: "github:" + repo + "/" + kind, Content: truncate(content, maxObservationChars)}
	summary := map[string]any{"tool_id": spec.ID, "repo": repo, "kind": kind, "bytes": len(body)}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

// compactGitHub keeps the useful fields of list/detail responses.
func compactGitHub(raw []byte) string {
	keep := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for _, k := range []string{"number", "title", "state", "html_url", "created_at", "updated_at", "body", "sha", "merged_at"} {
			if v, ok := m[k]; ok && v != nil {
				if s, ok := v.(string); ok && len(s) > 1500 {
					v = s[:1500] + "…"
				}
				out[k] = v
			}
		}
		if u, ok := m["user"].(map[string]any); ok {
			out["user"] = u["login"]
		}
		if c, ok := m["commit"].(map[string]any); ok {
			out["message"] = c["message"]
		}
		return out
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) == nil {
		var out []map[string]any
		for _, m := range list {
			out = append(out, keep(m))
		}
		b, _ := json.MarshalIndent(out, "", " ")
		return string(b)
	}
	var one map[string]any
	if json.Unmarshal(raw, &one) == nil {
		b, _ := json.MarshalIndent(keep(one), "", " ")
		return string(b)
	}
	return string(raw)
}
