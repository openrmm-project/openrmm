//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/creack/pty"
)

// unixTerm is an interactive shell running on a real PTY (creack/pty, CGO-free).
// The PTY's line discipline gives the viewer echo, line editing, job control and
// signals (Ctrl-C, Ctrl-Z) for free — a genuine terminal, not a pipe.
type unixTerm struct {
	f    *os.File
	cmd  *exec.Cmd
	name string
}

// startShell launches a login-ish interactive shell on a PTY. It inherits the
// agent process's uid/gid, so when the agent runs as the root system service the
// shell is root (elevated). TERM is set so curses apps (top, vim) render.
func startShell() (termSession, error) {
	shell, name := pickShell()
	cmd := exec.Command(shell, "-i")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &unixTerm{f: f, cmd: cmd, name: name}, nil
}

func (t *unixTerm) Read(p []byte) (int, error)  { return t.f.Read(p) }
func (t *unixTerm) Write(p []byte) (int, error) { return t.f.Write(p) }
func (t *unixTerm) Name() string                { return t.name }

func (t *unixTerm) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return pty.Setsize(t.f, &pty.Winsize{Cols: cols, Rows: rows})
}

func (t *unixTerm) Close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return t.f.Close()
}

// pickShell prefers bash, then the caller's $SHELL, then sh.
func pickShell() (path, name string) {
	for _, s := range []string{"/bin/bash", "/usr/bin/bash"} {
		if _, err := os.Stat(s); err == nil {
			return s, filepath.Base(s)
		}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh, filepath.Base(sh)
		}
	}
	return "/bin/sh", "sh"
}

// elevated reports whether the shell will run with root privileges.
func elevated() bool { return os.Geteuid() == 0 }
