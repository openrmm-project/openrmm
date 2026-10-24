package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
)

// The manifest's second signature, ECDSA P-256 over SHA-256. Ed25519 signs
// every artifact, but checking it from a shell needs OpenSSL 3, and a stock
// macOS (LibreSSL) and Linux with OpenSSL 1.1 only have `openssl dgst
// -verify`, which checks ECDSA. install.sh falls back to this signature on
// SHA256SUMS there, then to the manifest's SHA-256 of the agent; the agent
// itself keeps checking every update with Ed25519.
const p256SignatureVersion = "rmm-ecdsa-p256-sha256-v1"

// generateP256 returns a new key pair: the private key as base64 PKCS#8 DER,
// the public key as base64 SPKI DER (what install.sh and the server take).
func generateP256() (priv, pub string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	return encodeP256(key)
}

func encodeP256(key *ecdsa.PrivateKey) (priv, pub string, err error) {
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(privDER), base64.StdEncoding.EncodeToString(pubDER), nil
}

// decodeP256PrivateKey reads a base64 PKCS#8 DER private key and refuses any
// key that is not ECDSA on P-256.
func decodeP256PrivateKey(v string) (*ecdsa.PrivateKey, error) {
	if v == "" {
		return nil, fmt.Errorf("missing P-256 signing key")
	}
	der, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("the P-256 signing key is not an ECDSA P-256 key")
	}
	return key, nil
}

// signFileP256 writes <path>.p256.sig: the version and the base64 DER
// (ASN.1) ECDSA signature of the file's SHA-256, which is what
// `openssl dgst -sha256 -verify` checks.
func signFileP256(path string, key *ecdsa.PrivateKey) error {
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		return err
	}
	body := fmt.Sprintf("%s %s\n", p256SignatureVersion, base64.StdEncoding.EncodeToString(sig))
	return os.WriteFile(path+".p256.sig", []byte(body), 0o644)
}
