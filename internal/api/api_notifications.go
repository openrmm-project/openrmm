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

// Two transports: /api/config/smtp is the operator's account profile that carries
// password resets, /api/config/smtp/alerts the tenant's own one that carries
// notifications. They never mix: alerts do not fall back to the account relay
// (see settings.AlertSMTPConfig), and the account relay's test button mails only
// the caller.
//
// Each handler is bound to its profile at route registration rather than
// deriving it from the request path. Deriving it meant one string comparison
// stood between a save and the wrong transport: a path the matcher did not
// recognise fell through to the account profile, and a save routed there
// overwrites the settings that deliver password resets. Binding it makes that
// unrepresentable.

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

// smtpConfigFor resolves one profile. "enabled" on the alerts profile is
// whether alert mail will actually leave, which a sender stored without a host
// (by an older version) does not make true.
func (s *Server) smtpConfigFor(r *http.Request, profile string) (view map[string]any, err error) {
	if profile == settings.ProfileAlerts {
		stored, err := settings.SMTPProfile(r.Context(), s.store, s.cfg, settings.ProfileAlerts)
		if err != nil {
			return nil, err
		}
		out := smtpView(stored)
		out["enabled"] = settings.AlertTransport(stored).Enabled()
		return out, nil
	}
	// The account profile is env-backed and read-only, so there is no stored
	// form to contrast with what is in use.
	out := smtpView(settings.AccountSMTPConfig(s.cfg))
	out["read_only"] = true
	out["source"] = "env"
	return out, nil
}

func (s *Server) getSMTPConfig(profile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, err := s.smtpConfigFor(r, profile)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func (s *Server) updateSMTPConfig(profile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if _, err := settings.SaveSMTP(r.Context(), s.store, s.cfg, profile, in); err != nil {
			// The account transport is env-backed: a write is not a bad request, it
			// is a request to the wrong place. Everything else SaveSMTP rejects is a
			// validation failure (bad TLS mode, credentials on a cleartext link),
			// which the operator can act on.
			if errors.Is(err, settings.ErrAccountProfileReadOnly) {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			if errors.Is(err, settings.ErrAlertsNeedOwnServer) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "alerts_need_own_server"})
				return
			}
			if errors.Is(err, settings.ErrAlertsHostNotPublic) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "alerts_host_not_public"})
				return
			}
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Only the alerts profile reaches here: SaveSMTP refused anything else.
		_ = s.store.AddAudit(r.Context(), actorOf(r), "config.smtp.alerts.update", "app_settings",
			settings.AuditDetail(changedSMTP(profile, in)))
		view, err := s.smtpConfigFor(r, profile)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func changedSMTP(profile string, in settings.SMTPSaveRequest) []string {
	// The audit records which fields were submitted, never their values: the
	// point is a trail of who touched mail delivery, not a copy of the config.
	if strings.TrimSpace(in.Host) == "" {
		// Only a clear gets this far without a host: alert mail is now off.
		return []string{profile + "cleared"}
	}
	out := []string{profile + "host", profile + "port", profile + "from", profile + "tls_mode"}
	if strings.TrimSpace(in.Password) != "" {
		out = append(out, profile+"password")
	}
	return out
}

// testSMTPConfig sends one message with the stored settings.
//
// It sends synchronously, unlike every other message in this system: the whole
// value of a test button is telling the operator the result now. notify.Send's
// timeout bounds how long that can take.
func (s *Server) testSMTPConfig(profile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireUnrestricted(w, r, "global configuration") {
			return
		}
		var in struct {
			To string `json:"to"`
		}
		_ = decode(r, &in)
		to := strings.TrimSpace(in.To)
		own := ""
		if u, err := s.store.GetUserByID(r.Context(), actorOf(r)); err == nil {
			own = u.Email
		}
		if to == "" {
			// Default to the caller's own address so the common case is one click.
			to = own
		}
		if to == "" {
			writeErr(w, http.StatusBadRequest, "no recipient: pass \"to\", or set an address on your account")
			return
		}
		if !validEmail(to) {
			writeErr(w, http.StatusBadRequest, "invalid recipient address")
			return
		}
		// Each button tests the transport it sits next to, resolved the same way
		// delivery resolves it. Testing the account transport and calling it an
		// alert test is how a broken alert relay stays hidden.
		var cfg notify.Config
		var err error
		if profile == settings.ProfileAlerts {
			cfg, err = settings.AlertSMTPConfig(r.Context(), s.store, s.cfg)
		} else {
			cfg = settings.AccountSMTPConfig(s.cfg)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cfg.Enabled() {
			writeErr(w, http.StatusBadRequest, "configure an SMTP host and sender address first")
			return
		}
		// The account relay is the operator's, signing for the operator's
		// domain. A tenant administrator may check that it works, but only by
		// mailing themselves: an open "to" makes it a way to send the
		// operator's mail to anyone. The bootstrap admin token is the operator.
		if profile == settings.ProfileAccount && actorOf(r) != "admin" && to != "" && !strings.EqualFold(to, own) {
			writeErr(w, http.StatusForbidden, "the account transport sends test mail only to your own address")
			return
		}
		what := "Password reset and account mail leaves through this transport."
		if profile == settings.ProfileAlerts {
			what = "Alert notifications leave through this transport."
		}
		body := "This is a test message from OpenRMM.\n\n" + what + "\n"
		if err := notify.Send(cfg, notify.Message{To: to, Subject: "[OpenRMM] Test message", Body: body}); err != nil {
			// The SMTP server's own words are the most useful thing we can show, and
			// this endpoint is already restricted to operators who can edit settings.
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		action := "config.smtp.test"
		if profile == settings.ProfileAlerts {
			action = "config.smtp.alerts.test"
		}
		_ = s.store.AddAudit(r.Context(), actorOf(r), action, "app_settings", auditLabel("to", to))
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "to": to})
	}
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
	// The alerts transport, not the account one: whether alerts can be delivered
	// is what this screen is about.
	cfg, _ := settings.AlertSMTPConfig(r.Context(), s.store, s.cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		// Read-only here: shown so the user knows where their alerts land, but
		// changing it needs users.email.manage.
		"email":         u.Email,
		"subscriptions": notificationPrefs(explicit),
		// Without a transport nothing is delivered whatever the preferences say,
		// so the screen can tell the user that instead of leaving them waiting.
		"delivery_configured": cfg.Enabled(),
	})
}

// updateMyNotifications sets the caller's own subscriptions — which events they
// hear about, not where the mail goes.
//
// The address is intentionally not self-service. It is also the address a
// password reset resolves, so letting a user retarget it themselves is how an
// account takeover starts; it takes users.email.manage. Choosing which alerts to
// receive carries none of that risk.
func (s *Server) updateMyNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Email         *string         `json:"email"`         // rejected unless unchanged
		Subscriptions map[string]bool `json:"subscriptions"` // event type -> enabled
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Answered explicitly rather than ignored: a client that still submits the
	// old self-service field should be told the change did not happen instead of
	// showing the user a success it did not get. Resubmitting the current value
	// is a no-op, so a form that round-trips the field keeps working.
	if in.Email != nil && !strings.EqualFold(strings.TrimSpace(*in.Email), u.Email) {
		writeErr(w, http.StatusForbidden, "changing your address requires the users.email.manage permission")
		return
	}
	for eventType := range in.Subscriptions {
		if !store.ValidNotificationEvent(eventType) {
			writeErr(w, http.StatusBadRequest, "unknown event type: "+eventType)
			return
		}
	}
	for eventType, enabled := range in.Subscriptions {
		if err := s.store.SetNotificationSubscription(r.Context(), u.ID, eventType, enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.getMyNotifications(w, r)
}

// setUserEmail sets a user's address: where their alerts go, and where a
// password reset link is delivered.
//
// Guarded by users.email.manage on top of the usual delegation check, because
// rewriting this address and then requesting a reset is a complete account
// takeover for any user without a second factor. That is also why it is not
// folded into PATCH /users/{id} alongside the role.
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
