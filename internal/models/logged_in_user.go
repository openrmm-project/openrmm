package models

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLoggedInUserLen bounds the user name an agent reports. Windows caps a
// down-level DOMAIN\user at 15 + 1 + 20 characters and UPN-style names at 256,
// so this keeps every real name and nothing an agent could abuse.
const MaxLoggedInUserLen = 256

// CleanLoggedInUser normalises a user name an agent reported: trimmed, without
// control, invisible formatting or line/paragraph separator characters, valid
// UTF-8, and bounded. The
// value is shown in the console, exported and sent to AI tools, so it is
// treated as untrusted device data.
func CleanLoggedInUser(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if n == MaxLoggedInUserLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimSpace(b.String())
	if !utf8.ValidString(out) {
		return ""
	}
	return out
}
