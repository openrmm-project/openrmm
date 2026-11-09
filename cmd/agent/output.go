package main

import (
	"context"
	"os/exec"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
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
	return decodeOutputCP(b, systemCodePage())
}

// decodeShellOutput decodes a customer script's output for the shell that ran
// it. cmd and Windows PowerShell write in their console's page, which is not
// the page the agent itself would guess (see consoleCodePage); every other
// interpreter keeps the system guess.
func decodeShellOutput(shell string, b []byte) string {
	if shell == "powershell" || shell == "cmd" {
		return decodeOutputCP(b, consoleCodePage())
	}
	return decodeOutput(b)
}

// decodeConsoleOutput decodes what cmd or Windows PowerShell printed.
func decodeConsoleOutput(b []byte) string {
	return decodeOutputCP(b, consoleCodePage())
}

// decodeOutputCP is decodeOutput with the code page given: valid UTF-8 passes
// through untouched, anything else is read in that page.
func decodeOutputCP(b []byte, cp int) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	out, err := codePageEncoding(cp).NewDecoder().Bytes(b)
	if err != nil {
		// Undecodable under the reported page too. Return the raw bytes and let
		// the JSON layer substitute — no worse than before, and not a crash.
		return string(b)
	}
	return string(out)
}

// codePageEncoding maps a Windows code page to its decoder. It covers the
// OEM and ANSI pages of the languages the console ships in: Western European
// (437, 850, 1252), Central European (852, 1250), Cyrillic (866, 1251), Greek,
// Turkish, Japanese (932), Simplified Chinese (936), Korean (949) and
// Traditional Chinese (950). Anything else is read as Windows-1252, which keeps
// the Latin-1 range rather than discarding the byte.
func codePageEncoding(cp int) encoding.Encoding {
	switch cp {
	case 437:
		return charmap.CodePage437
	case 850:
		return charmap.CodePage850
	case 852:
		return charmap.CodePage852
	case 866:
		return charmap.CodePage866
	case 932:
		return japanese.ShiftJIS
	case 936:
		return simplifiedchinese.GBK
	case 949:
		return korean.EUCKR
	case 950:
		return traditionalchinese.Big5
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	}
	return charmap.Windows1252
}

// psUTF8Output asks Windows PowerShell to write its redirected output as UTF-8
// without a byte-order mark. The catch keeps a refusal from failing the program:
// the output is then in the console page, which decodeConsoleOutput still reads.
const psUTF8Output = "try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false } catch {}\n"

// agentPowerShell runs one of the agent's own PowerShell programs (the Windows
// Update scan and install, the hardware and software inventory, the device
// queries), which print JSON for the agent to parse, and returns what it
// printed as text.
//
// It asks for UTF-8 first. Without that, Windows PowerShell writes in its
// console's OEM page (850 on a pt-BR install), which cannot carry every
// language's text, and the bytes used to reach encoding/json undecoded, so
// "Instalação" in an event message arrived as "Instala\ufffd\ufffdo". Customer
// scripts do not get this request: it also changes how PowerShell reads the
// output of the programs a script runs, and a script parsing that could break.
func agentPowerShell(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psUTF8Output+script).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(decodeConsoleOutput(out), "\ufeff"), nil
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
