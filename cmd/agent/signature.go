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
)

const (
	signatureVersion = "rmm-ed25519-sha256-v1"
	signatureMaxSize = 16 << 10
)

// updatePublicKey is intended to be set at build time:
//
//	-ldflags "-X main.updatePublicKey=<base64-ed25519-public-key>"
//
// RMM_UPDATE_PUBLIC_KEY overrides it at runtime. Verification is controlled by
// RMM_UPDATE_VERIFY: "strict" (default), "warn", or "off". Production service
// configuration only accepts strict mode; weaker modes require explicit lab mode.
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
	pub, ok, err := configuredUpdatePublicKey()
	if err != nil {
		return nil, signatureFailure(mode, "invalid update public key: %v", err)
	}
	if !ok {
		return nil, signatureFailure(mode, "no update public key configured")
	}
	sig, err := fetchArtifactSignature(src + ".sig")
	if err != nil {
		return nil, signatureFailure(mode, "could not fetch signature for %s: %v", src, err)
	}
	if err := verifyFileSignature(path, pub, sig); err != nil {
		return nil, signatureFailure(mode, "signature verification failed for %s: %v", src, err)
	}
	log.Printf("security: verified update signature for %s", src)
	return sig, nil
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

func configuredUpdatePublicKey() (ed25519.PublicKey, bool, error) {
	v := effectiveUpdatePublicKey()
	if v == "" {
		return nil, false, nil
	}
	var raw []byte
	var err error
	if raw, err = base64.StdEncoding.DecodeString(v); err != nil {
		raw, err = hex.DecodeString(v)
	}
	if err != nil {
		return nil, true, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, true, fmt.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), true, nil
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
