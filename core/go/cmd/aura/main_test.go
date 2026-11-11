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

func TestSkillMarkdownIsSplitIntoNameDescriptionAndGuidance(t *testing.T) {
	name, desc, body := splitSkillMarkdown("---\nname: riset-bersumber\ndescription: \"Riset web yang dapat dipercaya\"\nsource: x\n---\n# Riset\n1. Cari.")
	if name != "riset-bersumber" || desc != "Riset web yang dapat dipercaya" || body != "# Riset\n1. Cari." {
		t.Fatalf("%q %q %q", name, desc, body)
	}
	tr := skillTriggers("riset-bersumber Riset web yang dapat dipercaya")
	if strings.Join(tr, ",") != "riset,bersumber,yang,dapat,dipercaya" {
		t.Fatalf("triggers %v", tr)
	}
}
