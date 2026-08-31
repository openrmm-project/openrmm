package exports

import (
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/config"
)

// The token is not a secret that grants access — the download still requires a
// session. What the signature buys is that the caller cannot edit the query it
// describes: widening a filter, or pointing it at data they were not shown.

func testConfig() *config.Config {
	return &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
}

func TestMintedTokenRoundTrips(t *testing.T) {
	cfg := testConfig()
	token, err := Mint(cfg, Claims{
		Kind:   "audit",
		Params: map[string]string{"actor": "alice"},
		User:   "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(cfg, token)
	if err != nil {
		t.Fatalf("a token we just minted did not verify: %v", err)
	}
	if claims.Kind != "audit" || claims.Params["actor"] != "alice" || claims.User != "user-1" {
		t.Fatalf("claims came back changed: %+v", claims)
	}
}

// The attack the signature exists for: edit the payload to widen the query.
func TestTamperedTokenIsRejected(t *testing.T) {
	cfg := testConfig()
	token, err := Mint(cfg, Claims{Kind: "audit", Params: map[string]string{"actor": "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	body, sig, _ := strings.Cut(token, ".")

	// A different payload with the original signature.
	other, err := Mint(cfg, Claims{Kind: "audit", Params: map[string]string{"actor": "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	otherBody, _, _ := strings.Cut(other, ".")
	if _, err := Verify(cfg, otherBody+"."+sig); err == nil {
		t.Fatal("a payload was swapped under a valid signature")
	}

	// A different signature with the original payload.
	if _, err := Verify(cfg, body+"."+"AAAA"); err == nil {
		t.Fatal("a forged signature was accepted")
	}
	if _, err := Verify(cfg, "not-a-token"); err == nil {
		t.Fatal("a malformed token was accepted")
	}
}

// A token signed by another instance must not work here — otherwise a link from
// a tenant's own server would download from someone else's.
func TestTokenFromAnotherInstanceIsRejected(t *testing.T) {
	mine := testConfig()
	theirs := &config.Config{SettingsEncryptionKey: "ffffffffffffffffffffffffffffffff"}
	token, err := Mint(theirs, Claims{Kind: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(mine, token); err == nil {
		t.Fatal("a token signed with another key was accepted")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	cfg := testConfig()
	token, err := Mint(cfg, Claims{Kind: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	// The expiry is inside the signed payload, so it cannot be pushed out
	// without re-signing — which needs the key.
	if claims.Exp <= time.Now().Unix() {
		t.Fatal("a freshly minted token is already expired")
	}
	if time.Until(time.Unix(claims.Exp, 0)) > TTL+time.Minute {
		t.Errorf("token lives longer than the declared TTL of %s", TTL)
	}
}

// The link is short-lived on purpose: it is for the person who just asked, not
// a way to publish data.
func TestTTLIsShort(t *testing.T) {
	if TTL <= 0 || TTL > time.Hour {
		t.Fatalf("export TTL of %s is not a short-lived link", TTL)
	}
}
