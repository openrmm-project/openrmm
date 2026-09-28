package settings

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/p1ngu/rmm/internal/store"
)

// Which MCP applications may connect.
//
// Applications register themselves (Dynamic Client Registration), which is how
// Claude and ChatGPT connect without setup — and also how anyone can register
// an application named "Claude" that sends users back to their own site. The
// defaults keep that open; an administrator can narrow it:
//
//   - Registration "allowlist": only applications whose redirect addresses are
//     on hosts the administrator listed may register or authorize.
//   - RequireApproval: a newly registered application waits, pending, until an
//     administrator approves it.
//
// Whatever the policy, the consent page warns loudly when an application sends
// the user back to a host that is neither a known MCP client nor listed here.

const (
	mcpOAuthRegistrationKey    = "mcp_oauth.registration"
	mcpOAuthAllowedHostsKey    = "mcp_oauth.allowed_hosts"
	mcpOAuthRequireApprovalKey = "mcp_oauth.require_approval"

	// MCPRegistrationOpen lets any application register (the default).
	MCPRegistrationOpen = "open"
	// MCPRegistrationAllowlist admits only applications on listed hosts.
	MCPRegistrationAllowlist = "allowlist"
)

// KnownMCPHosts are the MCP clients the consent page does not warn about.
// Only a warning depends on this list; it admits nothing by itself.
var KnownMCPHosts = []string{"claude.ai", "claude.com", "chatgpt.com", "chat.openai.com"}

// MCPOAuthPolicy is the administrator's policy for MCP applications.
type MCPOAuthPolicy struct {
	Registration    string   `json:"registration"`
	AllowedHosts    []string `json:"allowed_hosts"`
	RequireApproval bool     `json:"require_approval"`
}

// ErrInvalidMCPHost rejects an entry of the allowed-hosts list.
var ErrInvalidMCPHost = errors.New("allowed hosts must be host names such as claude.ai or *.example.com, without scheme, port or path")

var hostPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// NormalizeMCPHosts lower-cases, trims, de-duplicates and validates a list of
// hosts. "localhost" and IP addresses are accepted for local clients.
func NormalizeMCPHosts(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		if net.ParseIP(h) == nil && !hostPattern.MatchString(h) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidMCPHost, h)
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// hostMatches reports whether host is one of the entries: exact, or a
// subdomain of a "*.example.com" entry (not example.com itself).
func hostMatches(host string, entries []string) bool {
	host = strings.ToLower(host)
	for _, e := range entries {
		if strings.HasPrefix(e, "*.") {
			if strings.HasSuffix(host, e[1:]) && len(host) > len(e)-1 {
				return true
			}
		} else if host == e {
			return true
		}
	}
	return false
}

// HostAllowed reports whether an application sending users back to host may
// register and authorize under this policy.
func (p MCPOAuthPolicy) HostAllowed(host string) bool {
	if p.Registration != MCPRegistrationAllowlist {
		return true
	}
	return hostMatches(host, p.AllowedHosts)
}

// HostKnown reports whether the consent page may skip its warning for host.
func (p MCPOAuthPolicy) HostKnown(host string) bool {
	return hostMatches(host, KnownMCPHosts) || hostMatches(host, p.AllowedHosts)
}

// LoadMCPOAuthPolicy reads the policy; unset keys give the open default.
func LoadMCPOAuthPolicy(ctx context.Context, st *store.Store) (MCPOAuthPolicy, error) {
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		return MCPOAuthPolicy{}, err
	}
	p := MCPOAuthPolicy{Registration: MCPRegistrationOpen, AllowedHosts: []string{}}
	if r, ok := rows[mcpOAuthRegistrationKey]; ok && r.Value == MCPRegistrationAllowlist {
		p.Registration = MCPRegistrationAllowlist
	}
	if r, ok := rows[mcpOAuthAllowedHostsKey]; ok && r.Value != "" {
		// Stored normalized; a hand-edited row that no longer validates is
		// dropped entry by entry rather than admitting something unintended.
		for _, h := range strings.Split(r.Value, ",") {
			if norm, err := NormalizeMCPHosts([]string{h}); err == nil {
				p.AllowedHosts = append(p.AllowedHosts, norm...)
			}
		}
	}
	if r, ok := rows[mcpOAuthRequireApprovalKey]; ok && r.Value == "true" {
		p.RequireApproval = true
	}
	return p, nil
}

// SaveMCPOAuthPolicy validates and stores the policy.
func SaveMCPOAuthPolicy(ctx context.Context, st *store.Store, in MCPOAuthPolicy) (MCPOAuthPolicy, error) {
	if in.Registration != MCPRegistrationOpen && in.Registration != MCPRegistrationAllowlist {
		return MCPOAuthPolicy{}, fmt.Errorf("registration must be %q or %q", MCPRegistrationOpen, MCPRegistrationAllowlist)
	}
	hosts, err := NormalizeMCPHosts(in.AllowedHosts)
	if err != nil {
		return MCPOAuthPolicy{}, err
	}
	// An allowlist with nothing on it would lock out every application,
	// including the ones already in use; say so instead of doing it.
	if in.Registration == MCPRegistrationAllowlist && len(hosts) == 0 {
		return MCPOAuthPolicy{}, errors.New("list at least one host before allowing only listed hosts")
	}
	approval := "false"
	if in.RequireApproval {
		approval = "true"
	}
	for _, kv := range [][2]string{
		{mcpOAuthRegistrationKey, in.Registration},
		{mcpOAuthAllowedHostsKey, strings.Join(hosts, ",")},
		{mcpOAuthRequireApprovalKey, approval},
	} {
		if err := st.UpsertAppSetting(ctx, kv[0], kv[1], false); err != nil {
			return MCPOAuthPolicy{}, err
		}
	}
	return MCPOAuthPolicy{Registration: in.Registration, AllowedHosts: hosts, RequireApproval: in.RequireApproval}, nil
}
