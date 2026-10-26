// Package oidctest is a whole identity provider for tests: discovery, a
// published signing key, an authorization endpoint and a token endpoint that
// mints real RS256-signed ID tokens.
//
// Verification is the part of single sign-on that must not be improvised, so
// tests exercise it against real signatures rather than a stub that always
// says yes: a fake provider that never rejects anything proves the code calls a
// function, not that the function protects anything.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ClientID is the audience the fake provider signs tokens for.
const ClientID = "test-client"

// FakeIDP is a running identity provider.
type FakeIDP struct {
	Server *httptest.Server
	Issuer string
	KeyID  string

	// Claims, when set, rewrites the ID token's claims, so a test can make a
	// token wrong in exactly one way.
	Claims func(base map[string]any) map[string]any
	// Signer, when set, signs with a key other than the published one.
	Signer *rsa.PrivateKey
	// Deny, when set, makes /authorize answer with this OAuth error instead of
	// a code, the way a provider reports a user who declined or failed MFA.
	Deny string
	// Discovery, when set, adds fields to the discovery document.
	Discovery map[string]any

	key *rsa.PrivateKey
}

// New starts a provider and stops it when the test ends.
//
// /authorize sends the browser straight back to redirect_uri with a code and
// the state it was given: the user is taken to have signed in. The code is the
// nonce the request asked for, which /token echoes into the ID token, so a
// token only verifies for the sign-in that requested it.
func New(t testing.TB) *FakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	idp := &FakeIDP{key: key, KeyID: "test-key-1"}

	mux := http.NewServeMux()
	idp.Server = httptest.NewServer(mux)
	idp.Issuer = idp.Server.URL
	t.Cleanup(idp.Server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                idp.Issuer,
			"authorization_endpoint":                idp.Issuer + "/authorize",
			"token_endpoint":                        idp.Issuer + "/token",
			"jwks_uri":                              idp.Issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		for k, v := range idp.Discovery {
			doc[k] = v
		}
		writeJSON(w, doc)
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": idp.KeyID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		back, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || back.Scheme == "" {
			http.Error(w, "bad redirect_uri", http.StatusBadRequest)
			return
		}
		out := back.Query()
		out.Set("state", q.Get("state"))
		if idp.Deny != "" {
			out.Set("error", idp.Deny)
		} else {
			out.Set("code", q.Get("nonce"))
		}
		back.RawQuery = out.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		base := map[string]any{
			"iss": idp.Issuer,
			"sub": "provider-subject-123",
			"aud": ClientID,
			"exp": time.Now().Add(5 * time.Minute).Unix(),
			"iat": time.Now().Unix(),
			// The nonce echoed here is the one the sign-in asked for via the code.
			"nonce":          r.Form.Get("code"),
			"email":          "alice@example.com",
			"email_verified": true,
			"name":           "Alice Example",
		}
		if idp.Claims != nil {
			base = idp.Claims(base)
		}
		writeJSON(w, map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"id_token":     idp.Sign(t, base),
		})
	})
	return idp
}

// Sign signs claims as the provider would (or with Signer, when set).
func (idp *FakeIDP) Sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	key := idp.key
	if idp.Signer != nil {
		key = idp.Signer
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", idp.KeyID))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
