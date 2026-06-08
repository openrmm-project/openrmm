package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new random base32 TOTP secret (160-bit).
func GenerateTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

// TOTPURI builds the otpauth:// provisioning URI an authenticator app scans.
func TOTPURI(issuer, account, secret string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// hotp computes the 6-digit HOTP for a counter (RFC 4226).
func hotp(secret string, counter uint64) (string, bool) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", false
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off]&0x7f) << 24) | (uint32(sum[off+1]) << 16) | (uint32(sum[off+2]) << 8) | uint32(sum[off+3])
	return fmt.Sprintf("%06d", v%1000000), true
}

// ValidateTOTP checks a 6-digit code against the secret (RFC 6238, 30s step),
// allowing ±1 step of clock skew. Constant-time compare.
func ValidateTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	step := uint64(time.Now().Unix() / 30)
	for _, c := range []uint64{step - 1, step, step + 1} {
		if want, ok := hotp(secret, c); ok && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// recoveryAlphabet excludes ambiguous characters (0/O, 1/I/L) so codes are
// easy to transcribe.
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GenerateRecoveryCodes returns n high-entropy one-time codes formatted as
// "xxxxx-xxxxx" (10 chars from a 31-symbol alphabet ≈ 49 bits each).
func GenerateRecoveryCodes(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		var sb strings.Builder
		for j, v := range b {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(v)%len(recoveryAlphabet)])
		}
		out = append(out, sb.String())
	}
	return out
}

// NormalizeRecoveryCode lowercases and trims a submitted code so formatting
// (spaces, case) doesn't affect the hash lookup.
func NormalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.TrimSpace(code))
}
