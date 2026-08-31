package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/exports"
	"github.com/p1ngu/rmm/internal/store"
)

// Audit tools.
//
// Read-only, and deliberately so: there is no tool to purge or edit the trail.
// An assistant that could prune the record of what it did would make the record
// worth nothing.
//
// Two shapes, because "what happened last week" and "who approved that script"
// are different questions and answering both with rows is what wastes a context
// window and produces confident nonsense:
//
//   summary  counts, computed in the database. Thirty tokens, always exact.
//   entries  the rows themselves, capped, for when the model has to read them.
//
// And a third path that does not go through the model at all: export_audit
// hands back a signed URL. The data reaches the operator without a single row
// entering the conversation.

// maxAuditEntries caps rows returned to the model.
//
// Lower than the 1000 an inventory_report may return, and for a reason worth
// writing down: an audit entry is ~66 tokens, so a thousand of them is 66,000 —
// half a large context window. Worse, they are near-identical rows, and a model
// handed hundreds of those stops reading and starts sampling. It then answers
// "who approved that" plausibly and wrongly, which in an investigation is the
// worst possible outcome. If the answer needs more than this, it needs a
// narrower filter or the export.
const maxAuditEntries = 200

// defaultAuditWindow applies when the caller names no time range. Without it,
// the first question of every conversation sweeps the whole trail.
const defaultAuditWindow = 7 * 24 * time.Hour

func auditFilterFrom(in struct {
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Since  string `json:"since"`
	Until  string `json:"until"`
	Mode   string `json:"mode"`
}) (store.AuditFilter, error) {
	f := store.AuditFilter{
		Actor:  strings.TrimSpace(in.Actor),
		Action: strings.TrimSpace(in.Action),
		Target: strings.TrimSpace(in.Target),
	}
	parse := func(name, raw string) (time.Time, error) {
		if strings.TrimSpace(raw) == "" {
			return time.Time{}, nil
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp like 2026-08-01T00:00:00Z", name)
		}
		return t, nil
	}
	var err error
	if f.Since, err = parse("since", in.Since); err != nil {
		return f, err
	}
	if f.Until, err = parse("until", in.Until); err != nil {
		return f, err
	}
	if f.Since.IsZero() && f.Until.IsZero() {
		f.Since = time.Now().Add(-defaultAuditWindow)
	}
	return f, nil
}

// queryAudit answers with counts by default and rows only when asked.
func queryAudit(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Actor  string `json:"actor"`
		Action string `json:"action"`
		Target string `json:"target"`
		Since  string `json:"since"`
		Until  string `json:"until"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	// The global trail is not scoped, so a restricted principal is refused
	// outright — the same rule the REST handler applies.
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("the audit log is instance-wide; a scoped account cannot read it")
	}
	filter, err := auditFilterFrom(in)
	if err != nil {
		return nil, err
	}

	if in.Mode == "" || in.Mode == "summary" {
		return auditSummary(ctx, tc, filter)
	}
	if in.Mode != "entries" {
		return nil, fmt.Errorf("mode must be \"summary\" or \"entries\"")
	}

	filter.Limit = maxAuditEntries
	entries, total, err := tc.Store.ListAudit(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"total":    total,
		"returned": len(entries),
		"entries":  entries,
	}
	if total > len(entries) {
		out["truncated"] = true
		// Told to narrow rather than to page. Nine calls of 200 put the same
		// 1,800 rows in the window, one page at a time.
		out["note"] = fmt.Sprintf(
			"showing %d of %d. Narrow by actor, action or date rather than asking for more, "+
				"or call export_audit for the complete set as a CSV the operator downloads.",
			len(entries), total)
	}
	return out, nil
}

// auditSummary counts in the database. This is the answer to most questions
// asked of an audit log, it costs about thirty tokens, and it cannot be wrong
// the way a model summarising two hundred rows can.
func auditSummary(ctx context.Context, tc ToolContext, filter store.AuditFilter) (any, error) {
	byActor, byAction, total, err := tc.Store.AuditSummary(ctx, filter)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total":     total,
		"by_actor":  byActor,
		"by_action": byAction,
		"window": map[string]string{
			"since": timeOrEmpty(filter.Since),
			"until": timeOrEmpty(filter.Until),
		},
		"note": "counts computed in the database. Call again with mode=\"entries\" and a " +
			"narrower filter to see individual records.",
	}, nil
}

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// exportAudit hands back a link instead of rows.
//
// This is the path that should carry any "give me the data" request. The rows
// go from the database to the operator's disk without entering the model's
// context at all — no token cost, no truncation, and no chance of the file
// containing what the model remembered rather than what the database holds.
func exportAudit(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Actor  string `json:"actor"`
		Action string `json:"action"`
		Target string `json:"target"`
		Since  string `json:"since"`
		Until  string `json:"until"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("the audit log is instance-wide; a scoped account cannot read it")
	}
	if tc.Config == nil {
		return nil, fmt.Errorf("exports are not configured on this server")
	}
	filter, err := auditFilterFrom(in)
	if err != nil {
		return nil, err
	}

	// The count comes back so the model can say how large the export is without
	// fetching a single row of it.
	filter.Limit = 1
	_, total, err := tc.Store.ListAudit(ctx, filter)
	if err != nil {
		return nil, err
	}

	token, err := exports.Mint(tc.Config, exports.Claims{
		Kind: "audit",
		Params: map[string]string{
			"actor": filter.Actor, "action": filter.Action, "target": filter.Target,
			"since": timeOrEmpty(filter.Since), "until": timeOrEmpty(filter.Until),
		},
		User:  strings.TrimPrefix(strings.TrimPrefix(tc.Actor, "ai:"), "mcp:"),
		Scope: tc.ScopeUserID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"rows":         total,
		"download_url": fmt.Sprintf("%s/api/exports/csv?token=%s", strings.TrimRight(tc.PublicURL, "/"), token),
		"expires_in":   exports.TTL.String(),
		"note": "Give this link to the operator. The rows are streamed straight from the " +
			"database when it is opened — they are not in this conversation, and nothing " +
			"is stored on the server. Signing in is still required, and the link only " +
			"works for the account that asked for it.",
	}, nil
}
