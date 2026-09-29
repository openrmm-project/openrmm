package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// Confirming alert recipients.
//
// Through the operator's shared alert service (RMM_ALERTS_SMTP_*), alert mail
// goes only to addresses their owners confirmed. The addresses are typed in by
// a tenant administrator, and the alerts carry text the tenant controls: without
// the check, a tenant could point mail signed with the operator's domain at
// anyone. A confirmation is asked for when an address is set and can be sent
// again from the user's own notification settings or by an administrator.
//
// The confirmation mail itself goes through the shared service — the transport
// whose recipients it protects. An instance using a relay of its own does not
// need confirmations, and none is sent.

const (
	emailVerificationTTL      = 48 * time.Hour
	emailVerificationCooldown = 5 * time.Minute
	emailVerificationsPerDay  = 5   // to one address
	instanceVerificationsDay  = 100 // from one instance, to all addresses
)

var (
	errNoAlertService      = errors.New("this instance has no shared alert service, so addresses need no confirmation")
	errVerificationTooSoon = errors.New("a confirmation was sent a few minutes ago; check the inbox, or try again later")
	errVerificationLimit   = errors.New("too many confirmations for this address today; try again tomorrow")
	errInstanceLimit       = errors.New("this instance sent its daily allowance of confirmations; try again tomorrow")
	errNoAddress           = errors.New("this account has no email address")
)

// sendEmailVerification mails a confirmation link for a user's current
// address through the shared alert service, within a per-account throttle.
func (s *Server) sendEmailVerification(ctx context.Context, userID, email, actor string) error {
	cfg := settings.OperatorAlertSMTPConfig(s.cfg)
	if !cfg.Enabled() {
		return errNoAlertService
	}
	if email == "" {
		return errNoAddress
	}
	// Per address, whichever account asks — the throttle protects a mailbox —
	// and per instance, so one tenant cannot walk a list of strangers.
	if n, err := s.store.EmailVerificationsSince(ctx, email, time.Now().Add(-emailVerificationCooldown)); err != nil {
		return err
	} else if n > 0 {
		return errVerificationTooSoon
	}
	if n, err := s.store.EmailVerificationsSince(ctx, email, time.Now().Add(-24*time.Hour)); err != nil {
		return err
	} else if n >= emailVerificationsPerDay {
		return errVerificationLimit
	}
	if n, err := s.store.AllEmailVerificationsSince(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		return err
	} else if n >= instanceVerificationsDay {
		log.Printf("email verification: daily allowance of %d reached; confirmation to %s not sent", instanceVerificationsDay, email)
		return errInstanceLimit
	}
	token := auth.NewToken()
	if err := s.store.CreateEmailVerification(ctx, userID, email, auth.Hash(token), time.Now().Add(emailVerificationTTL)); err != nil {
		return err
	}
	link := strings.TrimRight(s.cfg.PublicURL, "/") + "/?verify-email=" + token
	host := s.cfg.PublicURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	body := fmt.Sprintf("Someone set this address to receive alerts from the OpenRMM instance %s.\n\n"+
		"If that was you or your team, confirm it:\n\n%s\n\n"+
		"The link works once and expires in %d hours. Until the address is confirmed, no alert is sent to it.\n"+
		"If you did not expect this, ignore it: nothing will be sent.\n",
		strings.TrimRight(host, "/"), link, int(emailVerificationTTL.Hours()))
	if err := notify.Send(cfg, notify.Message{To: email, Subject: "[OpenRMM] Confirm your address for alerts", Body: body}); err != nil {
		return fmt.Errorf("send confirmation: %w", err)
	}
	_ = s.store.AddAudit(ctx, actor, "user.email.verify_sent", userID, auditDetail(map[string]string{"email": email}))
	return nil
}

// verifyAfterEmailChange asks for a confirmation when an address is set and
// the shared service is what will deliver to it. In the background: the admin
// who set the address should not wait on SMTP, and a failure is logged.
func (s *Server) verifyAfterEmailChange(u *models.User, actor string) {
	if u == nil || u.Email == "" || u.EmailVerifiedAt != nil || !settings.OperatorAlertSMTPConfig(s.cfg).Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.sendEmailVerification(ctx, u.ID, u.Email, actor); err != nil && !errors.Is(err, errVerificationTooSoon) {
			log.Printf("email verification for user %s: %v", u.ID, err)
		}
	}()
}

// verificationStatus maps the send errors to answers a caller can act on.
func verificationStatus(err error) int {
	switch {
	case errors.Is(err, errVerificationTooSoon), errors.Is(err, errVerificationLimit), errors.Is(err, errInstanceLimit):
		return http.StatusTooManyRequests
	case errors.Is(err, errNoAlertService):
		return http.StatusConflict
	case errors.Is(err, errNoAddress):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
	}
}

// requestMyEmailVerification lets a user ask for a new confirmation link for
// their own address.
func (s *Server) requestMyEmailVerification(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	if u.EmailVerifiedAt != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_confirmed"})
		return
	}
	if err := s.sendEmailVerification(r.Context(), u.ID, u.Email, u.ID); err != nil {
		writeErr(w, verificationStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

// requestUserEmailVerification lets an administrator send a user a new link.
func (s *Server) requestUserEmailVerification(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUserByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if u.EmailVerifiedAt != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_confirmed"})
		return
	}
	if err := s.sendEmailVerification(r.Context(), u.ID, u.Email, actorOf(r)); err != nil {
		writeErr(w, verificationStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

// confirmEmailVerification redeems a link. Unauthenticated, like a password
// reset: holding the link is the proof. Unknown, used, expired and outdated
// links get one answer.
func (s *Server) confirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Token) == "" {
		writeErr(w, http.StatusBadRequest, "confirmation token required")
		return
	}
	userID, err := s.store.ConfirmEmailVerification(r.Context(), auth.Hash(strings.TrimSpace(in.Token)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "this confirmation link is no longer valid — ask for a new one")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), userID, "user.email.verify", userID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}
