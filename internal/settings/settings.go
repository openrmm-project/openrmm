// Package settings merges env config with editable DB-backed runtime settings.
package settings

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/store"
)

const (
	KeyAIProvider            = "ai.provider"
	KeyAIBaseURL             = "ai.base_url"
	KeyAIModel               = "ai.model"
	KeyAIAPIKey              = "ai.api_key"
	KeyRemoteDefaultProvider = "remote.default_provider"
	KeyMeshCentralURL        = "remote.meshcentral_url"
	KeyMeshCentralUser       = "remote.meshcentral_user"
	KeyMeshCentralTokenKey   = "remote.meshcentral_token_key"
	KeyMeshCentralEnabled    = "remote.meshcentral_enabled"
	// KeyMeshCentralMatchHostname opens a device with no linked MeshCentral
	// node by its hostname. Off unless an administrator turns it on.
	KeyMeshCentralMatchHostname = "remote.meshcentral_match_hostname"
	// KeyMeshCentralSharedAccount lets users with access to every device, and
	// no MeshCentral account of their own, sign in to MeshCentral as
	// KeyMeshCentralUser. Restricted users never use the shared account.
	KeyMeshCentralSharedAccount = "remote.meshcentral_shared_account"
	KeyRustDeskEnabled          = "remote.rustdesk_enabled"
	// KeyRemoteGuestAccess allows technicians holding devices.remote.share to
	// invite guests without an account. Off unless an administrator turns it on.
	KeyRemoteGuestAccess = "remote.guest_access"
	// KeyJobsAllowUnapproved lets the people who both approve jobs and manage
	// the configuration run a script on one device without the approval step.
	// Off unless an administrator turns it on; RMM_ALLOW_UNAPPROVED_JOBS only
	// sets the value an instance starts with. Bulk deployments and everything
	// the AI proposes always wait for approval.
	KeyJobsAllowUnapproved = "jobs.allow_unapproved"
)

var publicKeys = map[string]bool{
	KeyAIProvider:               true,
	KeyAIBaseURL:                true,
	KeyAIModel:                  true,
	KeyRemoteDefaultProvider:    true,
	KeyMeshCentralURL:           true,
	KeyMeshCentralUser:          true,
	KeyMeshCentralEnabled:       true,
	KeyMeshCentralMatchHostname: true,
	KeyMeshCentralSharedAccount: true,
	KeyRustDeskEnabled:          true,
	KeyRemoteGuestAccess:        true,
	KeyJobsAllowUnapproved:      true,
}

// Editable is the effective runtime subset that can be edited from the UI.
type Editable struct {
	AIProvider               string
	AIBaseURL                string
	AIModel                  string
	AIAPIKey                 string
	RemoteDefaultProvider    string
	MeshCentralURL           string
	MeshCentralUser          string
	MeshCentralTokenKey      string
	MeshCentralEnabled       bool
	MeshCentralMatchHostname bool
	MeshCentralSharedAccount bool
	RustDeskEnabled          bool
	RemoteGuestAccess        bool
	JobsAllowUnapproved      bool
}

// FromConfig builds the env-only baseline.
func FromConfig(cfg *config.Config) Editable {
	return Editable{
		AIProvider:               cfg.AIProvider,
		AIBaseURL:                cfg.AIBaseURL,
		AIModel:                  cfg.AIModel,
		AIAPIKey:                 cfg.AIAPIKey,
		RemoteDefaultProvider:    cfg.DefaultRemoteProvider,
		MeshCentralURL:           cfg.MeshCentralURL,
		MeshCentralUser:          cfg.MeshCentralUser,
		MeshCentralTokenKey:      cfg.MeshCentralTokenKey,
		MeshCentralEnabled:       cfg.MeshCentralEnabled,
		MeshCentralMatchHostname: cfg.MeshCentralMatchHostname,
		MeshCentralSharedAccount: cfg.MeshCentralSharedAccount,
		RustDeskEnabled:          cfg.RustDeskEnabled,
		// The environment is only the starting value: once an administrator
		// saves the setting, the stored value wins.
		JobsAllowUnapproved: cfg.AllowUnapprovedJobs,
	}
}

// ApplyToConfig mutates cfg with an effective runtime setting set.
func (e Editable) ApplyToConfig(cfg *config.Config) {
	cfg.AIProvider = e.AIProvider
	cfg.AIBaseURL = e.AIBaseURL
	cfg.AIModel = e.AIModel
	cfg.AIAPIKey = e.AIAPIKey
	cfg.DefaultRemoteProvider = e.RemoteDefaultProvider
	cfg.MeshCentralURL = e.MeshCentralURL
	cfg.MeshCentralUser = e.MeshCentralUser
	cfg.MeshCentralTokenKey = e.MeshCentralTokenKey
	cfg.MeshCentralEnabled = e.MeshCentralEnabled
	cfg.MeshCentralMatchHostname = e.MeshCentralMatchHostname
	cfg.MeshCentralSharedAccount = e.MeshCentralSharedAccount
	cfg.RustDeskEnabled = e.RustDeskEnabled
}

// Effective returns env config overlaid with DB settings.
func Effective(ctx context.Context, st *store.Store, cfg *config.Config) (Editable, error) {
	out := FromConfig(cfg)
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return out, err
	}
	get := func(key string) (string, bool, error) {
		row, ok := rows[key]
		if !ok || row.Value == "" {
			return "", false, nil
		}
		if row.IsSecret {
			v, err := decrypt(cfg, row.Value)
			if err != nil {
				// Fail-safe: if a stored secret can't be decrypted (e.g. the admin
				// token / encryption key was rotated), treat it as unset and keep
				// the app working instead of 500-ing the whole config screen. The
				// admin can simply re-enter the secret.
				log.Printf("settings: cannot decrypt %q (encryption key changed?); treating as unset", key)
				return "", false, nil
			}
			return v, true, nil
		}
		return row.Value, true, nil
	}
	for _, key := range []string{KeyAIProvider, KeyAIBaseURL, KeyAIModel, KeyAIAPIKey, KeyRemoteDefaultProvider, KeyMeshCentralURL, KeyMeshCentralUser, KeyMeshCentralTokenKey, KeyMeshCentralEnabled, KeyMeshCentralMatchHostname, KeyMeshCentralSharedAccount, KeyRustDeskEnabled, KeyRemoteGuestAccess, KeyJobsAllowUnapproved} {
		v, ok, err := get(key)
		if err != nil {
			return out, fmt.Errorf("load %s: %w", key, err)
		}
		if !ok {
			continue
		}
		switch key {
		case KeyAIProvider:
			out.AIProvider = v
		case KeyAIBaseURL:
			out.AIBaseURL = v
		case KeyAIModel:
			out.AIModel = v
		case KeyAIAPIKey:
			out.AIAPIKey = v
		case KeyRemoteDefaultProvider:
			out.RemoteDefaultProvider = v
		case KeyMeshCentralURL:
			out.MeshCentralURL = v
		case KeyMeshCentralUser:
			out.MeshCentralUser = v
		case KeyMeshCentralTokenKey:
			out.MeshCentralTokenKey = v
		case KeyMeshCentralEnabled:
			out.MeshCentralEnabled = v == "true"
		case KeyMeshCentralMatchHostname:
			out.MeshCentralMatchHostname = v == "true"
		case KeyMeshCentralSharedAccount:
			out.MeshCentralSharedAccount = v == "true"
		case KeyRustDeskEnabled:
			out.RustDeskEnabled = v == "true"
		case KeyRemoteGuestAccess:
			out.RemoteGuestAccess = v == "true"
		case KeyJobsAllowUnapproved:
			out.JobsAllowUnapproved = v == "true"
		}
	}
	return out, nil
}

// SaveRequest is accepted by PUT /api/config. Blank secret fields mean preserve.
type SaveRequest struct {
	AI struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	} `json:"ai"`
	Remote struct {
		DefaultProvider    string `json:"default"`
		MeshCentralURL     string `json:"meshcentral_url"`
		MeshCentralUser    string `json:"meshcentral_user"`
		MeshTokenKey       string `json:"meshcentral_token_key"`
		MeshCentralEnabled *bool  `json:"meshcentral_enabled"`        // nil = preserve current
		MeshMatchHostname  *bool  `json:"meshcentral_match_hostname"` // nil = preserve current
		MeshSharedAccount  *bool  `json:"meshcentral_shared_account"` // nil = preserve current
		RustDeskEnabled    *bool  `json:"rustdesk_enabled"`           // nil = preserve current
		GuestAccess        *bool  `json:"guest_access"`               // nil = preserve current
	} `json:"remote"`
	Jobs struct {
		AllowUnapproved *bool `json:"allow_unapproved"` // nil = preserve current
	} `json:"jobs"`
}

// Save persists editable settings. Secret empty strings preserve existing values.
func Save(ctx context.Context, st *store.Store, cfg *config.Config, in SaveRequest) (Editable, error) {
	current, err := Effective(ctx, st, cfg)
	if err != nil {
		return current, err
	}
	writePublic := func(key, value string) error {
		if !publicKeys[key] {
			return fmt.Errorf("not a public setting: %s", key)
		}
		return st.UpsertAppSetting(ctx, key, strings.TrimSpace(value), false)
	}
	writeSecret := func(key, value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		enc, err := encrypt(cfg, value)
		if err != nil {
			return err
		}
		return st.UpsertAppSetting(ctx, key, enc, true)
	}

	if in.AI.Provider == "" {
		in.AI.Provider = current.AIProvider
	}
	if in.AI.BaseURL == "" {
		in.AI.BaseURL = current.AIBaseURL
	}
	if in.AI.Model == "" {
		in.AI.Model = current.AIModel
	}
	if in.Remote.DefaultProvider == "" {
		in.Remote.DefaultProvider = current.RemoteDefaultProvider
	}
	if in.Remote.MeshCentralUser == "" {
		in.Remote.MeshCentralUser = current.MeshCentralUser
	}

	rustdesk := current.RustDeskEnabled
	if in.Remote.RustDeskEnabled != nil {
		rustdesk = *in.Remote.RustDeskEnabled
	}
	meshEnabled := current.MeshCentralEnabled
	if in.Remote.MeshCentralEnabled != nil {
		meshEnabled = *in.Remote.MeshCentralEnabled
	}
	meshMatchHostname := current.MeshCentralMatchHostname
	if in.Remote.MeshMatchHostname != nil {
		meshMatchHostname = *in.Remote.MeshMatchHostname
	}
	meshSharedAccount := current.MeshCentralSharedAccount
	if in.Remote.MeshSharedAccount != nil {
		meshSharedAccount = *in.Remote.MeshSharedAccount
	}
	guestAccess := current.RemoteGuestAccess
	if in.Remote.GuestAccess != nil {
		guestAccess = *in.Remote.GuestAccess
	}
	allowUnapproved := current.JobsAllowUnapproved
	if in.Jobs.AllowUnapproved != nil {
		allowUnapproved = *in.Jobs.AllowUnapproved
	}
	for _, op := range []struct{ key, value string }{
		{KeyAIProvider, in.AI.Provider},
		{KeyAIBaseURL, in.AI.BaseURL},
		{KeyAIModel, in.AI.Model},
		{KeyRemoteDefaultProvider, in.Remote.DefaultProvider},
		{KeyMeshCentralURL, in.Remote.MeshCentralURL},
		{KeyMeshCentralUser, in.Remote.MeshCentralUser},
		{KeyMeshCentralEnabled, strconv.FormatBool(meshEnabled)},
		{KeyMeshCentralMatchHostname, strconv.FormatBool(meshMatchHostname)},
		{KeyMeshCentralSharedAccount, strconv.FormatBool(meshSharedAccount)},
		{KeyRustDeskEnabled, strconv.FormatBool(rustdesk)},
		{KeyRemoteGuestAccess, strconv.FormatBool(guestAccess)},
		{KeyJobsAllowUnapproved, strconv.FormatBool(allowUnapproved)},
	} {
		if err := writePublic(op.key, op.value); err != nil {
			return current, err
		}
	}
	if err := writeSecret(KeyAIAPIKey, in.AI.APIKey); err != nil {
		return current, err
	}
	if err := writeSecret(KeyMeshCentralTokenKey, in.Remote.MeshTokenKey); err != nil {
		return current, err
	}
	return Effective(ctx, st, cfg)
}

// Encrypt/Decrypt expose the instance secret encryption for other packages
// (e.g. per-user TOTP secrets), using the same key derivation as settings.
func Encrypt(cfg *config.Config, plaintext string) (string, error)  { return encrypt(cfg, plaintext) }
func Decrypt(cfg *config.Config, ciphertext string) (string, error) { return decrypt(cfg, ciphertext) }

func encryptionKey(cfg *config.Config) [32]byte {
	key := cfg.SettingsEncryptionKey
	if key == "" {
		key = cfg.AdminToken
	}
	return sha256.Sum256([]byte(key))
}

func encrypt(cfg *config.Config, plaintext string) (string, error) {
	key := encryptionKey(cfg)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), nil)...)
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(out), nil
}

func decrypt(cfg *config.Config, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "enc:v1:") {
		return ciphertext, nil // migration path for any legacy plaintext value.
	}
	key := encryptionKey(cfg)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "enc:v1:"))
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, sealed := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func AuditDetail(changed []string) []byte {
	b, _ := json.Marshal(map[string]any{"changed": changed})
	return b
}
