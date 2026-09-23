package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacStableIdentity(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "RMMRemote.app")
	if id, stable := macStableIdentity(bundle); stable || id != "" {
		t.Fatalf("no marker: %q, %t", id, stable)
	}
	res := filepath.Join(bundle, "Contents", "Resources")
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(res, "StableCodeIdentity")
	if err := os.WriteFile(marker, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stable := macStableIdentity(bundle); stable {
		t.Fatal("an empty marker is not an identity")
	}
	dr := `identifier "io.openrmm.remote" and certificate root = H"a2fb72d5"`
	if err := os.WriteFile(marker, []byte(dr+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if id, stable := macStableIdentity(bundle); !stable || id != dr {
		t.Fatalf("marker: %q, %t", id, stable)
	}
}

func TestMacPrivacyIdentityChange(t *testing.T) {
	tests := []struct {
		name                           string
		current, candidate             string
		currentStable, candidateStable bool
		want                           bool
	}{
		{name: "same persistent identity", current: "dr-a", candidate: "dr-a", currentStable: true, candidateStable: true, want: false},
		{name: "rotated persistent identity", current: "dr-a", candidate: "dr-b", currentStable: true, candidateStable: true, want: true},
		{name: "two ad-hoc builds", want: true},
		{name: "ad-hoc to persistent", candidate: "dr-a", candidateStable: true, want: true},
		{name: "persistent to ad-hoc", current: "dr-a", currentStable: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := macPrivacyIdentityChanged(tt.current, tt.currentStable, tt.candidate, tt.candidateStable); got != tt.want {
				t.Fatalf("macPrivacyIdentityChanged() = %t, want %t", got, tt.want)
			}
		})
	}
}

// The log line has to tell, on its own, whether an update is why a Mac asked
// for its permissions again.
func TestDescribeMacIdentityChange(t *testing.T) {
	cases := []struct {
		name                           string
		replacing                      bool
		current, candidate             string
		currentStable, candidateStable bool
		want                           []string
	}{
		{"first install", false, "", "dr-a", false, true, []string{"first install", "certificate (dr-a)", "asks for its permissions once"}},
		{"ad-hoc to certificate", true, "", "dr-a", false, true, []string{"ad-hoc -> certificate (dr-a)", "reset"}},
		{"same certificate", true, "dr-a", "dr-a", true, true, []string{"certificate (dr-a) -> certificate (dr-a)", "grants kept"}},
		{"certificate to ad-hoc", true, "dr-a", "", true, false, []string{"-> ad-hoc", "reset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := describeMacIdentityChange(c.replacing, c.current, c.currentStable, c.candidate, c.candidateStable)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Fatalf("%q does not mention %q", got, w)
				}
			}
		})
	}
}

func TestOrNone(t *testing.T) {
	if orNone("") != "none" || orNone(`"abc"`) != `"abc"` {
		t.Fatal("orNone")
	}
}
