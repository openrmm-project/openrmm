package remote

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// TestMeshLoginTokenRoundTrip verifies the cookie is self-consistent and uses
// the exact MeshCentral layout (iv||tag||ciphertext, base64 with +/->@$): we
// decode it the way MeshCentral's decodeCookie would and check the payload.
func TestMeshLoginTokenRoundTrip(t *testing.T) {
	keyHex := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	tok, err := meshLoginToken(keyHex, "admin")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Reverse MeshCentral's char substitution and base64-decode.
	raw, err := base64.StdEncoding.DecodeString(strings.NewReplacer("@", "+", "$", "/").Replace(tok))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if len(raw) < 12+16 {
		t.Fatalf("token too short: %d", len(raw))
	}
	iv, tag, ct := raw[:12], raw[12:28], raw[28:]

	key, _ := hex.DecodeString(keyHex)
	block, _ := aes.NewCipher(key[:32])
	gcm, _ := cipher.NewGCM(block)
	// Go expects ciphertext||tag; we stored iv||tag||ciphertext, so reassemble.
	plain, err := gcm.Open(nil, iv, append(append([]byte{}, ct...), tag...), nil)
	if err != nil {
		t.Fatalf("gcm open: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload["userid"] != "user//admin" {
		t.Fatalf("userid = %v, want user//admin", payload["userid"])
	}
	if _, ok := payload["time"]; !ok {
		t.Fatalf("payload missing time field")
	}
}
