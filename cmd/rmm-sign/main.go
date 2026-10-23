// Command rmm-sign generates Ed25519 signing keys and detached signatures for
// OpenRMM agent/sidecar artifacts published under /dl, and the manifest's
// second signature with ECDSA P-256 (p256.go).
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
	printPublicKey := flag.Bool("print-public-key", false, "print the public key derived from the configured private key")
	key := flag.String("key", os.Getenv("RMM_SIGNING_KEY"), "base64 Ed25519 private key; defaults to RMM_SIGNING_KEY")
	genKeyP256 := flag.Bool("gen-key-p256", false, "generate a new ECDSA P-256 keypair for the manifest's second signature")
	printPublicKeyP256 := flag.Bool("print-public-key-p256", false, "print the P-256 public key derived from the configured P-256 private key")
	p256 := flag.Bool("p256", false, "sign the given files (the SHA256SUMS manifest) with the P-256 key, writing <file>.p256.sig")
	keyP256 := flag.String("key-p256", os.Getenv("RMM_SIGNING_KEY_P256"), "base64 PKCS#8 P-256 private key; defaults to RMM_SIGNING_KEY_P256")
	flag.Parse()

	if *genKeyP256 {
		priv, pub, err := generateP256()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("RMM_MANIFEST_PUBLIC_KEY=%s\n", pub)
		fmt.Printf("RMM_SIGNING_KEY_P256=%s\n", priv)
		return
	}
	if *printPublicKeyP256 {
		priv, err := decodeP256PrivateKey(*keyP256)
		if err != nil {
			log.Fatal(err)
		}
		_, pub, err := encodeP256(priv)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(pub)
		return
	}
	if *p256 {
		if flag.NArg() == 0 {
			log.Fatal("usage: rmm-sign -p256 -key-p256 <base64-pkcs8-key> SHA256SUMS")
		}
		priv, err := decodeP256PrivateKey(*keyP256)
		if err != nil {
			log.Fatal(err)
		}
		for _, path := range flag.Args() {
			if err := signFileP256(path, priv); err != nil {
				log.Fatalf("%s: %v", path, err)
			}
			fmt.Printf("signed %s.p256.sig\n", path)
		}
		return
	}

	if *genKey {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("RMM_UPDATE_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(pub))
		fmt.Printf("RMM_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(priv))
		return
	}
	if *printPublicKey {
		priv, err := decodePrivateKey(*key)
		if err != nil {
			log.Fatal(err)
		}
		pub := priv.Public().(ed25519.PublicKey)
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
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
