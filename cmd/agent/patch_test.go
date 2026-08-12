package main

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestParseWindowsPatchOutputKeepsAccentedTitle(t *testing.T) {
	const title = "2026-08 .NET Framework Atualização de Segurança (KB5120710)"
	utf8JSON := `[{"identifier":"KB5120710","title":"` + title + `","severity":"Important","categories":"Security Updates","kb":"KB5120710","reboot":false}]`
	cp1252JSON, err := charmap.Windows1252.NewEncoder().Bytes([]byte(utf8JSON))
	if err != nil {
		t.Fatal(err)
	}

	patches, err := parseWindowsPatchOutput(cp1252JSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(patches))
	}
	if got := patches[0].Title; got != title {
		t.Errorf("title = %q, want %q", got, title)
	}
	if strings.ContainsRune(patches[0].Title, '\uFFFD') {
		t.Errorf("title still contains replacement characters: %q", patches[0].Title)
	}
}
