package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

// handleRemoteQuery answers an on-demand request from the server for a
// remote-provider value. Today the only kind is the RustDesk peer id: the
// RustDesk client is installed out-of-band (not by this agent) and we only read
// its id with `rustdesk --get-id` so the console can open rustdesk://<id>.
func (a *agent) handleRemoteQuery(q protocol.RemoteQueryMsg) {
	res := protocol.RemoteQueryResultMsg{RequestID: q.RequestID}
	switch q.Kind {
	case protocol.RemoteQueryRustDeskID:
		if id, err := rustDeskID(); err != nil {
			res.Error = err.Error()
		} else {
			res.Value = id
		}
	default:
		res.Error = "unknown query kind: " + q.Kind
	}
	a.queue(protocol.MsgRemoteQueryResult, res)
}

// rustDeskID runs the locally-installed RustDesk client to read this machine's
// peer id.
func rustDeskID() (string, error) {
	bin, err := rustDeskBinary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--get-id").Output()
	if err != nil {
		return "", fmt.Errorf("rustdesk --get-id failed: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("RustDesk returned an empty id")
	}
	return id, nil
}

// rustDeskBinary locates the installed RustDesk client across operating systems.
func rustDeskBinary() (string, error) {
	if p, err := exec.LookPath("rustdesk"); err == nil {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			`C:\Program Files\RustDesk\rustdesk.exe`,
			`C:\Program Files (x86)\RustDesk\rustdesk.exe`,
		}
	case "darwin":
		candidates = []string{"/Applications/RustDesk.app/Contents/MacOS/rustdesk"}
	default:
		candidates = []string{"/usr/bin/rustdesk", "/usr/local/bin/rustdesk", "/snap/bin/rustdesk"}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("RustDesk is not installed on this device")
}
