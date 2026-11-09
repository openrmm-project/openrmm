package remote

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// meshCentralArgs reads a link the way MeshCentral's own page does
// (parseUriArgs in public/scripts/common-0.0.1.js, without decoding): split on
// '?', '&' and '|', and drop every value isSafeString2 refuses.
func meshCentralArgs(href string) map[string]string {
	parts := regexp.MustCompile(`[?&|]`).Split(href, -1)[1:]
	out := map[string]string{}
	for _, arg := range parts {
		i := strings.Index(arg, "=")
		if i < 0 {
			out[arg] = ""
			continue
		}
		name, value := arg[:i], arg[i+1:]
		if strings.ContainsAny(value, `<>&"'+()#%`) {
			delete(out, name)
			continue
		}
		out[name] = value
	}
	return out
}

func TestMeshConnectURLKeepsTheLinkedNodeLink(t *testing.T) {
	// Byte for byte what the provider built before hostname matching existed.
	if got, want := meshConnectURL("https://mesh.example", "", "node//abc$def", ""), "https://mesh.example/?viewmode=11&hide=31&node=node//abc$def"; got != want {
		t.Fatalf("deep link changed:\n got %s\nwant %s", got, want)
	}
	if got, want := meshConnectURL("https://mesh.example", "tok@en$", "node//abc", ""), "https://mesh.example/?login=tok%40en%24&viewmode=11&hide=31&node=node//abc"; got != want {
		t.Fatalf("login-token link changed:\n got %s\nwant %s", got, want)
	}
}

func TestMeshConnectURLByHostnameIsReadAsIsByMeshCentral(t *testing.T) {
	for _, host := range []string{"DESKTOP-1A2B3C", "web01.corp.example", "build_agent-7", "1st-floor-pc"} {
		name, err := meshDeviceName(host)
		if err != nil {
			t.Fatalf("%q refused: %v", host, err)
		}
		for _, token := range []string{"", "tok@en"} {
			link := meshConnectURL("https://mesh.example", token, "", name)
			args := meshCentralArgs(link)
			if args["gotodevicername"] != host {
				t.Fatalf("%s: MeshCentral would look for %q, not %q", link, args["gotodevicername"], host)
			}
			if args["viewmode"] != "11" || args["hide"] != "31" {
				t.Fatalf("%s: the desktop view arguments are lost: %v", link, args)
			}
			if _, ok := args["node"]; ok {
				t.Fatalf("%s: a hostname link must not name a node", link)
			}
		}
	}
}

// The hostname is whatever the agent reports. Anything MeshCentral could not
// match, or that would add arguments of its own to the link, is refused.
func TestMeshDeviceNameRefusesWhatALinkCannotCarry(t *testing.T) {
	for _, host := range []string{
		"",
		" DESKTOP-1",
		"my pc",
		"x&login=attacker-token",
		"x|viewmode=1",
		"x?gotonode=node//other",
		"x#frag",
		"a%20b",
		"a+b",
		"<img>",
		`a"b`,
		"a'b",
		"a(b)",
		"café",
		"-leading-hyphen",
		".leading-dot",
		"tab\there",
		"new\nline",
		// Names MeshCentral would read as numbers, and match loosely.
		"1234", "01234", "1234.", "1234.0", "1e0", "0e5", "0x10", "0X1f", "0b0", "0o0",
		strings.Repeat("a", 254),
	} {
		if _, err := meshDeviceName(host); err == nil {
			t.Errorf("%q was accepted", host)
		} else if host != "" && strings.Contains(err.Error(), host) {
			t.Errorf("the error echoes the agent-reported hostname: %v", err)
		}
	}
	if _, err := meshDeviceName(strings.Repeat("a", 253)); err != nil {
		t.Errorf("a 253-character hostname was refused: %v", err)
	}
}

func TestMeshProviderUnlinkedDevice(t *testing.T) {
	// No store: every case here is refused before a session is persisted.
	m := NewManager(nil, nil, "https://mesh.example", "", "admin", true, false)
	unlinked := &models.Agent{ID: "a1", Hostname: "x&login=attacker-token"}
	start := func(agent *models.Agent, opts StartOptions) error {
		t.Helper()
		_, err := m.Start(context.Background(), "meshcentral", agent, "tech", opts)
		return err
	}

	if err := start(unlinked, StartOptions{Unrestricted: true}); err == nil || !strings.Contains(err.Error(), "no MeshCentral node id") {
		t.Fatalf("off by default, an unlinked device must be refused: %v", err)
	}

	m.SetMeshMatchHostname(true)
	// Without a login token the browser signs in to MeshCentral by itself, as
	// anyone: a restricted caller never gets a hostname lookup there.
	if err := start(&models.Agent{ID: "a3", Hostname: "DESKTOP-1"}, StartOptions{MeshUser: "tech1"}); err == nil || !strings.Contains(err.Error(), "access to every device or a MeshCentral account of your own") {
		t.Fatalf("a restricted caller opened an unlinked device by hostname without a login token: %v", err)
	}
	err := start(unlinked, StartOptions{Unrestricted: true})
	if err == nil || !strings.Contains(err.Error(), "link its node instead") {
		t.Fatalf("a hostname a link cannot carry must be refused: %v", err)
	}
	if strings.Contains(err.Error(), "attacker") {
		t.Fatalf("the error echoes the agent-reported hostname: %v", err)
	}

	// With a login token, a restricted caller looks a hostname up through
	// their own account, which MeshCentral bounds: the lookup gets as far as
	// the name check. With no account of their own they are refused first.
	m.UpdateMeshConfig("https://mesh.example", strings.Repeat("ab", 32), "admin", true)
	if err := start(unlinked, StartOptions{MeshUser: "tech1"}); err == nil || !strings.Contains(err.Error(), "link its node instead") {
		t.Fatalf("a restricted caller with an account of their own did not reach the hostname check: %v", err)
	}
	if err := start(&models.Agent{ID: "a4", Hostname: "DESKTOP-1"}, StartOptions{}); err == nil || !strings.Contains(err.Error(), "no MeshCentral account of your own") {
		t.Fatalf("a restricted caller with no account of their own was not refused: %v", err)
	}

	m.UpdateMeshConfig("https://mesh.example", "", "admin", false)
	if err := start(&models.Agent{ID: "a2", Hostname: "DESKTOP-1"}, StartOptions{Unrestricted: true}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("a disabled provider must refuse, by hostname too: %v", err)
	}
}

// Which MeshCentral account a login token signs in as. A restricted caller
// only ever signs in as their own account; the shared one is for callers with
// access to every device, and only while it is turned on.
func TestMeshAccount(t *testing.T) {
	for _, c := range []struct {
		name         string
		opts         StartOptions
		shared       string
		sharedOn     bool
		account      string
		kind, errHas string
	}{
		{"own account, unrestricted", StartOptions{Unrestricted: true, MeshUser: "alice"}, "admin", true, "alice", "personal", ""},
		{"own account, restricted", StartOptions{MeshUser: "tech1"}, "admin", true, "tech1", "personal", ""},
		{"own account, shared turned off", StartOptions{MeshUser: "tech1"}, "admin", false, "tech1", "personal", ""},
		{"restricted, own account is the shared one", StartOptions{MeshUser: "Admin"}, "admin", true, "", "", "is the shared one"},
		{"unrestricted, own account is the shared one", StartOptions{Unrestricted: true, MeshUser: "admin"}, "admin", true, "admin", "personal", ""},
		{"restricted, no account", StartOptions{}, "admin", true, "", "", "only for users with access to every device"},
		{"unrestricted, no account", StartOptions{Unrestricted: true}, "admin", true, "admin", "shared", ""},
		{"unrestricted, no account, shared turned off", StartOptions{Unrestricted: true}, "admin", false, "", "", "turned off"},
		{"unrestricted, no account, no shared name", StartOptions{Unrestricted: true}, " ", true, "", "", "turned off"},
	} {
		account, kind, err := meshAccount(c.opts, c.shared, c.sharedOn)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) || account != "" {
				t.Errorf("%s: got %q (%s), %v; want an error saying %q", c.name, account, kind, err, c.errHas)
			}
			continue
		}
		if err != nil || account != c.account || kind != c.kind {
			t.Errorf("%s: got %q (%s), %v; want %q (%s)", c.name, account, kind, err, c.account, c.kind)
		}
	}
}

// jsNumber and jsParseInt are JavaScript's Number(v) and parseInt(v), for the
// characters a hostname may hold. NaN when JavaScript says NaN.
func jsNumber(v string) float64 {
	switch {
	case regexp.MustCompile(`^0[xX][0-9A-Fa-f]+$`).MatchString(v):
		n, _ := strconv.ParseUint(v[2:], 16, 64)
		return float64(n)
	case regexp.MustCompile(`^0[bB][01]+$`).MatchString(v):
		n, _ := strconv.ParseUint(v[2:], 2, 64)
		return float64(n)
	case regexp.MustCompile(`^0[oO][0-7]+$`).MatchString(v):
		n, _ := strconv.ParseUint(v[2:], 8, 64)
		return float64(n)
	case regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`).MatchString(v):
		n, _ := strconv.ParseFloat(v, 64)
		return n
	}
	return math.NaN()
}

func jsParseInt(v string) float64 {
	if m := regexp.MustCompile(`^[+-]?0[xX]([0-9A-Fa-f]+)`).FindStringSubmatch(v); m != nil {
		n, _ := strconv.ParseUint(m[1], 16, 64)
		return float64(n)
	}
	if m := regexp.MustCompile(`^[+-]?([0-9]+)`).FindStringSubmatch(v); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		return n
	}
	return math.NaN()
}

// Exhaustively, over the characters that matter: no hostname meshDeviceName
// accepts is one MeshCentral's page would turn into a number (its
// parseUriArgs keeps parseInt(v) when parseInt(v) == v, and the name is then
// compared with ==, so "1e0" would find a computer named "1").
func TestMeshDeviceNameNeverReadsAsANumberInMeshCentral(t *testing.T) {
	const alphabet = "019xXbBoOeE.-_aF"
	var walk func(prefix string, depth int)
	checked := 0
	walk = func(prefix string, depth int) {
		if prefix != "" {
			if _, err := meshDeviceName(prefix); err == nil {
				checked++
				if pi, n := jsParseInt(prefix), jsNumber(prefix); !math.IsNaN(pi) && !math.IsNaN(n) && pi == n {
					t.Errorf("%q is accepted, but MeshCentral reads it as the number %v", prefix, pi)
				}
			}
		}
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			walk(prefix+string(c), depth-1)
		}
	}
	walk("", 4)
	if checked < 10000 {
		t.Fatalf("only %d accepted names were checked", checked)
	}
	// The control: ordinary names that only start with digits stay accepted.
	for _, ok := range []string{"1st-floor", "10-0-0-5", "2fa-server", "web01", "0xdeadbeef-host"} {
		if _, err := meshDeviceName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}
