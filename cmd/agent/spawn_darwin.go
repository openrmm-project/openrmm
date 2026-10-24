//go:build darwin

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// remoteAgentLabel is the per-user LaunchAgent that owns desktop sessions, and
// the bundle identifier macOS keys the sidecar's privacy grants to. Reverse-DNS
// of a domain the project controls: the earlier com.openrmm.remote named a
// domain it does not own.
const remoteAgentLabel = "io.openrmm.remote"

// legacyRemoteAgentLabel is what endpoints installed before that rename carry.
const legacyRemoteAgentLabel = "com.openrmm.remote"

// launchctl runs /bin/launchctl; a variable so the tests can stand in for it.
var launchctl = func(args ...string) ([]byte, error) {
	return exec.Command("/bin/launchctl", args...).CombinedOutput()
}

func remoteAgentPlistPath(label string) string {
	return filepath.Join("/Library/LaunchAgents", label+".plist")
}

func remoteAgentPlist() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>-session</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><false/>
  <key>KeepAlive</key><false/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`, remoteAgentLabel, sidecarExePath(""), sessionFilePath())
}

// ensureRemoteAgent keeps the LaunchAgent in step with the running agent. The
// installer writes it too, but agents update themselves and installers do not
// re-run: a label or a path that changes between versions would otherwise leave
// the endpoint with a job the daemon can no longer start.
func ensureRemoteAgent(uid string) error {
	retireLegacyRemoteAgent(uid)
	path := remoteAgentPlistPath(remoteAgentLabel)
	want := remoteAgentPlist()
	if current, err := os.ReadFile(path); err != nil || string(current) != want {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		_ = os.Chown(path, 0, 0)
	}
	// Reload even when the plist did not change. launchd caches the executable's
	// Lightweight Code Requirement when the job is bootstrapped. Replacing an
	// ad-hoc signed bundle leaves that cache pointing at the previous CDHash and
	// kickstart then fails with OS_REASON_CODESIGNING ("needs LWCR update").
	_, _ = launchctl("bootout", "gui/"+uid+"/"+remoteAgentLabel)
	if out, err := launchctl("bootstrap", "gui/"+uid, path); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w: %s", remoteAgentLabel, err, strings.TrimSpace(string(out)))
	}
	log.Printf("remote: refreshed the %s LaunchAgent", remoteAgentLabel)
	return nil
}

// retireLegacyRemoteAgent removes the LaunchAgent the previous bundle
// identifier left on the endpoint.
//
// Its privacy rows are not touched, because nothing here can: tccutil resolves
// an identifier through LaunchServices and refuses one whose app is gone ("No
// such bundle identifier", OSStatus -10814), which by now it always is. The
// reset this used to attempt never succeeded on any Mac. Only test machines
// ever ran that identifier; docs/08 has the job that does clear the rows.
func retireLegacyRemoteAgent(uid string) {
	path := remoteAgentPlistPath(legacyRemoteAgentLabel)
	if _, err := os.Stat(path); err != nil {
		return
	}
	_, _ = launchctl("bootout", "gui/"+uid+"/"+legacyRemoteAgentLabel)
	if err := os.Remove(path); err != nil {
		log.Printf("remote: remove %s: %v", path, err)
		return
	}
	log.Printf("remote: retired the %s LaunchAgent", legacyRemoteAgentLabel)
}

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
	noChat := false
	for _, arg := range extra {
		switch arg {
		case "-files":
			filesOnly = true
		case "-no-files":
			noFiles = true
		case "-no-chat":
			noChat = true
		}
	}
	if os.Geteuid() != 0 || filesOnly {
		return startDirect(bin, sidecarArgs(relay, ticket, extra))
	}

	uid, err := consoleUID()
	if err != nil {
		return err
	}
	if err := ensureRemoteAgent(uid); err != nil {
		return err
	}
	return startSessionJob(uid, sessionJobDir(), relay, ticket, filesOnly, noFiles, noChat)
}

// sessionJobPrefix names the LaunchAgent job of one desktop session.
//
// Every session gets a job of its own. They used to share the one job above,
// restarted for each new session (kickstart -k), which killed the session
// already running: a guest's session ended the technician's it depends on,
// that ended the guest's, and the two kept replacing each other; two
// technicians on one Mac did the same. Privacy grants are not affected: macOS
// keys them to the app's code identity, not to the launchd label.
const sessionJobPrefix = remoteAgentLabel + ".session."

// sessionJobDir holds the session jobs' plists and parameter files: root's, so
// only root writes them; each parameter file is chowned to the session user.
func sessionJobDir() string {
	return filepath.Join(agentStateDir(), "remote-sessions")
}

// startSessionJob starts one session's sidecar as a new LaunchAgent job in the
// console user's session, leaving any other session running. Jobs of sessions
// that have ended are removed first.
func startSessionJob(uid, dir, relay, ticket string, filesOnly, noFiles, noChat bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.Chown(dir, 0, 0)
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	reapSessionJobs(uid, dir)

	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	label := sessionJobPrefix + hex.EncodeToString(id[:])
	params := filepath.Join(dir, label+".json")
	plist := filepath.Join(dir, label+".plist")
	if err := writeSessionFile(params, uid, relay, ticket, filesOnly, noFiles, noChat); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(sessionJobPlist(label, params)), 0o644); err != nil {
		_ = os.Remove(params)
		return err
	}
	_ = os.Chown(plist, 0, 0)
	if out, err := launchctl("bootstrap", "gui/"+uid, plist); err != nil {
		_ = os.Remove(params)
		_ = os.Remove(plist)
		return fmt.Errorf("launchctl bootstrap %s: %w: %s", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sessionJobPlist starts the sidecar as soon as the job is bootstrapped, once:
// it reads its parameters from params and deletes them.
func sessionJobPlist(label, params string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>-session</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`, label, sidecarExePath(""), params)
}

// reapSessionJobs boots out the jobs of sessions whose sidecar is no longer
// running and deletes their files. A running one is left alone.
func reapSessionJobs(uid, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, sessionJobPrefix) || !strings.HasSuffix(name, ".plist") {
			continue
		}
		label := strings.TrimSuffix(name, ".plist")
		target := "gui/" + uid + "/" + label
		out, err := launchctl("print", target)
		if err == nil && strings.Contains(string(out), "state = running") {
			continue
		}
		if err == nil {
			_, _ = launchctl("bootout", target)
		}
		_ = os.Remove(filepath.Join(dir, name))
		_ = os.Remove(filepath.Join(dir, label+".json"))
	}
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

// sessionFilePath is where the shared job (remoteAgentPlist) reads its
// parameters. The installer still writes that job, and earlier agents started
// every session through it; sessions now each get their own job and file
// (startSessionJob). Ownership, not the path, keeps a parameter file private:
// it is chowned to the session user and never readable by anyone else.
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
func writeSessionFile(path, uid, relay, ticket string, filesOnly, noFiles, noChat bool) error {
	body, err := json.Marshal(map[string]any{
		"relay": relay, "ticket": ticket, "files_only": filesOnly, "no_files": noFiles, "no_chat": noChat,
	})
	if err != nil {
		return err
	}
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
