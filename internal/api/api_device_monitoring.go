package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// The checks and tasks that apply to one device, for its detail drawer.
//
// GET /api/checks?agent_id= answers a different question: the checks assigned
// to the device itself. Most monitoring arrives through a policy on the
// device's department, site or client, so that list was empty on machines that
// were in fact being watched. These resolve the set the scheduler runs —
// ChecksForAgent and TasksForAgent, the same queries — and add what happened
// on this device: each check's state here, each task's last run here.
//
// The scheduler runs only enabled rows, so these list only enabled rows: a
// disabled check, or an AI proposal nobody has reviewed, is not applied to the
// machine, and the Checks and Scheduled tasks screens are where to find those.

// deviceCheck is a check as it stands on one device.
type deviceCheck struct {
	models.Check
	// PolicyName names the policy the check came through; empty when it is
	// assigned to the device directly.
	PolicyName string `json:"policy_name,omitempty"`
	// State is this device's: never-run checks read unknown.
	Status       string     `json:"status"`
	Value        *float64   `json:"value,omitempty"`
	Output       string     `json:"output"`
	LastResultAt *time.Time `json:"last_result_at,omitempty"`
}

// deviceTask is a task as it stands on one device.
type deviceTask struct {
	models.Task
	PolicyName string `json:"policy_name,omitempty"`
	// LastRunAt is when the task last queued a job on this device.
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
}

func (s *Server) listDeviceChecks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	ctx := r.Context()
	siteID, clientID, err := s.store.AgentScope(ctx, id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	checks, err := s.store.ChecksForAgent(ctx, id, siteID, clientID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	states := make(map[string]*models.CheckState, len(checks))
	for _, c := range checks {
		st, err := s.store.GetCheckState(ctx, c.ID, id)
		if errors.Is(err, store.ErrNotFound) {
			continue // dispatched to nothing yet: unknown
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		states[c.ID] = st
	}
	names, err := s.policyNames(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": deviceChecks(checks, states, names)})
}

func (s *Server) listDeviceTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	ctx := r.Context()
	siteID, clientID, err := s.store.AgentScope(ctx, id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	tasks, err := s.store.TasksForAgent(ctx, id, siteID, clientID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	lastRuns := make(map[string]time.Time, len(tasks))
	for _, t := range tasks {
		at, err := s.store.LastTaskRunTime(ctx, t.ID, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		lastRuns[t.ID] = at
	}
	names, err := s.policyNames(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": deviceTasks(tasks, lastRuns, names)})
}

// policyNames maps policy id to name. The names are those of policies already
// applied to a device the caller may see, so no scope filter narrows them.
func (s *Server) policyNames(ctx context.Context) (map[string]string, error) {
	policies, err := s.store.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(policies))
	for _, p := range policies {
		names[p.ID] = p.Name
	}
	return names, nil
}

// deviceChecks pairs each check with this device's state and the policy it
// came through. Direct assignments first, then by policy, then by name, so the
// drawer reads the same on every refresh.
func deviceChecks(checks []models.Check, states map[string]*models.CheckState, policies map[string]string) []deviceCheck {
	out := make([]deviceCheck, 0, len(checks))
	for _, c := range checks {
		row := deviceCheck{Check: c, Status: models.CheckUnknown}
		if c.PolicyID != nil {
			row.PolicyName = policies[*c.PolicyID]
		}
		if st := states[c.ID]; st != nil {
			if st.Status != "" {
				row.Status = st.Status
			}
			row.Value, row.Output, row.LastResultAt = st.LastValue, st.LastOutput, st.LastResultAt
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return lessBySource(out[i].PolicyID, out[i].PolicyName, out[i].Name, out[j].PolicyID, out[j].PolicyName, out[j].Name)
	})
	return out
}

// deviceTasks pairs each task with its last run on this device; a zero time
// means it has not run here.
func deviceTasks(tasks []models.Task, lastRuns map[string]time.Time, policies map[string]string) []deviceTask {
	out := make([]deviceTask, 0, len(tasks))
	for _, t := range tasks {
		row := deviceTask{Task: t}
		if t.PolicyID != nil {
			row.PolicyName = policies[*t.PolicyID]
		}
		if at := lastRuns[t.ID]; !at.IsZero() {
			at := at
			row.LastRunAt = &at
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return lessBySource(out[i].PolicyID, out[i].PolicyName, out[i].Name, out[j].PolicyID, out[j].PolicyName, out[j].Name)
	})
	return out
}

func lessBySource(aPolicy *string, aPolicyName, aName string, bPolicy *string, bPolicyName, bName string) bool {
	if (aPolicy == nil) != (bPolicy == nil) {
		return aPolicy == nil
	}
	if aPolicyName != bPolicyName {
		return aPolicyName < bPolicyName
	}
	return aName < bName
}
