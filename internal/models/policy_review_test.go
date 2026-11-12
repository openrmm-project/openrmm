package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleReview() *PolicyReview {
	warn, fail := 20.0, 10.0
	return &PolicyReview{
		Policy: Policy{ID: "p1", Name: "Workstations", ProposedBy: "mcp:claude"},
		Assignments: []PolicyAssignment{
			{ID: "a1", ScopeType: ScopeClient, ScopeID: "c1"},
			{ID: "a2", ScopeType: ScopeSite, ScopeID: "s1"},
		},
		Tasks: []Task{
			{ID: "t1", Name: "Clean temp", Shell: "powershell", Script: "Remove-Item $env:TEMP\\* -Recurse", ScheduleKind: ScheduleDaily, RunAtMinute: 180, Timezone: "America/Sao_Paulo", ProposedBy: "mcp:claude"},
			{ID: "t2", Name: "Human", Shell: "bash", Script: "true", ScheduleKind: ScheduleInterval, IntervalSec: 3600, Enabled: true},
		},
		Checks: []Check{
			{ID: "c1", Type: CheckDiskSpace, Name: "Disk", Config: json.RawMessage(`{"path":"C:","x":1}`), WarnThreshold: &warn, ErrThreshold: &fail, IntervalSec: 300, ProposedBy: "mcp:claude"},
		},
	}
}

// The fingerprint seals what the person read: the same content seals the same
// whatever order rows came back in, and any change that alters what the
// activation puts into effect changes it.
func TestPolicyReviewFingerprintSealsTheContent(t *testing.T) {
	base := sampleReview()
	base.Seal()
	if len(base.Fingerprint) != 64 || base.PendingTasks != 1 || base.PendingChecks != 1 {
		t.Fatalf("sealed review: %+v", base)
	}

	reordered := sampleReview()
	reordered.Assignments[0], reordered.Assignments[1] = reordered.Assignments[1], reordered.Assignments[0]
	reordered.Tasks[0], reordered.Tasks[1] = reordered.Tasks[1], reordered.Tasks[0]
	reordered.Checks[0].Config = json.RawMessage(`{ "x": 1, "path": "C:" }`)
	reordered.Seal()
	if reordered.Fingerprint != base.Fingerprint {
		t.Fatal("row order or JSON key order changed the fingerprint")
	}

	changes := map[string]func(r *PolicyReview){
		"script edited":        func(r *PolicyReview) { r.Tasks[0].Script += "; shutdown /r" },
		"schedule moved":       func(r *PolicyReview) { r.Tasks[0].RunAtMinute = 181 },
		"zone changed":         func(r *PolicyReview) { r.Tasks[0].Timezone = "UTC" },
		"library script bound": func(r *PolicyReview) { id := "s9"; r.Tasks[1].ScriptID = &id },
		"task added": func(r *PolicyReview) {
			r.Tasks = append(r.Tasks, Task{ID: "t3", Name: "More", Shell: "sh", Script: "x", ProposedBy: "mcp:claude"})
		},
		"scope added": func(r *PolicyReview) {
			r.Assignments = append(r.Assignments, PolicyAssignment{ID: "a3", ScopeType: ScopeAgent, ScopeID: "ag1"})
		},
		"scope widened":     func(r *PolicyReview) { r.Assignments[1].ScopeType = ScopeClient },
		"threshold changed": func(r *PolicyReview) { v := 5.0; r.Checks[0].ErrThreshold = &v },
		"config changed":    func(r *PolicyReview) { r.Checks[0].Config = json.RawMessage(`{"path":"D:","x":1}`) },
		"approved meanwhile": func(r *PolicyReview) {
			r.Tasks[0].ApprovedBy = "someone"
		},
		"human task switched on": func(r *PolicyReview) { r.Tasks[1].Enabled = false },
		"renamed":                func(r *PolicyReview) { r.Policy.Name = "Servers" },
	}
	for name, change := range changes {
		r := sampleReview()
		change(r)
		r.Seal()
		if r.Fingerprint == base.Fingerprint {
			t.Errorf("%s: fingerprint unchanged", name)
		}
	}
}

func TestPatchPolicyReviewFingerprint(t *testing.T) {
	sample := func() *PatchPolicyReview {
		return &PatchPolicyReview{
			Policy: PatchPolicy{ID: "pp1", Name: "Weekend", AutoApprove: []string{PatchClassSecurity}, AutoApproveAgeDays: 7,
				WindowDays: []int{0, 6}, WindowStartMin: 120, WindowEndMin: 300, Timezone: "Asia/Kuala_Lumpur", Reboot: RebootIfRequired, ProposedBy: "mcp:claude"},
			Assignments: []PatchPolicyAssignment{{ID: "a1", ScopeType: ScopeClient, ScopeID: "c1"}, {ID: "a2", ScopeType: ScopeSite, ScopeID: "s1"}},
		}
	}
	base := sample()
	base.Seal()
	swapped := sample()
	swapped.Assignments[0], swapped.Assignments[1] = swapped.Assignments[1], swapped.Assignments[0]
	swapped.Seal()
	if swapped.Fingerprint != base.Fingerprint {
		t.Fatal("assignment order changed the fingerprint")
	}
	for name, change := range map[string]func(p *PatchPolicyReview){
		"reboot always":    func(p *PatchPolicyReview) { p.Policy.Reboot = RebootAlways },
		"approve optional": func(p *PatchPolicyReview) { p.Policy.AutoApprove = append(p.Policy.AutoApprove, PatchClassOptional) },
		"no delay":         func(p *PatchPolicyReview) { p.Policy.AutoApproveAgeDays = 0 },
		"any day":          func(p *PatchPolicyReview) { p.Policy.WindowDays = nil },
		"window moved":     func(p *PatchPolicyReview) { p.Policy.WindowEndMin = 1439 },
		"zone changed":     func(p *PatchPolicyReview) { p.Policy.Timezone = "UTC" },
		"exclusion dropped": func(p *PatchPolicyReview) {
			p.Policy.Exclusions = []string{"KB1"}
		},
		"scope added": func(p *PatchPolicyReview) {
			p.Assignments = append(p.Assignments, PatchPolicyAssignment{ID: "a3", ScopeType: ScopeAgent, ScopeID: "ag"})
		},
	} {
		r := sample()
		change(r)
		r.Seal()
		if r.Fingerprint == base.Fingerprint {
			t.Errorf("%s: fingerprint unchanged", name)
		}
	}
}

func TestValidatePatchPolicy(t *testing.T) {
	ok := PatchPolicy{Name: "Weekend", AutoApprove: []string{PatchClassSecurity, PatchClassCritical}, AutoApproveAgeDays: 7,
		WindowDays: []int{0, 6}, WindowStartMin: 0, WindowEndMin: 1439, Reboot: RebootNever, Exclusions: []string{"KB5001234"}}
	if err := ValidatePatchPolicy(&ok); err != nil {
		t.Fatalf("valid policy refused: %v", err)
	}
	empty := PatchPolicy{Name: "Defaults"}
	if err := ValidatePatchPolicy(&empty); err != nil {
		t.Fatalf("a policy with only a name (defaults) refused: %v", err)
	}
	for name, change := range map[string]func(p *PatchPolicy){
		"no name":              func(p *PatchPolicy) { p.Name = "  " },
		"long name":            func(p *PatchPolicy) { p.Name = strings.Repeat("x", 201) },
		"unknown class":        func(p *PatchPolicy) { p.AutoApprove = []string{"everything"} },
		"negative age":         func(p *PatchPolicy) { p.AutoApproveAgeDays = -1 },
		"age beyond ten years": func(p *PatchPolicy) { p.AutoApproveAgeDays = 3651 },
		"weekday 7":            func(p *PatchPolicy) { p.WindowDays = []int{7} },
		"weekday repeated":     func(p *PatchPolicy) { p.WindowDays = []int{1, 1} },
		"start past midnight":  func(p *PatchPolicy) { p.WindowStartMin = 1440 },
		"negative end":         func(p *PatchPolicy) { p.WindowEndMin = -1 },
		"unknown reboot":       func(p *PatchPolicy) { p.Reboot = "sometimes" },
		"empty exclusion":      func(p *PatchPolicy) { p.Exclusions = []string{"KB1", " "} },
		"too many exclusions":  func(p *PatchPolicy) { p.Exclusions = make([]string, 501) },
	} {
		p := ok
		p.AutoApprove = append([]string(nil), ok.AutoApprove...)
		change(&p)
		if err := ValidatePatchPolicy(&p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
