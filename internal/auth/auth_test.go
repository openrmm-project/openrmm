package auth

import (
	"encoding/hex"
	"testing"
)

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b {
		t.Fatal("NewToken returned identical tokens")
	}
	if len(a) != 64 { // 32 bytes hex-encoded
		t.Fatalf("token length = %d, want 64", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("token is not valid hex: %v", err)
	}
}

func TestHash(t *testing.T) {
	h := Hash("secret")
	if h == "secret" {
		t.Fatal("Hash returned the plaintext")
	}
	if h != Hash("secret") {
		t.Fatal("Hash is not deterministic")
	}
	if h == Hash("secret2") {
		t.Fatal("different inputs produced the same hash")
	}
	if len(h) != 64 {
		t.Fatalf("hash length = %d, want 64 (sha256 hex)", len(h))
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Error("equal strings reported unequal")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Error("different strings reported equal")
	}
	if ConstantTimeEqual("abc", "abcd") {
		t.Error("different-length strings reported equal")
	}
	if ConstantTimeEqual("", "x") {
		t.Error("empty vs non-empty reported equal")
	}
}

func TestPassword(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword error: %v", err)
	}
	if hash == "hunter2" {
		t.Fatal("password stored in plaintext")
	}
	if !CheckPassword(hash, "hunter2") {
		t.Error("CheckPassword rejected the correct password")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("CheckPassword accepted a wrong password")
	}
	// bcrypt salts, so the same password hashes differently each time.
	hash2, _ := HashPassword("hunter2")
	if hash == hash2 {
		t.Error("bcrypt produced identical hashes (no salt?)")
	}
}
