package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/store"
)

// SMTP settings keys. These live only in the database, unlike the Editable set
// above which overlays env-provided config: there is no SMTP env baseline to
// overlay, and an operator fixing a rejected password should not need a redeploy.
//
// There are two profiles under two key prefixes. The account profile carries
// password resets and is the one an instance must have; the alerts profile is
// optional and carries notifications, so an operator can send monitoring mail
// from a different sender — or through a different relay entirely — from the
// mail that authenticates an account.
const (
	// ProfileAccount is the primary transport: password resets, and alerts too
	// unless the alerts profile overrides them.
	ProfileAccount = "smtp."
	// ProfileAlerts is the optional notification transport.
	ProfileAlerts = "smtp.alerts."
)

// SMTP settings keys for the account profile, kept as named constants because
// they are the ones referenced outside this file.
const (
	KeySMTPHost       = "smtp.host"
	KeySMTPPort       = "smtp.port"
	KeySMTPUsername   = "smtp.username"
	KeySMTPPassword   = "smtp.password" // secret
	KeySMTPFrom       = "smtp.from"
	KeySMTPFromName   = "smtp.from_name"
	KeySMTPTLSMode    = "smtp.tls_mode"
	KeySMTPSkipVerify = "smtp.skip_verify"
)

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

// SMTPConfig resolves the account mail transport — password resets, and the
// fallback for alerts when no alerts profile is configured.
//
// Read fresh on every use rather than cached at startup, so a corrected host or
// password takes effect on the next queued message instead of the next restart.
func SMTPConfig(ctx context.Context, st *store.Store, cfg *config.Config) (notify.Config, error) {
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	return smtpProfile(cfg, rows, ProfileAccount), nil
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
	return MergeAlertProfile(smtpProfile(cfg, rows, ProfileAccount), smtpProfile(cfg, rows, ProfileAlerts)), nil
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
// A blank host means different things per profile, and that is the point. On the
// account profile it clears the configuration, which is how an operator turns
// email off. On the alerts profile it means "use the account transport", so the
// sender identity is kept while the connection fields — credentials included —
// are cleared: an alerts profile with no host of its own must not hold a
// password for a server it does not name.
func SaveSMTP(ctx context.Context, st *store.Store, cfg *config.Config, profile string, in SMTPSaveRequest) (notify.Config, error) {
	if !ValidSMTPProfile(profile) {
		return notify.Config{}, fmt.Errorf("unknown SMTP profile %q", profile)
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
		if profile == ProfileAlerts {
			// No host here means "send alerts through the account transport". The
			// sender identity survives — it is the whole point of the profile in
			// this shape — while every connection field goes, credentials first:
			// a profile that names no server must not keep a password for one.
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
		// On the account profile, clearing the host disables email outright. There
		// is nothing left to validate, and the stored password is dropped too so a
		// disabled config keeps no live secret.
		if err := clear(smtpFields...); err != nil {
			return current, err
		}
		return notify.Config{}, nil
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
	if profile == ProfileAlerts {
		return AlertSMTPConfig(ctx, st, cfg)
	}
	return SMTPConfig(ctx, st, cfg)
}
