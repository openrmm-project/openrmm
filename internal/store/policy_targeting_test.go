package store

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestDetectedRole(t *testing.T) {
	for _, tc := range []struct {
		os, data, want string
	}{
		{"windows", `{"os":"windows","platform":"Microsoft Windows Server 2022 Standard","platform_family":"Server"}`, models.RoleServer},
		{"windows", `{"os":"windows","platform":"Microsoft Windows 11 Pro"}`, models.RoleWorkstation},
		{"darwin", `{"os":"darwin"}`, models.RoleWorkstation},
		{"linux", `{"os":"linux","platform":"ubuntu"}`, ""},
		// The enrolled OS decides, not the one the snapshot claims: a Linux
		// device saying it is Windows Server gets no role.
		{"linux", `{"os":"windows","platform":"Microsoft Windows Server 2022","platform_family":"Server"}`, ""},
		{"windows", `{"hostname":"x"}`, ""},
		{"", `{"os":"windows","platform_family":"Server"}`, ""},
		{"windows", `not json`, ""},
	} {
		if role := detectedRole(tc.os, []byte(tc.data)); role != tc.want {
			t.Errorf("%s %s -> %q, want %q", tc.os, tc.data, role, tc.want)
		}
	}
}

// The reach fragments are one definition every resolution query shares: they
// honour the switch, the global scope, both filters, the override and the
// exclusions, for whichever device alias the caller uses.
func TestReachFragmentsCoverEveryRule(t *testing.T) {
	reach := policiesReachingSQL("dev")
	for _, want := range []string{activePoliciesSQL, "'global'", "rpa.os = ''", "rpa.role = ''", "dev.role_override", "dev.os_role",
		"'darwin' THEN 'macos'", "policy_exclusions", "rpe.agent_id = dev.id", "rs.id = dev.site_id"} {
		if !strings.Contains(reach, want) {
			t.Errorf("policiesReachingSQL lacks %q", want)
		}
	}
	patch := patchPolicyReachesSQL("pp", "dev")
	for _, want := range []string{"patch_policy_assignments", "patch_policy_exclusions", "ppa.os = ''", "ppe.agent_id = dev.id"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patchPolicyReachesSQL lacks %q", want)
		}
	}
	if err := validateAssignmentTarget(models.ScopeGlobal, "some-client", "", ""); err == nil {
		t.Fatal("a global assignment with a scope id was accepted")
	}
	if err := validateAssignmentTarget(models.ScopeClient, "c1", "beos", ""); err == nil {
		t.Fatal("an unknown OS filter was accepted")
	}
	if err := validateAssignmentTarget(models.ScopeGlobal, models.GlobalScopeID, models.TargetOSWindows, models.RoleServer); err != nil {
		t.Fatal(err)
	}
}
