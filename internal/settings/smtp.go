package settings

import (
	"context"
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

// SMTPKeys is the full set, in the order the settings screen shows them.
var SMTPKeys = []string{
	KeySMTPHost, KeySMTPPort, KeySMTPUsername, KeySMTPPassword,
	KeySMTPFrom, KeySMTPFromName, KeySMTPTLSMode, KeySMTPSkipVerify,
}

// SMTPSecretKeys are stored encrypted at rest.
var SMTPSecretKeys = map[string]bool{KeySMTPPassword: true}

// SMTPConfig resolves the current mail transport settings.
//
// Read fresh on every use rather than cached at startup, so a corrected host or
// password takes effect on the next queued message instead of the next restart.
func SMTPConfig(ctx context.Context, st *store.Store, cfg *config.Config) (notify.Config, error) {
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	value := func(key string) string {
		row, ok := rows[key]
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

	port, err := strconv.Atoi(value(KeySMTPPort))
	if err != nil || port <= 0 {
		port = 587 // submission with STARTTLS, the near-universal default
	}
	tlsMode := value(KeySMTPTLSMode)
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	return notify.Config{
		Host:       value(KeySMTPHost),
		Port:       port,
		Username:   value(KeySMTPUsername),
		Password:   value(KeySMTPPassword),
		From:       value(KeySMTPFrom),
		FromName:   value(KeySMTPFromName),
		TLSMode:    tlsMode,
		SkipVerify: value(KeySMTPSkipVerify) == "true",
		Timeout:    20 * time.Second,
	}, nil
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

// SaveSMTP persists the mail transport settings.
//
// It validates through notify.Config so a bad TLS mode — or credentials aimed at
// an unencrypted link — is rejected at save time rather than discovered later as
// a silently stuck outbox. A completely blank host clears the configuration,
// which is how an operator turns email off.
func SaveSMTP(ctx context.Context, st *store.Store, cfg *config.Config, in SMTPSaveRequest) (notify.Config, error) {
	current, err := SMTPConfig(ctx, st, cfg)
	if err != nil {
		return current, err
	}
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
	// Clearing the host disables email; there is nothing left to validate, but the
	// stored password is dropped too so a disabled config keeps no live secret.
	if next.Host == "" {
		for _, key := range SMTPKeys {
			if err := st.UpsertAppSetting(ctx, key, "", SMTPSecretKeys[key]); err != nil {
				return current, err
			}
		}
		return notify.Config{}, nil
	}
	if err := next.Validate(); err != nil {
		return current, err
	}
	for _, op := range []struct {
		key, value string
	}{
		{KeySMTPHost, next.Host},
		{KeySMTPPort, strconv.Itoa(next.Port)},
		{KeySMTPUsername, next.Username},
		{KeySMTPFrom, next.From},
		{KeySMTPFromName, next.FromName},
		{KeySMTPTLSMode, next.TLSMode},
		{KeySMTPSkipVerify, strconv.FormatBool(next.SkipVerify)},
	} {
		if err := st.UpsertAppSetting(ctx, op.key, op.value, false); err != nil {
			return current, err
		}
	}
	if strings.TrimSpace(in.Password) != "" {
		enc, err := Encrypt(cfg, in.Password)
		if err != nil {
			return current, err
		}
		if err := st.UpsertAppSetting(ctx, KeySMTPPassword, enc, true); err != nil {
			return current, err
		}
	}
	return SMTPConfig(ctx, st, cfg)
}
