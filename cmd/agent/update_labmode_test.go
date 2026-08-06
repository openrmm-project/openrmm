package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Auto-update under "off" was structurally impossible: the mode makes
// verifiedArtifactSignature return nothing by design, and writePendingUpdate
// then demanded the very proof that had just been skipped. A lab agent
// downloaded a new build on every interval, discarded it, and never updated —
// with nothing to show for it unless someone read the agent's own log.
func setVerifyMode(t *testing.T, mode string) {
	t.Helper()
	previous := runtimeUpdateVerify
	runtimeUpdateVerify = mode
	t.Cleanup(func() { runtimeUpdateVerify = previous })
}

func stageBinaries(t *testing.T) (dir, exe, candidate string) {
	t.Helper()
	dir = t.TempDir()
	exe = filepath.Join(dir, "agent")
	candidate = filepath.Join(dir, "candidate")
	if err := os.WriteFile(exe, []byte("running agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, exe, candidate
}

func TestWritePendingUpdateWithoutVerificationNeedsNoSignature(t *testing.T) {
	setVerifyMode(t, "off")
	_, exe, candidate := stageBinaries(t)

	// No signature, and no .sig beside the running binary — exactly the state a
	// lab install leaves behind.
	if err := writePendingUpdate(exe, candidate, "etag", "0.4.8", "0.4.9", nil); err != nil {
		t.Fatalf("an unverified update should still record a rollback transaction: %v", err)
	}
	pending, err := readPendingUpdate(exe)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PreviousSHA256 == "" || pending.CandidateSHA256 == "" {
		t.Error("hashes must be recorded even without signatures — they are the whole rollback proof here")
	}
	if len(pending.CandidateSignature) != 0 || len(pending.PreviousSignature) != 0 {
		t.Error("no signature was verified, so none should be recorded")
	}
}

// Strict mode is untouched: without a verified signature there is no update.
func TestWritePendingUpdateStillRequiresSignatureWhenVerifying(t *testing.T) {
	for _, mode := range []string{"strict", "warn"} {
		t.Run(mode, func(t *testing.T) {
			setVerifyMode(t, mode)
			_, exe, candidate := stageBinaries(t)
			err := writePendingUpdate(exe, candidate, "etag", "0.4.8", "0.4.9", nil)
			if err == nil {
				t.Fatal("a verifying agent must refuse an unsigned candidate")
			}
			if !strings.Contains(err.Error(), "signature") {
				t.Errorf("error %q should name the missing signature", err)
			}
		})
	}
}

// The rollback transaction recorded without signatures is honoured only by an
// agent that is not verifying. This is the property that stops someone from
// stripping the signatures out of a record to get it accepted.
func TestValidatePendingRollbackHashOnlyOnlyUnderOff(t *testing.T) {
	setVerifyMode(t, "off")
	_, exe, candidate := stageBinaries(t)
	if err := writePendingUpdate(exe, candidate, "etag", "0.4.8", "0.4.9", nil); err != nil {
		t.Fatal(err)
	}
	// Perform the swap the way swapAndReload does.
	if err := os.Rename(exe, exe+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(candidate, exe); err != nil {
		t.Fatal(err)
	}

	if _, err := validatePendingRollback(exe); err != nil {
		t.Fatalf("an unverified agent should accept its own hash-only transaction: %v", err)
	}

	// Same files, same record — but now the agent verifies. The record has no
	// proof, so it must be refused rather than trusted.
	setVerifyMode(t, "strict")
	if _, err := validatePendingRollback(exe); err == nil {
		t.Fatal("a verifying agent must refuse a transaction with no authenticated proof")
	}
}

// Tampering is still caught without signatures: the hashes are what the rollback
// rests on in this mode, so they have to be checked.
func TestValidatePendingRollbackHashOnlyDetectsTampering(t *testing.T) {
	setVerifyMode(t, "off")
	_, exe, candidate := stageBinaries(t)
	if err := writePendingUpdate(exe, candidate, "etag", "0.4.8", "0.4.9", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(candidate, exe); err != nil {
		t.Fatal(err)
	}
	// Someone replaces the binary the rollback would restore.
	if err := os.WriteFile(exe+".old", []byte("planted binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingRollback(exe); err == nil {
		t.Fatal("a rollback target that does not match the recorded hash must be refused")
	}
}

// A signed transaction stays fully verified even if the agent is later flipped
// to "off" — the check keys on what was recorded, not on the current mode, so
// downgrading the mode cannot turn a signed record into an unchecked one.
func TestValidatePendingRollbackKeepsVerifyingSignedRecords(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	setVerifyMode(t, "strict")
	previousKey := runtimeUpdatePublicKey
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { runtimeUpdatePublicKey = previousKey })

	_, exe, candidate := stageBinaries(t)
	oldSum, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalArtifactSignature(exe+".sig", ed25519.Sign(priv, oldSum[:])); err != nil {
		t.Fatal(err)
	}
	newSum, err := fileSHA256(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidateSignature := ed25519.Sign(priv, newSum[:])
	if err := writePendingUpdate(exe, candidate, "etag", "0.4.8", "0.4.9", candidateSignature); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe+".sig", exe+".old.sig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(candidate, exe); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalArtifactSignature(exe+".sig", candidateSignature); err != nil {
		t.Fatal(err)
	}

	if _, err := validatePendingRollback(exe); err != nil {
		t.Fatalf("a correctly signed transaction should validate: %v", err)
	}

	// Now delete the sidecar and flip to "off". The record still claims
	// signatures, so it must not be waved through as a hash-only transaction.
	setVerifyMode(t, "off")
	if err := os.Remove(exe + ".sig"); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingRollback(exe); err == nil {
		t.Fatal("a signed record with a missing sidecar must be refused, whatever the mode")
	}
}

// swapAndReload renames the .sig sidecars around the binaries. Under "off" there
// are none, and failing the swap over a file the mode guarantees will not exist
// is precisely what left the agent running the old build.
func TestRenameSidecarToleratesAbsence(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "missing.sig")
	to := filepath.Join(dir, "moved.sig")
	if err := renameSidecar(from, to); err != nil {
		t.Fatalf("a missing sidecar must not fail the swap: %v", err)
	}

	if err := os.WriteFile(from, []byte("sig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameSidecar(from, to); err != nil {
		t.Fatalf("an existing sidecar should move: %v", err)
	}
	if _, err := os.Stat(to); err != nil {
		t.Errorf("sidecar was not moved: %v", err)
	}
}
