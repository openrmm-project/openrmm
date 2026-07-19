package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

// sidecarRefresh serializes background sidecar refreshes so two near-simultaneous
// sessions don't race on the temp file.
var sidecarRefresh sync.Mutex

// startRemoteSession launches the media sidecar (cmd/rmm-remote) for a remote
// session. The sidecar — not the agent — captures the screen and injects input,
// so it runs as a separate process (and can live in the interactive user
// session; the agent runs as a session-0 service, which cannot capture the
// desktop). This is a swappable integration point: replace the sidecar binary
// and nothing here changes.
func (a *agent) startRemoteSession(rs protocol.RemoteStartMsg) {
	relay, ticket := a.relayForStart(rs)
	allowFiles := rs.AllowFiles == nil || *rs.AllowFiles
	bin, err := a.ensureSidecar()
	if err != nil {
		log.Printf("remote: sidecar unavailable: %v", err)
		return
	}
	// Kind "files" runs the sidecar in file-transfer-only mode (no screen
	// capture); "desktop" (or empty) is the media engine. Desktop sessions can
	// additionally disable in-band file transfer when the operator lacks permission.
	var extra []string
	if rs.Kind == protocol.RemoteKindFiles {
		extra = append(extra, "-files")
		log.Printf("remote: launching sidecar (file-transfer only) for session %s", rs.SessionID)
	} else {
		log.Printf("remote: launching media sidecar for session %s", rs.SessionID)
	}
	if rs.Kind != protocol.RemoteKindFiles && !allowFiles {
		extra = append(extra, "-no-files")
	}
	// launchSidecar is per-OS: on Windows it spawns into the interactive user
	// session (CreateProcessAsUser) so a session-0 service can still capture the
	// desktop, falling back to a direct launch; elsewhere it's a plain exec.
	if ticket != "" && !sidecarSupportsTicket(bin) {
		// A cached pre-ticket sidecar cannot parse -ticket. Keep this agent-only
		// bridge during rollout; browser viewers never receive a URL ticket.
		log.Printf("remote: cached sidecar needs the legacy agent relay handshake")
		relay = a.legacyRelayURL(ticket)
		ticket = ""
	}
	if err := launchSidecar(bin, relay, ticket, extra...); err != nil {
		log.Printf("remote: failed to start sidecar: %v", err)
	}
}

func (a *agent) relayForStart(rs protocol.RemoteStartMsg) (relay, ticket string) {
	if rs.AgentTicket != "" {
		return a.relayURL(rs.SessionID), rs.AgentTicket
	}
	if rs.Token == "" {
		return a.relayURL(rs.SessionID), ""
	}
	if rs.RelayURL != "" {
		return rs.RelayURL, ""
	}
	return a.legacyRelayURL(rs.Token), ""
}

// relayURL points the sidecar at the relay on the SAME host the agent dials for
// /ws/agent. The session id is an identifier only; the ticket is sent in the
// WebSocket handshake instead of the URL.
func (a *agent) relayURL(sessionID string) string {
	u, err := url.Parse(a.wsURL)
	if err != nil {
		return a.wsURL
	}
	u.Path = "/ws/remote"
	u.RawQuery = "session=" + url.QueryEscape(sessionID) + "&role=agent"
	return u.String()
}

func (a *agent) legacyRelayURL(ticket string) string {
	u, err := url.Parse(a.wsURL)
	if err != nil {
		return a.wsURL
	}
	u.Path = "/ws/remote"
	u.RawQuery = "role=agent&token=" + url.QueryEscape(ticket)
	return u.String()
}

func sidecarSupportsTicket(path string) bool {
	return exec.Command(path, "-print-version").Run() == nil
}

func sidecarArgs(relay, ticket string, extra []string) []string {
	args := []string{"-relay", relay}
	if ticket != "" {
		args = append(args, "-ticket", ticket)
	}
	return append(args, extra...)
}

func sidecarName() string {
	if runtime.GOOS == "windows" {
		return "rmm-remote.exe"
	}
	return "rmm-remote"
}

// ensureSidecar returns the sidecar path, downloading it next to the agent
// binary from the server's /dl endpoint if it is not already present.
//
// When a cached sidecar already exists on Linux, it returns immediately and
// checks for a newer build in the BACKGROUND (applied to the next session) —
// keeping the ETag HEAD round-trip and any ~8 MB re-download OFF the
// session-start critical path, which is the dominant "time to first frame" on
// Linux. (Renaming over a running binary is fine on Linux; on Windows a running
// .exe is locked, so there we keep the synchronous check before launch.)
func (a *agent) ensureSidecar() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(exe), sidecarName())
	dlName := fmt.Sprintf("rmm-remote-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		dlName += ".exe"
	}
	src := a.httpBase() + "/dl/" + dlName
	etagPath := path + ".etag"

	if _, err := os.Stat(path); err == nil {
		if runtime.GOOS == "linux" {
			go a.refreshSidecar(src, path, etagPath)
			return path, nil
		}
		// Windows/macOS: verify the cached build synchronously before launch.
		remoteTag := remoteETag(src)
		if remoteTag == "" || remoteTag == readETag(etagPath) {
			return path, nil
		}
		log.Printf("remote: sidecar changed on server; refreshing")
		if err := a.fetchSidecar(src, path, etagPath, remoteTag); err != nil {
			return "", err
		}
		return path, nil
	}
	// No cached sidecar (first session): must download before we can launch.
	log.Printf("remote: downloading sidecar from %s", src)
	if err := a.fetchSidecar(src, path, etagPath, remoteETag(src)); err != nil {
		return "", err
	}
	return path, nil
}

// refreshSidecar updates the cached sidecar when the server has a newer build.
// It runs off the session-start path; at most one refresh runs at a time.
func (a *agent) refreshSidecar(src, path, etagPath string) {
	if !sidecarRefresh.TryLock() {
		return // a refresh is already in flight
	}
	defer sidecarRefresh.Unlock()
	remoteTag := remoteETag(src)
	if remoteTag == "" || remoteTag == readETag(etagPath) {
		return // server unreachable, or the cached sidecar is already current
	}
	log.Printf("remote: sidecar changed on server; refreshing for next session")
	if err := a.fetchSidecar(src, path, etagPath, remoteTag); err != nil {
		log.Printf("remote: sidecar refresh failed: %v", err)
	}
}

// fetchSidecar downloads, signature-verifies, and atomically installs the
// sidecar, recording its ETag for the cache comparison above.
func (a *agent) fetchSidecar(src, path, etagPath, remoteTag string) error {
	tmp := path + ".new"
	_ = os.Remove(tmp)
	if err := download(src, tmp); err != nil {
		return err
	}
	if err := enforceArtifactSignature(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, 0o755)
	}
	candidateVersion, err := binaryVersion(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sidecar validation failed: %w", err)
	}
	if currentVersion, err := binaryVersion(path); err == nil {
		if err := validateUpgradeVersion(currentVersion, candidateVersion); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("sidecar update refused: %w", err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if remoteTag != "" {
		_ = os.WriteFile(etagPath, []byte(remoteTag), 0o644)
	}
	return nil
}

// remoteETag returns the ETag of a URL via HEAD, or "" if unknown.
func remoteETag(src string) string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Head(src)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return resp.Header.Get("ETag")
}

func readETag(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// httpBase derives the server's HTTP origin from the agent WebSocket URL.
func (a *agent) httpBase() string {
	u, err := url.Parse(a.wsURL)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	default:
		u.Scheme = "http"
	}
	u.Path, u.RawQuery = "", ""
	return u.String()
}

func download(src, dst string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", src, resp.StatusCode)
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, dst)
}
