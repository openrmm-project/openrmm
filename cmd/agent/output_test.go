package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// The bug, reproduced: `Olá, café!` printed by a redirected Python on a
// pt-BR Windows arrives as cp1252 bytes. Handing those straight to a Go string
// and marshalling them replaced each accented byte with U+FFFD, which is what
// reached the database as `Ol<?>, caf<?>!`. The replacement is lossy — no
// downstream code can undo it — so it has to be handled at capture.
func TestDecodeOutputRecoversCP1252Accents(t *testing.T) {
	// "Olá, café! Python funcionando." in cp1252: á = 0xE1, é = 0xE9.
	raw := []byte("Ol\xe1, caf\xe9! Python funcionando.")

	// What the old path produced.
	spoiled, err := json.Marshal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spoiled), `\ufffd`) {
		t.Fatalf("precondition failed: marshalling raw bytes should lose them, got %s", spoiled)
	}

	got := decodeOutput(raw)
	if got != "Olá, café! Python funcionando." {
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
		"Olá, café! Python funcionando.",
		"日本語とEmoji 🎉",
		"tab\tnewline\nquote\"backslash\\",
	} {
		if got := decodeOutput([]byte(s)); got != s {
			t.Errorf("decodeOutput(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Measured on a pt-BR Windows 11 (2026-11-04): Windows PowerShell under the
// agent service wrote "Instalação" in its console's OEM page, 850, and the agent
// read the bytes as the ANSI page, 1252, so the console showed "Instala‡Æo".
func TestConsoleOutputIsReadInItsOwnPage(t *testing.T) {
	const text = "Instalação, café, não pôde"
	raw, err := charmap.CodePage850.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeOutputCP(raw, 1252); got != "Instala‡Æo, caf‚, nÆo p“de" {
		t.Fatalf("precondition failed: reading cp850 as 1252 should reproduce the measured text, got %q", got)
	}
	if got := decodeOutputCP(raw, 850); got != text {
		t.Errorf("decodeOutputCP(_, 850) = %q, want %q", got, text)
	}
}

// Every Windows page of the console's languages reads back, the double-byte
// Asian pages included: before, anything but a short list of single-byte pages
// was read as 1252.
func TestDecodeOutputCoversTheConsoleLanguages(t *testing.T) {
	for _, c := range []struct {
		cp   int
		text string
	}{
		{437, "Olá, café"},
		{850, "Atualização de Segurança"},
		{852, "Zażółć gęślą jaźń"},
		{866, "Установка обновления"},
		{1251, "Установка обновления"},
		{1252, "Actualización"},
		{932, "更新プログラムのインストール"},
		{936, "安装更新"},
		{949, "업데이트 설치"},
		{950, "安裝更新"},
	} {
		raw, err := codePageEncoding(c.cp).NewEncoder().Bytes([]byte(c.text))
		if err != nil {
			t.Fatalf("cp %d: cannot encode %q: %v", c.cp, c.text, err)
		}
		if utf8.Valid(raw) {
			t.Fatalf("cp %d: the sample encodes to valid UTF-8, so it tests nothing", c.cp)
		}
		if got := decodeOutputCP(raw, c.cp); got != c.text {
			t.Errorf("cp %d: decodeOutputCP = %q, want %q", c.cp, got, c.text)
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

	code, stdout, stderr := runScriptCtx(ctx, "python", "print('Olá, café! Python funcionando.')")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Olá, café!") {
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
