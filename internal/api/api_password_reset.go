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
	cfg, err := settings.SMTPConfig(ctx, s.store, s.cfg)
	if err != nil || !cfg.Enabled() {
		// Minting a token nobody can receive would let it expire unused while the
		// operator wonders why resets do not work. Say so in the log instead.
		log.Printf("password reset: SMTP is not configured; cannot send to %s", email)
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
	// Consume before validating the password: the token is single-use either way,
	// so an attacker cannot use a rejected password as a way to probe which
	// tokens are live. The user simply asks for a new link.
	userID, err := s.store.ConsumePasswordReset(r.Context(), auth.Hash(in.Token))
	if err != nil {
		// Expired, already used and never-existed are one answer on purpose.
		writeErr(w, http.StatusBadRequest, "this reset link is no longer valid — request a new one")
		return
	}
	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !validPassword(in.Password, user.Username) {
		writeErr(w, http.StatusBadRequest,
			"password must be at least 12 characters and not be a common password — request a new link and try again")
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
