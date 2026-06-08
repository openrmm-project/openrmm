package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 4226 Appendix D test vectors: secret = ASCII "12345678901234567890".
func TestHOTPVectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32 of the RFC secret
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676"}
	for c, w := range want {
		got, ok := hotp(secret, uint64(c))
		if !ok || got != w {
			t.Errorf("hotp(counter=%d) = %q (ok=%v), want %q", c, got, ok, w)
		}
	}
}

func TestValidateTOTPRejectsGarbage(t *testing.T) {
	s := GenerateTOTPSecret()
	if ValidateTOTP(s, "000000") && ValidateTOTP(s, "123456") {
		t.Error("ValidateTOTP accepted clearly wrong codes")
	}
	if ValidateTOTP(s, "12345") || ValidateTOTP(s, "") || ValidateTOTP(s, "abcdef") {
		t.Error("ValidateTOTP accepted malformed input")
	}
}

func TestValidateTOTPAcceptsCurrent(t *testing.T) {
	s := GenerateTOTPSecret()
	code, ok := hotp(s, uint64(time.Now().Unix()/30))
	if !ok {
		t.Fatal("hotp failed for a generated secret")
	}
	if !ValidateTOTP(s, code) {
		t.Error("a freshly-generated current code did not validate")
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	codes := GenerateRecoveryCodes(10)
	if len(codes) != 10 {
		t.Fatalf("got %d codes, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' { // xxxxx-xxxxx
			t.Errorf("bad code format: %q", c)
		}
		if strings.ToLower(c) != c {
			t.Errorf("code not lowercase: %q", c)
		}
		if seen[c] {
			t.Errorf("duplicate code: %q", c)
		}
		seen[c] = true
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	if got := NormalizeRecoveryCode("  ABcd-EFgh \n"); got != "abcd-efgh" {
		t.Errorf("NormalizeRecoveryCode = %q", got)
	}
}
