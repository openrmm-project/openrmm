package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// jobOutcomeWindow bounds how far back job_failure looks for each task's or
// script's latest run.
const jobOutcomeWindow = 30 * 24 * time.Hour

// ruleResolver returns the server rule of ruleType in force for a device: its
// governing check, or the fleet default. A read failure leaves the configuration
// unknown: it must neither invent an incident nor declare one recovered.
func (s *Scheduler) ruleResolver(ctx context.Context, ruleType string) func(agentID string) models.GoverningRule {
	fallback := models.GoverningRule{Type: ruleType, Config: builtInDefault(ruleType)}
	unknown := func(string) models.GoverningRule { return models.GoverningRule{Type: ruleType} }
	if defaults, err := s.store.AlertRuleDefaults(ctx); err != nil {
		s.recordError("read alert rule defaults", err)
		return unknown
	} else if d, ok := defaults[ruleType]; ok {
		fallback.Config = d.Config
	}
	governing, err := s.store.GoverningRules(ctx, ruleType)
	if err != nil {
		s.recordError("read "+ruleType+" rules", err)
		return unknown
	}
	return func(agentID string) models.GoverningRule {
		if r, ok := governing[agentID]; ok {
			return r
		}
		return fallback
	}
}

// builtInDefault is migration 028's seed, for when the defaults table cannot be
// read: connectivity alerts as it always did, the rest stay off.
func builtInDefault(ruleType string) models.ServerRuleConfig {
	if ruleType == models.CheckConnectivity {
		return models.ServerRuleConfig{Mode: models.RuleModeAlert, Severity: models.SeverityError, AfterMin: 3}
	}
	cfg, _ := models.ParseServerRuleConfig(ruleType, []byte(`{"mode":"ignore"}`))
	return cfg
}

func ruleID(r models.GoverningRule) *string {
	if r.CheckID == "" {
		return nil
	}
	id := r.CheckID
	return &id
}

func ruleLabel(r models.GoverningRule) string {
	switch {
	case r.CheckID == "":
		return "fleet default"
	case r.PolicyName != "":
		return fmt.Sprintf("rule %q of policy %q (%s)", r.Name, r.PolicyName, r.ScopeType)
	default:
		return fmt.Sprintf("rule %q", r.Name)
	}
}

// ruleCondition is one alert a server rule wants open.
type ruleCondition struct {
	rule        models.GoverningRule
	message     string
	observation string // identity of the state, so re-evaluating it adds no occurrence
}

type conditionKey struct{ agentID, key string }

// evaluateServerRules opens and closes the alerts of the server rules other
// than connectivity (which runs every tick in evaluateOffline): failed jobs,
// failed updates, pending reboots and outdated agents.
func (s *Scheduler) evaluateServerRules(ctx context.Context, agents []models.Agent, now time.Time) {
	jobRule := s.ruleResolver(ctx, models.CheckJobFailure)
	patchRule := s.ruleResolver(ctx, models.CheckPatchFailure)
	rebootRule := s.ruleResolver(ctx, models.CheckRebootPending)
	outdatedRule := s.ruleResolver(ctx, models.CheckAgentOutdated)

	want := map[conditionKey]ruleCondition{}
	// Job alerts close only on a later successful run of the same task or
	// script (or when no rule wants them): a failed run older than the window
	// is not proof the task recovered.
	jobHealthy := map[conditionKey]bool{}
	evaluated := map[string]bool{}
	unknownVersion := map[string]bool{}
	hosts := map[string]string{}
	live := map[string]bool{}
	for i := range agents {
		a := &agents[i]
		if a.Status == models.AgentRevoked {
			continue
		}
		live[a.ID], hosts[a.ID] = true, a.Hostname
	}

	if failed, err := s.store.FailedPatchCounts(ctx); err != nil {
		s.recordError("read failed updates", err)
	} else {
		evaluated[models.CheckPatchFailure] = true
		for id, n := range failed {
			if r := patchRule(id); live[id] && r.Config.Alerts() && n > 0 {
				want[conditionKey{id, models.CheckPatchFailure}] = ruleCondition{rule: r,
					message:     fmt.Sprintf("%d update(s) failed to install on %s.", n, hosts[id]),
					observation: fmt.Sprintf("patch:%d", n)}
			}
		}
	}
	if pending, err := s.store.RebootPendingSince(ctx); err != nil {
		s.recordError("read pending reboots", err)
	} else {
		evaluated[models.CheckRebootPending] = true
		for id, since := range pending {
			r := rebootRule(id)
			if live[id] && r.Config.Alerts() && now.Sub(since) >= time.Duration(r.Config.AfterDays)*24*time.Hour {
				want[conditionKey{id, models.CheckRebootPending}] = ruleCondition{rule: r,
					message:     fmt.Sprintf("A reboot has been pending on %s since %s.", hosts[id], since.UTC().Format("2006-01-02 15:04 UTC")),
					observation: "reboot:" + since.UTC().Format(time.RFC3339)}
			}
		}
	}
	if server, ok := parseVersion(s.serverVersion); ok {
		evaluated[models.CheckAgentOutdated] = true
		for i := range agents {
			a := &agents[i]
			r := outdatedRule(a.ID)
			if !live[a.ID] || !r.Config.Alerts() {
				continue
			}
			v, ok := parseVersion(a.Version)
			if !ok {
				unknownVersion[a.ID] = true
				continue
			}
			if behind(v, server, r.Config.Behind) {
				want[conditionKey{a.ID, models.CheckAgentOutdated}] = ruleCondition{rule: r,
					message:     fmt.Sprintf("Agent %s runs %s, behind the server's %s.", a.Hostname, a.Version, s.serverVersion),
					observation: "version:" + a.Version}
			}
		}
	}
	if outcomes, err := s.store.LatestJobOutcomes(ctx, now.Add(-jobOutcomeWindow)); err != nil {
		s.recordError("read job outcomes", err)
	} else {
		evaluated[models.CheckJobFailure] = true
		for _, o := range outcomes {
			r := jobRule(o.AgentID)
			k := conditionKey{o.AgentID, "job:" + o.Key}
			if !live[o.AgentID] {
				continue
			}
			counted := r.Config.Alerts() && (r.Config.Scope == "all" || o.FromTask)
			if counted && o.Failed {
				what := o.Description
				if what == "" {
					what = o.Key
				}
				want[k] = ruleCondition{rule: r,
					message:     fmt.Sprintf("%s failed on %s.", what, hosts[o.AgentID]),
					observation: "job:" + o.JobID}
			} else if !o.Failed {
				jobHealthy[k] = true
			}
		}
	}

	for k, c := range want {
		agentID := k.agentID
		transition, err := s.store.ObserveAlert(ctx, store.AlertObservation{
			AgentID: &agentID, SourceType: models.AlertSourceRule, SourceID: &agentID,
			ConditionKey: k.key, Severity: c.rule.Config.Severity, Message: c.message,
			Cooldown: 5 * time.Minute, ObservedAt: now, RuleID: ruleID(c.rule),
			ObservationID: k.key + ":" + c.observation,
		})
		if err != nil {
			s.recordError("raise "+k.key+" alert for "+agentID, err)
			continue
		}
		if transition.Notify {
			if err := s.queueAlertNotification(ctx, &transition.Alert, c.message); err != nil {
				s.recordError("enqueue "+k.key+" alert "+agentID, err)
			}
		}
	}

	open, err := s.store.OpenRuleAlerts(ctx)
	if err != nil {
		s.recordError("read open rule alerts", err)
		return
	}
	for i := range open {
		a := &open[i]
		if a.AgentID == nil {
			continue
		}
		k := conditionKey{*a.AgentID, a.ConditionKey}
		if _, still := want[k]; still {
			continue
		}
		var r models.GoverningRule
		var ruleType string
		switch {
		case strings.HasPrefix(a.ConditionKey, "job:"):
			ruleType = models.CheckJobFailure
			r = jobRule(k.agentID)
			if live[k.agentID] && r.Config.Alerts() && !jobHealthy[k] && (r.Config.Scope == "all" || strings.HasPrefix(a.ConditionKey, "job:task:")) {
				continue // no later success seen: keep it open
			}
		case a.ConditionKey == models.CheckPatchFailure:
			ruleType = models.CheckPatchFailure
			r = patchRule(k.agentID)
		case a.ConditionKey == models.CheckRebootPending:
			ruleType = models.CheckRebootPending
			r = rebootRule(k.agentID)
		case a.ConditionKey == models.CheckAgentOutdated:
			ruleType = models.CheckAgentOutdated
			r = outdatedRule(k.agentID)
		default:
			continue
		}
		if r.Config.Mode == "" {
			continue // unavailable rule data is not evidence of recovery
		}
		if r.Config.Alerts() && live[k.agentID] && (!evaluated[ruleType] || (ruleType == models.CheckAgentOutdated && unknownVersion[k.agentID])) {
			continue
		}
		kind, note := "automatic", "The condition cleared."
		if !r.Config.Alerts() || !live[k.agentID] {
			kind, note = "inapplicable", "No rule in force alerts on this any more: "+ruleLabel(r)+"."
		}
		resolved, err := s.store.ResolveAlertCondition(ctx, models.AlertSourceRule, a.AgentID, a.AgentID,
			a.ConditionKey, "system", kind, note, now)
		if err != nil {
			s.recordError("resolve "+a.ConditionKey+" alert for "+k.agentID, err)
			continue
		}
		if len(resolved) > 0 {
			if err := s.queueAlertNotification(ctx, &resolved[0], note); err != nil {
				s.recordError("enqueue "+a.ConditionKey+" recovery "+k.agentID, err)
			}
		}
	}
}

// parseVersion reads "major.minor.patch", ignoring a "v" prefix and any
// pre-release or build suffix. A dev build has none.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// behind reports whether agent is behind server by at least the given level:
// a major version, a minor version, or any release.
func behind(agent, server [3]int, level string) bool {
	switch level {
	case "major":
		return agent[0] < server[0]
	case "minor":
		return agent[0] < server[0] || (agent[0] == server[0] && agent[1] < server[1])
	default:
		for i := 0; i < 3; i++ {
			if agent[i] != server[i] {
				return agent[i] < server[i]
			}
		}
		return false
	}
}
