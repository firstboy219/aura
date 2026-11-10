// Command aulvm is the AUL virtual machine, built as a WebAssembly module
// (GOOS=wasip1 GOARCH=wasm, ADR-0050). It reads the checked program and its
// inputs from stdin, runs it, and asks the host for every tool call over
// stdin/stdout. It has no other way out: the Core instantiates it with no
// files, no network, no environment and no clock beyond WASI defaults.
package main

import (
	"encoding/json"
	"errors"
	"os"

	"aura.local/core/internal/aul/ir"
	"aura.local/core/internal/aul/vm"
)

type host struct {
	dec *json.Decoder
	enc *json.Encoder
}

func (h *host) Call(tool string, args map[string]any) (any, error) {
	if err := h.enc.Encode(ir.Message{Call: tool, Args: args}); err != nil {
		return nil, err
	}
	var r ir.Message
	if err := h.dec.Decode(&r); err != nil {
		return nil, err
	}
	if !r.OK {
		if r.Error == "" {
			r.Error = "refused"
		}
		return nil, errors.New(r.Error)
	}
	return r.Result, nil
}

func main() {
	h := &host{dec: json.NewDecoder(os.Stdin), enc: json.NewEncoder(os.Stdout)}
	var start ir.Message
	if err := h.dec.Decode(&start); err != nil || start.Program == nil {
		h.enc.Encode(ir.Message{Done: true, Error: "bad start message"})
		os.Exit(2)
	}
	out, err := vm.Run(start.Program, start.Inputs, h, start.MaxOps)
	if err != nil {
		h.enc.Encode(ir.Message{Done: true, Error: err.Error()})
		return
	}
	h.enc.Encode(ir.Message{Done: true, OK: true, Output: out})
}
