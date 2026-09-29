package store

import (
	"context"
	"time"
)

// ===================== Dashboard =====================
//
// The dashboard needs a few numbers and a short list, not the fleet: counting
// in the console meant downloading every device and the latest jobs with their
// scripts and output, and a count taken over "the latest 100 jobs" misses an
// older job still waiting for approval.

// agentVisible is true for the agent aliased "a" when the user in $1 may see
// it: every agent for a NULL user (an unscoped principal) or a user without
// scopes, otherwise the agents under one of the user's scopes.
const agentVisible = `($1::uuid IS NULL
	OR NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
	OR EXISTS (
	  SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
	    (us.scope_type='agent'  AND us.scope_id=a.id) OR
	    (us.scope_type='group'  AND us.scope_id=a.group_id) OR
	    (us.scope_type='site'   AND us.scope_id=a.site_id) OR
	    (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
	  )))`

// scopeArg is the $1 of agentVisible: NULL for an unscoped principal.
func scopeArg(userID string) any {
	if userID == "" {
		return nil
	}
	return userID
}

// DeviceCounts is the fleet at a glance.
type DeviceCounts struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

// DashboardDeviceCounts counts the devices a user may see ("" = all).
func (s *Store) DashboardDeviceCounts(ctx context.Context, userID string) (DeviceCounts, error) {
	var c DeviceCounts
	err := s.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE a.status='online') FROM agents a WHERE `+agentVisible,
		scopeArg(userID)).Scan(&c.Total, &c.Online)
	return c, err
}

// JobCounts is the work that needs a person.
type JobCounts struct {
	Awaiting int `json:"awaiting"`
	Failed   int `json:"failed"` // since the moment asked for
}

// DashboardJobCounts counts, over the devices a user may see, every job waiting
// for approval — however old — and the jobs that failed since a moment.
func (s *Store) DashboardJobCounts(ctx context.Context, userID string, failedSince time.Time) (JobCounts, error) {
	var c JobCounts
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE j.status='awaiting_approval'),
		        count(*) FILTER (WHERE j.status='failed' AND j.updated_at > $2)
		 FROM jobs j JOIN agents a ON a.id=j.agent_id
		 WHERE j.status IN ('awaiting_approval','failed') AND `+agentVisible,
		scopeArg(userID), failedSince).Scan(&c.Awaiting, &c.Failed)
	return c, err
}

// JobSummary is a job as a list shows it: no script, no output.
type JobSummary struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// AwaitingJobSummaries lists the newest jobs waiting for approval on the
// devices a user may see; DashboardJobCounts has how many there are in all.
func (s *Store) AwaitingJobSummaries(ctx context.Context, userID string, limit int) ([]JobSummary, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	rows, err := s.pool.Query(ctx,
		`SELECT j.id, j.agent_id, a.hostname, a.os, j.description, j.created_by, j.created_at
		 FROM jobs j JOIN agents a ON a.id=j.agent_id
		 WHERE j.status='awaiting_approval' AND `+agentVisible+`
		 ORDER BY j.created_at DESC, j.id DESC LIMIT $2`,
		scopeArg(userID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobSummary{}
	for rows.Next() {
		var j JobSummary
		if err := rows.Scan(&j.ID, &j.AgentID, &j.Hostname, &j.OS, &j.Description, &j.CreatedBy, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
