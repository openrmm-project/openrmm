package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMacBundleChoice(t *testing.T) {
	cases := []struct {
		name    string
		content *string
		want    string
	}{
		{"no file means the default", nil, "signed"},
		{"ad-hoc chosen", bundleChoiceText("adhoc\n"), "adhoc"},
		{"signed chosen", bundleChoiceText("signed"), "signed"},
		{"surrounding whitespace is not a different choice", bundleChoiceText("  adhoc \n"), "adhoc"},
		{"an unknown value falls back to the default", bundleChoiceText("adhock"), "signed"},
		{"an empty file falls back to the default", bundleChoiceText(""), "signed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.content != nil {
				if err := os.WriteFile(filepath.Join(dir, macBundleChoiceFile), []byte(*tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := readMacBundleChoice(dir); got != tc.want {
				t.Fatalf("readMacBundleChoice = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMacBundleArchive(t *testing.T) {
	// The signed name is the one every agent already in the field asks for; if
	// it moved, those agents would stop finding a bundle at all.
	if got := macBundleArchive("arm64", "signed"); got != "rmm-remote-darwin-arm64.app.tar.gz" {
		t.Fatalf("signed archive = %q", got)
	}
	if got := macBundleArchive("amd64", "adhoc"); got != "rmm-remote-darwin-amd64.adhoc.app.tar.gz" {
		t.Fatalf("adhoc archive = %q", got)
	}
}

func bundleChoiceText(s string) *string { return &s }
