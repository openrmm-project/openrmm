package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The bug, reproduced: `Olá, Miquéias!` printed by a redirected Python on a
// pt-BR Windows arrives as cp1252 bytes. Handing those straight to a Go string
// and marshalling them replaced each accented byte with U+FFFD, which is what
// reached the database as `Ol<?>, Miqu<?>ias!`. The replacement is lossy — no
// downstream code can undo it — so it has to be handled at capture.
func TestDecodeOutputRecoversCP1252Accents(t *testing.T) {
	// "Olá, Miquéias! Python funcionando." in cp1252: á = 0xE1, é = 0xE9.
	raw := []byte("Ol\xe1, Miqu\xe9ias! Python funcionando.")

	// What the old path produced.
	spoiled, err := json.Marshal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spoiled), `\ufffd`) {
		t.Fatalf("precondition failed: marshalling raw bytes should lose them, got %s", spoiled)
	}

	got := decodeOutput(raw)
	if got != "Olá, Miquéias! Python funcionando." {
		t.Errorf("decodeOutput = %q, want the accents restored", got)
	}
	// And it survives the JSON round-trip that corrupted it before.
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `\ufffd`) {
		t.Errorf("decoded output still lost characters in JSON: %s", encoded)
	}
}

// Correct UTF-8 must pass through untouched — the fallback is for output that
// fails the check, and decoding valid UTF-8 as cp1252 would mangle it.
func TestDecodeOutputLeavesValidUTF8Alone(t *testing.T) {
	for _, s := range []string{
		"",
		"plain ascii",
		"Olá, Miquéias! Python funcionando.",
		"日本語とEmoji 🎉",
		"tab\tnewline\nquote\"backslash\\",
	} {
		if got := decodeOutput([]byte(s)); got != s {
			t.Errorf("decodeOutput(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Truncation used to slice bytes, so a limit landing inside a multi-byte
// character produced an invalid tail — corrupting UTF-8 output from Linux and
// macOS agents too, not just Windows.
func TestTruncateRunesNeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("á", 50) // 2 bytes each
	for n := 1; n < len(s); n++ {
		got := truncateRunes(s, n)
		body := strings.TrimSuffix(got, "…(truncated)")
		if !json.Valid([]byte(`"` + body + `"`)) {
			t.Fatalf("truncateRunes(_, %d) produced an invalid tail: %q", n, body)
		}
		for _, r := range body {
			if r == '�' {
				t.Fatalf("truncateRunes(_, %d) split a character: %q", n, body)
			}
		}
	}

	if got := truncateRunes("short", 100); got != "short" {
		t.Errorf("under the limit should be untouched, got %q", got)
	}
}

// End to end through the real execution path: a Python script printing accented
// text comes back intact. On a UTF-8 host this passes without the fallback; the
// point is that the whole path preserves the characters.
func TestPythonOutputKeepsAccents(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	code, stdout, stderr := runScriptCtx(ctx, "python", "print('Olá, Miquéias! Python funcionando.')")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Olá, Miquéias!") {
		t.Errorf("stdout = %q, want the accents intact", stdout)
	}
	if strings.ContainsRune(stdout, '�') {
		t.Errorf("stdout carries replacement characters: %q", stdout)
	}

	encoded, err := json.Marshal(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `\ufffd`) {
		t.Errorf("output lost characters on the way to JSON: %s", encoded)
	}
}

// The interpreter is asked for UTF-8 stdio explicitly, so the fallback is a
// safety net rather than the mechanism. A Python that honours PYTHONIOENCODING
// writes UTF-8 even when the locale says otherwise.
func TestPythonHonoursTheRequestedEncoding(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, stdout, stderr := runScriptCtx(ctx, "python", "import sys; print(sys.stdout.encoding)")
	if got := strings.ToLower(strings.TrimSpace(stdout)); !strings.Contains(got, "utf-8") && !strings.Contains(got, "utf8") {
		t.Errorf("stdout encoding = %q (stderr %q), want UTF-8 to have been requested", stdout, stderr)
	}
}
