package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

// startRemoteSession launches the media sidecar (cmd/rmm-remote) for a remote
// session. The sidecar — not the agent — captures the screen and injects input,
// so it runs as a separate process (and can live in the interactive user
// session; the agent runs as a session-0 service, which cannot capture the
// desktop). This is the integration point the RustDesk fork replaces: swap the
// sidecar binary and nothing here changes. See docs/12.
func (a *agent) startRemoteSession(rs protocol.RemoteStartMsg) {
	relay := a.relayURL(rs.Token)
	bin, err := a.ensureSidecar()
	if err != nil {
		log.Printf("remote: sidecar unavailable: %v", err)
		return
	}
	log.Printf("remote: launching media sidecar for session %s", rs.SessionID)
	// launchSidecar is per-OS: on Windows it spawns into the interactive user
	// session (CreateProcessAsUser) so a session-0 service can still capture the
	// desktop, falling back to a direct launch; elsewhere it's a plain exec.
	if err := launchSidecar(bin, relay); err != nil {
		log.Printf("remote: failed to start sidecar: %v", err)
	}
}

// relayURL points the sidecar at the relay on the SAME host the agent dials for
// /ws/agent — the server's RemoteStartMsg.RelayURL is built from the public URL
// (which may be localhost), so we derive ours from the agent's own server URL.
func (a *agent) relayURL(token string) string {
	u, err := url.Parse(a.wsURL)
	if err != nil {
		return a.wsURL
	}
	u.Path = "/ws/remote"
	u.RawQuery = "token=" + url.QueryEscape(token) + "&role=agent"
	return u.String()
}

func sidecarName() string {
	if runtime.GOOS == "windows" {
		return "rmm-remote.exe"
	}
	return "rmm-remote"
}

// ensureSidecar returns the sidecar path, downloading it next to the agent
// binary from the server's /dl endpoint if it is not already present.
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

	// Keep the cached sidecar only if it matches the server's current build.
	// We compare the ETag (changes on any content change — unlike size, which can
	// collide between builds) stored alongside the binary. If the HEAD fails we
	// keep whatever is on disk.
	etagPath := path + ".etag"
	remoteTag := remoteETag(src)
	if _, err := os.Stat(path); err == nil {
		if remoteTag == "" || remoteTag == readETag(etagPath) {
			return path, nil
		}
		log.Printf("remote: sidecar changed on server; refreshing")
	}
	log.Printf("remote: downloading sidecar from %s", src)
	tmp := path + ".new"
	_ = os.Remove(tmp)
	if err := download(src, tmp); err != nil {
		return "", err
	}
	if err := enforceArtifactSignature(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, 0o755)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if remoteTag != "" {
		_ = os.WriteFile(etagPath, []byte(remoteTag), 0o644)
	}
	return path, nil
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
