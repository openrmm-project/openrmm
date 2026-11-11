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

// MaxEnvironmentBytes bounds the combined environment data; individual fields have
// no business character limit. API request and operating-system limits also apply.
const MaxEnvironmentBytes = 8 << 20

func ValidEnvironmentSize(env map[string]string) error {
	total := 0
	for name, value := range env {
		if len(name) > MaxEnvironmentBytes-total {
			return fmt.Errorf("script environment exceeds payload budget")
		}
		total += len(name)
		if len(value) > MaxEnvironmentBytes-total-2 {
			return fmt.Errorf("script environment exceeds payload budget")
		}
		total += len(value) + 2
	}
	return nil
}

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

var userName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	canonical := strings.ToUpper(name)
	if !userName.MatchString(name) || strings.HasPrefix(canonical, "RMM_") || strings.HasPrefix(canonical, "OPENRMM_") {
		return fmt.Errorf("variable name must start with a letter or underscore and contain only letters, digits and underscores; RMM_ and OPENRMM_ are reserved")
	}
	if _, blocked := reserved[canonical]; blocked {
		return fmt.Errorf("variable name %s is reserved", name)
	}
	return nil
}

// Normalize sorts and de-duplicates the names a script asks to receive.
func Normalize(names []string) ([]string, error) {
	if len(names) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]string, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if err := ValidUserName(name); err != nil {
			return nil, err
		}
		key := strings.ToUpper(name)
		if previous, ok := seen[key]; ok {
			if previous != name {
				return nil, fmt.Errorf("variable names cannot differ only by letter case")
			}
			continue
		}
		seen[key] = name
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) > 32 {
		return nil, fmt.Errorf("at most 32 script variables")
	}
	return out, nil
}

// ValidValue rejects data that cannot be carried by an environment variable.
// Size is bounded by the API/transport and the target OS, not a field policy.
func ValidValue(_ bool, value string) error {
	if value == "" {
		return fmt.Errorf("value required")
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("value cannot contain NUL or line breaks")
	}
	return nil
}

// CheckShell keeps values as data. CMD expands percent variables before parsing
// operators; even quoting cannot make arbitrary credential values inert.
func CheckShell(shell string, count int) error {
	if shell == "cmd" && count > 0 {
		return fmt.Errorf("cmd scripts cannot receive variables; use powershell")
	}
	return nil
}

// Redact removes secret values from text that will be stored. Longer values
// are removed first so one secret that contains another is not left behind.
func Redact(text string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	// Accept short secrets without turning repeated masking into output growth.
	// Each replacement is no longer than any nonempty secret in the snapshot.
	width := 8
	for _, secret := range ordered {
		if len(secret) > 0 && len(secret) < width {
			width = len(secret)
		}
	}
	marker := strings.Repeat("*", width)
	for _, secret := range ordered {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, marker)
		}
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
