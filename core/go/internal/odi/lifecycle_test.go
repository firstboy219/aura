package odi

import (
	"testing"

	"aura.local/core/internal/task"
)

func ptr(s string) *string { return &s }

func tk(state task.State, reason string) *task.Task {
	t := &task.Task{ID: "t1", State: state}
	if reason != "" {
		t.Reason = ptr(reason)
	}
	return t
}

func step(id string, state task.StepState, tool, side string) *task.Step {
	s := &task.Step{ID: id, TaskID: "t1", State: state, SideEffect: side}
	if tool != "" {
		s.ToolID = ptr(tool)
	}
	return s
}

func ev() Evidence {
	return Evidence{StepStarted: map[string]bool{}, StepSucceeded: map[string]bool{}, StepConfirmed: map[string]bool{}}
}

// Every persisted state lands on the canonical state that contracts/odi/state-mapping.json promises.
func TestDeriveCanonicalStates(t *testing.T) {
	cases := []struct {
		name  string
		t     *task.Task
		steps []*task.Step
		e     func(*Evidence)
		want  string
		ext   string
	}{
		{"created", tk(task.Created, ""), nil, nil, Accepted, ""},
		{"planning without plan", tk(task.Planning, ""), nil, nil, CognitionPending, ""},
		{"planning with plan", tk(task.Planning, ""), []*task.Step{step("a", task.StepPending, "web.search", "read")}, nil, Planned, ""},
		{"running", tk(task.Running, ""), nil, nil, Running, ""},
		{"verifying", tk(task.Running, ""), nil, func(e *Evidence) { e.VerifyingNow = true }, Verifying, ""},
		{"awaiting approval", tk(task.WaitingApproval, ""), nil, nil, AwaitingApproval, ""},
		{"unknown outcome needs recovery", tk(task.WaitingApproval, "OUTCOME_UNKNOWN"), nil, nil, RecoveryRequired, ""},
		{"paused", tk(task.Paused, "kill_switch"), nil, nil, Paused, ""},
		{"completed", tk(task.Succeeded, ""), []*task.Step{step("a", task.StepSucceeded, "web.search", "read")}, nil, Succeeded, ""},
		{"completed with a failed step is partial", tk(task.Succeeded, ""), []*task.Step{step("a", task.StepSucceeded, "x", "read"), step("b", task.StepFailed, "y", "read")}, nil, Partial, ""},
		{"a step skipped by a replan is not a failure", tk(task.Succeeded, ""), []*task.Step{step("a", task.StepSucceeded, "x", "read"), step("b", task.StepSkipped, "y", "read")}, nil, Succeeded, ""},
		{"failed", tk(task.Failed, "TOOL_FAILED"), nil, nil, Failed, ""},
		{"cancelled", tk(task.Cancelled, ""), nil, nil, Cancelled, ""},
		{"budget exceeded is failed", tk(task.BudgetExceeded, "BUDGET_EXCEEDED"), nil, nil, Failed, ""},
		{"wall time is expired", tk(task.BudgetExceeded, "max_wall_seconds"), nil, nil, Expired, ""},
		{"waiting input is an extension", tk(task.WaitingInput, ""), nil, nil, "", "WAITING_INPUT"},
		{"waiting external is an extension", tk(task.Waiting, ""), nil, nil, "", "WAITING_EXTERNAL"},
		{"scheduled is an extension", tk(task.Scheduled, ""), nil, nil, "", "SCHEDULED"},
	}
	for _, c := range cases {
		e := ev()
		if c.e != nil {
			c.e(&e)
		}
		got := Derive(c.t, c.steps, e)
		if got.CanonicalState != c.want || got.Extension != c.ext {
			t.Errorf("%s: canonical=%q extension=%q, want %q / %q", c.name, got.CanonicalState, got.Extension, c.want, c.ext)
		}
	}
}

// AT-09: a tool response without a verified postcondition is never reported as completed-and-verified.
func TestDeriveTruthfulClaims(t *testing.T) {
	write := []*task.Step{step("s1", task.StepSucceeded, "files.write", "write_external")}
	done := tk(task.Succeeded, "")
	for _, c := range []struct {
		name       string
		steps      []*task.Step
		e          func(*Evidence)
		level      string
		claim      string
		verifiedOK bool
	}{
		{"tool said ok, nothing checked", write, func(e *Evidence) { e.StepSucceeded["s1"] = true }, V1, ClaimCompletedUnverified, false},
		{"tool said ok and verification was skipped", write, func(e *Evidence) { e.StepSucceeded["s1"] = true }, V1, ClaimCompletedUnverified, false},
		{"only an intent was recorded", write, func(e *Evidence) { e.StepStarted["s1"] = true }, V0, ClaimAttemptedOnly, false},
		{"nothing recorded for a consequential step", write, nil, V0, ClaimAttemptedOnly, false},
		{"postcondition observed", write, func(e *Evidence) { e.StepSucceeded["s1"] = true; e.Verified = true }, V2, ClaimCompletedVerified, true},
		{"verification failed after a tool success", write, func(e *Evidence) { e.StepSucceeded["s1"] = true; e.VerifyFailed = true }, V1, ClaimUncertain, false},
		{"authoritative confirmation", write, func(e *Evidence) { e.StepSucceeded["s1"] = true; e.StepConfirmed["s1"] = true; e.Verified = true }, V3, ClaimCompletedVerified, true},
		{"thinking only", []*task.Step{step("s1", task.StepSucceeded, "", "pure")}, nil, LevelNone, ClaimNoExternalEffect, false},
	} {
		e := ev()
		if c.e != nil {
			c.e(&e)
		}
		a := Derive(done, c.steps, e)
		if a.VerificationLevel != c.level || a.Claim != c.claim {
			t.Errorf("%s: level=%s claim=%s, want %s / %s", c.name, a.VerificationLevel, a.Claim, c.level, c.claim)
		}
		if (a.Claim == ClaimCompletedVerified) != c.verifiedOK {
			t.Errorf("%s: completed_verified=%v, want %v", c.name, a.Claim == ClaimCompletedVerified, c.verifiedOK)
		}
	}
}

func TestDeriveUncertainOutcomeIsNeverACompletionClaim(t *testing.T) {
	e := ev()
	e.OutcomeUnknown = true
	a := Derive(tk(task.WaitingApproval, "OUTCOME_UNKNOWN"), []*task.Step{step("s1", task.StepWaitingApproval, "files.write", "write_external")}, e)
	if a.Claim != ClaimUncertain || !a.Uncertain || a.CanonicalState != RecoveryRequired {
		t.Fatalf("got %+v", a)
	}
}

func TestDeriveCountsSteps(t *testing.T) {
	a := Derive(tk(task.Running, ""), []*task.Step{step("1", task.StepSucceeded, "x", "read"), step("2", task.StepFailed, "y", "read"), step("3", task.StepPending, "z", "read"), step("4", task.StepSkipped, "z", "read")}, ev())
	if a.StepsTotal != 3 || a.StepsSucceeded != 1 || a.StepsFailed != 1 || a.StepsOutstanding != 1 {
		t.Fatalf("got %+v", a)
	}
}

func TestWeakestEffectDrivesTheReplyNote(t *testing.T) {
	write := []*task.Step{step("w", task.StepSucceeded, "files.write", "write_external")}
	read := []*task.Step{step("r", task.StepSucceeded, "web.search", "read")}
	for _, c := range []struct {
		name  string
		steps []*task.Step
		e     func(*Evidence)
		level string
		bad   bool
	}{
		{"read only needs no note", read, func(e *Evidence) { e.StepSucceeded["r"] = true }, LevelNone, false},
		{"write the tool called ok, nothing checked", write, func(e *Evidence) { e.StepSucceeded["w"] = true }, V1, true},
		{"write attempted only", write, func(e *Evidence) { e.StepStarted["w"] = true }, V0, true},
		{"write checked against its criteria", write, func(e *Evidence) { e.StepSucceeded["w"] = true; e.Verified = true }, LevelNone, false},
		{"write confirmed by authoritative state", write, func(e *Evidence) { e.StepSucceeded["w"] = true; e.StepConfirmed["w"] = true }, LevelNone, false},
		{"verification failed after the write", write, func(e *Evidence) { e.StepSucceeded["w"] = true; e.Verified = true; e.VerifyFailed = true }, V1, true},
	} {
		e := ev()
		if c.e != nil {
			c.e(&e)
		}
		lvl, bad, _ := weakestEffect(c.steps, e)
		if lvl != c.level || bad != c.bad {
			t.Errorf("%s: level=%s unverified=%v, want %s / %v", c.name, lvl, bad, c.level, c.bad)
		}
	}
}
