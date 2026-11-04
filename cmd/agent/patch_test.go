package main

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// The scan's title keeps its accents both ways it can arrive: as UTF-8 (the
// agent asks PowerShell for it) and, when PowerShell refuses, in the console's
// OEM page, 850 on a pt-BR install.
func TestParseWindowsPatchOutputKeepsAccentedTitle(t *testing.T) {
	const title = "2026-08 .NET Framework Atualização de Segurança (KB5120710)"
	utf8JSON := `[{"identifier":"KB5120710","title":"` + title + `","severity":"Important","categories":"Security Updates","kb":"KB5120710","reboot":false}]`
	cp850JSON, err := charmap.CodePage850.NewEncoder().Bytes([]byte(utf8JSON))
	if err != nil {
		t.Fatal(err)
	}

	for name, text := range map[string]string{
		"utf-8":            decodeOutputCP([]byte(utf8JSON), 850),
		"console page 850": decodeOutputCP(cp850JSON, 850),
	} {
		patches, err := parseWindowsPatchOutput(text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(patches) != 1 {
			t.Fatalf("%s: got %d patches, want 1", name, len(patches))
		}
		if got := patches[0].Title; got != title {
			t.Errorf("%s: title = %q, want %q", name, got, title)
		}
		if strings.ContainsRune(patches[0].Title, '\uFFFD') {
			t.Errorf("%s: title still contains replacement characters: %q", name, patches[0].Title)
		}
	}
}
