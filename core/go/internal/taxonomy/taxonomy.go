// Package taxonomy is the failure taxonomy and the fixed root-cause taxonomy of
// the closed self-improvement loop (Blueprint §8A.5, §8.2). Every failed step
// and every terminal failure carries a canonical reason code; the Limitation
// Ledger diagnoses from these codes deterministically before any model is used.
package taxonomy

import "strings"

// Canonical reason codes (Blueprint §8.2 examples plus the codes AURA emits).
const (
	BudgetExceeded      = "BUDGET_EXCEEDED"
	MaxIterations       = "MAX_ITERATIONS"
	MaxWallTime         = "MAX_WALL_TIME"
	PolicyDenied        = "POLICY_DENIED"
	ProviderUnavailable = "PROVIDER_UNAVAILABLE"
	ModelOutputInvalid  = "MODEL_OUTPUT_INVALID"
	VerificationFailed  = "VERIFICATION_FAILED"
	OwnerRejected       = "OWNER_REJECTED"
	ApprovalExpired     = "APPROVAL_EXPIRED"
	ToolFailed          = "TOOL_FAILED"
	PlanInvalid         = "PLAN_INVALID"
	ReplanLimit         = "REPLAN_LIMIT"
	WorkerCrash         = "WORKER_CRASH"
	InputTimeout        = "INPUT_TIMEOUT"
	KillSwitch          = "KILL_SWITCH"
	CancelledByOwner    = "CANCELLED_BY_OWNER"
	InterruptedWatch    = "INTERRUPTED"
	InternalError       = "INTERNAL_ERROR"
)

// Root causes (Blueprint §8A.5 diagnosis taxonomy). Nothing else is allowed.
const (
	CausePlanStrategy       = "plan_prompt_strategy"
	CauseWeakSkill          = "missing_or_weak_skill"
	CauseMissingTool        = "missing_tool"
	CauseWrongTierRoute     = "wrong_model_tier_or_route"
	CauseKnowledgeGap       = "knowledge_or_data_gap"
	CauseExternalDependency = "external_dependency"
	CauseCodeDefect         = "defect_in_aura_code"
	CausePermissionBoundary = "permission_boundary"
)

// Causes lists the fixed root-cause taxonomy.
var Causes = []string{CausePlanStrategy, CauseWeakSkill, CauseMissingTool, CauseWrongTierRoute, CauseKnowledgeGap,
	CauseExternalDependency, CauseCodeDefect, CausePermissionBoundary}

// ValidCause reports whether c is in the fixed taxonomy.
func ValidCause(c string) bool {
	for _, x := range Causes {
		if x == c {
			return true
		}
	}
	return false
}

// Canonical maps AURA's detailed internal reasons (the free-form text kept in
// task/step `reason`) to a canonical reason code. Unknown reasons are
// INTERNAL_ERROR, never silently dropped.
func Canonical(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case r == "":
		return ""
	case strings.HasPrefix(r, "budget_exceeded_iterations"), strings.HasPrefix(r, "max_attempts"):
		return MaxIterations
	case strings.HasPrefix(r, "budget_exceeded"):
		return BudgetExceeded
	case strings.HasPrefix(r, "max_wall"), strings.Contains(r, "deadline"):
		return MaxWallTime
	case strings.HasPrefix(r, "capability_denied"), strings.HasPrefix(r, "forbidden"), strings.HasPrefix(r, "tier_unsupported"),
		strings.HasPrefix(r, "protected_topic"), strings.HasPrefix(r, "approval_policy_changed"), strings.HasPrefix(r, "delegation_denied"):
		return PolicyDenied
	case strings.HasPrefix(r, "all_routes_failed"), strings.HasPrefix(r, "no_route_for_tier"), strings.HasPrefix(r, "provider"):
		return ProviderUnavailable
	case strings.HasPrefix(r, "invalid_model_output"), strings.HasPrefix(r, "invalid_step_result"), strings.HasPrefix(r, "invalid_verification"),
		strings.HasPrefix(r, "invalid_watch_result"):
		return ModelOutputInvalid
	case strings.HasPrefix(r, "verification_failed"), strings.HasPrefix(r, "completion_check_failed"):
		return VerificationFailed
	case strings.HasPrefix(r, "approval_rejected"), strings.HasPrefix(r, "outcome_unknown_without_intent"):
		return OwnerRejected
	case strings.HasPrefix(r, "approval_expired"):
		return ApprovalExpired
	case strings.HasPrefix(r, "invalid_plan"), strings.HasPrefix(r, "invalid_step"), strings.HasPrefix(r, "invalid_params"):
		return PlanInvalid
	case strings.HasPrefix(r, "replan_limit"):
		return ReplanLimit
	case strings.HasPrefix(r, "worker_crashed"), strings.HasPrefix(r, "worker_protocol"), strings.HasPrefix(r, "recovery_limit"),
		strings.HasPrefix(r, "worker_timeout"):
		return WorkerCrash
	case strings.HasPrefix(r, "step_failed"), strings.HasPrefix(r, "tool_failed"), strings.HasPrefix(r, "subagent_"):
		return ToolFailed
	case strings.HasPrefix(r, "kill_switch"):
		return KillSwitch
	case strings.HasPrefix(r, "cancelled"), strings.HasPrefix(r, "ancestor_cancelled"):
		return CancelledByOwner
	case strings.HasPrefix(r, "interrupted"):
		return InterruptedWatch
	case strings.HasPrefix(r, "input_timeout"):
		return InputTimeout
	}
	return InternalError
}

// CandidateCause is the deterministic first-pass diagnosis of a reason code,
// with a confidence (0..1). Low confidence asks for a model-assisted refinement.
func CandidateCause(code string) (cause string, confidence float64) {
	switch code {
	case ProviderUnavailable:
		return CauseExternalDependency, 0.85
	case PolicyDenied, OwnerRejected, ApprovalExpired:
		return CausePermissionBoundary, 0.9
	case ModelOutputInvalid:
		return CauseWrongTierRoute, 0.7
	case PlanInvalid, ReplanLimit:
		return CausePlanStrategy, 0.7
	case VerificationFailed:
		return CauseWeakSkill, 0.5
	case ToolFailed:
		return CauseExternalDependency, 0.5
	case WorkerCrash, InternalError:
		return CauseCodeDefect, 0.6
	case MaxIterations, MaxWallTime, BudgetExceeded:
		return CausePlanStrategy, 0.5
	}
	return CausePlanStrategy, 0.3
}

// Remedy classes (Blueprint §8A.5).
const (
	R0 = "R0" // runtime self-healing
	R1 = "R1" // configuration / routing
	R2 = "R2" // skill (L1)
	R3 = "R3" // tool (L2), P4
	R4 = "R4" // brain (L3), P6
	R5 = "R5" // Core / policy (L4): proposal only
	R6 = "R6" // owner action
)

// RemedyClass maps a diagnosed cause to the remedy class that may act on it.
// A permission boundary is NEVER remedied by widening permissions: it becomes
// an owner request (R6).
func RemedyClass(cause string) string {
	switch cause {
	case CausePermissionBoundary, CauseKnowledgeGap:
		return R6
	case CauseExternalDependency:
		return R0
	case CauseWrongTierRoute:
		return R1
	case CausePlanStrategy, CauseWeakSkill:
		return R2
	case CauseMissingTool:
		return R3
	case CauseCodeDefect:
		return R5
	}
	return R6
}
