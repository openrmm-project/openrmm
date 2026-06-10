package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Agent self-update. The agent periodically checks the server's /dl endpoint for
// a newer build of its own binary — exactly the ETag mechanism the remote sidecar
// already uses (see remote.go). When the published binary changes, the agent
// downloads it, validates it actually runs on this machine, atomically swaps it
// in, and reloads (re-exec on Unix; a detached service restart on Windows). So a
// fleet-wide upgrade is just "republish to /dl"; the agents pick it up on their
// own. Disable with RMM_AUTO_UPDATE=0; tune the cadence with RMM_UPDATE_INTERVAL.
//
// Bootstrap caveat: an agent must already be running a build that CONTAINS this
// code for auto-update to work — the very first rollout onto self-updating agents
// is still a manual (re)install.

const defaultUpdateInterval = 30 * time.Minute

// updateLoop checks for a new agent build shortly after connecting, then on a
// fixed interval, until the connection ends.
func (a *agent) updateLoop(done <-chan struct{}) {
	if !autoUpdateEnabled() {
		return
	}
	interval := updateInterval()
	// Stagger the first check so a freshly-published build doesn't cause every
	// agent in the fleet to download at the same instant.
	first := time.NewTimer(60 * time.Second)
	defer first.Stop()
	select {
	case <-done:
		return
	case <-first.C:
		a.maybeSelfUpdate()
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			a.maybeSelfUpdate()
		}
	}
}

// maybeSelfUpdate upgrades the agent binary if the server publishes a different
// one. It is conservative: any error leaves the running agent untouched.
func (a *agent) maybeSelfUpdate() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	src := a.httpBase() + "/dl/" + agentDownloadName()
	remoteTag := remoteETag(src)
	if remoteTag == "" {
		return // server unreachable or doesn't expose an ETag; try again later
	}
	etagPath := exe + ".update-etag"
	if remoteTag == readETag(etagPath) {
		return // already running the published build
	}
	log.Printf("update: server build changed (etag %s); downloading new agent", remoteTag)

	tmp := exe + ".new"
	if err := download(src, tmp); err != nil {
		log.Printf("update: download failed: %v", err)
		return
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, 0o755)
	}
	if err := enforceArtifactSignature(src, tmp); err != nil {
		log.Printf("update: refusing unsigned/untrusted agent build: %v", err)
		_ = os.Remove(tmp)
		return
	}
	// Guard against a truncated / corrupt / wrong-arch download bricking the host:
	// only swap a binary that actually executes and reports a version here.
	if err := validateBinary(tmp); err != nil {
		log.Printf("update: downloaded binary failed validation, discarding: %v", err)
		_ = os.Remove(tmp)
		return
	}
	// If the published build is byte-identical to the one already running (e.g.
	// the freshly installed agent, whose binary came from /dl), just record the
	// ETag — no restart needed. We still converge correctly when content differs.
	if same, _ := sameContent(exe, tmp); same {
		_ = os.WriteFile(etagPath, []byte(remoteTag), 0o644)
		_ = os.Remove(tmp)
		log.Printf("update: published build matches the running agent; recorded etag, no restart")
		return
	}
	if err := swapAndReload(exe, tmp, etagPath, remoteTag); err != nil {
		log.Printf("update: could not apply update: %v", err)
		return
	}
	// On Unix swapAndReload re-execs and never returns; reaching here is Windows
	// (a detached restart was scheduled) or a no-reload fallback.
	log.Printf("update: new agent staged; restarting")
}

// validateBinary runs the candidate with -print-version and requires it to exit
// cleanly with non-empty output. A wrong-architecture or corrupt binary fails to
// exec, so this catches the dangerous cases before we replace the live binary.
func validateBinary(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-print-version").Output()
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("empty version output")
	}
	return nil
}

// agentDownloadName is the published binary name for this OS/arch on /dl.
func agentDownloadName() string {
	n := fmt.Sprintf("rmm-agent-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

func autoUpdateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RMM_AUTO_UPDATE"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

func updateInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("RMM_UPDATE_INTERVAL"))
	if v == "" {
		return defaultUpdateInterval
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 5*time.Minute {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 300 {
		return time.Duration(secs) * time.Second
	}
	return defaultUpdateInterval
}

// sameContent reports whether two files have identical size and SHA-256.
func sameContent(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if fa.Size() != fb.Size() {
		return false, nil
	}
	ha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cleanupOldBinary removes the previous binary left beside the executable by a
// past update (kept briefly for rollback). Safe to call on every startup.
func cleanupOldBinary() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
		_ = os.Remove(exe + ".new")
	}
}
