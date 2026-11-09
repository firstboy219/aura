// Command aura is the AURA Go Core entry point.
//
// Phase 0 skeleton: only `version` and `doctor` exist. Every other command from
// Blueprint §11 is reported as not implemented rather than faked.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

// Version is overridden at build time with -ldflags "-X main.Version=...".
var Version = "0.0.0-p0"

// p1Commands are the CLI commands specified for Phase 1 (Blueprint §11).
var p1Commands = []string{"serve", "task", "trace", "approve", "reject", "stop"}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "aura %s (%s/%s, %s)\n", Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	case "doctor":
		fmt.Fprintf(stdout, "aura %s\nphase: P0 skeleton\nruntime: not implemented (Phase 1)\n", Version)
		return 0
	}
	for _, c := range p1Commands {
		if args[0] == c {
			fmt.Fprintf(stderr, "aura %s: not implemented in Phase 0 (scheduled for Phase 1)\n", c)
			return 3
		}
	}
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: aura <version|doctor>")
}
