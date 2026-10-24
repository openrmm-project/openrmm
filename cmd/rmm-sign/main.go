// Command rmm-sign generates Ed25519 signing keys and detached signatures for
// OpenRMM agent/sidecar artifacts published under /dl, and the manifest's
// second signature with ECDSA P-256 (p256.go).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const signatureVersion = "rmm-ed25519-sha256-v1"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is the command, with its environment and output passed in so a test can
// see everything it prints. The private keys are never flag defaults: the flag
// package prints a default in -h and in every usage error, which put the key
// from the environment on the terminal or in a build log. They are read from
// the environment only after parsing, by the operation that signs.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rmm-sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	genKey := fs.Bool("gen-key", false, "generate a new Ed25519 signing keypair")
	printPublicKey := fs.Bool("print-public-key", false, "print the public key derived from the configured private key")
	key := fs.String("key", "", "base64 Ed25519 private key; RMM_SIGNING_KEY when not given (prefer it: arguments are visible to other processes)")
	genKeyP256 := fs.Bool("gen-key-p256", false, "generate a new ECDSA P-256 keypair for the manifest's second signature")
	printPublicKeyP256 := fs.Bool("print-public-key-p256", false, "print the P-256 public key derived from the configured P-256 private key")
	p256 := fs.Bool("p256", false, "sign the given files (the SHA256SUMS manifest) with the P-256 key, writing <file>.p256.sig")
	keyP256 := fs.String("key-p256", "", "base64 PKCS#8 P-256 private key; RMM_SIGNING_KEY_P256 when not given")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "rmm-sign: "+format+"\n", a...)
		return 1
	}
	ed25519Key := func() string { return flagOrEnv(*key, getenv, "RMM_SIGNING_KEY") }
	p256Key := func() string { return flagOrEnv(*keyP256, getenv, "RMM_SIGNING_KEY_P256") }

	switch {
	case *genKeyP256:
		priv, pub, err := generateP256()
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "RMM_MANIFEST_PUBLIC_KEY=%s\n", pub)
		fmt.Fprintf(stdout, "RMM_SIGNING_KEY_P256=%s\n", priv)
		return 0
	case *printPublicKeyP256:
		priv, err := decodeP256PrivateKey(p256Key())
		if err != nil {
			return fail("%v", err)
		}
		_, pub, err := encodeP256(priv)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, pub)
		return 0
	case *p256:
		if fs.NArg() == 0 {
			return fail("usage: rmm-sign -p256 SHA256SUMS (key in RMM_SIGNING_KEY_P256)")
		}
		priv, err := decodeP256PrivateKey(p256Key())
		if err != nil {
			return fail("%v", err)
		}
		for _, path := range fs.Args() {
			if err := signFileP256(path, priv); err != nil {
				return fail("%s: %v", path, err)
			}
			fmt.Fprintf(stdout, "signed %s.p256.sig\n", path)
		}
		return 0
	case *genKey:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "RMM_UPDATE_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(pub))
		fmt.Fprintf(stdout, "RMM_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(priv))
		return 0
	case *printPublicKey:
		priv, err := decodePrivateKey(ed25519Key())
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return 0
	}

	if fs.NArg() == 0 {
		return fail("usage: rmm-sign <artifact> [artifact...] (key in RMM_SIGNING_KEY)")
	}
	priv, err := decodePrivateKey(ed25519Key())
	if err != nil {
		return fail("%v", err)
	}
	for _, path := range fs.Args() {
		if err := signFile(path, priv); err != nil {
			return fail("%s: %v", path, err)
		}
		fmt.Fprintf(stdout, "signed %s.sig\n", path)
	}
	return 0
}

// flagOrEnv is the flag's value when one was given, else the environment's.
func flagOrEnv(flagValue string, getenv func(string) string, name string) string {
	if flagValue != "" {
		return flagValue
	}
	return getenv(name)
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
