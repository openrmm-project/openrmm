// Package oidc signs a person in through an external identity provider.
//
// It answers exactly one question — who is this — and deliberately not the
// other one. Roles and access scope live in the RMM's own database, so a
// provider cannot promote itself, and an operator who points the console at a
// hosted control plane is not handing that control plane administrative power
// over their fleet. The provider vouches for an identity; this server decides
// what the identity is allowed to do.
//
// Verification is delegated to github.com/coreos/go-oidc, which does the part
// that must not be improvised: fetching and caching the provider's keys,
// checking the signature and algorithm, and validating the issuer, audience
// and expiry of the ID token. What is written here is the part that is
// specific to this application — which claim becomes a username, and what to do
// with someone the console has never seen.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is what the provider vouched for, reduced to what this server uses.
// Nothing about permissions appears here, on purpose.
type Identity struct {
	// Issuer and Subject together name the account. Never the email on its own:
	// addresses get reassigned when people leave, and `sub` is the only claim
	// OIDC guarantees is stable and unique within an issuer. Matching on email
	// means whoever inherits alice@ inherits Alice's console.
	Issuer  string
	Subject string

	// Username is the claim chosen by RMM_OIDC_USERNAME_CLAIM, used when an
	// account is created and to link an existing local user on first sign-in.
	Username string
	Email    string
	Name     string
	// EmailVerified is reported, not enforced, because providers disagree about
	// it: some omit it entirely for a directory where every address is by
	// definition verified. The caller decides.
	EmailVerified bool
	ACR           string
	AMR           []string
}

// Config is the resolved provider configuration.
type Config struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	UsernameClaim string
	ACRValues     []string
	RequiredAMR   []string
}

// Provider is a discovered OIDC provider, ready to sign people in. Build it
// once at startup and share it: it caches the provider's signing keys and
// refetches them on its own when they rotate.
type Provider struct {
	cfg      Config
	oauth    oauth2.Config
	verifier *coreoidc.IDTokenVerifier
	provider *coreoidc.Provider
}

// Discover fetches the provider's metadata and returns a ready Provider.
//
// It is called at startup and it fails loudly: a console configured for SSO
// that silently starts without it is a console where the sign-in button is
// broken and nobody knows why until someone tries it.
func Discover(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc: issuer and client id are required")
	}
	// go-oidc checks that the issuer inside the discovery document matches the
	// URL it was fetched from. That check is the reason a stolen discovery
	// document cannot redirect this server to someone else's keys.
	provider, err := coreoidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery failed for %s: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{coreoidc.ScopeOpenID, "profile", "email"}
	}
	return &Provider{
		cfg:      cfg,
		provider: provider,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&coreoidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// AuthCodeURL is where the browser is sent to sign in.
//
// state, nonce and the PKCE challenge each defend a different step, and all
// three are used because dropping any one of them is a real attack:
//
//	state     the callback belongs to a sign-in this server started (CSRF)
//	nonce     the ID token was minted for this sign-in, not replayed from an
//	          older one
//	PKCE      the authorization code is redeemed by whoever asked for it, even
//	          if the code leaks from a log or a referrer header
func (p *Provider) AuthCodeURL(state, nonce, codeVerifier string) string {
	opts := []oauth2.AuthCodeOption{
		coreoidc.Nonce(nonce),
		oauth2.S256ChallengeOption(codeVerifier),
	}
	if len(p.cfg.ACRValues) > 0 {
		opts = append(opts, oauth2.SetAuthURLParam("acr_values", strings.Join(p.cfg.ACRValues, " ")))
	}
	return p.oauth.AuthCodeURL(state, opts...)
}

// Exchange redeems the authorization code and verifies the ID token that comes
// back, returning the identity it vouches for.
//
// expectedNonce must be the nonce stored when the sign-in started. A token
// whose nonce does not match is refused: that is what stops an ID token
// captured elsewhere from being posted at this callback.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (*Identity, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("oidc: code exchange failed: %w", err)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, errors.New("oidc: provider returned no id_token (is the 'openid' scope granted?)")
	}
	idToken, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("oidc: id token verification failed: %w", err)
	}
	if idToken.Nonce != expectedNonce {
		return nil, errors.New("oidc: id token nonce does not match this sign-in")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: could not read claims: %w", err)
	}
	// acr_values is only a request to the provider. Enforce the local policy
	// against signed claims before resolving or provisioning any account.
	if len(p.cfg.ACRValues) > 0 && !contains(p.cfg.ACRValues, stringClaim(claims, "acr")) {
		return nil, errors.New("oidc: authentication context does not satisfy policy")
	}
	methods := stringListClaim(claims, "amr")
	for _, required := range p.cfg.RequiredAMR {
		if !contains(methods, required) {
			return nil, errors.New("oidc: authentication methods do not satisfy policy")
		}
	}
	return p.identityFrom(idToken.Issuer, idToken.Subject, claims)
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func stringListClaim(claims map[string]any, key string) []string {
	values, ok := claims[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		v, ok := value.(string)
		if !ok {
			return nil
		}
		result = append(result, v)
	}
	return result
}

// identityFrom maps a verified token's claims onto an Identity. Split out from
// Exchange so the mapping — the part with the judgement calls in it — is
// testable without a provider.
func (p *Provider) identityFrom(issuer, subject string, claims map[string]any) (*Identity, error) {
	if subject == "" {
		return nil, errors.New("oidc: token has no subject claim")
	}
	id := &Identity{
		Issuer:        issuer,
		Subject:       subject,
		Email:         stringClaim(claims, "email"),
		Name:          stringClaim(claims, "name"),
		EmailVerified: boolClaim(claims, "email_verified"),
		ACR:           stringClaim(claims, "acr"),
		AMR:           stringListClaim(claims, "amr"),
	}

	claimName := p.cfg.UsernameClaim
	if claimName == "" {
		claimName = "email"
	}
	id.Username = strings.TrimSpace(stringClaim(claims, claimName))
	// Falling back is better than failing: some providers omit `email` for
	// service directories but always send `preferred_username`, and vice versa.
	// Refusing the sign-in over a missing optional claim would be a support
	// ticket for something this can resolve.
	if id.Username == "" {
		for _, fallback := range []string{"email", "preferred_username", "sub"} {
			if v := strings.TrimSpace(stringClaim(claims, fallback)); v != "" {
				id.Username = v
				break
			}
		}
	}
	if id.Username == "" {
		return nil, fmt.Errorf("oidc: no username: neither %q nor email, preferred_username or sub was present", claimName)
	}
	return id, nil
}

func stringClaim(claims map[string]any, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

func boolClaim(claims map[string]any, key string) bool {
	switch v := claims[key].(type) {
	case bool:
		return v
	case string:
		// Some providers send "true"/"false" as strings. Reading it is a line;
		// not reading it is a confusing bug report.
		return v == "true"
	}
	return false
}

// NewState returns a cryptographically random, URL-safe value for state,
// nonce, and the PKCE verifier.
func NewState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// httpClientWithTimeout keeps a wedged provider from holding a request open
// forever. Discovery and the token exchange both go through it.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

// WithHTTPClient returns a context that makes go-oidc and oauth2 use c. Both
// libraries read the client from the context rather than taking one.
func WithHTTPClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c)
}

var _ = json.Marshal // keep encoding/json for claim debugging helpers
