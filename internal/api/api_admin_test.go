package api

import "testing"

func TestSupportsCredentialRotation(t *testing.T) {
	cases := map[string]bool{
		"0.4.2":  false,
		"0.4.3":  false,
		"0.4.4":  false,
		"0.4.5":  true,
		"0.4.10": true,
		"0.5.0":  true,
		"1.0.0":  true,
		"v0.4.5": true,
		"dev":    false,
	}
	for version, want := range cases {
		if got := supportsCredentialRotation(version); got != want {
			t.Errorf("supportsCredentialRotation(%q) = %t, want %t", version, got, want)
		}
	}
}
