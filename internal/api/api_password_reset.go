package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

const (
	passwordResetTTL     = time.Hour
	passwordResetTimeout = 30 * time.Second

	// Per-account throttle on reset mail. The route's per-IP limit does not
	// protect a mailbox: from many addresses, anyone could send one person an
	// endless stream of reset mail through the operator's relay, spending its
	// quota and its domain's reputation. Past these, requests are accepted and
	// silently dropped, like any other reset request that sends nothing.
	passwordResetCooldown = 5 * time.Minute
	passwordResetsPerDay  = 5
)

// resetAccepted is returned to every reset request, whatever happens next.
//
// Saying "if an account exists" rather than confirming one does is what keeps
// this endpoint from being an account-existence oracle: it is unauthenticated,
// so a distinguishable answer would let anyone test an address list against the
// instance.
var resetAccepted = map[string]string{
	"status": "accepted",
	"detail": "If an account exists for that address, a reset link is on its way.",
}

// requestPasswordReset starts the flow. It answers immediately and identically
// in every case, then does the real work in the background.
//
// The background hand-off is the point, not an optimization. Looking the user
// up, minting a token and waiting on SMTP all take time that varies with whether
// the account exists, and an attacker can measure that. Doing none of it before
// responding removes the timing signal along with the content one.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	_ = decode(r, &in)
	email := strings.TrimSpace(in.Email)
	// Answer first, work later — see above. A malformed address is accepted with
	// the same message so even "is this a valid address" reveals nothing.
	writeJSON(w, http.StatusAccepted, resetAccepted)
	if email == "" || !validEmail(email) {
		return
	}
	go s.deliverPasswordReset(email)
}

// deliverPasswordReset runs detached from the request, so it takes a fresh
// context: the request's is already cancelled by the time this matters.
func (s *Server) deliverPasswordReset(email string) {
	ctx, cancel := context.WithTimeout(context.Background(), passwordResetTimeout)
	defer cancel()

	userID, username, err := s.store.UserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("password reset: lookup failed: %v", err)
		}
		return // no account: nothing to send, and the caller was already told nothing
	}
	// With single sign-on on, an account linked to it has no password to
	// reset: minting one would let whoever reads the mailbox sign in around the
	// provider's MFA and its offboarding.
	if sso, err := s.signsInWithSSO(ctx, userID); err != nil || sso {
		if err != nil {
			log.Printf("password reset: SSO lookup failed: %v", err)
		}
		return
	}
	if recent, err := s.store.PasswordResetsSince(ctx, userID, time.Now().Add(-passwordResetCooldown)); err != nil || recent > 0 {
		if err != nil {
			log.Printf("password reset: throttle lookup failed: %v", err)
		}
		return
	}
	if today, err := s.store.PasswordResetsSince(ctx, userID, time.Now().Add(-24*time.Hour)); err != nil || today >= passwordResetsPerDay {
		if err != nil {
			log.Printf("password reset: throttle lookup failed: %v", err)
		} else {
			log.Printf("password reset: daily limit reached for user %s; request dropped", userID)
		}
		return
	}
	cfg := settings.AccountSMTPConfig(s.cfg)
	if !cfg.Enabled() {
		// Minting a token nobody can receive would let it expire unused while the
		// operator wonders why resets do not work. Say so in the log instead.
		log.Printf("password reset: RMM_SMTP_* is not configured; cannot send to %s", email)
		return
	}
	token := auth.NewToken()
	if err := s.store.CreatePasswordReset(ctx, userID, auth.Hash(token), time.Now().Add(passwordResetTTL)); err != nil {
		log.Printf("password reset: could not create token: %v", err)
		return
	}
	if err := notify.SendPasswordReset(cfg, email, username, s.passwordResetURL(token), passwordResetTTL); err != nil {
		log.Printf("password reset: delivery to %s failed: %v", email, err)
		return
	}
	_ = s.store.AddAudit(ctx, "system", "user.password.reset_requested", userID, nil)
}

// passwordResetURL builds the link the console handles. The token travels in the
// query string because it must survive a cold load in whatever browser the mail
// client opens — there is no session to carry it.
func (s *Server) passwordResetURL(token string) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	return base + "/?reset=" + token
}

// confirmPasswordReset redeems the token and sets the new password.
func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Token = strings.TrimSpace(in.Token)
	if in.Token == "" {
		writeErr(w, http.StatusBadRequest, "reset token required")
		return
	}
	// Look the token up without redeeming it, so a password that fails the
	// rules leaves the link usable — which is what the error below promises.
	// This tells a caller nothing a redemption attempt would not: a token is
	// 256 random bits, so "is this one live" is not a question anyone can ask
	// of a guess.
	const invalidLink = "this reset link is no longer valid — request a new one"
	tokenHash := auth.Hash(in.Token)
	userID, err := s.store.PasswordResetUser(r.Context(), tokenHash)
	if err != nil {
		// Expired, already used and never-existed are one answer on purpose.
		writeErr(w, http.StatusBadRequest, invalidLink)
		return
	}
	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// A link issued before single sign-on was turned on must not set a
	// password on an account that now signs in through the provider.
	if sso, err := s.signsInWithSSO(r.Context(), userID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if sso {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this account signs in with single sign-on", "code": "sso_required"})
		return
	}
	if problem := passwordProblem(in.Password, user.Username); problem != "" {
		writeErr(w, http.StatusBadRequest, problem+" — the link is still valid, try again")
		return
	}
	// Redeem only now. The UPDATE is what makes the link single-use: of two
	// concurrent confirmations, one gets the row and the other is told the
	// link is gone.
	if consumed, err := s.store.ConsumePasswordReset(r.Context(), tokenHash); err != nil || consumed != userID {
		writeErr(w, http.StatusBadRequest, invalidLink)
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), userID, hash); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// Drop every session. Unlike a self-service password change there is no
	// caller session to preserve, and whoever prompted the reset may be the
	// person already holding a stolen one.
	if err := s.store.DeleteUserSessionsExcept(r.Context(), userID, ""); err != nil {
		log.Printf("password reset: could not clear sessions for %s: %v", userID, err)
	}
	_ = s.store.AddAudit(r.Context(), userID, "user.password.reset_completed", userID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// signsInWithSSO reports whether an account must sign in through the identity
// provider: single sign-on is on and the account is linked to it. Such an
// account gets no password sign-in and no password reset, or the provider's
// MFA, conditional access and offboarding could all be stepped around with a
// password. Turning single sign-on off (RMM_OIDC_ENABLED) is the break-glass:
// the accounts sign in with their passwords again.
func (s *Server) signsInWithSSO(ctx context.Context, userID string) (bool, error) {
	if !s.cfg.OIDCEnabled {
		return false, nil
	}
	return s.store.UserLinkedToSSO(ctx, userID)
}
