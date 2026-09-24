package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// Moving and deleting devices in bulk, from the console and — through an
// approved proposal — from the AI or an MCP client.
//
// Both paths run the same code below, with the caller's permissions and access
// scope. For a proposal that caller is the person approving it: what the AI
// asked for is re-validated as if that person had asked, at the moment they
// approve, so a proposal can never do more than its approver could do by hand.

// bulkError is a refusal with the status to report it under.
type bulkError struct {
	status int
	msg    string
}

func (e *bulkError) Error() string { return e.msg }

func bulkErr(status int, format string, args ...any) error {
	return &bulkError{status: status, msg: fmt.Sprintf(format, args...)}
}

func writeBulkErr(w http.ResponseWriter, err error) {
	var be *bulkError
	if errors.As(err, &be) {
		writeErr(w, be.status, be.msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// bulkFailure is one device a bulk delete could not remove.
type bulkFailure struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname,omitempty"`
	Error    string `json:"error"`
}

// normalizeAgentIDs validates and de-duplicates a list of device ids.
func normalizeAgentIDs(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, bulkErr(http.StatusBadRequest, "invalid device id %q", raw)
		}
		id = parsed.String()
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, bulkErr(http.StatusBadRequest, "no devices given")
	}
	if len(out) > store.MaxBulkDevices {
		return nil, bulkErr(http.StatusBadRequest, "%d devices in one operation; the limit is %d", len(out), store.MaxBulkDevices)
	}
	return out, nil
}

// bulkAgents checks that every device exists and is within the caller's scope,
// and returns their hostnames. Nothing is changed unless all of them pass.
func (s *Server) bulkAgents(r *http.Request, ids []string) (map[string]string, error) {
	hosts, err := s.store.AgentHostnames(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	visible, scoped, err := s.scopedAgentSet(r)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if scoped && !visible[id] {
			// Same answer whether it exists or not: a scoped caller must not be
			// able to probe for devices outside its scope.
			return nil, bulkErr(http.StatusForbidden, "device %s is outside your access scope", id)
		}
		if _, ok := hosts[id]; !ok {
			return nil, bulkErr(http.StatusNotFound, "device %s no longer exists", id)
		}
	}
	return hosts, nil
}

// checkScopeRef is requireScope without writing a response.
func (s *Server) checkScopeRef(r *http.Request, scopeType, scopeID string) error {
	if !validScopeType(scopeType) || scopeID == "" {
		return bulkErr(http.StatusBadRequest, "invalid %s reference", scopeType)
	}
	if _, err := uuid.Parse(scopeID); err != nil {
		return bulkErr(http.StatusBadRequest, "invalid %s id %q", scopeType, scopeID)
	}
	if uid := s.scopeUserID(r); uid != "" {
		ok, err := s.store.ScopeAllowedForUser(r.Context(), uid, scopeType, scopeID)
		if err != nil {
			return err
		}
		if !ok {
			return bulkErr(http.StatusForbidden, "that %s is outside your access scope", scopeType)
		}
		return nil
	}
	ok, err := s.store.ScopeExists(r.Context(), scopeType, scopeID)
	if err != nil {
		return err
	}
	if !ok {
		return bulkErr(http.StatusNotFound, "%s %s not found", scopeType, scopeID)
	}
	return nil
}

// validateMove checks a move completely, changing nothing.
func (s *Server) validateMove(r *http.Request, rawIDs []string, siteID string, groupID *string) ([]string, map[string]string, error) {
	ids, err := normalizeAgentIDs(rawIDs)
	if err != nil {
		return nil, nil, err
	}
	hosts, err := s.bulkAgents(r, ids)
	if err != nil {
		return nil, nil, err
	}
	if siteID == "" {
		return nil, nil, bulkErr(http.StatusBadRequest, "a destination site is required")
	}
	if err := s.checkScopeRef(r, models.ScopeSite, siteID); err != nil {
		return nil, nil, err
	}
	if groupID != nil && *groupID != "" {
		if err := s.checkScopeRef(r, models.ScopeGroup, *groupID); err != nil {
			return nil, nil, err
		}
	}
	return ids, hosts, nil
}

// moveDevices moves them all or none, and audits each device.
func (s *Server) moveDevices(r *http.Request, rawIDs []string, siteID string, groupID *string, via map[string]string) (int, error) {
	ids, hosts, err := s.validateMove(r, rawIDs, siteID, groupID)
	if err != nil {
		return 0, err
	}
	if err := s.store.MoveAgents(r.Context(), ids, siteID, groupID); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return 0, bulkErr(http.StatusNotFound, "a device or the department no longer exists; nothing was moved")
		case errors.Is(err, store.ErrConflict):
			return 0, bulkErr(http.StatusConflict, "the department belongs to another site; nothing was moved")
		}
		return 0, err
	}
	for _, id := range ids {
		detail := map[string]string{"hostname": hosts[id], "site_id": siteID, "bulk": fmt.Sprint(len(ids))}
		if groupID != nil && *groupID != "" {
			detail["group_id"] = *groupID
		}
		for k, v := range via {
			detail[k] = v
		}
		_ = s.store.AddAudit(r.Context(), actorOf(r), "device.move", id, auditDetail(detail))
	}
	return len(ids), nil
}

// validateDelete checks a delete completely, changing nothing.
func (s *Server) validateDelete(r *http.Request, rawIDs []string, confirmCount int) ([]string, map[string]string, error) {
	ids, err := normalizeAgentIDs(rawIDs)
	if err != nil {
		return nil, nil, err
	}
	// The count the person typed must be the count about to be deleted. It
	// catches the person who confirmed without reading, and equally a client
	// that sends more devices than the dialog showed.
	if confirmCount != len(ids) {
		return nil, nil, bulkErr(http.StatusBadRequest, "confirmation does not match: %d devices would be deleted, %d confirmed", len(ids), confirmCount)
	}
	hosts, err := s.bulkAgents(r, ids)
	if err != nil {
		return nil, nil, err
	}
	return ids, hosts, nil
}

// deleteDevices deletes each device on its own: a deletion also tears down the
// device's live connections, which no database transaction can roll back, so
// one failure is reported rather than undoing the others.
func (s *Server) deleteDevices(r *http.Request, rawIDs []string, confirmCount int, via map[string]string) (int, []bulkFailure, error) {
	ids, hosts, err := s.validateDelete(r, rawIDs, confirmCount)
	if err != nil {
		return 0, nil, err
	}
	deleted := 0
	failed := []bulkFailure{}
	for _, id := range ids {
		if err := s.deleteOneDevice(r, id, hosts[id], via); err != nil {
			log.Printf("bulk delete %s: %v", id, err)
			failed = append(failed, bulkFailure{AgentID: id, Hostname: hosts[id], Error: "could not be deleted"})
			continue
		}
		deleted++
	}
	return deleted, failed, nil
}

// deleteOneDevice is the single-device delete, shared by every path.
func (s *Server) deleteOneDevice(r *http.Request, id, hostname string, via map[string]string) error {
	unlock := s.lockAgentLifecycle(id)
	defer unlock()
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		return err
	}
	s.closeRetiredDeviceMonitoring(r, id, "deleted")
	// The durable delete succeeds before the in-memory relay is permanently
	// fenced. A failed database operation must not leave an active device blocked.
	s.terminateAgentTransport(r.Context(), id)
	detail := map[string]string{"hostname": hostname}
	for k, v := range via {
		detail[k] = v
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.delete", id, auditDetail(detail))
	return nil
}

// ---------- console ----------

func (s *Server) bulkMoveDevices(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentIDs []string `json:"agent_ids"`
		SiteID   string   `json:"site_id"`
		GroupID  *string  `json:"group_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	moved, err := s.moveDevices(r, in.AgentIDs, in.SiteID, in.GroupID, nil)
	if err != nil {
		writeBulkErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"moved": moved})
}

func (s *Server) bulkDeleteDevices(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentIDs     []string `json:"agent_ids"`
		ConfirmCount int      `json:"confirm_count"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	deleted, failed, err := s.deleteDevices(r, in.AgentIDs, in.ConfirmCount, nil)
	if err != nil {
		writeBulkErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "failed": failed})
}

// ---------- proposals ----------

// opVisible reports whether every device a proposal names is within the
// caller's scope. A scoped person neither sees nor decides proposals that reach
// outside it.
func (s *Server) opVisible(r *http.Request, op *models.DeviceOperation) (bool, error) {
	visible, scoped, err := s.scopedAgentSet(r)
	if err != nil || !scoped {
		return err == nil, err
	}
	for _, id := range op.AgentIDs {
		if !visible[id] {
			return false, nil
		}
	}
	return true, nil
}

func (s *Server) listDeviceOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := s.store.ListDeviceOperations(r.Context(), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]models.DeviceOperation, 0, len(ops))
	for i := range ops {
		ok, err := s.opVisible(r, &ops[i])
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			out = append(out, ops[i])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

// loadDecidableOp fetches a proposal the caller may decide, writing the
// refusal itself when they may not.
func (s *Server) loadDecidableOp(w http.ResponseWriter, r *http.Request) *models.DeviceOperation {
	op, err := s.store.GetDeviceOperation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), "operation not found")
		return nil
	}
	if ok, err := s.opVisible(r, op); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil
	} else if !ok {
		writeErr(w, http.StatusNotFound, "operation not found")
		return nil
	}
	if op.Status != "awaiting_approval" {
		writeErr(w, http.StatusConflict, "this operation was already "+op.Status)
		return nil
	}
	// Deciding a deletion takes the right to delete devices; deciding a move,
	// the right to reorganise them. Holding either one is not enough for both.
	need := models.PermClientsManage
	if op.Kind == models.DeviceOpDelete {
		need = models.PermDevicesDelete
	}
	if !hasPermission(r, need) {
		writeErr(w, http.StatusForbidden, "deciding this operation requires the "+need+" permission")
		return nil
	}
	return op
}

func (s *Server) approveDeviceOperation(w http.ResponseWriter, r *http.Request) {
	op := s.loadDecidableOp(w, r)
	if op == nil {
		return
	}
	var in struct {
		ConfirmCount int `json:"confirm_count"`
	}
	_ = decode(r, &in)

	// Everything is checked as the approver before the proposal is claimed, so
	// a refusal leaves it waiting for someone who is allowed to approve it.
	var err error
	if op.Kind == models.DeviceOpMove {
		_, _, err = s.validateMove(r, op.AgentIDs, deref(op.SiteID), op.GroupID)
	} else {
		_, _, err = s.validateDelete(r, op.AgentIDs, in.ConfirmCount)
	}
	if err != nil {
		writeBulkErr(w, err)
		return
	}
	claimed, err := s.store.ClaimDeviceOperation(r.Context(), op.ID, actorOf(r))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "this operation was decided by someone else in the meantime")
			return
		}
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	via := map[string]string{"operation": claimed.ID, "proposed_by": claimed.ProposedBy}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device_operation.approve", claimed.ID,
		auditDetail(map[string]string{"kind": claimed.Kind, "devices": fmt.Sprint(len(claimed.AgentIDs)), "proposed_by": claimed.ProposedBy}))

	if claimed.Kind == models.DeviceOpMove {
		moved, err := s.moveDevices(r, claimed.AgentIDs, deref(claimed.SiteID), claimed.GroupID, via)
		if err != nil {
			_ = s.store.FinishDeviceOperation(r.Context(), claimed.ID, "failed", map[string]string{"error": err.Error()})
			writeBulkErr(w, err)
			return
		}
		_ = s.store.FinishDeviceOperation(r.Context(), claimed.ID, "completed", map[string]int{"moved": moved})
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "moved": moved})
		return
	}
	deleted, failed, err := s.deleteDevices(r, claimed.AgentIDs, len(claimed.AgentIDs), via)
	if err != nil {
		_ = s.store.FinishDeviceOperation(r.Context(), claimed.ID, "failed", map[string]string{"error": err.Error()})
		writeBulkErr(w, err)
		return
	}
	status := "completed"
	if len(failed) > 0 {
		status = "failed"
	}
	_ = s.store.FinishDeviceOperation(r.Context(), claimed.ID, status, map[string]any{"deleted": deleted, "failed": failed})
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "deleted": deleted, "failed": failed})
}

func (s *Server) rejectDeviceOperation(w http.ResponseWriter, r *http.Request) {
	op := s.loadDecidableOp(w, r)
	if op == nil {
		return
	}
	if err := s.store.RejectDeviceOperation(r.Context(), op.ID, actorOf(r)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "this operation was decided by someone else in the meantime")
			return
		}
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device_operation.reject", op.ID,
		auditDetail(map[string]string{"kind": op.Kind, "devices": fmt.Sprint(len(op.AgentIDs)), "proposed_by": op.ProposedBy}))
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
