package taxonomy

import "testing"

func TestCanonicalCoversEngineReasons(t *testing.T) {
	cases := map[string]string{
		"invalid_plan: step 1 uses tool": PlanInvalid, "invalid_model_output": ModelOutputInvalid, "budget_exceeded_task": BudgetExceeded,
		"budget_exceeded_iterations": MaxIterations, "max_wall_seconds": MaxWallTime, "capability_denied": PolicyDenied,
		"approval_rejected": OwnerRejected, "approval_expired": ApprovalExpired, "verification_failed": VerificationFailed,
		"completion_check_failed": VerificationFailed, "all_routes_failed": ProviderUnavailable, "worker_crashed": WorkerCrash,
		"step_failed: tool_failed: HTTP 404": ToolFailed, "replan_limit": ReplanLimit, "argument_without_provenance": PolicyDenied,
		"domain_not_allowed": PolicyDenied, "aul_integrity": AULIntegrity, "aul_workflow_failed: x": AULFailed, "weird thing": InternalError, "": "",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// A permission boundary is never an automatic-remedy class (R6 = owner action).
func TestPermissionBoundaryIsOwnerRequest(t *testing.T) {
	cause, _ := CandidateCause(PolicyDenied)
	if cause != CausePermissionBoundary || RemedyClass(cause) != R6 {
		t.Fatal(cause, RemedyClass(cause))
	}
	for _, c := range Causes {
		if RemedyClass(c) == "" {
			t.Errorf("no remedy class for %s", c)
		}
	}
	if ValidCause("because aliens") {
		t.Fatal("free-text cause accepted")
	}
}
