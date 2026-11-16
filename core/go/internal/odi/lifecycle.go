package odi

import (
	"context"

	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

// Canonical task states (ODI AURA Blueprint v1.1 §5). contracts/odi/state-mapping.json is the written form of the mapping below.
const (
	Received         = "RECEIVED"
	Validating       = "VALIDATING"
	Accepted         = "ACCEPTED"
	Rejected         = "REJECTED"
	CognitionPending = "COGNITION_PENDING"
	Planned          = "PLANNED"
	PolicyCheck      = "POLICY_CHECK"
	AwaitingApproval = "AWAITING_APPROVAL"
	Ready            = "READY"
	Running          = "RUNNING"
	Verifying        = "VERIFYING"
	Succeeded        = "SUCCEEDED"
	Partial          = "PARTIAL"
	Failed           = "FAILED"
	Cancelled        = "CANCELLED"
	Expired          = "EXPIRED"
	Paused           = "PAUSED"
	RecoveryRequired = "RECOVERY_REQUIRED"
)

// Verification levels (Blueprint §15): what the evidence behind a result really is.
const (
	LevelNone = "none" // nothing was attempted that needs evidence
	V0        = "V0"   // execution attempted
	V1        = "V1"   // the tool reported success
	V2        = "V2"   // a postcondition was observed (completion criteria checked against the output)
	V3        = "V3"   // independent confirmation or authoritative system state
)

// Claims say what AURA may truthfully tell the owner (Blueprint §15 "Truthful status"; AT-09).
const (
	ClaimInProgress          = "in_progress"
	ClaimWaiting             = "waiting"
	ClaimCompletedVerified   = "completed_verified"       // V2/V3
	ClaimCompletedUnverified = "completed_unverified"     // the tool said it worked; nothing observed it (V1)
	ClaimAttemptedOnly       = "attempted_only"           // V0: do not say it is done
	ClaimNoExternalEffect    = "completed_no_side_effect" // only thinking/reading
	ClaimPartial             = "partial"
	ClaimFailed              = "failed"
	ClaimUncertain           = "uncertain" // an external outcome is unknown: reconcile, do not claim
	ClaimCancelled           = "cancelled"
)

// Assurance is the additive, derived view of a task that the API returns next to the persisted state. Nothing here is stored.
type Assurance struct {
	CanonicalState    string `json:"canonical_state"`
	Extension         string `json:"extension,omitempty"` // AURA state with no Blueprint equivalent (WAITING_INPUT, WAITING_EXTERNAL, SCHEDULED)
	VerificationLevel string `json:"verification_level"`
	Claim             string `json:"claim"`
	StepsTotal        int    `json:"steps_total"`
	StepsSucceeded    int    `json:"steps_succeeded"`
	StepsFailed       int    `json:"steps_failed"`
	StepsOutstanding  int    `json:"steps_outstanding"`
	Uncertain         bool   `json:"uncertain,omitempty"`
}

// Evidence is what the event log says, reduced to what Derive needs.
type Evidence struct {
	StepStarted    map[string]bool // TOOL_INTENT / TOOL_STARTED seen for the step
	StepSucceeded  map[string]bool // TOOL_SUCCEEDED seen
	StepConfirmed  map[string]bool // an authoritative confirmation event (V3), none emitted yet
	Verified       bool            // last task-level verdict is VERIFIED
	VerifyFailed   bool            // last verdict is VERIFICATION_FAILED
	VerifyingNow   bool            // VERIFY_STARTED with no verdict after it
	OutcomeUnknown bool            // a step's external outcome is unknown
}

func isConsequential(sideEffect string) bool {
	return sideEffect == "write_external" || sideEffect == "write_idempotent"
}

// Derive is pure: the same task, steps and evidence always give the same Assurance (unit-tested).
func Derive(t *task.Task, steps []*task.Step, ev Evidence) Assurance {
	a := Assurance{VerificationLevel: LevelNone}
	var active []*task.Step
	for _, s := range steps {
		if s.State == task.StepSkipped { // superseded by a replan
			continue
		}
		active = append(active, s)
		a.StepsTotal++
		switch s.State {
		case task.StepSucceeded:
			a.StepsSucceeded++
		case task.StepFailed:
			a.StepsFailed++
		default:
			a.StepsOutstanding++
		}
	}
	a.Uncertain = ev.OutcomeUnknown
	reason := ""
	if t.Reason != nil {
		reason = *t.Reason
	}
	switch t.State {
	case task.Created:
		a.CanonicalState = Accepted
	case task.Planning:
		a.CanonicalState = CognitionPending
		if len(active) > 0 {
			a.CanonicalState = Planned
		}
	case task.Running:
		a.CanonicalState = Running
		if ev.VerifyingNow {
			a.CanonicalState = Verifying
		}
	case task.WaitingApproval:
		a.CanonicalState = AwaitingApproval
		if ev.OutcomeUnknown || reason == "OUTCOME_UNKNOWN" {
			a.CanonicalState, a.Uncertain = RecoveryRequired, true
		}
	case task.Paused:
		a.CanonicalState = Paused
	case task.Succeeded:
		a.CanonicalState = Succeeded
		if a.StepsFailed > 0 {
			a.CanonicalState = Partial
		}
	case task.Failed:
		a.CanonicalState = Failed
	case task.Cancelled:
		a.CanonicalState = Cancelled
	case task.BudgetExceeded:
		a.CanonicalState = Failed // reason_code BUDGET_EXCEEDED (Appendix B)
		if reason == "max_wall_seconds" {
			a.CanonicalState = Expired
		}
	default: // WAITING_INPUT, WAITING_EXTERNAL, SCHEDULED: AURA states the Blueprint has no name for
		a.Extension = string(t.State)
	}

	// Verification level: the weakest evidence among the steps that did something, raised by a task-level verdict.
	level, any, consequential := 3, false, false
	for _, s := range active {
		if s.ToolID == nil || s.State != task.StepSucceeded {
			continue
		}
		any = true
		if isConsequential(s.SideEffect) {
			consequential = true
		}
		l := 0
		switch {
		case ev.StepConfirmed[s.ID]:
			l = 3
		case ev.StepSucceeded[s.ID]:
			l = 1
		case ev.StepStarted[s.ID]:
			l = 0
		}
		if l < level {
			level = l
		}
	}
	if any {
		switch {
		case level == 3 && !ev.Verified:
			level = 3
		case ev.Verified && level < 2 && !ev.VerifyFailed:
			level = 2
		}
		a.VerificationLevel = [...]string{V0, V1, V2, V3}[level]
	} else if ev.Verified && !ev.VerifyFailed && len(active) > 0 {
		a.VerificationLevel = V2
	}

	switch a.CanonicalState {
	case Succeeded:
		switch {
		case a.VerificationLevel == V2 || a.VerificationLevel == V3:
			a.Claim = ClaimCompletedVerified
		case a.VerificationLevel == V1:
			a.Claim = ClaimCompletedUnverified
		case a.VerificationLevel == V0:
			a.Claim = ClaimAttemptedOnly
		case consequential:
			a.Claim = ClaimAttemptedOnly
		default:
			a.Claim = ClaimNoExternalEffect
		}
		if ev.VerifyFailed {
			a.Claim = ClaimUncertain
		}
	case Partial:
		a.Claim = ClaimPartial
	case Failed, Expired:
		a.Claim = ClaimFailed
	case RecoveryRequired:
		a.Claim = ClaimUncertain
	case Cancelled:
		a.Claim = ClaimCancelled
	case Paused, AwaitingApproval:
		a.Claim = ClaimWaiting
	default:
		a.Claim = ClaimInProgress
		if a.Extension != "" {
			a.Claim = ClaimWaiting
		}
	}
	if a.Uncertain && a.Claim != ClaimFailed && a.Claim != ClaimCancelled {
		a.Claim = ClaimUncertain
	}
	return a
}

// LoadEvidence reads the few event types Derive needs.
func LoadEvidence(ctx context.Context, q store.Querier, taskID string) (Evidence, error) {
	ev := Evidence{StepStarted: map[string]bool{}, StepSucceeded: map[string]bool{}, StepConfirmed: map[string]bool{}}
	rows, err := q.QueryContext(ctx, `select seq, coalesce(step_id,''), type from task_events where task_id = ? and type in
		('TOOL_INTENT','TOOL_STARTED','TOOL_SUCCEEDED','AUTHORITATIVE_CONFIRMED','VERIFY_STARTED','VERIFIED','VERIFICATION_FAILED','VERIFICATION_SKIPPED','OUTCOME_UNKNOWN') order by seq`, taskID)
	if err != nil {
		return ev, err
	}
	defer rows.Close()
	var verifyStart, verdict int64
	for rows.Next() {
		var seq int64
		var step, typ string
		if err := rows.Scan(&seq, &step, &typ); err != nil {
			return ev, err
		}
		switch typ {
		case "TOOL_INTENT", "TOOL_STARTED":
			ev.StepStarted[step] = true
		case "TOOL_SUCCEEDED":
			ev.StepStarted[step], ev.StepSucceeded[step] = true, true
		case "AUTHORITATIVE_CONFIRMED":
			ev.StepConfirmed[step] = true
		case "VERIFY_STARTED":
			verifyStart = seq
		case "VERIFIED":
			verdict, ev.Verified, ev.VerifyFailed = seq, true, false
		case "VERIFICATION_FAILED":
			verdict, ev.Verified, ev.VerifyFailed = seq, false, true
		case "VERIFICATION_SKIPPED":
			verdict = seq
		case "OUTCOME_UNKNOWN":
			ev.OutcomeUnknown = true
		}
	}
	ev.VerifyingNow = verifyStart > verdict
	return ev, rows.Err()
}

// Load derives the assurance of one persisted task.
func Load(ctx context.Context, q store.Querier, t *task.Task) (Assurance, error) {
	steps, err := task.Steps(ctx, q, t.ID)
	if err != nil {
		return Assurance{}, err
	}
	ev, err := LoadEvidence(ctx, q, t.ID)
	if err != nil {
		return Assurance{}, err
	}
	// An outcome that was unknown stays unknown only while the step is parked.
	if ev.OutcomeUnknown {
		parked := false
		for _, s := range steps {
			if s.State == task.StepWaitingApproval {
				parked = true
			}
		}
		ev.OutcomeUnknown = parked
	}
	return Derive(t, steps, ev), nil
}

// UnverifiedEffects reports whether the task did something consequential (write_external / write_idempotent) that no postcondition check observed,
// and at what evidence level it stands. The reply written to the owner then carries a note, so "done" is never stronger than the evidence (AT-09).
func UnverifiedEffects(ctx context.Context, q store.Querier, taskID string) (level string, unverified bool, err error) {
	steps, err := task.Steps(ctx, q, taskID)
	if err != nil {
		return "", false, err
	}
	ev, err := LoadEvidence(ctx, q, taskID)
	if err != nil {
		return "", false, err
	}
	return weakestEffect(steps, ev)
}

func weakestEffect(steps []*task.Step, ev Evidence) (string, bool, error) {
	level, seen := 3, false
	for _, s := range steps {
		if s.ToolID == nil || s.State != task.StepSucceeded || !isConsequential(s.SideEffect) {
			continue
		}
		seen = true
		l := 0
		switch {
		case ev.StepConfirmed[s.ID]:
			l = 3
		case ev.StepSucceeded[s.ID]:
			l = 1
		}
		if l < level {
			level = l
		}
	}
	if !seen || level >= 3 || (ev.Verified && !ev.VerifyFailed) {
		return LevelNone, false, nil
	}
	return [...]string{V0, V1, V2, V3}[level], true, nil
}
