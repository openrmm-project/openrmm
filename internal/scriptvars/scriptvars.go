// Package scriptvars is the rule for variables a library script may read
// while it runs. A name declared on the script is resolved for the device
// (device, then department, site, client, then the global value) and handed
// to the agent as an environment variable of that exact name. Secrets are
// encrypted at rest and never returned to a reader of the API, the audit
// log, or a job's stored output.
package scriptvars

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/secretcrypto"
)

const (
	MinSecretLen = 8
	MaxValueLen  = 4096
)

// Built-in names are injected for every supported run. They are not secrets
// and cannot be overridden by a user-defined variable.
const (
	BuiltinAgentID       = "RMM_AGENT_ID"
	BuiltinAgentHostname = "RMM_AGENT_HOSTNAME"
	BuiltinClientID      = "RMM_CLIENT_ID"
	BuiltinClientName    = "RMM_CLIENT_NAME"
	BuiltinSiteID        = "RMM_SITE_ID"
	BuiltinSiteName      = "RMM_SITE_NAME"
)

var userName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,40}$`)

// reserved are process controls a script variable must not replace.
var reserved = map[string]struct{}{
	"PATH": {}, "PATHEXT": {}, "COMSPEC": {}, "SYSTEMROOT": {}, "WINDIR": {},
	"TMP": {}, "TEMP": {}, "HOME": {}, "SHELL": {}, "IFS": {}, "USER": {},
	"LOGNAME": {}, "PWD": {}, "OLDPWD": {}, "SHLVL": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "DYLD_INSERT_LIBRARIES": {},
	"PYTHONPATH": {}, "PYTHONSTARTUP": {}, "NODE_OPTIONS": {}, "PERL5OPT": {},
	"BASH_ENV": {}, "ENV": {}, "PROMPT_COMMAND": {}, "PS1": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	"SSLKEYLOGFILE": {}, "GIT_SSH_COMMAND": {},
}

// Device is the context a run is resolved against.
type Device struct {
	AgentID    string
	Hostname   string
	ClientID   string
	ClientName string
	SiteID     string
	SiteName   string
	GroupID    string
}

// Builtins returns the context variables for a device. Missing relations are
// empty strings, so a script can tell "no client" from "variable not set".
func Builtins(d Device) map[string]string {
	return map[string]string{
		BuiltinAgentID:       d.AgentID,
		BuiltinAgentHostname: d.Hostname,
		BuiltinClientID:      d.ClientID,
		BuiltinClientName:    d.ClientName,
		BuiltinSiteID:        d.SiteID,
		BuiltinSiteName:      d.SiteName,
	}
}

// ValidUserName reports whether a caller may create a variable of this name.
func ValidUserName(name string) error {
	if !userName.MatchString(name) || strings.HasPrefix(name, "RMM_") || strings.HasPrefix(name, "OPENRMM_") {
		return fmt.Errorf("variable name must be A-Z, digits and underscores, and cannot start with RMM_ or OPENRMM_")
	}
	if _, blocked := reserved[name]; blocked {
		return fmt.Errorf("variable name %s is reserved", name)
	}
	return nil
}

// Normalize sorts and de-duplicates the names a script asks to receive.
func Normalize(names []string) ([]string, error) {
	if len(names) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if err := ValidUserName(name); err != nil {
			return nil, err
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) > 32 {
		return nil, fmt.Errorf("at most 32 script variables")
	}
	return out, nil
}

// ValidValue checks a value before it is stored. Secrets have a minimum
// length so redaction cannot be dodged with a one-character needle.
func ValidValue(secret bool, value string) error {
	if value == "" {
		return fmt.Errorf("value required")
	}
	if len(value) > MaxValueLen {
		return fmt.Errorf("value is longer than %d characters", MaxValueLen)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("value cannot contain line breaks")
	}
	if secret && len(value) < MinSecretLen {
		return fmt.Errorf("a secret must be at least %d characters", MinSecretLen)
	}
	return nil
}

// Redact removes secret values from text that will be stored. Longer values
// are removed first so one secret that contains another is not left behind.
func Redact(text string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if len(secret) < MinSecretLen {
			continue
		}
		text = strings.ReplaceAll(text, secret, "********")
	}
	return text
}

// Seal encrypts the secret values that were sent with one run, so the result
// can be redacted later without keeping them in the job row in the clear.
func Seal(cfg *config.Config, secrets []string) (string, error) {
	if len(secrets) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(secrets)
	if err != nil {
		return "", err
	}
	return secretcrypto.Encrypt(cfg, string(raw))
}

// Open reverses Seal. A failure means the output must be withheld, not stored
// unredacted.
func Open(cfg *config.Config, blob string) ([]string, error) {
	if blob == "" {
		return nil, nil
	}
	if !strings.HasPrefix(blob, "enc:v1:") {
		return nil, fmt.Errorf("invalid encrypted redactions")
	}
	plain, err := secretcrypto.Decrypt(cfg, blob)
	if err != nil {
		return nil, err
	}
	var secrets []string
	if err := json.Unmarshal([]byte(plain), &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}
