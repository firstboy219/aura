package policy

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// AuthStrengthOf says how an approver authenticated, from the principal string the API builds (api.decide). It is recorded with the approval so the
// owner can see later whether a consequential approval had a fresh second factor (Blueprint §9 "authentication strength").
func AuthStrengthOf(by string) string {
	switch {
	case strings.Contains(by, "step-up"):
		return "owner_web_step_up_totp"
	case strings.HasPrefix(by, "owner(web"):
		return "owner_web_session"
	case strings.HasPrefix(by, "member:"):
		return "member_token"
	case by == "owner":
		return "owner_token"
	}
	return "unknown"
}

func newNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
