package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, nil, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "aura ") {
		t.Fatalf("exit %d %q", code, out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"rm"}, nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestNoShellCommand(t *testing.T) {
	var out, errb bytes.Buffer
	for _, c := range []string{"exec", "shell", "run"} {
		if code := run([]string{c, "id"}, nil, &out, &errb); code != 2 {
			t.Fatalf("%s accepted", c)
		}
	}
}
