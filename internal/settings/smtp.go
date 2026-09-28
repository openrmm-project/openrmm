package settings

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/store"
)

// SMTP transports. There are two, and they are configured in different places
// on purpose.
//
// The account transport carries password resets. It is env-only — no console
// form, no app_settings row. The server operator controls password-reset mail;
// an editable copy in the database would let a settings administrator redirect
// it. A reset link is an authentication artefact, not a preference.
//
// The alerts transport carries notifications and is the tenant's own: their
// relay, their sender, edited in the console. It never rides the account
// transport. On a hosted instance the account transport is the operator's relay,
// signing for the operator's domain; letting a tenant send through it — even
// only with a sender of their choosing — would let any tenant admin mail
// strangers as the operator. Without a host of its own, alert mail is off.
const (
	// ProfileAccount is the env-backed transport: password resets only.
	// Read-only through the API.
	ProfileAccount = "smtp."
	// ProfileAlerts is the DB-backed notification transport, configured by the
	// tenant. Alert mail is off until it names a host.
	ProfileAlerts = "smtp.alerts."
)

// ErrAlertsNeedOwnServer rejects an alerts profile that sets a sender or
// credentials without naming a server: there is no shared relay to send them
// through.
var ErrAlertsNeedOwnServer = errors.New("alert mail leaves only through your own SMTP server: set a host, or clear the sender to turn alert mail off")

// ErrAccountProfileReadOnly is returned when a caller tries to write the account
// profile, which only the environment can set.
var ErrAccountProfileReadOnly = errors.New("the account SMTP transport is configured by the environment (RMM_SMTP_*) and cannot be edited here")

// smtpFields are the per-profile key suffixes, in the order the settings screen
// shows them.
var smtpFields = []string{
	"host", "port", "username", "password",
	"from", "from_name", "tls_mode", "skip_verify",
}

// ValidSMTPProfile reports whether a profile name is one this package knows, so
// a caller cannot address an arbitrary settings prefix.
func ValidSMTPProfile(profile string) bool {
	return profile == ProfileAccount || profile == ProfileAlerts
}

// SMTPKeys returns every key belonging to a profile.
func SMTPKeys(profile string) []string {
	out := make([]string, 0, len(smtpFields))
	for _, f := range smtpFields {
		out = append(out, profile+f)
	}
	return out
}

// smtpSecret reports whether a key holds a password, which is stored encrypted.
func smtpSecret(key string) bool { return strings.HasSuffix(key, ".password") }

// AccountSMTPConfig resolves the account mail transport — password resets. It is
// never used for alerts; see AlertSMTPConfig.
//
// Pure, and reads nothing from the database: the environment is the only source.
// An empty host means email is off, which is the correct default for a fresh
// self-hosted instance that has not been told where to send mail.
func AccountSMTPConfig(cfg *config.Config) notify.Config {
	if cfg == nil || cfg.SMTPHost == "" {
		return notify.Config{}
	}
	port := cfg.SMTPPort
	if port <= 0 {
		port = 587 // submission with STARTTLS, the near-universal default
	}
	tlsMode := cfg.SMTPTLSMode
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	return notify.Config{
		Host:       cfg.SMTPHost,
		Port:       port,
		Username:   cfg.SMTPUsername,
		Password:   cfg.SMTPPassword,
		From:       cfg.SMTPFrom,
		FromName:   cfg.SMTPFromName,
		TLSMode:    tlsMode,
		SkipVerify: cfg.SMTPSkipVerify,
		Timeout:    20 * time.Second,
	}
}

// AlertSMTPConfig resolves the transport that carries notifications: the
// alerts profile and nothing else. Nothing is inherited from the account
// transport — not the relay, not its credentials, not its sender.
func AlertSMTPConfig(ctx context.Context, st *store.Store, cfg *config.Config) (notify.Config, error) {
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	return alertDelivery(cfg, smtpProfile(cfg, rows, ProfileAlerts)), nil
}

// alertDelivery is AlertTransport plus the dial policy: the alerts relay was
// typed into the console, so it may only be a public address unless the
// operator allowed private ones (RMM_SMTP_ALERTS_ALLOW_PRIVATE).
func alertDelivery(cfg *config.Config, alerts notify.Config) notify.Config {
	out := AlertTransport(alerts)
	if out.Host != "" {
		out.PublicOnly = cfg == nil || !cfg.SMTPAlertsAllowPrivate
	}
	return out
}

// ErrAlertsHostNotPublic rejects an alerts relay that resolves to a private,
// loopback or link-local address while those are not allowed.
var ErrAlertsHostNotPublic = errors.New("the alerts SMTP server must be a public address (an internal relay needs RMM_SMTP_ALERTS_ALLOW_PRIVATE=true on the server)")

// checkAlertsHost refuses, at save time, a host that resolves to a non-public
// address. It is early feedback, not the guard — the dialer checks the address
// it actually connects to. A name that does not resolve right now is let
// through for the same reason: the dial decides.
func checkAlertsHost(ctx context.Context, cfg *config.Config, host string) error {
	if cfg != nil && cfg.SMTPAlertsAllowPrivate {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !notify.PublicIP(ip) {
			return ErrAlertsHostNotPublic
		}
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if !notify.PublicIP(a.IP) {
			return ErrAlertsHostNotPublic
		}
	}
	return nil
}

// AlertTransport is the rule on its own, so it can be tested without a
// database: an alerts profile that names no host is no transport at all. A
// sender stored by an older version, which used to ride the account relay, is
// ignored rather than honoured.
func AlertTransport(alerts notify.Config) notify.Config {
	if alerts.Host == "" {
		return notify.Config{}
	}
	return alerts
}

// SMTPProfile resolves one profile exactly as stored. The settings screen needs
// this to show an operator what they typed; delivery uses AccountSMTPConfig or
// AlertSMTPConfig.
func SMTPProfile(ctx context.Context, st *store.Store, cfg *config.Config, profile string) (notify.Config, error) {
	if !ValidSMTPProfile(profile) {
		return notify.Config{}, fmt.Errorf("unknown SMTP profile %q", profile)
	}
	if profile == ProfileAccount {
		// Nothing is stored for this profile; what the operator "typed" is the
		// environment the server booted with.
		return AccountSMTPConfig(cfg), nil
	}
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	return smtpProfile(cfg, rows, profile), nil
}

// smtpProfile reads one profile's keys out of an already-loaded settings map.
func smtpProfile(cfg *config.Config, rows map[string]store.AppSetting, profile string) notify.Config {
	value := func(field string) string {
		row, ok := rows[profile+field]
		if !ok || row.Value == "" {
			return ""
		}
		if row.IsSecret {
			v, err := decrypt(cfg, row.Value)
			if err != nil {
				// Same fail-safe as Effective: an unreadable secret is treated as
				// unset so the console keeps working and the admin can re-enter it.
				return ""
			}
			return v
		}
		return row.Value
	}

	port, err := strconv.Atoi(value("port"))
	if err != nil || port <= 0 {
		port = 587 // submission with STARTTLS, the near-universal default
	}
	tlsMode := value("tls_mode")
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	return notify.Config{
		Host:       value("host"),
		Port:       port,
		Username:   value("username"),
		Password:   value("password"),
		From:       value("from"),
		FromName:   value("from_name"),
		TLSMode:    tlsMode,
		SkipVerify: value("skip_verify") == "true",
		Timeout:    20 * time.Second,
	}
}

// SMTPSaveRequest is accepted by PUT /api/config/smtp. As with the AI and
// MeshCentral secrets, a blank password preserves the stored one so the UI never
// has to round-trip it.
type SMTPSaveRequest struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"` // blank = preserve
	From       string `json:"from"`
	FromName   string `json:"from_name"`
	TLSMode    string `json:"tls_mode"`
	SkipVerify *bool  `json:"skip_verify"` // nil = preserve current
}

// SaveSMTP persists one profile's mail transport settings.
//
// It validates through notify.Config so a bad TLS mode — or credentials aimed at
// an unencrypted link — is rejected at save time rather than discovered later as
// a silently stuck outbox.
//
// Only the alerts profile is writable; the account transport comes from the
// environment and is rejected here.
//
// A blank host turns alert mail off and clears the whole profile. A sender or
// credentials without a host are refused (ErrAlertsNeedOwnServer): there is no
// shared relay they could be meant for.
func SaveSMTP(ctx context.Context, st *store.Store, cfg *config.Config, profile string, in SMTPSaveRequest) (notify.Config, error) {
	if !ValidSMTPProfile(profile) {
		return notify.Config{}, fmt.Errorf("unknown SMTP profile %q", profile)
	}
	if profile == ProfileAccount {
		return notify.Config{}, ErrAccountProfileReadOnly
	}
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	current := smtpProfile(cfg, rows, profile)
	next := notify.Config{
		Host:       strings.TrimSpace(in.Host),
		Port:       in.Port,
		Username:   strings.TrimSpace(in.Username),
		Password:   current.Password,
		From:       strings.TrimSpace(in.From),
		FromName:   strings.TrimSpace(in.FromName),
		TLSMode:    strings.TrimSpace(in.TLSMode),
		SkipVerify: current.SkipVerify,
		Timeout:    current.Timeout,
	}
	if strings.TrimSpace(in.Password) != "" {
		next.Password = in.Password
	}
	if in.SkipVerify != nil {
		next.SkipVerify = *in.SkipVerify
	}
	if next.Port <= 0 {
		next.Port = 587
	}
	if next.TLSMode == "" {
		next.TLSMode = "starttls"
	}
	clear := func(fields ...string) error {
		for _, f := range fields {
			if err := st.UpsertAppSetting(ctx, profile+f, "", smtpSecret(profile+f)); err != nil {
				return err
			}
		}
		return nil
	}

	if next.Host == "" {
		// No host: alert mail off. A sender or a login here would have been
		// meant for some relay, and the only other one is the operator's.
		// The display name alone is harmless — the console fills it in by
		// default — so it is dropped with the rest rather than refused.
		if next.From != "" || next.Username != "" || strings.TrimSpace(in.Password) != "" {
			return current, ErrAlertsNeedOwnServer
		}
		if err := clear(smtpFields...); err != nil {
			return current, err
		}
		return AlertSMTPConfig(ctx, st, cfg)
	}
	if err := next.Validate(); err != nil {
		return current, err
	}
	if err := checkAlertsHost(ctx, cfg, next.Host); err != nil {
		return current, err
	}
	for _, op := range [][2]string{
		{"host", next.Host},
		{"port", strconv.Itoa(next.Port)},
		{"username", next.Username},
		{"from", next.From},
		{"from_name", next.FromName},
		{"tls_mode", next.TLSMode},
		{"skip_verify", strconv.FormatBool(next.SkipVerify)},
	} {
		if err := st.UpsertAppSetting(ctx, profile+op[0], op[1], false); err != nil {
			return current, err
		}
	}
	if strings.TrimSpace(in.Password) != "" {
		enc, err := Encrypt(cfg, in.Password)
		if err != nil {
			return current, err
		}
		if err := st.UpsertAppSetting(ctx, profile+"password", enc, true); err != nil {
			return current, err
		}
	}
	return AlertSMTPConfig(ctx, st, cfg)
}
