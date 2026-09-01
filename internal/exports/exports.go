// Package exports mints and verifies the signed tokens behind CSV downloads.
//
// It is its own package because both halves need it and they cannot import each
// other: the HTTP handler lives in internal/api, the AI tool that hands out a
// link lives in internal/ai, and api already imports ai.
package exports

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/config"
)

// TTL bounds how long a minted link works. Short, because the link is
// meant to be used immediately by whoever asked for it — it is not a way to
// publish data.
const TTL = 30 * time.Minute

// MaxRows caps a single download. Not a context concern — nothing here
// reaches a model — but an unbounded CSV is a way to make the server do
// arbitrary work on one request.
const MaxRows = 100000

type Claims struct {
	Kind   string            `json:"k"`
	Params map[string]string `json:"p"`
	User   string            `json:"u"` // who asked; re-checked at download
	Scope  string            `json:"s"` // their access scope at mint time
	Exp    int64             `json:"e"`
}

func signingKey(cfg *config.Config) []byte {
	key := cfg.SettingsEncryptionKey
	if key == "" {
		key = cfg.AdminToken
	}
	sum := sha256.Sum256([]byte("openrmm-export|" + key))
	return sum[:]
}

// mintExportToken signs a description of the query. The token is not a secret
// that grants access on its own: the download still requires authentication, so
// a leaked link is useless without a session. What the signature buys is that
// the caller cannot edit the query it describes — widening a filter, or
// pointing it at another tenant's data.
func Mint(cfg *config.Config, claims Claims) (string, error) {
	claims.Exp = time.Now().Add(TTL).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, signingKey(cfg))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func Verify(cfg *config.Config, token string) (*Claims, error) {
	body, sig, found := strings.Cut(token, ".")
	if !found {
		return nil, fmt.Errorf("malformed export token")
	}
	mac := hmac.New(sha256.New, signingKey(cfg))
	mac.Write([]byte(body))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	// Constant-time: a byte-by-byte comparison on a signature tells an attacker
	// how much of a forgery was right, one request at a time.
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return nil, fmt.Errorf("export token signature does not match")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("malformed export token")
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("malformed export token")
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("export link has expired; ask for a new one")
	}
	return &claims, nil
}
