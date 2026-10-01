package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/settings"
)

// totpCodeAt is RFC 6238 for the test, computed independently of the package
// under test.
func totpCodeAt(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off]&0x7f) << 24) | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", v%1000000)
}

// A two-factor code is valid for about ninety seconds. Once it has signed
// someone in, the same code — seen over a shoulder or relayed by a phishing
// page — must not sign in again inside that window.
func TestTOTPCodeSignsInOnlyOnceIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	id, username, _ := e.resetUser("totp-replay@example.test")
	secret := auth.GenerateTOTPSecret()
	enc, err := settings.Encrypt(&config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetPendingUserTOTP(e.ctx, id, enc); err != nil {
		t.Fatal(err)
	}
	if err := e.store.EnablePendingUserTOTP(e.ctx, id, enc, nil); err != nil {
		t.Fatal(err)
	}
	login := func(code string) int {
		status, _ := e.do(http.MethodPost, "/api/auth/login",
			fmt.Sprintf(`{"username":%q,"password":%q,"code":%q}`, username, resetTestPassword, code), "")
		return status
	}

	now := time.Now().Unix() / 30
	current := totpCodeAt(t, secret, now)
	if status := login(current); status != http.StatusOK {
		t.Fatalf("first sign-in with a current code: HTTP %d", status)
	}
	if status := login(current); status != http.StatusUnauthorized {
		t.Fatalf("the same code replayed: HTTP %d, want 401", status)
	}
	// An older code inside the drift window is refused too: its step is behind
	// the one already spent.
	if previous := totpCodeAt(t, secret, now-1); previous != current {
		if status := login(previous); status != http.StatusUnauthorized {
			t.Fatalf("an earlier step after a later one was spent: HTTP %d, want 401", status)
		}
	}
	// Control: the next step's code still works.
	if next := totpCodeAt(t, secret, now+1); next != current {
		if status := login(next); status != http.StatusOK {
			t.Fatalf("the next step's code: HTTP %d, want 200", status)
		}
	}
}

// A secret the server cannot decrypt (the settings key was rotated) must be a
// refusal, not an empty 200 that the console reads as success.
func TestTOTPUndecryptableSecretIsRefusedIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	id, _, session := e.resetUser("totp-undecryptable@example.test")
	otherKey := &config.Config{SettingsEncryptionKey: "fedcba9876543210fedcba9876543210"}
	enc, err := settings.Encrypt(otherKey, auth.GenerateTOTPSecret())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetPendingUserTOTP(e.ctx, id, enc); err != nil {
		t.Fatal(err)
	}
	if err := e.store.EnablePendingUserTOTP(e.ctx, id, enc, nil); err != nil {
		t.Fatal(err)
	}
	if status, _ := e.do(http.MethodPost, "/api/me/2fa/recovery-codes", `{"code":"123456"}`, session); status != http.StatusBadRequest {
		t.Errorf("new recovery codes with an undecryptable secret: HTTP %d, want 400", status)
	}
	body := fmt.Sprintf(`{"current_password":%q,"code":"123456"}`, resetTestPassword)
	if status, _ := e.do(http.MethodPost, "/api/me/2fa/setup", body, session); status != http.StatusUnauthorized {
		t.Errorf("2FA setup with an undecryptable secret: HTTP %d, want 401", status)
	}
}
