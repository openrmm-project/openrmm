//go:build linux

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func cmd(args ...string) []byte {
	var b []byte
	for _, a := range args {
		b = append(b, a...)
		b = append(b, 0)
	}
	return b
}

func TestParseXCmdlineLightDM(t *testing.T) {
	// LightDM greeter style.
	s, ok := parseXCmdline("X", cmd("/usr/bin/X", ":0", "-seat", "seat0", "-auth", "/run/lightdm/root/:0", "vt1", "-nolisten", "tcp"))
	if !ok {
		t.Fatal("expected ok")
	}
	if s.display != ":0" || s.auth != "/run/lightdm/root/:0" || s.vt != 1 || s.xwayland {
		t.Fatalf("got %+v", s)
	}
}

func TestParseXCmdlineGDM(t *testing.T) {
	// GDM Xorg greeter style.
	s, ok := parseXCmdline("Xorg", cmd("/usr/lib/xorg/Xorg", "vt2", "-displayfd", "3", "-auth", "/run/user/126/gdm/Xauthority", ":1", "-background", "none"))
	if !ok {
		t.Fatal("expected ok")
	}
	if s.display != ":1" || s.auth != "/run/user/126/gdm/Xauthority" || s.vt != 2 {
		t.Fatalf("got %+v", s)
	}
}

func TestParseXCmdlineGDMDisplayFD(t *testing.T) {
	// Real modern GDM Xorg session: NO ":N" on the command line — the display is
	// chosen at runtime via -displayfd, so display must be left empty for the
	// caller to resolve from the process socket (displayFromPid).
	s, ok := parseXCmdline("Xorg", cmd("/usr/lib/xorg/Xorg", "vt2", "-displayfd", "3", "-auth", "/run/user/1000/gdm/Xauthority", "-nolisten", "tcp", "-background", "none", "-noreset", "-keeptty", "-novtswitch", "-verbose", "3"))
	if !ok {
		t.Fatal("expected ok")
	}
	if s.display != "" {
		t.Fatalf("display should be empty (resolved later), got %q", s.display)
	}
	if s.auth != "/run/user/1000/gdm/Xauthority" || s.vt != 2 || s.xwayland {
		t.Fatalf("got %+v", s)
	}
}

func TestDisplayNumFromSocketPath(t *testing.T) {
	cases := map[string]int{
		"@/tmp/.X11-unix/X1":  1,
		"/tmp/.X11-unix/X0":   0,
		"@/tmp/.X11-unix/X10": 10,
		"@/tmp/.ICE-unix/123": -1,
		"/run/some/other":     -1,
		"@/tmp/.X11-unix/Xa":  -1,
	}
	for path, want := range cases {
		if got := displayNumFromSocketPath(path); got != want {
			t.Errorf("displayNumFromSocketPath(%q) = %d, want %d", path, got, want)
		}
	}
}

func TestParseXCmdlineIgnoresNonX(t *testing.T) {
	if _, ok := parseXCmdline("bash", cmd("bash", "-c", "X :0")); ok {
		t.Fatal("non-X process should be rejected")
	}
}

func TestParseXCmdlineXwayland(t *testing.T) {
	s, ok := parseXCmdline("Xwayland", cmd("/usr/bin/Xwayland", ":1024", "-rootless"))
	if !ok || !s.xwayland {
		t.Fatalf("expected xwayland, got %+v ok=%v", s, ok)
	}
}

func TestSelectActivePrefersActiveVT(t *testing.T) {
	greeter := xServer{display: ":0", auth: "/g", vt: 1}
	user := xServer{display: ":1", auth: "/u", vt: 2}
	got, ok := selectActive([]xServer{greeter, user}, 2)
	if !ok || got.display != ":1" {
		t.Fatalf("active VT 2 should select user session, got %+v", got)
	}
	got, _ = selectActive([]xServer{greeter, user}, 1)
	if got.display != ":0" {
		t.Fatalf("active VT 1 should select greeter, got %+v", got)
	}
}

func TestSelectActiveDeprioritizesXwayland(t *testing.T) {
	xwl := xServer{display: ":1024", auth: "/w", vt: 2, xwayland: true}
	xorg := xServer{display: ":0", auth: "/x", vt: 1}
	got, ok := selectActive([]xServer{xwl, xorg}, -1) // VT unknown
	if !ok || got.xwayland {
		t.Fatalf("should prefer real Xorg over Xwayland, got %+v", got)
	}
}

func TestSelectActiveEmpty(t *testing.T) {
	if _, ok := selectActive(nil, 1); ok {
		t.Fatal("no candidates should return ok=false")
	}
}

func TestAllDigits(t *testing.T) {
	for _, s := range []string{"0", "42", "1234"} {
		if !allDigits(s) {
			t.Errorf("allDigits(%q) = false", s)
		}
	}
	for _, s := range []string{"", "1a", ":0", "vt1"} {
		if allDigits(s) {
			t.Errorf("allDigits(%q) = true", s)
		}
	}
}

// The desktop's owner is whoever owns its Xauthority file; a file of root's (a
// login screen, or LightDM's) names nobody.
func TestAuthAccount(t *testing.T) {
	if got := authAccount("/etc/hostname"); got != "" {
		t.Errorf("a root-owned file named %q", got)
	}
	if got := authAccount("/no/such/file"); got != "" {
		t.Errorf("a missing file named %q", got)
	}
	uid := os.Getuid()
	if uid < 1000 || uid == 65534 {
		t.Skip("needs a regular account to own the test file")
	}
	f := filepath.Join(t.TempDir(), "Xauthority")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if got := authAccount(f); got != accountName(me.Username) {
		t.Errorf("authAccount = %q, want %q", got, me.Username)
	}
}
