package models

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What an agent reports is shown in the console, exported and given to AI
// tools: cleaned of control and invisible formatting characters, trimmed,
// valid UTF-8 and bounded, with real names intact.
func TestCleanLoggedInUser(t *testing.T) {
	for in, want := range map[string]string{
		`ACME\maria`:           `ACME\maria`,
		`AzureAD\JoãoSilva`:    `AzureAD\JoãoSilva`,
		"  ops  ":              "ops",
		"ma\x00ria\r\n":        "maria",
		"admin\u202eexe.txt":   "adminexe.txt",
		"zero\u200bwidth":      "zerowidth",
		"line\u2028para\u2029": "linepara",
		"bad\xffutf8":          "badutf8",
		"":                     "",
		"=HYPERLINK(\"x\")":    "=HYPERLINK(\"x\")", // spreadsheet safety is the CSV writer's job
	} {
		if got := CleanLoggedInUser(in); got != want {
			t.Errorf("CleanLoggedInUser(%q) = %q, want %q", in, got, want)
		}
	}
	long := CleanLoggedInUser(strings.Repeat("ä", MaxLoggedInUserLen+50))
	if utf8.RuneCountInString(long) != MaxLoggedInUserLen || !utf8.ValidString(long) {
		t.Fatalf("long name: %d runes", utf8.RuneCountInString(long))
	}
}
