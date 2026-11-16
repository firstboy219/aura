package policy

import "testing"

func TestRiskLabelsNeverRelaxEnforcement(t *testing.T) {
	for _, c := range []struct {
		side string
		tier Tier
		want Risk
	}{
		{"pure", A0, R0}, {"read", A0, R0}, {"read", A2, R1}, {"write_idempotent", A0, R1}, {"write_idempotent", A2, R2},
		{"write_external", A0, R2}, {"write_external", A2, R3}, {"write_external", A3, R4}, {"read", A3, R4}, {"", A0, R0},
	} {
		if got := RiskOf(c.side, c.tier); got != c.want {
			t.Errorf("RiskOf(%q,%s)=%s want %s", c.side, c.tier, got, c.want)
		}
	}
	// R4 is hard-deny: the tier that maps to it must stay forbidden (no emergency override turns it into an allowed action).
	if err := CheckTier(A3); err == nil {
		t.Fatal("A3 must stay denied")
	}
}
