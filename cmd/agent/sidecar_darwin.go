//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// codesign ships with the Xcode Command Line Tools, not with macOS itself:
// /usr/bin/codesign is a shim that fails on a stock Mac. Kept in a variable so
// the tests can point it at a missing binary.
var codesignExe = "/usr/bin/codesign"

func codesignUsable() bool {
	return exec.Command(codesignExe, "--version").Run() == nil
}

// macOS ships the sidecar as a signed application bundle, not a bare binary.
// Privacy grants — Screen Recording, Accessibility, Local Network — are keyed to
// a bundle; a loose executable is never offered to the operator to allow, so a
// LAN-hosted server cannot be reached at all. The bundle lives outside the
// agent's own directory because it is an application, not a helper binary.
const macSidecarBundle = "/Library/Application Support/OpenRMM/RMMRemote.app"

func sidecarDownloadName() string {
	return fmt.Sprintf("rmm-remote-darwin-%s.app.tar.gz", runtime.GOARCH)
}

func sidecarExePath(string) string {
	return filepath.Join(macSidecarBundle, "Contents", "MacOS", "rmm-remote")
}

func macBundleForExe(exe string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(exe)))
}

// validateSidecarCandidate reports whether the staged bundle keeps the macOS
// code identity that privacy grants are attached to. macOS stores the old app's
// designated requirement with those grants, and a new app that does not satisfy
// it is a different application as far as privacy is concerned. The caller logs
// the difference rather than refusing the update. Ed25519 authenticates the
// downloaded archive separately and does not establish this macOS identity.
func validateSidecarCandidate(currentExe, candidateExe string) error {
	// Most Macs have no Command Line Tools, so this check cannot run there.
	// Refusing the update then would leave a first session with no sidecar at
	// all; the archive's Ed25519 signature is the integrity check either way.
	if !codesignUsable() {
		log.Printf("remote: codesign unavailable (Xcode Command Line Tools); skipping the macOS identity check")
		return nil
	}
	candidate := macBundleForExe(candidateExe)
	if err := verifyMacBundle(candidate); err != nil {
		return fmt.Errorf("candidate macOS signature: %w", err)
	}
	if _, err := os.Stat(currentExe); os.IsNotExist(err) {
		return nil // first install has no prior privacy grant to preserve
	} else if err != nil {
		return err
	}
	current := macBundleForExe(currentExe)
	if err := verifyMacBundle(current); err != nil {
		return fmt.Errorf("installed macOS signature: %w", err)
	}
	output, err := exec.Command(codesignExe, "-d", "-r-", current).CombinedOutput()
	if err != nil {
		return fmt.Errorf("read installed designated requirement: %w: %s", err, strings.TrimSpace(string(output)))
	}
	requirement := ""
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "designated => ") {
			requirement = strings.TrimSpace(strings.TrimPrefix(line, "designated => "))
			break
		}
	}
	if requirement == "" {
		return fmt.Errorf("installed macOS bundle has no designated requirement")
	}
	output, err = exec.Command(codesignExe, "--verify", "--deep", "--strict", "-R="+requirement, candidate).CombinedOutput()
	if err != nil {
		return fmt.Errorf("candidate changes macOS privacy identity; keeping installed bundle: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyMacBundle(bundle string) error {
	output, err := exec.Command(codesignExe, "--verify", "--deep", "--strict", bundle).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign verification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	output, err = exec.Command(codesignExe, "-d", "--verbose=4", bundle).CombinedOutput()
	if err != nil {
		return fmt.Errorf("read macOS bundle identifier: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "Identifier=io.openrmm.remote" {
			return nil
		}
	}
	return fmt.Errorf("macOS bundle identifier is not io.openrmm.remote")
}

// Written by the bundle builder when a persistent signing identity was used.
// Its absence means the bundle is ad-hoc signed, so macOS sees every published
// build as a different application.
const macStableIdentityMarker = "Contents/Resources/StableCodeIdentity"

func macStableIdentity(bundle string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(bundle, macStableIdentityMarker))
	if err != nil {
		return "", false
	}
	identity := strings.TrimSpace(string(b))
	return identity, identity != ""
}

// macPrivacyIdentityChanged is deliberately conservative. An ad-hoc build has
// no identity that a later build can satisfy, and moving either to or from a
// persistent identity also invalidates the rows already stored by TCC.
func macPrivacyIdentityChanged(current string, currentStable bool, candidate string, candidateStable bool) bool {
	return !currentStable || !candidateStable || current != candidate
}

// resetStalePrivacyGrants drops the privacy rows macOS recorded for the bundle
// identifier. Those rows outlive the build they were granted to: the Privacy
// panes keep listing the sidecar as allowed while macOS denies it, which reads
// as the permission being broken rather than needing to be granted again.
// Clearing them makes macOS ask once, properly, after an update.
func resetStalePrivacyGrants(label string) {
	run := func(name string, args ...string) {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			log.Printf("remote: reset privacy grants: %s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	// Accessibility is recorded system-wide, Screen Recording per user, so the
	// reset has to run in both contexts to clear every stale row.
	run("/usr/bin/tccutil", "reset", "All", label)
	if uid, err := consoleUID(); err == nil {
		run("/bin/launchctl", "asuser", uid, "/usr/bin/tccutil", "reset", "All", label)
	}
	log.Printf("remote: cleared stale macOS privacy grants for %s; the operator is asked again on the next session", label)
}

// stageSidecar unpacks the archive next to it so the candidate can be probed
// before anything replaces the bundle in use.
func stageSidecar(artifact string) (string, func(string) error, func(), error) {
	stage := artifact + ".stage"
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", nil, nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(stage)
		_ = os.Remove(artifact)
	}
	// --strip-components keeps the archive's own top-level name from mattering.
	// --no-same-owner: the archive carries the CI runner's numeric uid, and
	// restoring it would leave the bundle owned by whichever local account
	// happens to share that number.
	cmd := exec.Command("/usr/bin/tar", "-xzf", artifact, "-C", stage, "--strip-components", "1", "--no-same-owner")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("unpack sidecar bundle: %w: %s", err, out)
	}
	exe := filepath.Join(stage, "Contents", "MacOS", "rmm-remote")
	if _, err := os.Stat(exe); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("sidecar bundle has no %s", exe)
	}
	commit := func(dest string) error {
		// dest is the executable inside the bundle; install the bundle itself.
		bundle := macBundleForExe(dest)
		candidateIdentity, candidateStable := macStableIdentity(stage)
		parent := filepath.Dir(bundle)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
		// /Library/Application Support is root:admin and group-writable, so a
		// directory created there by an administrator account stays writable by
		// it. The sidecar holds Screen Recording and Accessibility, so anyone
		// who can replace the bundle inherits those grants. MkdirAll leaves an
		// existing directory alone, which is exactly the case to correct.
		if err := os.Chown(parent, 0, 0); err != nil {
			return fmt.Errorf("take ownership of %s: %w", parent, err)
		}
		if err := os.Chmod(parent, 0o755); err != nil {
			return fmt.Errorf("restrict %s: %w", parent, err)
		}
		// Two renames rather than one: a directory cannot be renamed over a
		// populated directory. The old bundle is moved aside first so a failure
		// leaves something to fall back to rather than nothing.
		old := bundle + ".old"
		_ = os.RemoveAll(old)
		replacing := false
		resetPrivacy := false
		if _, err := os.Stat(bundle); err == nil {
			currentIdentity, currentStable := macStableIdentity(bundle)
			resetPrivacy = macPrivacyIdentityChanged(currentIdentity, currentStable, candidateIdentity, candidateStable)
			if err := os.Rename(bundle, old); err != nil {
				return err
			}
			replacing = true
		}
		if err := os.Rename(stage, bundle); err != nil {
			_ = os.Rename(old, bundle) // put the working one back
			return err
		}
		_ = os.RemoveAll(old)
		_ = os.Remove(artifact)
		// TCC keeps showing an old ad-hoc build as enabled after replacing it even
		// though the new build cannot satisfy the stored designated requirement.
		// Clear only rows that cannot apply to the candidate. A persistent signing
		// identity with the same DR keeps valid grants intact across updates.
		if replacing && resetPrivacy {
			resetStalePrivacyGrants(remoteAgentLabel)
		}
		return nil
	}
	return exe, commit, cleanup, nil
}
