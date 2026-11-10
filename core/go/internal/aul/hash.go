package aul

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"aura.local/core/internal/aul/ir"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Hash is the sha256 of a program's canonical IR (what the owner approves and runs).
func Hash(p *ir.Program) string {
	b, _ := json.Marshal(p)
	return sha256Hex(b)
}
