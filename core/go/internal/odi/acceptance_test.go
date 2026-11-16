//go:build odi_acceptance

package odi

import "testing"

// Run with: go test -tags odi_acceptance ./internal/odi/
// Every Blueprint acceptance test (Appendix C) that is not met yet FAILS here, on purpose: this is the honest, executable gap list.
// A status changes to "met" in contracts/odi/acceptance-catalogue.json only together with a real test that proves it.
func TestBlueprintAcceptance(t *testing.T) {
	var cat struct {
		Tests []struct{ ID, Title, Status, Phase, Evidence string } `json:"tests"`
	}
	readJSON(t, "acceptance-catalogue.json", &cat)
	for _, a := range cat.Tests {
		a := a
		t.Run(a.ID, func(t *testing.T) {
			switch a.Status {
			case "met", "not_applicable_yet":
			default:
				t.Errorf("%s [%s, due in %s]: %s (%s)", a.ID, a.Status, a.Phase, a.Title, a.Evidence)
			}
		})
	}
}
