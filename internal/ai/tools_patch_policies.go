package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p1ngu/rmm/internal/models"
)

// proposePatchPolicy proposes a patch policy — auto-approval, exclusions,
// maintenance window, reboot behaviour and the scopes it covers — switched off.
// A person reviews it in Patches > Policies and activates it there; until then
// it approves, installs and reboots nothing (PatchPoliciesForAgent selects
// WHERE enabled, and the database refuses to switch on an unreviewed proposal).
// The rules are the REST handler's: patch policies are global definitions, so a
// principal confined to some clients cannot create one.
func proposePatchPolicy(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Name               string          `json:"name"`
		AutoApprove        []string        `json:"auto_approve"`
		AutoApproveAgeDays *int            `json:"auto_approve_age_days"`
		Exclusions         []string        `json:"exclusions"`
		WindowDays         []int           `json:"window_days"`
		WindowStartMin     *int            `json:"window_start_min"`
		WindowEndMin       *int            `json:"window_end_min"`
		Timezone           string          `json:"timezone"`
		Reboot             string          `json:"reboot"`
		Assignments        []proposedScope `json:"assignments"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("scoped users cannot create global patch policy definitions")
	}
	p := &models.PatchPolicy{
		Name: strings.TrimSpace(in.Name), AutoApprove: in.AutoApprove, AutoApproveAgeDays: 7,
		Exclusions: in.Exclusions, WindowDays: in.WindowDays, WindowStartMin: 0, WindowEndMin: 1439,
		Reboot: in.Reboot, Enabled: false, ProposedBy: tc.proposalActor(),
	}
	if in.AutoApproveAgeDays != nil {
		p.AutoApproveAgeDays = *in.AutoApproveAgeDays
	}
	if in.WindowStartMin != nil {
		p.WindowStartMin = *in.WindowStartMin
	}
	if in.WindowEndMin != nil {
		p.WindowEndMin = *in.WindowEndMin
	}
	if p.Reboot == "" {
		p.Reboot = models.RebootIfRequired
	}
	tz, err := models.NormalizeTimezone(in.Timezone)
	if err != nil {
		return nil, err
	}
	p.Timezone = tz
	if err := models.ValidatePatchPolicy(p); err != nil {
		return nil, err
	}
	scopes, err := proposalScopes(ctx, tc, in.Assignments, models.ScopeClient, models.ScopeSite, models.ScopeAgent)
	if err != nil {
		return nil, err
	}
	assignments := make([]models.PatchPolicyAssignment, 0, len(scopes))
	for _, a := range scopes {
		assignments = append(assignments, models.PatchPolicyAssignment{ScopeType: a.ScopeType, ScopeID: a.ScopeID})
	}
	review, err := tc.Store.CreatePatchPolicyProposal(ctx, p, assignments)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":     review.Policy.ID,
		"kind":   "patch_policy",
		"name":   review.Policy.Name,
		"status": "awaiting_review",
		"scopes": len(review.Assignments),
		"message": "PATCH POLICY PROPOSED SWITCHED OFF. It approves, installs and reboots nothing until a person " +
			"reviews it in Patches > Policies and activates it. Tell the user it is waiting for their review.",
	}, nil
}

// listPatchPolicies lists the patch policies the caller may see, so a model
// can tell whether one already covers what the operator asks for.
func listPatchPolicies(ctx context.Context, tc ToolContext) (any, error) {
	pols, err := tc.Store.ListPatchPolicies(ctx)
	if err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		visible := pols[:0]
		for _, p := range pols {
			ok, err := tc.Store.PatchPolicyAllowedForUser(ctx, tc.ScopeUserID, p.ID)
			if err != nil {
				return nil, err
			}
			if ok {
				visible = append(visible, p)
			}
		}
		pols = visible
	}
	if pols == nil {
		pols = []models.PatchPolicy{}
	}
	return map[string]any{"patch_policies": pols}, nil
}
