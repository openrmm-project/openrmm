package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ===================== Inventory reports =====================
//
// Fleet-wide inventory aggregation computed in the database (deterministic),
// scoped to the caller's access scope and optionally filtered to one
// client / site / department. The AI's inventory_report tool and the REST
// /api/reports/* endpoints both call these, so there is a single source of truth.

// ReportFilter scopes an inventory report.
type ReportFilter struct {
	ScopeUserID string // "" = unrestricted (admin / unscoped principal)
	ScopeType   string // "" | "client" | "site" | "group"
	ScopeID     string // matching id; empty = the whole in-scope fleet
}

// where builds the shared WHERE fragment (per-user access scope AND the optional
// client/site/group filter) plus its positional args. The access scope and the
// filter are ANDed, so a restricted user filtering an out-of-scope target gets
// an empty result — never a cross-tenant leak. Joins must expose aliases
// a (agents), s (sites), cl (clients), g (device_groups).
func (f ReportFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ScopeUserID != "" {
		args = append(args, f.ScopeUserID)
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(
			NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$%[1]d)
			OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$%[1]d AND (
				(us.scope_type='agent'  AND us.scope_id=a.id) OR
				(us.scope_type='group'  AND us.scope_id=a.group_id) OR
				(us.scope_type='site'   AND us.scope_id=a.site_id) OR
				(us.scope_type='client' AND us.scope_id=cl.id)
			)))`, n))
	}
	switch f.ScopeType {
	case "client":
		args = append(args, f.ScopeID)
		conds = append(conds, fmt.Sprintf("cl.id=$%d", len(args)))
	case "site":
		args = append(args, f.ScopeID)
		conds = append(conds, fmt.Sprintf("a.site_id=$%d", len(args)))
	case "group":
		args = append(args, f.ScopeID)
		conds = append(conds, fmt.Sprintf("a.group_id=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// reportJoins is the shared agent -> site -> client / group join chain.
const reportJoins = `
	LEFT JOIN sites s         ON s.id = a.site_id
	LEFT JOIN clients cl      ON cl.id = s.client_id
	LEFT JOIN device_groups g ON g.id = a.group_id`

// SoftwareRow is one installed package on one machine.
type SoftwareRow struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Manufacturer string `json:"manufacturer"`
	Hostname     string `json:"hostname"`
	Client       string `json:"client"`
	Site         string `json:"site"`
	Department   string `json:"department"`
}

// SoftwareReport returns one row per (package, machine) across the in-scope
// fleet. Machines without a software array simply contribute no rows.
func (s *Store) SoftwareReport(ctx context.Context, f ReportFilter) ([]SoftwareRow, error) {
	where, args := f.where()
	q := `
		SELECT COALESCE(sw->>'name',''), COALESCE(sw->>'version',''),
		       COALESCE(sw->>'manufacturer', sw->>'publisher', sw->>'vendor', ''),
		       a.hostname, COALESCE(cl.name,''), COALESCE(s.name,''), COALESCE(g.name,'')
		FROM inventory inv
		JOIN agents a ON a.id = inv.agent_id` + reportJoins + `
		CROSS JOIN LATERAL jsonb_array_elements(
			CASE WHEN jsonb_typeof(inv.data->'software')='array'
			     THEN inv.data->'software' ELSE '[]'::jsonb END) AS sw` +
		where + `
		ORDER BY lower(sw->>'name'), sw->>'version', a.hostname`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwareRow
	for rows.Next() {
		var r SoftwareRow
		if err := rows.Scan(&r.Name, &r.Version, &r.Manufacturer, &r.Hostname, &r.Client, &r.Site, &r.Department); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HardwareRow is the hardware profile of one machine. Beyond the top-level
// CPU/memory/platform scalars it also surfaces the nested `hardware` object the
// agent collects per-OS (manufacturer, model, serial, BIOS) and a joined
// summary of the GPU(s) and storage device(s).
type HardwareRow struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Serial       string `json:"serial_number"`
	CPUModel     string `json:"cpu_model"`
	CPUCores     int    `json:"cpu_cores"`
	MemTotalByte int64  `json:"mem_total_bytes"`
	GPU          string `json:"gpu"`
	Storage      string `json:"storage"`       // joined storage-device models
	StorageBytes int64  `json:"storage_bytes"` // total capacity across all storage devices
	BIOS         string `json:"bios"`
	Platform     string `json:"platform"`
	PlatformVer  string `json:"platform_version"`
	Kernel       string `json:"kernel_version"`
	IP           string `json:"ip"`
	MAC          string `json:"mac"`
	Client       string `json:"client"`
	Site         string `json:"site"`
	Department   string `json:"department"`
}

// jsonbArray guards jsonb_array_elements against a missing/non-array value
// (which would otherwise error): it yields an empty array unless `path` is an
// array. Used inline for the GPU/storage aggregation.
const hwGPUs = `CASE WHEN jsonb_typeof(inv.data->'hardware'->'gpus')='array' THEN inv.data->'hardware'->'gpus' ELSE '[]'::jsonb END`
const hwStorage = `CASE WHEN jsonb_typeof(inv.data->'hardware'->'storage_devices')='array' THEN inv.data->'hardware'->'storage_devices' ELSE '[]'::jsonb END`
const netIfaces = `CASE WHEN jsonb_typeof(inv.data->'network_interfaces')='array' THEN inv.data->'network_interfaces' ELSE '[]'::jsonb END`

// HardwareReport returns one row per in-scope machine that has reported
// inventory, with its full hardware profile pulled from the JSONB snapshot.
func (s *Store) HardwareReport(ctx context.Context, f ReportFilter) ([]HardwareRow, error) {
	where, args := f.where()
	q := `
		SELECT a.hostname, a.os, a.arch,
		       COALESCE(inv.data->'hardware'->>'manufacturer',''),
		       COALESCE(inv.data->'hardware'->>'system_model',''),
		       COALESCE(inv.data->'hardware'->>'serial_number',''),
		       COALESCE(inv.data->>'cpu_model',''),
		       COALESCE((inv.data->>'cpu_cores')::int, 0),
		       COALESCE((inv.data->>'mem_total_bytes')::bigint, 0),
		       COALESCE((SELECT string_agg(NULLIF(gp->>'name',''), ', ')
		                 FROM jsonb_array_elements(` + hwGPUs + `) gp), ''),
		       COALESCE((SELECT string_agg(NULLIF(sd->>'model',''), ', ')
		                 FROM jsonb_array_elements(` + hwStorage + `) sd), ''),
		       COALESCE((SELECT SUM((sd->>'size_bytes')::bigint)
		                 FROM jsonb_array_elements(` + hwStorage + `) sd), 0),
		       COALESCE(inv.data->'hardware'->>'bios',''),
		       COALESCE(inv.data->>'platform',''),
		       COALESCE(inv.data->>'platform_version',''),
		       COALESCE(inv.data->>'kernel_version',''),
		       COALESCE((SELECT string_agg(NULLIF(ni->>'ipv4',''), ', ')
		                 FROM jsonb_array_elements(` + netIfaces + `) ni), ''),
		       COALESCE((SELECT string_agg(NULLIF(ni->>'mac',''), ', ')
		                 FROM jsonb_array_elements(` + netIfaces + `) ni), ''),
		       COALESCE(cl.name,''), COALESCE(s.name,''), COALESCE(g.name,'')
		FROM inventory inv
		JOIN agents a ON a.id = inv.agent_id` + reportJoins +
		where + `
		ORDER BY a.hostname`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HardwareRow
	for rows.Next() {
		var r HardwareRow
		if err := rows.Scan(&r.Hostname, &r.OS, &r.Arch, &r.Manufacturer, &r.Model, &r.Serial,
			&r.CPUModel, &r.CPUCores, &r.MemTotalByte, &r.GPU, &r.Storage, &r.StorageBytes, &r.BIOS,
			&r.Platform, &r.PlatformVer, &r.Kernel, &r.IP, &r.MAC, &r.Client, &r.Site, &r.Department); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MachineRow is one machine in the fleet (parque) report.
type MachineRow struct {
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	Status       string     `json:"status"`
	AgentVersion string     `json:"agent_version"`
	CPUModel     string     `json:"cpu_model"`
	MemTotalByte int64      `json:"mem_total_bytes"`
	IP           string     `json:"ip"`
	MAC          string     `json:"mac"`
	Client       string     `json:"client"`
	Site         string     `json:"site"`
	Department   string     `json:"department"`
	LastSeen     *time.Time `json:"last_seen"`
	CollectedAt  *time.Time `json:"collected_at"`
}

// MachinesReport returns one row per in-scope machine (online or not). Unlike
// the software/hardware reports it is based on agents (LEFT JOIN inventory), so
// machines that have never reported inventory still appear.
func (s *Store) MachinesReport(ctx context.Context, f ReportFilter) ([]MachineRow, error) {
	where, args := f.where()
	q := `
		SELECT a.hostname, a.os, a.arch, a.status, a.version,
		       COALESCE(inv.data->>'cpu_model',''),
		       COALESCE((inv.data->>'mem_total_bytes')::bigint, 0),
		       COALESCE((SELECT string_agg(NULLIF(ni->>'ipv4',''), ', ')
		                 FROM jsonb_array_elements(` + netIfaces + `) ni), ''),
		       COALESCE((SELECT string_agg(NULLIF(ni->>'mac',''), ', ')
		                 FROM jsonb_array_elements(` + netIfaces + `) ni), ''),
		       COALESCE(cl.name,''), COALESCE(s.name,''), COALESCE(g.name,''),
		       a.last_seen, inv.collected_at
		FROM agents a` + reportJoins + `
		LEFT JOIN inventory inv ON inv.agent_id = a.id` +
		where + `
		ORDER BY a.hostname`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRow
	for rows.Next() {
		var r MachineRow
		if err := rows.Scan(&r.Hostname, &r.OS, &r.Arch, &r.Status, &r.AgentVersion,
			&r.CPUModel, &r.MemTotalByte, &r.IP, &r.MAC, &r.Client, &r.Site, &r.Department,
			&r.LastSeen, &r.CollectedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ===================== Aggregation =====================
//
// Counting in the database, rather than handing rows to something that will
// count them badly.
//
// "How many machines have nginx" does not need a row per install. Answered here
// it costs one query and is exact; answered by a model reading a thousand rows
// it costs a large slice of a context window and produces a number that looks
// right. The second failure is the dangerous one, because nothing about the
// answer reveals it.
//
// This is also what makes the row cap tolerable: with a summary available, the
// cases that need rows are the narrow ones, and narrow ones fit.

// SummaryRow is one group and how much of the fleet it covers.
type SummaryRow struct {
	Key      string `json:"key"`
	Machines int    `json:"machines"` // distinct devices in this group
	Rows     int    `json:"rows"`     // matching rows; for software, installs
}

// groupExpr whitelists what may be grouped on, per report. A caller-supplied
// column name reaching SQL is how a report endpoint becomes an injection point,
// so the parameter selects from this map and can never contribute text to the
// query.
var groupExpr = map[string]map[string]string{
	"software": {
		"name":         `sw->>'name'`,
		"version":      `COALESCE(sw->>'name','') || ' ' || COALESCE(sw->>'version','')`,
		"manufacturer": `COALESCE(sw->>'manufacturer', sw->>'publisher', sw->>'vendor', '')`,
		"client":       `COALESCE(cl.name,'')`,
	},
	"hardware": {
		"os":           `a.os`,
		"manufacturer": `COALESCE(inv.data->'hardware'->>'manufacturer','')`,
		"model":        `COALESCE(inv.data->'hardware'->>'model','')`,
		"platform":     `COALESCE(inv.data->>'platform','')`,
		"client":       `COALESCE(cl.name,'')`,
	},
	"machines": {
		"os":         `a.os`,
		"status":     `a.status`,
		"client":     `COALESCE(cl.name,'')`,
		"site":       `COALESCE(s.name,'')`,
		"department": `COALESCE(g.name,'')`,
		"version":    `a.version`,
	},
}

// DefaultGroupBy is what a report groups on when the caller does not say.
var DefaultGroupBy = map[string]string{
	"software": "name", "hardware": "os", "machines": "os",
}

// GroupOptions lists what a report can be grouped by, for an error message that
// tells the caller what to do instead of only what went wrong.
func GroupOptions(reportType string) []string {
	out := make([]string, 0, len(groupExpr[reportType]))
	for k := range groupExpr[reportType] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// maxSummaryGroups caps the groups returned. Ordered by size, so the cap keeps
// what matters: a fleet with 9,000 distinct packages has a long tail nobody
// reads, and the answer to "what is installed" is the head of it.
const maxSummaryGroups = 50

// InventorySummary counts the fleet by one dimension.
//
// `match` narrows before grouping — the direct form of "how many machines have
// nginx", which grouping alone cannot answer when the package sits outside the
// top of a long tail.
func (s *Store) InventorySummary(ctx context.Context, f ReportFilter, reportType, groupBy, match string) ([]SummaryRow, int, error) {
	exprs, ok := groupExpr[reportType]
	if !ok {
		return nil, 0, fmt.Errorf("unknown report_type %q", reportType)
	}
	if groupBy == "" {
		groupBy = DefaultGroupBy[reportType]
	}
	expr, ok := exprs[groupBy]
	if !ok {
		return nil, 0, fmt.Errorf("cannot group %s by %q; try one of: %s",
			reportType, groupBy, strings.Join(GroupOptions(reportType), ", "))
	}

	where, args := f.where()
	from := `FROM inventory inv JOIN agents a ON a.id = inv.agent_id` + reportJoins
	if reportType == "software" {
		from += `
		CROSS JOIN LATERAL jsonb_array_elements(
			CASE WHEN jsonb_typeof(inv.data->'software')='array'
			     THEN inv.data->'software' ELSE '[]'::jsonb END) AS sw`
	}
	if reportType == "machines" {
		// Machines are counted from the agent list, not from reported inventory:
		// a device that has never checked in is still part of the fleet, and
		// leaving it out would quietly under-report exactly the machines an
		// operator is looking for.
		from = `FROM agents a` + reportJoins + ` LEFT JOIN inventory inv ON inv.agent_id = a.id`
	}

	if strings.TrimSpace(match) != "" {
		args = append(args, "%"+strings.TrimSpace(match)+"%")
		clause := fmt.Sprintf("(%s) ILIKE $%d", expr, len(args))
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
	}

	// Distinct devices as well as rows: for software the two differ, and
	// "installs" is rarely the number anyone wants.
	query := fmt.Sprintf(
		`SELECT %s AS grp, count(DISTINCT a.id) AS machines, count(*) AS rows_ %s%s
		 GROUP BY grp ORDER BY machines DESC, rows_ DESC, grp LIMIT %d`,
		expr, from, where, maxSummaryGroups+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []SummaryRow
	for rows.Next() {
		var r SummaryRow
		var key *string
		if err := rows.Scan(&key, &r.Machines, &r.Rows); err != nil {
			return nil, 0, err
		}
		if key != nil {
			r.Key = *key
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// One extra was fetched to detect a longer tail without a second count.
	truncated := 0
	if len(out) > maxSummaryGroups {
		out = out[:maxSummaryGroups]
		truncated = 1
	}
	return out, truncated, nil
}
