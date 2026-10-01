package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/oidc/oidctest"
)

// The provider these tests sign in against is oidctest.FakeIDP: real keys,
// real signatures, a nonce that must match. See that package for why.
type fakeIDP = oidctest.FakeIDP

func newFakeIDP(t *testing.T) *fakeIDP { return oidctest.New(t) }

func discover(t *testing.T, idp *fakeIDP, usernameClaim string) *Provider {
	t.Helper()
	p, err := Discover(context.Background(), Config{
		Issuer:        idp.Issuer,
		ClientID:      "test-client",
		ClientSecret:  "test-secret",
		RedirectURL:   "https://rmm.example.com/api/auth/oidc/callback",
		Scopes:        []string{"openid", "email"},
		UsernameClaim: usernameClaim,
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return p
}

// The happy path, end to end: a real signature, verified against the published
// key, with the nonce this sign-in asked for.
func TestExchangeVerifiesAndReturnsIdentity(t *testing.T) {
	idp := newFakeIDP(t)
	p := discover(t, idp, "email")

	id, err := p.Exchange(context.Background(), "nonce-abc", "verifier", "nonce-abc")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Subject != "provider-subject-123" {
		t.Errorf("subject = %q", id.Subject)
	}
	if id.Username != "alice@example.com" {
		t.Errorf("username = %q, want the email claim", id.Username)
	}
	if id.Issuer != idp.Issuer {
		t.Errorf("issuer = %q", id.Issuer)
	}
	if !id.EmailVerified {
		t.Error("email_verified was dropped")
	}
}

// A token minted for a different sign-in must not be accepted here. Without the
// nonce check, an ID token captured anywhere else can be posted at this
// callback.
func TestExchangeRejectsMismatchedNonce(t *testing.T) {
	idp := newFakeIDP(t)
	p := discover(t, idp, "email")

	_, err := p.Exchange(context.Background(), "nonce-from-another-login", "verifier", "nonce-we-expected")
	if err == nil {
		t.Fatal("a token carrying someone else's nonce was accepted")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("error should name the nonce, got: %v", err)
	}
}

// Signed with a key the provider does not publish.
func TestExchangeRejectsWrongSigningKey(t *testing.T) {
	idp := newFakeIDP(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.Signer = other
	p := discover(t, idp, "email")

	if _, err := p.Exchange(context.Background(), "n", "v", "n"); err == nil {
		t.Fatal("a token signed with an unpublished key was accepted")
	}
}

// Minted for a different client. Without the audience check, a token issued to
// any other application at the same provider would sign someone in here.
func TestExchangeRejectsWrongAudience(t *testing.T) {
	idp := newFakeIDP(t)
	idp.Claims = func(c map[string]any) map[string]any { c["aud"] = "some-other-app"; return c }
	p := discover(t, idp, "email")

	if _, err := p.Exchange(context.Background(), "n", "v", "n"); err == nil {
		t.Fatal("a token minted for another client was accepted")
	}
}

func TestExchangeRejectsExpiredToken(t *testing.T) {
	idp := newFakeIDP(t)
	idp.Claims = func(c map[string]any) map[string]any {
		c["exp"] = time.Now().Add(-2 * time.Hour).Unix()
		return c
	}
	p := discover(t, idp, "email")

	if _, err := p.Exchange(context.Background(), "n", "v", "n"); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// The claim named in configuration wins.
func TestUsernameClaimIsHonoured(t *testing.T) {
	idp := newFakeIDP(t)
	idp.Claims = func(c map[string]any) map[string]any {
		c["preferred_username"] = "alice"
		return c
	}
	p := discover(t, idp, "preferred_username")

	id, err := p.Exchange(context.Background(), "n", "v", "n")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Username != "alice" {
		t.Errorf("username = %q, want the configured claim", id.Username)
	}
	if id.UsernameFallback {
		t.Error("the configured claim was present, yet the username is marked as a fallback")
	}
}

// A provider that omits the configured claim should not break the sign-in when
// something usable is present. Falling back beats a support ticket.
func TestUsernameFallsBackWhenClaimIsAbsent(t *testing.T) {
	idp := newFakeIDP(t)
	p := discover(t, idp, "preferred_username") // never sent by this provider

	id, err := p.Exchange(context.Background(), "n", "v", "n")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Username != "alice@example.com" {
		t.Errorf("username = %q, want the email fallback", id.Username)
	}
	// A fallback may name a new account but must not claim an existing one.
	if !id.UsernameFallback {
		t.Error("username came from a fallback claim but is not marked as one")
	}
}

// Nothing usable at all is an error, not a blank username that would collide
// with the next one.
func TestNoUsableUsernameIsRefused(t *testing.T) {
	p := &Provider{cfg: Config{UsernameClaim: "preferred_username"}}
	if _, err := p.identityFrom("https://issuer", "", map[string]any{}); err == nil {
		t.Fatal("a token with no subject was accepted")
	}
	if _, err := p.identityFrom("https://issuer", "sub-1", map[string]any{}); err == nil {
		t.Fatal("an identity with no username was accepted")
	}
}

// Some providers send email_verified as a string.
func TestEmailVerifiedAcceptsStringForm(t *testing.T) {
	p := &Provider{cfg: Config{UsernameClaim: "email"}}
	id, err := p.identityFrom("https://issuer", "s", map[string]any{
		"email": "b@example.com", "email_verified": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !id.EmailVerified {
		t.Error(`email_verified:"true" was read as false`)
	}
}

// The URL sent to the provider must carry all three defences.
func TestAuthCodeURLCarriesStateNonceAndPKCE(t *testing.T) {
	idp := newFakeIDP(t)
	p := discover(t, idp, "email")

	raw := p.AuthCodeURL("the-state", "the-nonce", "the-verifier")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("state") != "the-state" {
		t.Error("state missing")
	}
	if q.Get("nonce") != "the-nonce" {
		t.Error("nonce missing")
	}
	if q.Get("code_challenge") == "" {
		t.Error("PKCE challenge missing")
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 — plain offers no protection", got)
	}
	// The challenge is a hash, so the verifier itself must never appear.
	if strings.Contains(raw, "the-verifier") {
		t.Error("the PKCE verifier leaked into the authorization URL")
	}
}

func TestNewStateIsRandomAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		v, err := NewState()
		if err != nil {
			t.Fatal(err)
		}
		if seen[v] {
			t.Fatal("NewState repeated a value")
		}
		seen[v] = true
		if len(v) < 40 {
			t.Errorf("state is only %d chars", len(v))
		}
		if v != url.QueryEscape(v) {
			t.Errorf("state needs escaping: %q", v)
		}
	}
}

func TestDiscoverRequiresIssuerAndClient(t *testing.T) {
	if _, err := Discover(context.Background(), Config{ClientID: "x"}); err == nil {
		t.Error("discovery without an issuer was accepted")
	}
	if _, err := Discover(context.Background(), Config{Issuer: "https://x"}); err == nil {
		t.Error("discovery without a client id was accepted")
	}
}

func TestAuthenticationContextPolicy(t *testing.T) {
	idp := newFakeIDP(t)
	p := discover(t, idp, "email")
	p.cfg.ACRValues = []string{"urn:example:acr:mfa"}
	p.cfg.RequiredAMR = []string{"otp", "mfa"}
	u, _ := url.Parse(p.AuthCodeURL("s", "n", "v"))
	if u.Query().Get("acr_values") != "urn:example:acr:mfa" {
		t.Fatal("ACR policy not requested")
	}
	for _, tc := range []struct {
		name    string
		acr     any
		amr     any
		allowed bool
	}{
		{"missing", nil, nil, false},
		{"single factor", "urn:example:acr:single-factor", []string{"pwd"}, false},
		{"ACR only", "urn:example:acr:mfa", nil, false},
		{"recovery is not OTP", "urn:example:acr:mfa", []string{"pwd", "recovery", "mfa"}, false},
		{"malformed methods", "urn:example:acr:mfa", "otp mfa", false},
		{"OTP proof", "urn:example:acr:mfa", []string{"pwd", "otp", "mfa"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idp.Claims = func(c map[string]any) map[string]any { c["acr"] = tc.acr; c["amr"] = tc.amr; return c }
			id, err := p.Exchange(context.Background(), "n", "v", "n")
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
			if tc.allowed && (id.ACR != tc.acr || !contains(id.AMR, "otp")) {
				t.Fatal("verified evidence was dropped")
			}
		})
	}
}

// TestExternalProviderInterop verifies an opt-in OIDC provider with a real
// authorization code. Its caller supplies provider-specific values and claims.
func TestExternalProviderInterop(t *testing.T) {
	issuer := os.Getenv("RMM_TEST_OIDC_ISSUER")
	if issuer == "" {
		t.Skip("set RMM_TEST_OIDC_ISSUER to test an external provider")
	}
	required := []string{
		"RMM_TEST_OIDC_CLIENT_ID", "RMM_TEST_OIDC_SECRET", "RMM_TEST_OIDC_REDIRECT_URL",
		"RMM_TEST_OIDC_CODE", "RMM_TEST_OIDC_VERIFIER", "RMM_TEST_OIDC_ACR",
		"RMM_TEST_OIDC_AMR", "RMM_TEST_OIDC_SUBJECT", "RMM_TEST_OIDC_EMAIL",
		"RMM_TEST_OIDC_NONCE",
	}
	for _, name := range required {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is required when testing an external provider", name)
		}
	}
	methods := strings.Split(os.Getenv("RMM_TEST_OIDC_AMR"), ",")
	p, err := Discover(context.Background(), Config{
		Issuer:       issuer,
		ClientID:     os.Getenv("RMM_TEST_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("RMM_TEST_OIDC_SECRET"),
		RedirectURL:  os.Getenv("RMM_TEST_OIDC_REDIRECT_URL"),
		ACRValues:    []string{os.Getenv("RMM_TEST_OIDC_ACR")},
		RequiredAMR:  methods,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Exchange(context.Background(), os.Getenv("RMM_TEST_OIDC_CODE"), os.Getenv("RMM_TEST_OIDC_VERIFIER"), os.Getenv("RMM_TEST_OIDC_NONCE"))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != os.Getenv("RMM_TEST_OIDC_SUBJECT") || id.Email != os.Getenv("RMM_TEST_OIDC_EMAIL") {
		t.Fatal("external provider returned an unexpected identity")
	}
	for _, method := range methods {
		if !contains(id.AMR, method) {
			t.Fatalf("external provider did not report required method %q", method)
		}
	}
}
