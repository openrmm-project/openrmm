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
	"log"
	"os"
	"strconv"
	"strings"
)

type xServer struct {
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
	if s.display == "" {
		s.display = ":0"
	}
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
			cands = append(cands, s)
		}
	}
	pick, ok := selectActive(cands, readActiveVT())
	if !ok {
		return "", "", false
	}
	if pick.xwayland {
		log.Printf("rmm-remote: WARNING active X is Xwayland (%s) — a Wayland session can't be captured via X11; the screen will be black. Use an Xorg session/greeter.", pick.display)
	}
	return pick.display, pick.auth, true
}
