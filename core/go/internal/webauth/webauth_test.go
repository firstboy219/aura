package webauth

import (
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if VerifyPassword(h, "correct horse battery") != nil || VerifyPassword(h, "correct horse batterz") == nil {
		t.Fatal("password verification wrong")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

// RFC 6238 Appendix B test vector (SHA-1, T=59 → 94287082, 8 digits; 6 digits: 287082).
func TestTOTPVectorAndReplay(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	at := time.Unix(59, 0)
	code, _ := TOTPCode(secret, at)
	if code != "287082" {
		t.Fatalf("RFC 6238 vector: %s", code)
	}
	step, ok := VerifyTOTP(secret, code, at, 0)
	if !ok {
		t.Fatal("valid code refused")
	}
	if _, ok := VerifyTOTP(secret, code, at, step); ok {
		t.Fatal("a code was accepted twice")
	}
	if _, ok := VerifyTOTP(secret, code, at.Add(5*time.Minute), 0); ok {
		t.Fatal("an old code was accepted")
	}
	if _, ok := VerifyTOTP(secret, "28708x", at, 0); ok {
		t.Fatal("non-numeric code accepted")
	}
}
