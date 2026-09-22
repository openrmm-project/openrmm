package settings

import (
	"context"
	"errors"
	"fmt"
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
// The alerts transport carries notifications and stays fully editable in the
// console, because who a tenant's monitoring mail comes from is genuinely their
// choice. When it names no host of its own it rides the account transport.
const (
	// ProfileAccount is the env-backed transport: password resets, and the
	// fallback for alerts. Read-only through the API.
	ProfileAccount = "smtp."
	// ProfileAlerts is the optional, DB-backed notification transport.
	ProfileAlerts = "smtp.alerts."
)

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

// AccountSMTPConfig resolves the account mail transport — password resets, and
// the fallback for alerts when no alerts profile is configured.
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

// AlertSMTPConfig resolves the transport that carries notifications.
//
// Inheritance is deliberately all-or-nothing on the connection, and per-field
// only on the sender identity:
//
//   - No alerts host means "the same server": the account transport is used with
//     the alert sender identity layered on top, which is the common case of
//     alerts@ and no-reply@ leaving through one relay.
//   - An alerts host means a genuinely separate transport, and then nothing is
//     inherited. Carrying the account profile's username and password over to a
//     host the operator named separately would hand one server's mailbox
//     credentials to another.
func AlertSMTPConfig(ctx context.Context, st *store.Store, cfg *config.Config) (notify.Config, error) {
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	return MergeAlertProfile(AccountSMTPConfig(cfg), smtpProfile(cfg, rows, ProfileAlerts)), nil
}

// MergeAlertProfile applies the inheritance rule described on AlertSMTPConfig.
// Split out from the settings lookup so the rule that keeps one server's
// credentials off another server can be tested on its own.
func MergeAlertProfile(account, alerts notify.Config) notify.Config {
	if alerts.Host != "" {
		return alerts
	}
	out := account
	if alerts.From != "" {
		out.From = alerts.From
		// The display name travels with the address it belongs to: keeping the
		// account profile's name next to the alert address would read as a
		// mismatched "OpenRMM Account <alerts@…>".
		out.FromName = alerts.FromName
	} else if alerts.FromName != "" {
		out.FromName = alerts.FromName
	}
	return out
}

// SMTPProfile resolves one profile exactly as stored, with no inheritance. The
// settings screen needs this to show an operator what they typed; delivery uses
// SMTPConfig or AlertSMTPConfig, which apply the fallback rules.
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
// A blank host means "use the account transport", so the sender identity is kept
// while the connection fields — credentials included — are cleared: an alerts
// profile with no host of its own must not hold a password for a server it does
// not name.
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
		// No host here means "send alerts through the account transport". The
		// sender identity survives — it is the whole point of the profile in this
		// shape — while every connection field goes, credentials first: a profile
		// that names no server must not keep a password for one.
		if err := clear("host", "port", "username", "password", "tls_mode", "skip_verify"); err != nil {
			return current, err
		}
		for _, op := range [][2]string{{"from", next.From}, {"from_name", next.FromName}} {
			if err := st.UpsertAppSetting(ctx, profile+op[0], op[1], false); err != nil {
				return current, err
			}
		}
		return AlertSMTPConfig(ctx, st, cfg)
	}
	if err := next.Validate(); err != nil {
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
