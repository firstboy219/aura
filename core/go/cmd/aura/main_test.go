package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "aura ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestPhase1CommandsAreNotFaked(t *testing.T) {
	for _, c := range p1Commands {
		var out, errb bytes.Buffer
		if code := run([]string{c}, &out, &errb); code != 3 {
			t.Fatalf("%s: exit %d, want 3", c, code)
		}
		if !strings.Contains(errb.String(), "not implemented") {
			t.Fatalf("%s: %q", c, errb.String())
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"rm"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
