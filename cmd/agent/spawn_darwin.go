//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// remoteAgentLabel is the per-user LaunchAgent that owns desktop sessions. The
// installer writes its plist; the daemon only ever kickstarts it.
const remoteAgentLabel = "com.openrmm.remote"

// launchSidecar starts the media sidecar. A file-transfer session has no UI and
// runs straight from the daemon, as root; a desktop session cannot.
//
// Two constraints rule out every way of spawning it from the daemon directly,
// and they pull in opposite directions. TCC grants are per user, so the process
// must run AS the console user for the operator's "Allow" to apply. And TCC
// judges a request by its RESPONSIBLE process, so nothing Apple-signed may sit
// in between: with sudo in the chain, tccd refuses to prompt at all ("Platform
// binary prompting is 'Deny'") and the sidecar is denied — on macOS 15 that
// surfaces first as local networking, where the relay dial fails with
// "no route to host". `launchctl asuser` alone avoids the platform binary but
// leaves the process as root, whose TCC database is not the one the operator
// granted in — capture then returns a black frame containing only the sidecar's
// own window.
//
// A LaunchAgent satisfies both: launchd starts it inside the user's session, as
// that user, with an identity of its own.
func launchSidecar(bin, relay, ticket string, extra ...string) error {
	filesOnly := false
	noFiles := false
	for _, arg := range extra {
		switch arg {
		case "-files":
			filesOnly = true
		case "-no-files":
			noFiles = true
		}
	}
	if os.Geteuid() != 0 || filesOnly {
		return startDirect(bin, sidecarArgs(relay, ticket, extra))
	}

	uid, err := consoleUID()
	if err != nil {
		return err
	}
	if err := writeSessionFile(uid, relay, ticket, filesOnly, noFiles); err != nil {
		return err
	}
	// -k restarts the job if a previous session left it running.
	out, err := exec.Command("/bin/launchctl", "kickstart", "-k", "gui/"+uid+"/"+remoteAgentLabel).CombinedOutput()
	if err != nil {
		_ = os.Remove(sessionFilePath())
		return fmt.Errorf("launchctl kickstart %s: %w: %s", remoteAgentLabel, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// startDirect is the non-GUI path: no session, no user switch.
func startDirect(bin string, args []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// Start() only reports that the exec succeeded. Waiting briefly turns an
	// immediate death into a launch failure the viewer can be told about,
	// instead of a session reported as started that never streams anything.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return fmt.Errorf("sidecar exited immediately: %w", err)
	case <-time.After(1500 * time.Millisecond):
		return nil
	}
}

func consoleUID() (string, error) {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == 0 {
		return "", fmt.Errorf("macOS remote desktop requires a logged-in console user")
	}
	return strconv.FormatUint(uint64(stat.Uid), 10), nil
}

// sessionFilePath is fixed, because a LaunchAgent's plist is static and cannot
// carry a per-user name. Ownership, not the path, is what keeps it private: the
// file is chowned to the session user and never readable by anyone else.
func sessionFilePath() string {
	return filepath.Join(agentStateDir(), "remote-session.json")
}

func agentStateDir() string {
	if exe, err := os.Executable(); err == nil && filepath.Dir(exe) != "" {
		if _, err := os.Stat("/etc/rmm-agent"); err == nil {
			return "/etc/rmm-agent"
		}
		return filepath.Dir(exe)
	}
	return "/etc/rmm-agent"
}

// writeSessionFile hands the LaunchAgent what its static plist cannot carry.
// The ticket is a credential, so it goes to a file owned by the session user
// with no group or world access — never an argument, which `ps` shows to
// everyone, and never an environment variable, which launchctl scopes globally.
func writeSessionFile(uid, relay, ticket string, filesOnly, noFiles bool) error {
	body, err := json.Marshal(map[string]any{
		"relay": relay, "ticket": ticket, "files_only": filesOnly, "no_files": noFiles,
	})
	if err != nil {
		return err
	}
	path := sessionFilePath()
	_ = os.Remove(path)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	n, err := strconv.Atoi(uid)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.Chown(path, n, -1); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("session file ownership: %w", err)
	}
	return nil
}
