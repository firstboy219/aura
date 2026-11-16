package odi

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Self-engineering change levels (ODI AURA Blueprint v1.1 §16, ADR-0077).
const (
	L0 = "L0" // documentation and validated knowledge updates
	L1 = "L1" // non-critical skills, templates, bounded configuration
	L2 = "L2" // source code, APIs, workflows, model artifacts
	L3 = "L3" // identity, authorization, security policy, audit, approval gates, core runtime controls, production infrastructure
)

// immutable lists what no self-engineering change may weaken: Owner authority, approval gates, audit integrity, sandbox boundaries, secrets policy, egress,
// fallback policy and rollback controls (Blueprint §16 "Immutable safety invariants"). A path under any of these is L3: explicit owner approval, never self-approved.
// A test checks that every entry still exists in the repository, so the list cannot rot.
var immutable = []struct{ prefix, why string }{
	{"core/go/internal/policy/", "policy, approvals, grants and the audit hash chain"},
	{"core/go/internal/vault/", "secrets policy"},
	{"core/go/internal/egress/", "egress controls"},
	{"core/go/internal/nexus/", "NEXUS gateway, fallback authorization and the Teacher Gate"},
	{"core/go/internal/device/", "device identity, pairing and revocation"},
	{"core/go/internal/sandbox/", "sandbox boundary"},
	{"core/go/internal/workerhost/", "worker isolation host"},
	{"core/go/internal/webauth/", "owner authentication"},
	{"core/go/internal/api/authz.go", "roles and authorization"},
	{"core/go/internal/api/web.go", "web owner sessions and step-up"},
	{"core/go/internal/api/devices.go", "device endpoints"},
	{"core/go/internal/api/nexus_admin.go", "fallback and Teacher Gate administration"},
	{"core/go/internal/budget/", "budget enforcement and the kill switch"},
	{"core/go/internal/gateway/", "model gateway (cognition routing)"},
	{"core/go/internal/store/migrations/", "schema, including approvals, audit and secrets"},
	{"deploy/", "production infrastructure and isolation definitions"},
	{"shell/", "the shell sandbox"},
	{"contracts/odi/acceptance-catalogue.json", "the acceptance evidence (a status changes only with a real test)"},
	{"docs/MASTER_BLUEPRINT.md", "owner document"}, {"docs/ISLAMIC_CHARTER.md", "owner document"}, {"AGENTS.md", "owner document"}, {"CLAUDE.md", "owner document"},
}

// Classify returns the level of a change from the paths it touches (conservative: the highest level of any path; an unknown path is L2) and the reasons.
func Classify(paths []string) (level string, reasons []string, err error) {
	if len(paths) == 0 {
		return "", nil, fmt.Errorf("a change must list the paths it touches")
	}
	rank := map[string]int{L0: 0, L1: 1, L2: 2, L3: 3}
	level = L0
	set := map[string]bool{}
	for _, raw := range paths {
		p := path.Clean(strings.TrimPrefix(strings.ReplaceAll(raw, "\\", "/"), "./"))
		if p == "." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") || strings.Contains(p, "\x00") {
			return "", nil, fmt.Errorf("path %q is not a repository-relative path", raw)
		}
		lv, why := classifyOne(p)
		if rank[lv] > rank[level] {
			level = lv
		}
		set[fmt.Sprintf("%s %s: %s", lv, p, why)] = true
	}
	for r := range set {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	return level, reasons, nil
}

func classifyOne(p string) (string, string) {
	for _, im := range immutable {
		if p == im.prefix || strings.HasPrefix(p, im.prefix) {
			return L3, "protected safety invariant: " + im.why
		}
	}
	switch {
	case strings.HasPrefix(p, "docs/") && strings.HasSuffix(p, ".md"), strings.HasSuffix(p, ".md") && !strings.Contains(p, "/"):
		return L0, "documentation"
	case strings.HasPrefix(p, "skills/") && (strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".json")):
		return L1, "skill"
	case strings.HasPrefix(p, "benchmarks/"):
		return L1, "benchmark data"
	case strings.HasPrefix(p, "core/go/"), strings.HasPrefix(p, "intelligence/"), strings.HasPrefix(p, "contracts/"), strings.HasPrefix(p, "browser/"), strings.HasPrefix(p, "dev"), strings.HasPrefix(p, "android/"):
		return L2, "source code, API or workflow"
	}
	return L2, "unknown path: treated as source until classified"
}

var (
	reTestFile   = regexp.MustCompile(`(^|/)(test_[^/]+\.py|[^/]+_test\.go|[^/]+\.test\.[jt]sx?)$`)
	reDiffFile   = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)
	reSkipAdded  = regexp.MustCompile(`^\+.*(\bt\.Skip(Now|f)?\(|@pytest\.mark\.(skip|xfail)|\bpytest\.skip\(|\bit\.skip\(|\bxit\()`)
	reAssertGone = regexp.MustCompile(`^-\s*(if .*\bt\.(Fatal|Fatalf|Error|Errorf)\b|.*\bt\.(Fatal|Fatalf|Error|Errorf)\(|\s*assert\b|\s*self\.assert|\s*require\.)`)
	reAssertAdd  = regexp.MustCompile(`^\+\s*(.*\bt\.(Fatal|Fatalf|Error|Errorf)\(|\s*assert\b|\s*self\.assert|\s*require\.)`)
)

// WeakensTests inspects a unified diff for the ways a change can "pass" by weakening its evidence (Blueprint §16: do not suppress alerts, delete failed evidence, lower
// test thresholds, or claim success without verification): deleted test files, tests newly skipped, test files that lose more assertions than they gain, and
// acceptance statuses raised in the catalogue. It is a heuristic guard, not a proof; a finding blocks the change until a human has looked.
func WeakensTests(diff string) []string {
	var findings []string
	file, removed, added := "", 0, 0
	isTest := false
	flush := func() {
		if isTest && removed > added {
			findings = append(findings, fmt.Sprintf("%s: removes %d assertion lines and adds %d", file, removed, added))
		}
		removed, added = 0, 0
	}
	deleted := false
	for _, line := range strings.Split(diff, "\n") {
		if m := reDiffFile.FindStringSubmatch(line); m != nil {
			flush()
			file, isTest, deleted = m[2], reTestFile.MatchString(m[2]), false
			continue
		}
		if strings.HasPrefix(line, "deleted file mode") && isTest {
			findings = append(findings, file+": test file deleted")
			deleted = true
			continue
		}
		if deleted {
			continue
		}
		if isTest && reSkipAdded.MatchString(line) {
			findings = append(findings, file+": a test is skipped: "+strings.TrimSpace(line[1:]))
		}
		if isTest && reAssertGone.MatchString(line) {
			removed++
		}
		if isTest && reAssertAdd.MatchString(line) {
			added++
		}
		if strings.HasSuffix(file, "acceptance-catalogue.json") && strings.HasPrefix(line, "+") && strings.Contains(line, `"status"`) && strings.Contains(line, `"met"`) {
			findings = append(findings, file+": an acceptance status was set to met: it needs the test that proves it in the same change")
		}
	}
	flush()
	return findings
}

// Proposal is the record a repair or improvement must carry (Blueprint §16 "Repair safety").
type Proposal struct {
	Title               string   `json:"title"`
	Symptom             string   `json:"symptom"`
	Evidence            []string `json:"evidence"`
	RootCauseConfidence string   `json:"root_cause_confidence"` // low | medium | high
	Components          []string `json:"components"`
	Risks               []string `json:"risks"`
	TestPlan            string   `json:"test_plan"`
	RollbackPlan        string   `json:"rollback_plan"`
	ExpectedOutcome     string   `json:"expected_outcome"`
}

// Validate says what is missing. L2 and L3 need tests and a rollback plan; every level needs a symptom, evidence and an expected outcome.
func (p Proposal) Validate(level string) []string {
	var bad []string
	need := func(ok bool, what string) {
		if !ok {
			bad = append(bad, what)
		}
	}
	need(strings.TrimSpace(p.Title) != "" && len(p.Title) <= 200, "title (1..200 characters)")
	need(strings.TrimSpace(p.Symptom) != "", "symptom")
	need(len(p.Evidence) > 0, "evidence")
	need(strings.TrimSpace(p.ExpectedOutcome) != "", "expected_outcome")
	need(p.RootCauseConfidence == "low" || p.RootCauseConfidence == "medium" || p.RootCauseConfidence == "high", "root_cause_confidence (low|medium|high)")
	if level == L2 || level == L3 {
		need(strings.TrimSpace(p.TestPlan) != "", "test_plan (required from L2)")
		need(strings.TrimSpace(p.RollbackPlan) != "", "rollback_plan (required from L2)")
		need(len(p.Risks) > 0, "risks (required from L2)")
	}
	return bad
}

// Statuses and the legal moves between them. Nothing deploys without passing through approved, and nothing is accepted without evidence of the deployment.
var transitions = map[string][]string{
	"draft":             {"awaiting_approval", "rejected", "blocked"},
	"awaiting_approval": {"approved", "rejected", "blocked"},
	"approved":          {"in_canary", "deployed", "rejected"},
	"in_canary":         {"deployed", "rolled_back"},
	"deployed":          {"accepted", "rolled_back"},
	"blocked":           {"draft", "rejected"},
}

// CanMove reports whether a status change is legal.
func CanMove(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// ApprovalRule says who may approve a level and what they need. L0 and (pre-approved) L1 are recorded automatically after validation; L2 needs an owner decision;
// L3 needs the owner with a second factor. Nobody approves their own proposal unless they are the owner, and a model or worker never approves anything.
type ApprovalRule struct {
	OwnerRequired   bool
	StepUpRequired  bool
	AutoAfterChecks bool
}

func RuleFor(level string, l1Preapproved bool) ApprovalRule {
	switch level {
	case L0:
		return ApprovalRule{AutoAfterChecks: true}
	case L1:
		if l1Preapproved {
			return ApprovalRule{AutoAfterChecks: true}
		}
		return ApprovalRule{OwnerRequired: true}
	case L2:
		return ApprovalRule{OwnerRequired: true}
	}
	return ApprovalRule{OwnerRequired: true, StepUpRequired: true}
}
