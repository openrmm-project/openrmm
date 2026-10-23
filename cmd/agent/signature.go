package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

const (
	signatureVersion = "rmm-ed25519-sha256-v1"
	signatureMaxSize = 16 << 10
)

// updatePublicKey is intended to be set at build time:
//
//	-ldflags "-X main.updatePublicKey=<base64-ed25519-public-key>"
//
// RMM_UPDATE_PUBLIC_KEY overrides it at runtime. Each source may carry several
// keys separated by commas/whitespace: an update is accepted when its signature
// verifies under any of them, so a signing key can be rotated by pinning the new
// key next to the old one and dropping the old one after the fleet has converged.
// Verification is controlled by RMM_UPDATE_VERIFY: "strict" (default), "warn", or
// "off". Production service configuration only accepts strict mode; weaker modes
// require explicit lab mode.
var updatePublicKey string
var runtimeUpdatePublicKey string
var runtimeUpdateVerify string

func enforceArtifactSignature(src, path string) error {
	_, err := verifiedArtifactSignature(src, path)
	return err
}

func verifiedArtifactSignature(src, path string) ([]byte, error) {
	mode := updateVerifyMode()
	if mode == "off" {
		return nil, nil
	}
	keys, err := configuredUpdatePublicKeys()
	if err != nil {
		return nil, signatureFailure(mode, "invalid update public key: %v", err)
	}
	if len(keys) == 0 {
		return nil, signatureFailure(mode, "no update public key configured")
	}
	sig, err := fetchArtifactSignature(src + ".sig")
	if err != nil {
		return nil, signatureFailure(mode, "could not fetch signature for %s: %v", src, err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return nil, signatureFailure(mode, "could not hash %s: %v", path, err)
	}
	// Accept the signature if it verifies under any pinned key. Trusting a set
	// (not one key) is what lets an operator rotate the signing key by adding the
	// new key alongside the old and later dropping the old one, without
	// reinstalling every endpoint.
	for _, pub := range keys {
		if ed25519.Verify(pub, sum[:], sig) {
			log.Printf("security: verified update signature for %s (key %s)", src, publicKeyFingerprint(pub))
			return sig, nil
		}
	}
	return nil, signatureFailure(mode, "signature verification failed for %s: no trusted update key matched", src)
}

func updateVerifyMode() string {
	v := runtimeUpdateVerify
	if v == "" {
		v = os.Getenv("RMM_UPDATE_VERIFY")
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "0", "no":
		return "off"
	case "strict", "require", "required", "true", "1", "yes":
		return "strict"
	case "warn", "warning":
		return "warn"
	case "":
		return "strict"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

// configuredUpdatePublicKeys returns the set of trusted update public keys. The
// effective source (runtime flag/env, then build-time default) may list several
// keys separated by commas, semicolons or whitespace. Every listed key must be a
// well-formed 32-byte Ed25519 key; a malformed entry is an error so a typo fails
// closed rather than silently shrinking the trust set. Duplicates are collapsed.
func configuredUpdatePublicKeys() ([]ed25519.PublicKey, error) {
	tokens := splitPublicKeyList(effectiveUpdatePublicKey())
	if len(tokens) == 0 {
		return nil, nil
	}
	keys := make([]ed25519.PublicKey, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		raw, err := decodePublicKey(tok)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[string(raw)]; dup {
			continue
		}
		seen[string(raw)] = struct{}{}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

func decodePublicKey(v string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		raw, err = hex.DecodeString(v)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return raw, nil
}

func splitPublicKeyList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
}

func publicKeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

func effectiveUpdatePublicKey() string {
	v := strings.TrimSpace(runtimeUpdatePublicKey)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("RMM_UPDATE_PUBLIC_KEY"))
	}
	if v == "" {
		v = strings.TrimSpace(updatePublicKey)
	}
	return v
}

func signatureFailure(mode, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	if mode == "strict" {
		return err
	}
	log.Printf("security: update signature warning: %v", err)
	return nil
}

func fetchArtifactSignature(src string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(src)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, signatureMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > signatureMaxSize {
		return nil, fmt.Errorf("signature too large")
	}
	return parseArtifactSignature(raw)
}

func parseArtifactSignature(raw []byte) ([]byte, error) {
	fields := strings.Fields(strings.TrimSpace(string(raw)))
	if len(fields) == 2 {
		if fields[0] != signatureVersion {
			return nil, fmt.Errorf("unsupported signature format %q", fields[0])
		}
		fields = fields[1:]
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("invalid signature format")
	}
	sig, err := base64.StdEncoding.DecodeString(fields[0])
	if err != nil {
		return nil, err
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("expected %d signature bytes, got %d", ed25519.SignatureSize, len(sig))
	}
	return sig, nil
}

// verifyLocalArtifactSignature verifies a local file's detached signature proof
// against any pinned update key. It backs the self-update rollback bookkeeping,
// which must accept the same key set that authorized the update in the first
// place.
func verifyLocalArtifactSignature(path string, sig []byte) error {
	keys, err := configuredUpdatePublicKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("no update public key configured")
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	for _, pub := range keys {
		if ed25519.Verify(pub, sum[:], sig) {
			return nil
		}
	}
	return fmt.Errorf("no trusted update key matched")
}

// verifyOwnArtifactSignature checks the running binary against the detached
// signature at sigPath under the pinned update keys. install.sh runs it on a
// host whose OpenSSL cannot check Ed25519, after the manifest's P-256
// signature has already proven this binary is the published one: it catches a
// wrong --update-public-key at install time, instead of at the first update,
// which refuses a binary with no valid local signature proof.
func verifyOwnArtifactSignature(sigPath string) error {
	sig, err := readLocalArtifactSignature(sigPath)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := verifyLocalArtifactSignature(exe, sig); err != nil {
		return fmt.Errorf("this binary does not verify under the pinned update key: %w", err)
	}
	return nil
}

func verifyFileSignature(path string, pub ed25519.PublicKey, sig []byte) error {
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, sum[:], sig) {
		return fmt.Errorf("bad signature")
	}
	return nil
}

func fileSHA256(path string) ([32]byte, error) {
	var zero [32]byte
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return zero, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
