//go:build linux

package main

// Seat-following for Linux: discover the X server that owns the currently active
// virtual terminal — the display-manager greeter (GDM/LightDM/SDDM) before login,
// the user's session after — and its Xauthority cookie, so the media loop can
// (re)attach as the session changes. This is the Linux analogue of the Windows
// desktop-following (login screen → user desktop → lock).
//
// It works by scanning /proc for the X server processes and reading each one's
// command line (-auth <file>, display ":N", "vtN"). Reading other users' /proc
// entries and auth files requires root — the agent runs the sidecar as a
// system service, so this holds. Wayland greeters/sessions only expose an
// Xwayland server (deprioritized), which cannot capture native Wayland surfaces.

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type xServer struct {
	pid      int
	display  string
	auth     string
	vt       int
	xwayland bool
}

// readActiveVT returns the active virtual-terminal number, or -1 if unknown.
func readActiveVT() int {
	b, err := os.ReadFile("/sys/class/tty/tty0/active") // e.g. "tty2\n"
	if err != nil {
		return -1
	}
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "tty")
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// parseXCmdline extracts the display, auth file and VT from an X server's
// process name + NUL-separated /proc cmdline. ok is false for non-X processes.
func parseXCmdline(comm string, cmdline []byte) (xServer, bool) {
	comm = strings.TrimSpace(comm)
	isX := comm == "Xorg" || comm == "X"
	isXwl := comm == "Xwayland"
	if !isX && !isXwl {
		return xServer{}, false
	}
	args := strings.FieldsFunc(string(cmdline), func(r rune) bool { return r == 0 })
	s := xServer{vt: -1, xwayland: isXwl}
	for i, a := range args {
		switch {
		case len(a) >= 2 && a[0] == ':' && allDigits(a[1:]):
			s.display = a
		case a == "-auth" && i+1 < len(args):
			s.auth = args[i+1]
		case strings.HasPrefix(a, "vt") && len(a) > 2 && allDigits(a[2:]):
			if n, err := strconv.Atoi(a[2:]); err == nil {
				s.vt = n
			}
		}
	}
	// NOTE: display may be "" — modern GDM launches Xorg with -displayfd (no ":N"
	// on the command line), so the server picks the display at runtime. The caller
	// resolves it from the process's listening socket (displayFromPid).
	return s, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// selectActive picks the X server bound to the active VT, preferring a real
// Xorg over Xwayland. Falls back to the first real Xorg, then anything.
func selectActive(cands []xServer, activeVT int) (xServer, bool) {
	if len(cands) == 0 {
		return xServer{}, false
	}
	if activeVT >= 0 {
		for _, c := range cands {
			if !c.xwayland && c.vt == activeVT {
				return c, true
			}
		}
	}
	for _, c := range cands {
		if !c.xwayland {
			return c, true
		}
	}
	if activeVT >= 0 {
		for _, c := range cands {
			if c.vt == activeVT {
				return c, true
			}
		}
	}
	return cands[0], true
}

// findActiveX scans /proc for X servers and returns the active one's display +
// auth file. ok is false when no X server is running (headless / pure Wayland).
// sessionAccount is the person whose desktop is active. The sidecar itself
// runs as root here, so it asks the desktop (desktopAccount).
func sessionAccount() string {
	auth := ""
	if _, a, ok := findActiveX(); ok {
		auth = a
	}
	return desktopAccount(auth)
}

// desktopAccount names the person at a desktop whose Xauthority file is auth:
// that file's owner when it is a person's (GDM keeps it in the user's runtime
// directory); else the user session logind has active on seat0 (LightDM keeps
// every session's file as root); else the sidecar's own account.
func desktopAccount(auth string) string {
	if a := authAccount(auth); a != "" {
		return a
	}
	if a := logindAccount(runLoginctl); a != "" {
		return a
	}
	return processAccount()
}

// logindAccount is the user of the session logind has active on seat0, when it
// is a person's own session and not a display manager's greeter.
func logindAccount(loginctl func(args ...string) ([]byte, error)) string {
	out, err := loginctl("show-seat", "seat0", "--property=ActiveSession")
	if err != nil {
		return ""
	}
	id := loginctlProperties(out)["ActiveSession"]
	if id == "" || strings.ContainsAny(id, "/ \t") {
		return ""
	}
	out, err = loginctl("show-session", id, "--property=Name", "--property=Class")
	if err != nil {
		return ""
	}
	props := loginctlProperties(out)
	if props["Class"] != "user" {
		return ""
	}
	return accountName(props["Name"])
}

// loginctlProperties reads loginctl show-* output: one Key=value per line.
func loginctlProperties(out []byte) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

// runLoginctl runs loginctl for logindAccount. Systems without systemd have no
// loginctl, and the name is simply not found.
func runLoginctl(args ...string) ([]byte, error) {
	path, err := exec.LookPath("loginctl")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, args...).Output()
}

// authAccount is the account an Xauthority file belongs to, when it is a
// person's: a login screen's file belongs to the display manager, a system
// account below uid 1000 (LightDM keeps every session's file as root).
func authAccount(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid < 1000 || st.Uid == 65534 {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return ""
	}
	return accountName(u.Username)
}

func findActiveX() (display, auth string, ok bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", "", false
	}
	var cands []xServer
	for _, e := range entries {
		if !allDigits(e.Name()) {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name != "Xorg" && name != "X" && name != "Xwayland" {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || len(bytes.TrimRight(cmdline, "\x00")) == 0 {
			continue
		}
		if s, ok := parseXCmdline(name, cmdline); ok {
			s.pid, _ = strconv.Atoi(e.Name())
			cands = append(cands, s)
		}
	}
	pick, ok := selectActive(cands, readActiveVT())
	if !ok {
		return "", "", false
	}
	// Resolve the display when it wasn't on the command line (GDM -displayfd):
	// look at which /tmp/.X11-unix/Xn socket the server process actually holds.
	if pick.display == "" {
		if d := displayFromPid(pick.pid); d != "" {
			pick.display = d
		} else {
			pick.display = ":0" // last-resort default
		}
	}
	if pick.xwayland {
		log.Printf("rmm-remote: WARNING active X is Xwayland (%s) — a Wayland session can't be captured via X11; the screen will be black. Use an Xorg session/greeter.", pick.display)
	}
	return pick.display, pick.auth, true
}

// displayFromPid resolves the X display number an Xorg process serves by finding
// the /tmp/.X11-unix/Xn unix socket it holds open. Modern GDM starts Xorg with
// -displayfd (no ":N" argument), so the display can't be read from the command
// line; we map the process's socket inodes (/proc/<pid>/fd) to their paths via
// /proc/net/unix and extract the display number. Returns "" if undetermined.
func displayFromPid(pid int) string {
	if pid <= 0 {
		return ""
	}
	fdDir := "/proc/" + strconv.Itoa(pid) + "/fd"
	ents, err := os.ReadDir(fdDir)
	if err != nil {
		return ""
	}
	inodes := map[string]bool{}
	for _, e := range ents {
		link, err := os.Readlink(fdDir + "/" + e.Name())
		if err != nil {
			continue
		}
		if strings.HasPrefix(link, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	if len(inodes) == 0 {
		return ""
	}
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return ""
	}
	best := -1
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line) // ... Inode(6) Path(7)
		if len(f) < 8 || !inodes[f[6]] {
			continue
		}
		n := displayNumFromSocketPath(f[7])
		if n >= 0 && (best < 0 || n < best) {
			best = n
		}
	}
	if best < 0 {
		return ""
	}
	return ":" + strconv.Itoa(best)
}

// displayNumFromSocketPath extracts N from an X socket path like
// "@/tmp/.X11-unix/X1" or "/tmp/.X11-unix/X1"; returns -1 if it isn't one.
func displayNumFromSocketPath(path string) int {
	p := strings.TrimPrefix(path, "@")
	const pfx = "/tmp/.X11-unix/X"
	if !strings.HasPrefix(p, pfx) {
		return -1
	}
	num := p[len(pfx):]
	if !allDigits(num) {
		return -1
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return -1
	}
	return n
}
