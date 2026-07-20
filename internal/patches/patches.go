// Package patches dispatches patch scan/install/reboot messages to agents and
// records their reports/results. It is the server-side counterpart of the
// agent's patch scanner, and is shared by the API and the scheduler so the
// install guardrail (only approved patches are dispatched) lives in one place.
package patches

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

var (
	// ErrOffline is returned when a dispatch targets an agent that isn't connected.
	ErrOffline = errors.New("agent is offline")
	// ErrNoApproved is returned when none of the requested patches are currently
	// approved for the endpoint. This is intentionally not treated as "install all".
	ErrNoApproved = errors.New("no approved patches available for this device")
)

// Service brokers patch messages between the server and agents.
type Service struct {
	store *store.Store
	hub   *hub.Hub
}

// New builds the patch service.
func New(s *store.Store, h *hub.Hub) *Service { return &Service{store: s, hub: h} }

func (s *Service) send(agentID, typ string, payload any) bool {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	return s.hub.Send(agentID, frame)
}

// DispatchScan asks an online agent to scan for missing patches.
func (s *Service) DispatchScan(agentID string) bool {
	return s.send(agentID, protocol.MsgPatchScan, protocol.PatchScanMsg{})
}

// DispatchInstall claims approved patches before delivery and always sends their
// explicit identifiers. This prevents unapproved patches and duplicate requests
// from reaching an endpoint package manager.
func (s *Service) DispatchInstall(ctx context.Context, agentID string, identifiers []string) error {
	claimed, err := s.store.ClaimApprovedPatches(ctx, agentID, identifiers)
	if err != nil {
		return err
	}
	if len(claimed) == 0 {
		return ErrNoApproved
	}
	if !s.send(agentID, protocol.MsgPatchInstall, protocol.PatchInstallMsg{
		RequestID: uuid.NewString(), Identifiers: claimed,
	}) {
		_ = s.store.RequeueClaimedPatches(ctx, agentID, claimed)
		return ErrOffline
	}
	return nil
}

// DispatchReboot reboots an online agent and clears its pending-reboot flag.
func (s *Service) DispatchReboot(ctx context.Context, agentID string, delaySeconds int) error {
	if !s.send(agentID, protocol.MsgReboot, protocol.RebootMsg{DelaySeconds: delaySeconds}) {
		return ErrOffline
	}
	return s.store.SetRebootPending(ctx, agentID, false)
}

// OnReport persists an agent's missing-patch report.
func (s *Service) OnReport(ctx context.Context, agentID string, rep *protocol.PatchReportMsg) error {
	return s.store.UpsertPatchReport(ctx, agentID, rep)
}

// OnResult records an install run's outcome.
func (s *Service) OnResult(ctx context.Context, agentID string, res *protocol.PatchResultMsg) error {
	return s.store.ApplyPatchResult(ctx, agentID, res)
}
