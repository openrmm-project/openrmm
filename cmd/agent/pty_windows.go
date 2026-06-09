//go:build windows

package main

import (
	"fmt"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// winTerm is an interactive shell on a real Windows pseudo-console (ConPTY,
// Windows 10 1809+). ConPTY gives the viewer a genuine terminal — echo, line
// editing, ANSI/VT colors and curses-style TUIs all work, just like the Unix
// PTY path. The shell inherits the agent's token, so under the SYSTEM service it
// is elevated.
type winTerm struct {
	cpty *conpty.ConPty
	name string
}

func startShell() (termSession, error) {
	if !conpty.IsConPtyAvailable() {
		return nil, fmt.Errorf("ConPTY not available (needs Windows 10 1809+)")
	}
	// A reasonable initial size; the viewer sends a resize as soon as it connects.
	cpty, err := conpty.Start("powershell.exe -NoLogo -NoProfile", conpty.ConPtyDimensions(120, 30))
	if err != nil {
		return nil, err
	}
	return &winTerm{cpty: cpty, name: "powershell"}, nil
}

func (t *winTerm) Read(p []byte) (int, error)  { return t.cpty.Read(p) }
func (t *winTerm) Write(p []byte) (int, error) { return t.cpty.Write(p) }
func (t *winTerm) Name() string                { return t.name }

func (t *winTerm) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return t.cpty.Resize(int(cols), int(rows))
}

func (t *winTerm) Close() error { return t.cpty.Close() }

// elevated reports whether the current process token is a member of the
// Administrators group (SYSTEM, which the service runs as, qualifies).
func elevated() bool {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID, windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid,
	); err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	member, err := windows.Token(0).IsMember(sid)
	return err == nil && member
}
