package remote

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// meshLoginToken builds a MeshCentral login-token cookie, a Go port of
// MeshCentral's encodeCookie (AES-256-GCM). The server must run with
// "AllowLoginToken": true and "AllowFraming": true, and tokenKeyHex must be the
// server's loginTokenKey.
//
// Format (matching MeshCentral): AES-256-GCM with the first 32 bytes of the key,
// output = iv(12) || authTag(16) || ciphertext, base64 with '+'->'@' and '/'->'$'.
// The payload is {"userid":"user//<user>","domainid":"","time":<unix>}.
func meshLoginToken(tokenKeyHex, meshUser string) (string, error) {
	key, err := hex.DecodeString(strings.TrimSpace(tokenKeyHex))
	if err != nil {
		return "", fmt.Errorf("mesh token key is not valid hex: %w", err)
	}
	if len(key) < 32 {
		return "", fmt.Errorf("mesh token key too short (need >= 32 bytes, got %d)", len(key))
	}

	payload := map[string]any{
		"userid":   "user//" + meshUser,
		"domainid": "",
		"time":     time.Now().Unix(),
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block) // 12-byte nonce, 16-byte tag (MeshCentral defaults)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}

	// Go's Seal returns ciphertext||tag; MeshCentral expects iv||tag||ciphertext.
	sealed := gcm.Seal(nil, iv, plaintext, nil)
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	out := make([]byte, 0, len(iv)+len(tag)+len(ct))
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ct...)

	b64 := base64.StdEncoding.EncodeToString(out)
	return strings.NewReplacer("+", "@", "/", "$").Replace(b64), nil
}
