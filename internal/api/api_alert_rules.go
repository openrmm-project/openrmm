package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func (s *Server) listAlertDefaults(w http.ResponseWriter, r *http.Request) {
	defaults, err := s.store.AlertRuleDefaults(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": defaults, "can_manage": s.scopeUserID(r) == "" && hasPermission(r, models.PermChecksManage)})
}

func (s *Server) updateAlertDefault(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "fleet alert defaults") {
		return
	}
	kind := chi.URLParam(r, "type")
	if !models.IsServerRule(kind) {
		writeErr(w, http.StatusNotFound, "unknown server rule type")
		return
	}
	var raw json.RawMessage
	if err := decode(r, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid config")
		return
	}
	cfg, err := models.ParseServerRuleConfig(kind, raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetAlertRuleDefault(r.Context(), kind, cfg, actorOf(r)); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"type": kind, "config": cfg})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert_default.update", "alert_rule_defaults", detail)
	writeJSON(w, http.StatusOK, models.AlertRuleDefault{Type: kind, Config: cfg, UpdatedBy: actorOf(r)})
}

type ruleOverride struct {
	CheckID   string `json:"check_id"`
	Name      string `json:"name"`
	ScopeType string `json:"scope_type"`
	Devices   int    `json:"devices"`
}

func (s *Server) checkOverrides(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "fleet rule coverage") {
		return
	}
	c, ok := s.requireCheck(w, r, chi.URLParam(r, "id"), false)
	if !ok {
		return
	}
	if !models.IsServerRule(c.Type) {
		writeJSON(w, http.StatusOK, map[string]any{"covered": 0, "overrides": []ruleOverride{}})
		return
	}
	covered, err := s.store.CheckCoveredAgents(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	governing, err := s.store.GoverningRules(r.Context(), c.Type)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	groups := map[string]*ruleOverride{}
	for _, agentID := range covered {
		rule := governing[agentID]
		if rule.CheckID == c.ID {
			continue
		}
		if groups[rule.CheckID] == nil {
			scope := rule.ScopeType
			if scope == "" {
				scope = "fleet"
			}
			groups[rule.CheckID] = &ruleOverride{CheckID: rule.CheckID, Name: rule.Name, ScopeType: scope}
		}
		groups[rule.CheckID].Devices++
	}
	out := []ruleOverride{}
	for _, group := range groups {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CheckID < out[j].CheckID })
	writeJSON(w, http.StatusOK, map[string]any{"covered": len(covered), "overrides": out})
}

// Preferences expose rule labels only, never scripts or check configuration.
// Delivery separately rechecks alerts.view and the incident's immutable scope.
type notificationRuleChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Server) notificationRuleChoices(r *http.Request) ([]notificationRuleChoice, error) {
	out := []notificationRuleChoice{}
	if !hasPermission(r, models.PermAlertsView) {
		return out, nil
	}
	checks, err := s.store.ListChecks(r.Context(), "", "")
	if err != nil {
		return nil, err
	}
	visible, scoped, err := s.scopedAgentSet(r)
	if err != nil {
		return nil, err
	}
	policies := map[string]bool{}
	for _, c := range checks {
		allowed := !scoped || inScope(visible, c.AgentID)
		if !allowed && c.PolicyID != nil {
			var known bool
			allowed, known = policies[*c.PolicyID]
			if !known {
				allowed, err = s.store.PolicyAllowedForUser(r.Context(), s.scopeUserID(r), *c.PolicyID)
				if err != nil {
					return nil, err
				}
				policies[*c.PolicyID] = allowed
			}
		}
		if allowed {
			out = append(out, notificationRuleChoice{ID: c.ID, Name: c.Name, Type: c.Type})
		}
	}
	return out, nil
}

func validateNotificationRules(f *store.NotificationRuleFilter, choices []notificationRuleChoice) error {
	if f.Mode != "all" && f.Mode != "selected" {
		return errors.New("rules.mode must be all or selected")
	}
	if len(f.CheckIDs) > 500 {
		return errors.New("at most 500 rules may be selected")
	}
	allowed := map[string]bool{}
	for _, c := range choices {
		allowed[c.ID] = true
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range f.CheckIDs {
		if _, err := uuid.Parse(id); err != nil || !allowed[id] {
			return errors.New("a selected rule is unavailable or outside your access scope")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	f.CheckIDs = ids
	return nil
}

func (s *Server) auditServerRule(r *http.Request, check *models.Check, action string) {
	if !models.IsServerRule(check.Type) {
		return
	}
	detail, _ := json.Marshal(map[string]any{"id": check.ID, "type": check.Type, "config": check.Config})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert_rule."+action, "checks", detail)
}
