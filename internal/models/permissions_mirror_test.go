package models

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The console keeps its own copy of the default role permissions, used when a
// stored session carries no permission list. A copy is a thing that drifts: when
// users.email.manage was added here, the console's admin list was left behind,
// which hid the address editor from administrators who did hold the permission.
//
// This reads the console's copy and compares it to the source of truth, so the
// next addition cannot repeat that silently. It is a Go test rather than a
// frontend one because the authoritative list lives here.
func TestConsoleRolePermissionsMirrorTheDefaults(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "openrmm", "src", "mockapi.jsx"))
	if err != nil {
		// A moved or renamed client is a real signal, not a reason to skip: the
		// mirror still exists somewhere and this test has stopped guarding it.
		t.Fatalf("cannot read the console API client: %v", err)
	}

	roleArray := regexp.MustCompile(`(?m)^\s*(readonly|tech|admin):\s*\[([^\]]*)\]`)
	matches := roleArray.FindAllStringSubmatch(string(source), -1)
	if len(matches) != 3 {
		t.Fatalf("found %d role arrays in DEFAULT_ROLE_PERMS, want 3", len(matches))
	}

	for _, m := range matches {
		role, list := m[1], m[2]
		var got []string
		for _, raw := range strings.Split(list, ",") {
			if v := strings.Trim(strings.TrimSpace(raw), "'\""); v != "" {
				got = append(got, v)
			}
		}
		want := DefaultRolePermissions(role)
		if len(want) == 0 {
			t.Fatalf("%s is not a known role here", role)
		}
		missing := difference(want, got)
		extra := difference(got, want)
		if len(missing) > 0 {
			t.Errorf("the console's %s list is missing %v", role, missing)
		}
		if len(extra) > 0 {
			t.Errorf("the console's %s list has %v, which no longer exist here", role, extra)
		}
	}
}

func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, v := range b {
		inB[v] = true
	}
	var out []string
	for _, v := range a {
		if !inB[v] {
			out = append(out, v)
		}
	}
	return out
}
