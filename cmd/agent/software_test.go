package main

import (
	"strings"
	"testing"
)

func TestParseTabbedSoftwareIncludesManufacturer(t *testing.T) {
	rows := parseTabbed([]byte("alpha\t1.2.3\tExample Corp\nbeta\t4.5.6\n"))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if got := rows[0]["name"]; got != "alpha" {
		t.Errorf("name = %q, want alpha", got)
	}
	if got := rows[0]["version"]; got != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got)
	}
	if got := rows[0]["manufacturer"]; got != "Example Corp" {
		t.Errorf("manufacturer = %q, want Example Corp", got)
	}
	if got := rows[1]["manufacturer"]; got != "" {
		t.Errorf("missing manufacturer = %q, want empty", got)
	}
}

func TestParseWindowsSoftwareIncludesPublisher(t *testing.T) {
	rows := parseWindowsSoftware([]byte(`{"DisplayName":"Example App","DisplayVersion":"7.0","Publisher":"Example Corp"}`))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0]["manufacturer"]; got != "Example Corp" {
		t.Errorf("manufacturer = %q, want Example Corp", got)
	}
}

func TestParseDarwinSoftwareIncludesSigningIdentity(t *testing.T) {
	rows := parseDarwinSoftware([]byte(`{"SPApplicationsDataType":[{"_name":"Example App","version":"7.0","signed_by":["Developer ID Application: Example Corp"]},{"_name":"System App","obtained_from":"apple"}]}`))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if got := rows[0]["manufacturer"]; !strings.Contains(got, "Example Corp") {
		t.Errorf("manufacturer = %q, want signing identity", got)
	}
	if got := rows[1]["manufacturer"]; got != "apple" {
		t.Errorf("fallback manufacturer = %q, want apple", got)
	}
}
