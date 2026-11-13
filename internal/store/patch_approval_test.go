package store

import "testing"

// The catalog's approval is what the Patches screen shows next to each patch.
// It used to be "approved_count >= missing_count", where approved_count counted
// only rows still waiting to install: the moment an install was claimed, or
// failed, the patch read as unapproved again and offered the Approve button
// for a decision already made.
func TestCatalogApprovalFromDeviceCounts(t *testing.T) {
	cases := []struct {
		name string
		row  PatchCatalogRow
		want string
	}{
		{"nobody decided", PatchCatalogRow{PendingCount: 3}, PatchApprovalPending},
		{"approved, waiting", PatchCatalogRow{ApprovedCount: 2}, PatchApprovalApproved},
		{"approved, installing", PatchCatalogRow{InstallingCount: 2}, PatchApprovalApproved},
		{"approved, install failed", PatchCatalogRow{FailedCount: 1}, PatchApprovalApproved},
		{"approved, installed", PatchCatalogRow{InstalledCount: 1}, PatchApprovalApproved},
		{"approved, a mix of install states", PatchCatalogRow{ApprovedCount: 1, InstallingCount: 1, FailedCount: 1, InstalledCount: 1}, PatchApprovalApproved},
		{"declined", PatchCatalogRow{DeclinedCount: 4}, PatchApprovalDeclined},
		{"declined, installed where it was before", PatchCatalogRow{DeclinedCount: 1}, PatchApprovalDeclined},
		{"approved here, undecided there", PatchCatalogRow{ApprovedCount: 1, PendingCount: 1}, PatchApprovalPartial},
		{"declined here, undecided there", PatchCatalogRow{DeclinedCount: 1, PendingCount: 1}, PatchApprovalPartial},
		{"approved here, declined there", PatchCatalogRow{InstallingCount: 1, DeclinedCount: 1}, PatchApprovalPartial},
		{"no device rows at all", PatchCatalogRow{}, PatchApprovalPending},
	}
	for _, c := range cases {
		if got := catalogApproval(c.row); got != c.want {
			t.Errorf("%s: approval = %q, want %q", c.name, got, c.want)
		}
	}
}

// A filter lists the patches with at least one device in that state, so a
// partial patch appears under each state it has.
func TestCatalogApprovalFilter(t *testing.T) {
	partial := PatchCatalogRow{PendingCount: 1, FailedCount: 1, DeclinedCount: 1}
	for _, f := range []string{"", PatchApprovalPending, PatchApprovalApproved, PatchApprovalDeclined} {
		if !matchesApprovalFilter(partial, f) {
			t.Errorf("a patch pending, approved and declined on different devices is missing from %q", f)
		}
	}
	approved := PatchCatalogRow{InstallingCount: 2}
	if !matchesApprovalFilter(approved, PatchApprovalApproved) {
		t.Error("a patch being installed is missing from the approved list")
	}
	if matchesApprovalFilter(approved, PatchApprovalPending) || matchesApprovalFilter(approved, PatchApprovalDeclined) {
		t.Error("a patch approved everywhere is listed as pending or declined")
	}
	declined := PatchCatalogRow{DeclinedCount: 1}
	if matchesApprovalFilter(declined, PatchApprovalApproved) || !matchesApprovalFilter(declined, PatchApprovalDeclined) {
		t.Error("a declined patch is filed under the wrong list")
	}

	for _, f := range []string{"", "pending", "approved", "declined"} {
		if !ValidPatchApprovalFilter(f) {
			t.Errorf("filter %q refused", f)
		}
	}
	// "partial" is a state a row can have, not a list: it is the overlap of the others.
	for _, f := range []string{"partial", "excluded", "APPROVED", "approve"} {
		if ValidPatchApprovalFilter(f) {
			t.Errorf("filter %q accepted", f)
		}
	}
}
