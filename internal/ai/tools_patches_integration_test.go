package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/protocol"
)

// install_patches is the one configuration-era tool that ACTS rather than
// proposes, so its safety is the only thing standing between the model and an
// unapproved package reaching an endpoint. That safety is not in this package:
// DispatchInstall calls ClaimApprovedPatches, which claims only rows a person
// already approved. This asserts that end to end, because "I read the query"
// is not verification.

type patchFixture struct {
	proposalFixture
	svc      *patches.Service
	outbound <-chan []byte
}

// newPatchFixture gives the in-scope device two missing patches and has a
// PERSON approve exactly one of them.
func newPatchFixture(t *testing.T) patchFixture {
	t.Helper()
	f := newProposalFixture(t)

	if err := f.st.UpsertPatchReport(f.ctx, f.mineAgent, &protocol.PatchReportMsg{
		OS: "linux",
		Patches: []protocol.PatchItem{
			{Identifier: "openssl", Title: "OpenSSL", Classification: protocol.PatchSecurity},
			{Identifier: "vim", Title: "Vim", Classification: protocol.PatchOptional},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.ListAgentPatches(f.ctx, f.mineAgent)
	if err != nil {
		t.Fatal(err)
	}
	var opensslID string
	for _, row := range rows {
		if row.Identifier == "openssl" {
			opensslID = row.PatchID
		}
	}
	if opensslID == "" {
		t.Fatal("fixture: the openssl patch was not persisted")
	}
	// The human decision, made outside the AI entirely.
	if err := f.st.SetApproval(f.ctx, opensslID, models.ScopeAgent, f.mineAgent, "approve", "manual", "operator"); err != nil {
		t.Fatal(err)
	}

	h := hub.New()
	return patchFixture{proposalFixture: f, svc: patches.New(f.st, h), outbound: h.Add(f.mineAgent, nil)}
}

func (f patchFixture) tcWithPatches() ToolContext {
	tc := f.tc()
	tc.Patches = f.svc
	return tc
}

// dispatched reads what actually went to the agent. The identifiers on the wire
// are the real answer: anything else is a claim about intent.
func (f patchFixture) dispatched(t *testing.T) []string {
	t.Helper()
	select {
	case raw := <-f.outbound:
		var envelope protocol.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != protocol.MsgPatchInstall {
			t.Fatalf("frame type = %q, want %q", envelope.Type, protocol.MsgPatchInstall)
		}
		var install protocol.PatchInstallMsg
		if err := json.Unmarshal(envelope.Data, &install); err != nil {
			t.Fatal(err)
		}
		return install.Identifiers
	default:
		return nil
	}
}

func installArgs(agentID string, ids ...string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"agent_id": agentID, "identifiers": ids})
	return b
}

// The load-bearing case: the model names both patches, and only the approved one
// leaves the server.
func TestInstallPatchesSendsOnlyWhatAPersonApprovedIntegration(t *testing.T) {
	f := newPatchFixture(t)

	res := Execute(f.ctx, f.tcWithPatches(), "install_patches", installArgs(f.mineAgent, "openssl", "vim"))
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("install_patches failed: %s", res.Output)
	}

	sent := f.dispatched(t)
	if len(sent) != 1 || sent[0] != "openssl" {
		t.Fatalf("identifiers on the wire = %v, want only the approved one", sent)
	}
	// And the unapproved one was not quietly moved along either.
	rows, err := f.st.ListAgentPatches(f.ctx, f.mineAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Identifier == "vim" && row.Status != models.PatchStatusMissing {
			t.Errorf("the unapproved patch changed status to %q", row.Status)
		}
	}
}

// Asking for ONLY an unapproved patch installs nothing, and says so rather than
// reporting a success the operator would believe.
func TestInstallPatchesRefusesWhenNothingIsApprovedIntegration(t *testing.T) {
	f := newPatchFixture(t)

	res := Execute(f.ctx, f.tcWithPatches(), "install_patches", installArgs(f.mineAgent, "vim"))
	if !strings.Contains(res.Output, `"error"`) {
		t.Fatalf("an unapproved-only install reported success: %s", res.Output)
	}
	if sent := f.dispatched(t); sent != nil {
		t.Fatalf("something was dispatched for an unapproved patch: %v", sent)
	}
}

// The model must not be able to say "install everything" implicitly: passing no
// identifiers would reach ClaimApprovedPatches' NULL branch, which claims every
// approved patch on the device. That is a decision, not a default.
func TestInstallPatchesRequiresExplicitIdentifiersIntegration(t *testing.T) {
	f := newPatchFixture(t)

	res := Execute(f.ctx, f.tcWithPatches(), "install_patches", json.RawMessage(`{"agent_id":"`+f.mineAgent+`"}`))
	if !strings.Contains(res.Output, "identifiers is required") {
		t.Fatalf("an implicit install-everything was accepted: %s", res.Output)
	}
	if sent := f.dispatched(t); sent != nil {
		t.Fatalf("an argument-less install still dispatched %v", sent)
	}
}

// Scope applies here as everywhere: a device belonging to another tenant is
// refused before the patch service is even consulted.
func TestInstallPatchesRefusesAnOutOfScopeDeviceIntegration(t *testing.T) {
	f := newPatchFixture(t)

	res := Execute(f.ctx, f.tcWithPatches(), "install_patches", installArgs(f.theirAgent, "openssl"))
	if !strings.Contains(res.Output, "outside your access scope") {
		t.Fatalf("install_patches reached an out-of-scope device: %s", res.Output)
	}
}

// An offline device is a dispatch failure, not a silent success, and the claim
// is returned so the patch is not left stuck in 'installing' forever.
func TestInstallPatchesOnAnOfflineDeviceRequeuesTheClaimIntegration(t *testing.T) {
	f := newProposalFixture(t)
	if err := f.st.UpsertPatchReport(f.ctx, f.mineAgent, &protocol.PatchReportMsg{
		OS:      "linux",
		Patches: []protocol.PatchItem{{Identifier: "openssl-" + uuid.NewString(), Title: "OpenSSL", Classification: protocol.PatchSecurity}},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.ListAgentPatches(f.ctx, f.mineAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetApproval(f.ctx, rows[0].PatchID, models.ScopeAgent, f.mineAgent, "approve", "manual", "operator"); err != nil {
		t.Fatal(err)
	}

	tc := f.tc()
	tc.Patches = patches.New(f.st, hub.New()) // nobody connected
	res := Execute(f.ctx, tc, "install_patches", installArgs(f.mineAgent, rows[0].Identifier))
	if !strings.Contains(res.Output, `"error"`) {
		t.Fatalf("an offline dispatch reported success: %s", res.Output)
	}
	after, err := f.st.ListAgentPatches(f.ctx, f.mineAgent)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Status != models.PatchStatusApproved {
		t.Errorf("the claim was not returned: status = %q, want approved", after[0].Status)
	}
}
