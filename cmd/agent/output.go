package main

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Script output is bytes, and on Windows those bytes are usually not UTF-8.
//
// A redirected child process writes in the system's code page unless it is told
// otherwise: Python uses `locale.getpreferredencoding()` (cp1252 on a pt-BR
// install), cmd and Windows PowerShell use the OEM page (cp850). In every case
// `á` arrives as a single byte that is not valid UTF-8 on its own.
//
// Handing those bytes to a Go string and marshalling them as JSON replaces each
// one with U+FFFD, and the replacement is *lossy* — the original byte is gone, so
// nothing downstream can recover it. That is how `Olá, café` could reach the
// database as `Ol<?>, caf<?>`.
//
// Two defences, in order of preference:
//
//  1. Ask the interpreter for UTF-8 in the first place (see runScriptCtx). That
//     is lossless and needs no guessing.
//  2. Failing that, decode here from the code page the OS reports. A guess, but a
//     far better one than discarding the byte.
func decodeOutput(b []byte) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	decoder := charmap.Windows1252
	switch systemCodePage() {
	case 437:
		decoder = charmap.CodePage437
	case 850:
		decoder = charmap.CodePage850
	case 852:
		decoder = charmap.CodePage852
	case 1250:
		decoder = charmap.Windows1250
	case 1251:
		decoder = charmap.Windows1251
	case 1253:
		decoder = charmap.Windows1253
	case 1254:
		decoder = charmap.Windows1254
	}
	out, err := decoder.NewDecoder().Bytes(b)
	if err != nil {
		// Undecodable under the reported page too. Return the raw bytes and let
		// the JSON layer substitute — no worse than before, and not a crash.
		return string(b)
	}
	return string(out)
}

// truncateRunes caps output without splitting a character in half.
//
// The previous byte slice (`s[:n]`) cut multi-byte characters mid-sequence, so a
// long line of correctly-encoded Portuguese ended in an invalid tail that JSON
// then replaced. That failure is not Windows-specific: it corrupts UTF-8 output
// from Linux and macOS agents too, whenever the limit lands inside a character.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}
