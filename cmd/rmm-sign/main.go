// Command rmm-sign generates Ed25519 signing keys and detached signatures for
// RMM-AI agent/sidecar artifacts published under /dl.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

const signatureVersion = "rmm-ed25519-sha256-v1"

func main() {
	genKey := flag.Bool("gen-key", false, "generate a new Ed25519 signing keypair")
	key := flag.String("key", os.Getenv("RMM_SIGNING_KEY"), "base64 Ed25519 private key; defaults to RMM_SIGNING_KEY")
	flag.Parse()

	if *genKey {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("RMM_UPDATE_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(pub))
		fmt.Printf("RMM_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(priv))
		return
	}

	if flag.NArg() == 0 {
		log.Fatal("usage: rmm-sign -key <base64-private-key> <artifact> [artifact...]")
	}
	priv, err := decodePrivateKey(*key)
	if err != nil {
		log.Fatal(err)
	}
	for _, path := range flag.Args() {
		if err := signFile(path, priv); err != nil {
			log.Fatalf("%s: %v", path, err)
		}
		fmt.Printf("signed %s.sig\n", path)
	}
}

func decodePrivateKey(v string) (ed25519.PrivateKey, error) {
	if v == "" {
		return nil, fmt.Errorf("missing signing key")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("expected %d private-key bytes, got %d", ed25519.PrivateKeySize, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

func signFile(path string, priv ed25519.PrivateKey) error {
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, sum[:])
	body := fmt.Sprintf("%s %s\n", signatureVersion, base64.StdEncoding.EncodeToString(sig))
	return os.WriteFile(path+".sig", []byte(body), 0o644)
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
