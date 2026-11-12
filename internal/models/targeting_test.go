package models

import "testing"

// A device's role is detected from what its agent reports: Windows by product
// type, or by edition name for agents that do not send it; a Mac is a
// workstation; Linux has none until an administrator sets one.
func TestDetectRole(t *testing.T) {
	for _, tc := range []struct {
		os, platform, family, want string
	}{
		{"windows", "Microsoft Windows 11 Pro", "Standalone Workstation", RoleWorkstation},
		{"windows", "Microsoft Windows Server 2022 Datacenter", "Server", RoleServer},
		{"windows", "Microsoft Windows Server 2019 Standard", "Server (Domain Controller)", RoleServer},
		// Older agents send no product type: the edition name decides.
		{"windows", "Microsoft Windows Server 2016 Standard", "", RoleServer},
		{"windows", "Microsoft Windows 10 Enterprise", "", RoleWorkstation},
		// The product type wins over a misleading name.
		{"windows", "Contoso Server Edition", "Standalone Workstation", RoleWorkstation},
		{"windows", "", "", ""},
		{"darwin", "darwin", "", RoleWorkstation},
		{"linux", "ubuntu", "debian", ""},
		{"linux", "rocky", "rhel", ""},
		{"freebsd", "freebsd", "", ""},
	} {
		if got := DetectRole(tc.os, tc.platform, tc.family); got != tc.want {
			t.Errorf("DetectRole(%q, %q, %q) = %q, want %q", tc.os, tc.platform, tc.family, got, tc.want)
		}
	}
	if EffectiveRole(RoleWorkstation, RoleServer) != RoleServer || EffectiveRole(RoleServer, "") != RoleServer || EffectiveRole("", "") != "" {
		t.Fatal("the override must win, and detection apply without one")
	}
	if TargetOS("darwin") != TargetOSMacOS || TargetOS("windows") != TargetOSWindows {
		t.Fatal("OS vocabulary")
	}
	for _, ok := range [][2]string{{"", ""}, {"windows", ""}, {"", "server"}, {"macos", "workstation"}, {"linux", "server"}} {
		if !ValidTargetFilter(ok[0], ok[1]) {
			t.Errorf("valid filter %v refused", ok)
		}
	}
	for _, bad := range [][2]string{{"darwin", ""}, {"Windows", ""}, {"", "Server"}, {"", "desktop"}, {"android", ""}} {
		if ValidTargetFilter(bad[0], bad[1]) {
			t.Errorf("invalid filter %v accepted", bad)
		}
	}
}

// The activation review seals each assignment's filters and the devices left
// out: narrowing, widening or excluding after the review was read changes it.
func TestPolicyReviewSealsTargeting(t *testing.T) {
	base := func() *PolicyReview {
		return &PolicyReview{Policy: Policy{ID: "p1", Name: "Servers"},
			Assignments: []PolicyAssignment{{ID: "a1", ScopeType: ScopeClient, ScopeID: "c1", OS: TargetOSWindows, Role: RoleServer}},
			Exclusions:  []PolicyExclusion{{AgentID: "d1"}, {AgentID: "d2"}}}
	}
	ref := base()
	ref.Seal()
	swapped := base()
	swapped.Exclusions[0], swapped.Exclusions[1] = swapped.Exclusions[1], swapped.Exclusions[0]
	swapped.Seal()
	if swapped.Fingerprint != ref.Fingerprint {
		t.Fatal("exclusion order changed the fingerprint")
	}
	for name, change := range map[string]func(r *PolicyReview){
		"role widened": func(r *PolicyReview) { r.Assignments[0].Role = "" },
		"os changed":   func(r *PolicyReview) { r.Assignments[0].OS = TargetOSLinux },
		"made global": func(r *PolicyReview) {
			r.Assignments[0].ScopeType, r.Assignments[0].ScopeID = ScopeGlobal, GlobalScopeID
		},
		"device put back": func(r *PolicyReview) { r.Exclusions = r.Exclusions[:1] },
		"device excluded": func(r *PolicyReview) { r.Exclusions = append(r.Exclusions, PolicyExclusion{AgentID: "d3"}) },
	} {
		r := base()
		change(r)
		r.Seal()
		if r.Fingerprint == ref.Fingerprint {
			t.Errorf("%s: fingerprint unchanged", name)
		}
	}
	patch := &PatchPolicyReview{Policy: PatchPolicy{ID: "pp"}, Assignments: []PatchPolicyAssignment{{ScopeType: ScopeSite, ScopeID: "s1"}}}
	patch.Seal()
	filtered := &PatchPolicyReview{Policy: PatchPolicy{ID: "pp"}, Assignments: []PatchPolicyAssignment{{ScopeType: ScopeSite, ScopeID: "s1", Role: RoleServer}}}
	filtered.Seal()
	excluded := &PatchPolicyReview{Policy: PatchPolicy{ID: "pp"}, Assignments: patch.Assignments, Exclusions: []PolicyExclusion{{AgentID: "d1"}}}
	excluded.Seal()
	if patch.Fingerprint == filtered.Fingerprint || patch.Fingerprint == excluded.Fingerprint {
		t.Fatal("a patch policy review does not seal filters and exclusions")
	}
}
