package main

import (
	"testing"

	"github.com/shirou/gopsutil/v3/host"
)

// Windows: the user at the physical console wins; otherwise the first other
// active session (remote desktop); the name is DOMAIN\user, AzureAD\user for
// an Entra ID account.
func TestWindowsSessionUser(t *testing.T) {
	for name, tc := range map[string]struct {
		sessions []winSession
		console  uint32
		want     string
	}{
		"console user":  {[]winSession{{ID: 3, Domain: "ACME", User: "rdp.user"}, {ID: 1, Domain: "ACME", User: "maria"}}, 1, `ACME\maria`},
		"azure ad":      {[]winSession{{ID: 1, Domain: "AzureAD", User: "JoaoSilva"}}, 1, `AzureAD\JoaoSilva`},
		"local account": {[]winSession{{ID: 1, Domain: "PC-01", User: "suporte"}}, 1, `PC-01\suporte`},
		"rdp only":      {[]winSession{{ID: 2, Domain: "ACME", User: "admin"}}, 1, `ACME\admin`},
		"console empty": {[]winSession{{ID: 1, Domain: "", User: ""}, {ID: 4, Domain: "ACME", User: "ops"}}, 1, `ACME\ops`},
		"no domain":     {[]winSession{{ID: 1, User: "kiosk"}}, 1, "kiosk"},
		"nobody":        {nil, 1, ""},
		"trimmed":       {[]winSession{{ID: 1, Domain: " ACME ", User: " maria "}}, 1, `ACME\maria`},
	} {
		if got := windowsSessionUser(tc.sessions, tc.console); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// Linux and other Unixes: someone at the machine (no host, or an X display)
// before someone connected over SSH; utmp's bookkeeping entries are skipped.
func TestUtmpSessionUser(t *testing.T) {
	for name, tc := range map[string]struct {
		users []host.UserStat
		want  string
	}{
		"local first": {[]host.UserStat{{User: "ops", Terminal: "pts/0", Host: "10.0.0.5"}, {User: "maria", Terminal: "tty2"}}, "maria"},
		"x display":   {[]host.UserStat{{User: "ops", Host: "10.0.0.5"}, {User: "ana", Terminal: ":0", Host: ":0"}}, "ana"},
		"ssh only":    {[]host.UserStat{{User: "root", Terminal: "pts/1", Host: "192.0.2.10"}}, "root"},
		"bookkeeping": {[]host.UserStat{{User: "LOGIN", Terminal: "tty1"}, {User: "reboot"}, {User: " "}}, ""},
		"nobody":      {nil, ""},
	} {
		if got := utmpSessionUser(tc.users); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// The platform reader never fails a heartbeat: on a machine without a
// session it reports "".
func TestLoggedInUserDoesNotPanic(t *testing.T) {
	_ = loggedInUser()
}
