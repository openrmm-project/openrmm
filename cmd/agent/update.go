package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sort"
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

const (
	updateHealthDelay   = 30 * time.Second
	updateRollbackAfter = 2 * time.Minute
)

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
	signature, err := verifiedArtifactSignature(src, tmp)
	if err != nil {
		log.Printf("update: refusing unsigned/untrusted agent build: %v", err)
		_ = os.Remove(tmp)
		return
	}
	// Guard against a truncated / corrupt / wrong-arch download bricking the host:
	// only swap a binary that actually executes and reports a version here.
	candidateVersion, err := binaryVersion(tmp)
	if err != nil {
		log.Printf("update: downloaded binary failed validation, discarding: %v", err)
		_ = os.Remove(tmp)
		return
	}
	if err := validateUpgradeVersion(agentVersion, candidateVersion); err != nil {
		log.Printf("update: refusing candidate: %v", err)
		_ = os.Remove(tmp)
		return
	}
	// If the published build is byte-identical to the one already running (e.g.
	// the freshly installed agent, whose binary came from /dl), just record the
	// ETag — no restart needed. We still converge correctly when content differs.
	if same, _ := sameContent(exe, tmp); same {
		_ = os.WriteFile(etagPath, []byte(remoteTag), 0o644)
		if len(signature) > 0 {
			_ = writeLocalArtifactSignature(exe+".sig", signature)
		}
		_ = os.Remove(tmp)
		log.Printf("update: published build matches the running agent; recorded etag, no restart")
		return
	}
	if err := writePendingUpdate(exe, tmp, remoteTag, agentVersion, candidateVersion, signature); err != nil {
		log.Printf("update: could not create authenticated rollback transaction: %v", err)
		_ = os.Remove(tmp)
		return
	}
	if err := writeLocalArtifactSignature(tmp+".sig", signature); err != nil {
		log.Printf("update: could not stage candidate signature: %v", err)
		_ = os.Remove(updatePendingPath(exe))
		_ = os.Remove(tmp)
		return
	}
	if err := swapAndReload(exe, tmp); err != nil {
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
	_, err := binaryVersion(path)
	return err
}

func binaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-print-version").Output()
	if err != nil {
		return "", err
	}
	v := string(bytes.TrimSpace(out))
	if v == "" {
		return "", fmt.Errorf("empty version output")
	}
	if _, err := parseVersion(v); err != nil {
		return "", fmt.Errorf("invalid version %q: %w", v, err)
	}
	return v, nil
}

func validateUpgradeVersion(current, candidate string) error {
	cmp, err := compareVersions(candidate, current)
	if err != nil {
		return err
	}
	if cmp < 0 {
		return fmt.Errorf("downgrade from %s to %s is not allowed", current, candidate)
	}
	return nil
}

type parsedVersion struct {
	major, minor, patch int
	pre                 []string
}

func parseVersion(raw string) (parsedVersion, error) {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	v = strings.SplitN(v, "+", 2)[0]
	parts := strings.SplitN(v, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return parsedVersion{}, fmt.Errorf("expected semantic version")
	}
	nums := make([]int, 3)
	for i, value := range core {
		if value == "" || (len(value) > 1 && value[0] == '0') {
			return parsedVersion{}, fmt.Errorf("invalid numeric component")
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return parsedVersion{}, fmt.Errorf("invalid numeric component")
		}
		nums[i] = n
	}
	parsed := parsedVersion{major: nums[0], minor: nums[1], patch: nums[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return parsedVersion{}, fmt.Errorf("empty prerelease")
		}
		parsed.pre = strings.Split(parts[1], ".")
		for _, id := range parsed.pre {
			if id == "" {
				return parsedVersion{}, fmt.Errorf("empty prerelease identifier")
			}
			for _, r := range id {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '-' {
					return parsedVersion{}, fmt.Errorf("invalid prerelease identifier")
				}
			}
		}
	}
	return parsed, nil
}

func compareVersions(a, b string) (int, error) {
	av, err := parseVersion(a)
	if err != nil {
		return 0, fmt.Errorf("invalid candidate version %q: %w", a, err)
	}
	bv, err := parseVersion(b)
	if err != nil {
		return 0, fmt.Errorf("invalid current version %q: %w", b, err)
	}
	left := []int{av.major, av.minor, av.patch}
	right := []int{bv.major, bv.minor, bv.patch}
	for i := range left {
		if left[i] < right[i] {
			return -1, nil
		}
		if left[i] > right[i] {
			return 1, nil
		}
	}
	if len(av.pre) == 0 && len(bv.pre) == 0 {
		return 0, nil
	}
	if len(av.pre) == 0 {
		return 1, nil
	}
	if len(bv.pre) == 0 {
		return -1, nil
	}
	for i := 0; i < len(av.pre) && i < len(bv.pre); i++ {
		ai, aerr := strconv.Atoi(av.pre[i])
		bi, berr := strconv.Atoi(bv.pre[i])
		switch {
		case aerr == nil && berr == nil && ai != bi:
			if ai < bi {
				return -1, nil
			}
			return 1, nil
		case aerr == nil && berr != nil:
			return -1, nil
		case aerr != nil && berr == nil:
			return 1, nil
		case av.pre[i] != bv.pre[i]:
			ordered := []string{av.pre[i], bv.pre[i]}
			sort.Strings(ordered)
			if ordered[0] == av.pre[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	if len(av.pre) < len(bv.pre) {
		return -1, nil
	}
	if len(av.pre) > len(bv.pre) {
		return 1, nil
	}
	return 0, nil
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

func cleanupUpdateTemps() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".new")
		_ = os.Remove(exe + ".new.sig")
		_ = os.Remove(exe + ".failed")
		_ = os.Remove(updatePendingPath(exe) + ".new")
	}
}

func updatePendingPath(exe string) string { return exe + ".update-pending" }

type pendingUpdate struct {
	ETag               string    `json:"etag"`
	PreviousVersion    string    `json:"previous_version"`
	CandidateVersion   string    `json:"candidate_version"`
	PreviousSHA256     string    `json:"previous_sha256"`
	CandidateSHA256    string    `json:"candidate_sha256"`
	PreviousSignature  []byte    `json:"previous_signature"`
	CandidateSignature []byte    `json:"candidate_signature"`
	CreatedAt          time.Time `json:"created_at"`
}

func writePendingUpdate(exe, candidate, etag, previousVersion, candidateVersion string, signature []byte) error {
	if len(signature) == 0 {
		return fmt.Errorf("a verified Ed25519 candidate signature is required")
	}
	previousSignature, err := readLocalArtifactSignature(exe + ".sig")
	if err != nil {
		return fmt.Errorf("current binary has no local signature proof; reinstall with a signed installer before auto-update: %w", err)
	}
	if err := verifyLocalArtifactSignature(exe, previousSignature); err != nil {
		return fmt.Errorf("current binary signature proof failed: %w", err)
	}
	previousHash, err := hashFile(exe)
	if err != nil {
		return err
	}
	candidateHash, err := hashFile(candidate)
	if err != nil {
		return err
	}
	pending := pendingUpdate{
		ETag:               etag,
		PreviousVersion:    previousVersion,
		CandidateVersion:   candidateVersion,
		PreviousSHA256:     previousHash,
		CandidateSHA256:    candidateHash,
		PreviousSignature:  previousSignature,
		CandidateSignature: signature,
		CreatedAt:          time.Now().UTC(),
	}
	body, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	tmp := updatePendingPath(exe) + ".new"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, updatePendingPath(exe))
}

func writeLocalArtifactSignature(path string, signature []byte) error {
	if len(signature) == 0 {
		return fmt.Errorf("empty artifact signature")
	}
	body := fmt.Sprintf("%s %s\n", signatureVersion, base64.StdEncoding.EncodeToString(signature))
	return os.WriteFile(path, []byte(body), 0o644)
}

func readLocalArtifactSignature(path string) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseArtifactSignature(body)
}

func readPendingUpdate(exe string) (pendingUpdate, error) {
	var pending pendingUpdate
	body, err := os.ReadFile(updatePendingPath(exe))
	if err != nil {
		return pending, err
	}
	if err := json.Unmarshal(body, &pending); err != nil {
		return pending, fmt.Errorf("invalid pending update record: %w", err)
	}
	return pending, nil
}

func validatePendingRollback(exe string) (pendingUpdate, error) {
	pending, err := readPendingUpdate(exe)
	if err != nil {
		return pending, err
	}
	if pending.PreviousVersion == "" || pending.CandidateVersion == "" || len(pending.PreviousSignature) == 0 || len(pending.CandidateSignature) == 0 {
		return pending, fmt.Errorf("pending update has no authenticated rollback proof")
	}
	if err := validateUpgradeVersion(pending.PreviousVersion, pending.CandidateVersion); err != nil {
		return pending, fmt.Errorf("invalid update direction: %w", err)
	}
	currentHash, err := hashFile(exe)
	if err != nil {
		return pending, err
	}
	previousHash, err := hashFile(exe + ".old")
	if err != nil {
		return pending, err
	}
	if currentHash != pending.CandidateSHA256 || previousHash != pending.PreviousSHA256 {
		return pending, fmt.Errorf("rollback artifacts do not match the recorded transaction")
	}
	currentSignature, err := readLocalArtifactSignature(exe + ".sig")
	if err != nil || !bytes.Equal(currentSignature, pending.CandidateSignature) {
		return pending, fmt.Errorf("candidate signature sidecar does not match the recorded transaction")
	}
	previousSignature, err := readLocalArtifactSignature(exe + ".old.sig")
	if err != nil || !bytes.Equal(previousSignature, pending.PreviousSignature) {
		return pending, fmt.Errorf("previous signature sidecar does not match the recorded transaction")
	}
	if err := verifyLocalArtifactSignature(exe, pending.CandidateSignature); err != nil {
		return pending, fmt.Errorf("candidate signature proof failed: %w", err)
	}
	if err := verifyLocalArtifactSignature(exe+".old", pending.PreviousSignature); err != nil {
		return pending, fmt.Errorf("previous binary signature proof failed: %w", err)
	}
	return pending, nil
}

func confirmHealthyUpdate(done <-chan struct{}) {
	timer := time.NewTimer(updateHealthDelay)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	pending := updatePendingPath(exe)
	transaction, err := readPendingUpdate(exe)
	if err != nil {
		return
	}
	_ = os.WriteFile(exe+".update-etag", []byte(transaction.ETag), 0o644)
	_ = os.Remove(exe + ".old")
	_ = os.Remove(exe + ".old.sig")
	_ = os.Remove(pending)
	log.Printf("update: authenticated connection remained healthy; rollback backup removed")
}

func rollbackUnhealthyUpdate() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	info, err := os.Stat(updatePendingPath(exe))
	if err != nil || time.Since(info.ModTime()) < updateRollbackAfter {
		return false
	}
	log.Printf("update: no stable authenticated reconnect; rolling back")
	if _, err := validatePendingRollback(exe); err != nil {
		log.Printf("update: authenticated rollback refused: %v", err)
		return false
	}
	if err := rollbackAndReload(exe); err != nil {
		log.Printf("update: rollback failed: %v", err)
		return false
	}
	return true
}

func recoverInterruptedUpdateAtStartup() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	info, err := os.Stat(updatePendingPath(exe))
	if err != nil || time.Since(info.ModTime()) < updateRollbackAfter {
		return false
	}
	if _, err := validatePendingRollback(exe); err != nil {
		log.Printf("update: startup rollback refused: %v", err)
		return false
	}
	log.Printf("update: recovering stale signed update transaction before agent startup")
	if err := rollbackAndReload(exe); err != nil {
		log.Printf("update: startup rollback failed: %v", err)
		return false
	}
	return true
}

func emergencyRollback() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := validatePendingRollback(exe); err != nil {
		return fmt.Errorf("emergency rollback refused: %w", err)
	}
	return rollbackAndRestartService(exe)
}
