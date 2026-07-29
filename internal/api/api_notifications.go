package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// ---------- SMTP transport (instance-wide) ----------

// smtpView is the read shape. Like every other secret on the settings screen the
// password is exposed only as a boolean: the console never needs the value back,
// and not returning it means a compromised session cannot exfiltrate it.
func smtpView(cfg notify.Config) map[string]any {
	return map[string]any{
		"host":         cfg.Host,
		"port":         cfg.Port,
		"username":     cfg.Username,
		"from":         cfg.From,
		"from_name":    cfg.FromName,
		"tls_mode":     cfg.TLSMode,
		"skip_verify":  cfg.SkipVerify,
		"password_set": cfg.Password != "",
		"enabled":      cfg.Enabled(),
	}
}

func (s *Server) getSMTPConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := settings.SMTPConfig(r.Context(), s.store, s.cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, smtpView(cfg))
}

func (s *Server) updateSMTPConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
	var in settings.SMTPSaveRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.From != "" && !validEmail(in.From) {
		writeErr(w, http.StatusBadRequest, "invalid sender address")
		return
	}
	cfg, err := settings.SaveSMTP(r.Context(), s.store, s.cfg, in)
	if err != nil {
		// SaveSMTP's failures are validation failures (bad TLS mode, credentials
		// on a cleartext link), which the operator can act on.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "config.smtp.update", "app_settings",
		settings.AuditDetail(changedSMTP(in)))
	writeJSON(w, http.StatusOK, smtpView(cfg))
}

func changedSMTP(in settings.SMTPSaveRequest) []string {
	// The audit records which fields were submitted, never their values: the
	// point is a trail of who touched mail delivery, not a copy of the config.
	var out []string
	if strings.TrimSpace(in.Host) == "" {
		return []string{"smtp.cleared"}
	}
	out = append(out, "smtp.host", "smtp.port", "smtp.from", "smtp.tls_mode")
	if strings.TrimSpace(in.Password) != "" {
		out = append(out, "smtp.password")
	}
	return out
}

// testSMTPConfig sends one message with the stored settings.
//
// It sends synchronously, unlike every other message in this system: the whole
// value of a test button is telling the operator the result now. notify.Send's
// timeout bounds how long that can take.
func (s *Server) testSMTPConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	_ = decode(r, &in)
	to := strings.TrimSpace(in.To)
	if to == "" {
		// Default to the caller's own address so the common case is one click.
		if u, err := s.store.GetUserByID(r.Context(), actorOf(r)); err == nil {
			to = u.Email
		}
	}
	if to == "" {
		writeErr(w, http.StatusBadRequest, "no recipient: pass \"to\", or set an address on your account")
		return
	}
	if !validEmail(to) {
		writeErr(w, http.StatusBadRequest, "invalid recipient address")
		return
	}
	cfg, err := settings.SMTPConfig(r.Context(), s.store, s.cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !cfg.Enabled() {
		writeErr(w, http.StatusBadRequest, "configure an SMTP host and sender address first")
		return
	}
	body := "This is a test message from OpenRMM.\n\n" +
		"If you are reading it, alert delivery is working.\n"
	if err := notify.Send(cfg, notify.Message{To: to, Subject: "[OpenRMM] Test message", Body: body}); err != nil {
		// The SMTP server's own words are the most useful thing we can show, and
		// this endpoint is already restricted to operators who can edit settings.
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "config.smtp.test", "app_settings", auditLabel("to", to))
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "to": to})
}

// ---------- Per-user notification preferences ----------

// notificationPrefs merges a user's explicit choices over the defaults, so the
// screen always shows a complete, meaningful set.
func notificationPrefs(explicit map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(store.NotificationEventTypes))
	for _, eventType := range store.NotificationEventTypes {
		enabled, ok := explicit[eventType]
		if !ok {
			enabled = store.DefaultSubscribed(eventType)
		}
		out = append(out, map[string]any{
			"event_type": eventType,
			"enabled":    enabled,
			"default":    store.DefaultSubscribed(eventType),
			"explicit":   ok,
		})
	}
	return out
}

// sessionUser resolves the caller to a real user row. The bootstrap admin token
// and API tokens authenticate as principals without one; they have no mailbox
// and no preferences, so they are turned away rather than silently no-oped.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	u, err := s.store.GetUserByID(r.Context(), actorOf(r))
	if err != nil {
		writeErr(w, http.StatusForbidden, "notification preferences belong to a user account")
		return nil, false
	}
	return u, true
}

func (s *Server) getMyNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	explicit, err := s.store.ListNotificationSubscriptions(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg, _ := settings.SMTPConfig(r.Context(), s.store, s.cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"email":         u.Email,
		"subscriptions": notificationPrefs(explicit),
		// Without a transport nothing is delivered whatever the preferences say,
		// so the screen can tell the user that instead of leaving them waiting.
		"delivery_configured": cfg.Enabled(),
	})
}

// updateMyNotifications sets the caller's own address and subscriptions. Email
// is self-service because the address is where *their* alerts go; the access
// scope that decides which devices they hear about is unaffected by it.
func (s *Server) updateMyNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Email         *string         `json:"email"`         // nil = leave unchanged
		Subscriptions map[string]bool `json:"subscriptions"` // event type -> enabled
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	for eventType := range in.Subscriptions {
		if !store.ValidNotificationEvent(eventType) {
			writeErr(w, http.StatusBadRequest, "unknown event type: "+eventType)
			return
		}
	}
	if in.Email != nil {
		updated, err := s.setEmail(r, u.ID, *in.Email)
		if err != nil {
			writeErr(w, emailStatus(err), emailMessage(err))
			return
		}
		u = updated
	}
	for eventType, enabled := range in.Subscriptions {
		if err := s.store.SetNotificationSubscription(r.Context(), u.ID, eventType, enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.getMyNotifications(w, r)
}

// setUserEmail lets an operator with users.manage set someone else's address —
// needed to onboard a technician who should get alerts before their first login.
func (s *Server) setUserEmail(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "id")
	if _, ok := s.requireUserManage(w, r, target); !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := s.setEmail(r, target, in.Email)
	if err != nil {
		writeErr(w, emailStatus(err), emailMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// setEmail validates, persists and audits an address change.
func (s *Server) setEmail(r *http.Request, userID, email string) (*models.User, error) {
	email = strings.TrimSpace(email)
	if email != "" && !validEmail(email) {
		return nil, errInvalidEmail
	}
	u, err := s.store.UpdateUserEmail(r.Context(), userID, email)
	if err != nil {
		return nil, err
	}
	detail, _ := json.Marshal(map[string]string{"user": u.Username, "email": u.Email})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.email.update", userID, detail)
	return u, nil
}

var errInvalidEmail = errors.New("invalid email address")

func emailStatus(err error) int {
	switch {
	case errors.Is(err, errInvalidEmail):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict
	default:
		return httpStatusFor(err)
	}
}

func emailMessage(err error) string {
	if errors.Is(err, store.ErrConflict) {
		return "that address is already used by another user"
	}
	return err.Error()
}

// validEmail is deliberately shallow: it rejects the typos and the header
// injection attempts, and leaves proving the mailbox exists to actually sending
// to it. A stricter regex would reject valid addresses for no gain.
func validEmail(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 254 || strings.ContainsAny(v, "\r\n ") {
		return false
	}
	addr, err := mail.ParseAddress(v)
	return err == nil && addr.Address == v && strings.Count(v, "@") == 1
}
