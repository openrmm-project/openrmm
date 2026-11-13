// Package secretcrypto implements the instance's existing AES-GCM secret format.
// It has no dependency on settings or the database, so store can use it too.
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/p1ngu/rmm/internal/config"
	"io"
	"strings"
)

func encryptionKey(cfg *config.Config) [32]byte {
	key := cfg.SettingsEncryptionKey
	if key == "" {
		key = cfg.AdminToken
	}
	return sha256.Sum256([]byte(key))
}

func Encrypt(cfg *config.Config, plaintext string) (string, error) {
	key := encryptionKey(cfg)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), nil)...)
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(out), nil
}

func Decrypt(cfg *config.Config, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "enc:v1:") {
		return ciphertext, nil // migration path for any legacy plaintext value.
	}
	key := encryptionKey(cfg)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "enc:v1:"))
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, sealed := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
