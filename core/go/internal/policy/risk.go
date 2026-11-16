package policy

// RiskPolicyVersion names the mapping below. It is a PROPOSAL (ODI Blueprint v1.1 §8: "exact mapping is an Owner decision and must be configurable with
// versioned policy"); until the owner approves another mapping this one only LABELS decisions, it never relaxes the existing A0–A3 enforcement.
const RiskPolicyVersion = "risk-policy-v1-proposed"

// Risk is the Blueprint's operational scale: R0 read-only/public or low impact; R1 reversible low-impact internal change; R2 consequential
// business or data change; R3 high-impact financial, external, security-sensitive or broad-scope action; R4 prohibited.
type Risk string

const (
	R0 Risk = "R0"
	R1 Risk = "R1"
	R2 Risk = "R2"
	R3 Risk = "R3"
	R4 Risk = "R4"
)

// RiskOf labels an action by its declared side effect and AURA's approval tier. A3 (forbidden) is always R4 and stays hard-denied.
func RiskOf(sideEffect string, tier Tier) Risk {
	if tier == A3 {
		return R4
	}
	switch sideEffect {
	case "write_external":
		if tier == A2 {
			return R3
		}
		return R2
	case "write_idempotent":
		if tier == A2 {
			return R2
		}
		return R1
	default: // pure, read, unknown
		if tier == A2 {
			return R1
		}
		return R0
	}
}
