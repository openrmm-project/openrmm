package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// A Mac fetches one of two remote-desktop bundles, built from the same binary:
//
//   - signed, the default: carries the project's code-signing certificate, so
//     macOS recognises every update as the same application and the privacy
//     grants an operator gave survive it;
//   - adhoc: depends on no certificate at all, at the price of macOS treating
//     each update as a new application and asking for the grants again.
//
// The choice lives in the agent's state directory rather than in the service
// arguments, because install.sh rewrites those on every reinstall. It has to
// stay put: switching variants is itself an identity change, and one that
// happens silently costs every grant on the machine. install.sh writes the file
// only when --macos-bundle is given, so a reinstall that says nothing keeps it.
//
// Platform-neutral on purpose, so the naming is tested on every CI runner and
// not only on the one that is billed ten times over.
const macBundleChoiceFile = "remote-bundle"

// readMacBundleChoice returns "signed" or "adhoc". Anything else — a missing
// file, an unreadable one, a hand edit that says neither — is the default. The
// installer refuses unknown values, so only a manual edit reaches that branch,
// and it is logged rather than guessed at.
func readMacBundleChoice(stateDir string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, macBundleChoiceFile))
	if err != nil {
		return "signed"
	}
	switch choice := strings.TrimSpace(string(b)); choice {
	case "signed", "adhoc":
		return choice
	default:
		log.Printf("remote: %s says %q, which is neither signed nor adhoc; using the signed bundle", macBundleChoiceFile, choice)
		return "signed"
	}
}

// macBundleArchive is the name the variant is published under in /dl/. The
// signed one keeps the name every earlier agent already asks for, so agents in
// the field stay on the variant they have until they are told otherwise.
func macBundleArchive(arch, choice string) string {
	if choice == "adhoc" {
		return fmt.Sprintf("rmm-remote-darwin-%s.adhoc.app.tar.gz", arch)
	}
	return fmt.Sprintf("rmm-remote-darwin-%s.app.tar.gz", arch)
}

// Written by the bundle builder when a persistent signing identity was used.
// Its absence means the bundle is ad-hoc signed, so macOS sees every published
// build as a different application. Read as a file, with no codesign involved,
// which is why this lives here and is tested on every runner.
const macStableIdentityMarker = "Contents/Resources/StableCodeIdentity"

func macStableIdentity(bundle string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(macStableIdentityMarker)))
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

func describeMacIdentity(identity string, stable bool) string {
	if !stable {
		return "ad-hoc"
	}
	return "certificate (" + identity + ")"
}

// describeMacIdentityChange is the log line for a bundle replacement.
func describeMacIdentityChange(replacing bool, current string, currentStable bool, candidate string, candidateStable bool) string {
	if !replacing {
		return "first install, identity " + describeMacIdentity(candidate, candidateStable) + "; macOS asks for its permissions once"
	}
	change := "identity " + describeMacIdentity(current, currentStable) + " -> " + describeMacIdentity(candidate, candidateStable)
	if macPrivacyIdentityChanged(current, currentStable, candidate, candidateStable) {
		return change + "; privacy grants do not carry over and are reset, so macOS asks again"
	}
	return change + "; same identity, privacy grants kept"
}
