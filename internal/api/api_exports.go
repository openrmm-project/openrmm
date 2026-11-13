package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/p1ngu/rmm/internal/exports"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// Exports: a CSV the caller downloads, without the data ever passing through a
// model or landing on a disk.
//
// It exists because of how the AI produced a "download" before: the model read
// the rows, retyped them into its answer, and the console saved what it had
// typed. That paid for the data twice in context, was slow, and produced a file
// containing what the model REMEMBERED rather than what the database holds —
// which at a thousand rows is not the same thing.
//
// Nothing is stored. The URL carries a signed token describing the query, and
// fetching it re-runs that query and streams the result. There is no file to
// expire, no directory to sweep, no orphan to leak disk, and no cleanup job that
// can fail quietly. Expiry is in the signature.
//
// Re-running rather than snapshotting has a second benefit worth stating: the
// download is authorised against the CALLER'S CURRENT access, not the access
// they had when the link was minted. A revoked account cannot use a link it was
// given yesterday.

// exportCSV streams the export named by the token.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	claims, err := exports.Verify(s.cfg, r.URL.Query().Get("token"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// The link belongs to the person who asked for it. Without this, a link
	// pasted into a shared channel would work for anyone with any account.
	if claims.User != "" && claims.User != actorOf(r) {
		writeErr(w, http.StatusForbidden, "this export link belongs to another account")
		return
	}
	// And their access is re-read now rather than trusted from the token, so a
	// scope narrowed since minting narrows the download too.
	if claims.Scope != s.scopeUserID(r) {
		writeErr(w, http.StatusForbidden, "your access scope changed; ask for a new export link")
		return
	}

	switch claims.Kind {
	case "audit":
		s.streamAuditCSV(w, r, claims)
	case "inventory":
		s.streamInventoryCSV(w, r, claims)
	default:
		writeErr(w, http.StatusBadRequest, "unknown export kind")
	}
}

// csvStream wraps the response as a CSV download and hands back a writer.
func csvStream(w http.ResponseWriter, filename string) *spreadsheetSafeCSV {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// Nothing here should be cached: the URL expires, and a cached copy would
	// outlive it in a proxy.
	w.Header().Set("Cache-Control", "no-store")
	return &spreadsheetSafeCSV{csv.NewWriter(w)}
}

// spreadsheetSafeCSV neutralises cells a spreadsheet would run as a formula.
// Most of what these exports carry is reported by the devices themselves —
// hostnames, software names, manufacturers — so anyone in control of one
// endpoint chooses text that a technician later opens in Excel. A leading
// apostrophe makes the cell literal text there and is plainly visible
// everywhere else.
type spreadsheetSafeCSV struct{ *csv.Writer }

func (c *spreadsheetSafeCSV) Write(record []string) error {
	safe := make([]string, len(record))
	for i, cell := range record {
		safe[i] = spreadsheetSafeCell(cell)
	}
	return c.Writer.Write(safe)
}

// formulaPrefixes start a formula in common spreadsheets, including the
// full-width forms some locales convert.
const formulaPrefixes = "=+-@＝＋－＠"

// isCellBreak reports characters where a spreadsheet may start a new cell.
// A comma is this file's separator, but Excel opens a .csv with the regional
// list separator — ';' in pt-BR, de-DE, es-ES — and then splits a value such as
// "x;=1+1" into a second cell that begins with the formula. Line breaks start
// a new row for a reader that splits there.
func isCellBreak(r rune) bool {
	return r == ',' || r == ';' || r == '\t' || r == '\r' || r == '\n'
}

// spreadsheetSafeCell prefixes an apostrophe wherever a cell could begin with a
// formula: at the start of the value and after every cell break, looking past
// whitespace, control and format characters (BOM, zero-width space). It never
// changes the stored source value, only the exported text; ordinary text is
// returned unchanged. The console's csvEscape applies the same policy.
func spreadsheetSafeCell(cell string) string {
	var at []int
	pending, insertAt := true, 0
	for i, r := range cell {
		switch {
		case isCellBreak(r):
			pending, insertAt = true, i+utf8.RuneLen(r)
		case !pending:
		case unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		case strings.ContainsRune(formulaPrefixes, r):
			at, pending = append(at, insertAt), false
		default:
			pending = false
		}
	}
	// A value that starts with a tab or line break is guarded as a whole too,
	// as before: some readers trim it and then see what follows.
	if cell != "" && strings.ContainsRune("\t\r\n", rune(cell[0])) && (len(at) == 0 || at[0] != 0) {
		at = append([]int{0}, at...)
	}
	if len(at) == 0 {
		return cell
	}
	var b strings.Builder
	prev := 0
	for _, i := range at {
		b.WriteString(cell[prev:i])
		b.WriteByte('\'')
		prev = i
	}
	b.WriteString(cell[prev:])
	return b.String()
}

func (s *Server) streamAuditCSV(w http.ResponseWriter, r *http.Request, claims *exports.Claims) {
	if !hasPermission(r, models.PermAuditView) {
		writeErr(w, http.StatusForbidden, "audit.view is required")
		return
	}
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "restricted principals cannot read the global audit log")
		return
	}

	filter := store.AuditFilter{
		Actor:  claims.Params["actor"],
		Action: claims.Params["action"],
		Target: claims.Params["target"],
	}
	if v := claims.Params["since"]; v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Since = parsed
		}
	}
	if v := claims.Params["until"]; v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Until = parsed
		}
	}

	out := csvStream(w, fmt.Sprintf("audit-%s.csv", time.Now().UTC().Format("2006-01-02")))
	defer out.Flush()
	_ = out.Write([]string{"time", "actor", "action", "target", "detail"})

	// Paged rather than loaded whole: a year of audit does not belong in the
	// server's memory to be written out again.
	const page = 1000
	for offset := 0; offset < exports.MaxRows; offset += page {
		filter.Limit, filter.Offset = page, offset
		entries, _, err := s.store.ListAudit(r.Context(), filter)
		if err != nil {
			// The status line is long gone by now, so the error cannot be an HTTP
			// code. Saying so inside the file is the honest option: a truncated
			// CSV that ends mid-row and explains itself beats one that just stops.
			_ = out.Write([]string{"", "", "EXPORT FAILED", "", err.Error()})
			return
		}
		for _, e := range entries {
			_ = out.Write([]string{
				e.CreatedAt.UTC().Format(time.RFC3339), e.Actor, e.Action, e.Target,
				string(e.Detail),
			})
		}
		if len(entries) < page {
			return
		}
		out.Flush()
	}
	_ = out.Write([]string{"", "", "TRUNCATED", "",
		fmt.Sprintf("stopped at %d rows; narrow the filter to export the rest", exports.MaxRows)})
}

func (s *Server) streamInventoryCSV(w http.ResponseWriter, r *http.Request, claims *exports.Claims) {
	if !hasPermission(r, models.PermReportsView) {
		writeErr(w, http.StatusForbidden, "reports.view is required")
		return
	}

	// The access scope is re-read from the CURRENT request, not taken from the
	// token. The token records what it was at mint time only so a change can be
	// detected and refused up front; the query itself is always filtered by who
	// is asking now.
	filter := store.ReportFilter{
		ScopeUserID: s.scopeUserID(r),
		ScopeType:   claims.Params["scope_type"],
		ScopeID:     claims.Params["scope_id"],
	}

	kind := claims.Params["report_type"]
	out := csvStream(w, fmt.Sprintf("%s-%s.csv", kind, time.Now().UTC().Format("2006-01-02")))
	defer out.Flush()

	num := func(n int64) string { return fmt.Sprintf("%d", n) }
	switch kind {
	case "software":
		rows, err := s.store.SoftwareReport(r.Context(), filter)
		if err != nil {
			_ = out.Write([]string{"EXPORT FAILED", err.Error()})
			return
		}
		_ = out.Write([]string{"name", "version", "manufacturer", "hostname", "client", "site", "department"})
		for _, x := range rows {
			_ = out.Write([]string{x.Name, x.Version, x.Manufacturer, x.Hostname, x.Client, x.Site, x.Department})
		}
	case "hardware":
		rows, err := s.store.HardwareReport(r.Context(), filter)
		if err != nil {
			_ = out.Write([]string{"EXPORT FAILED", err.Error()})
			return
		}
		_ = out.Write([]string{"hostname", "os", "arch", "manufacturer", "model", "serial_number",
			"cpu_model", "cpu_cores", "mem_total_bytes", "gpu", "storage", "storage_bytes",
			"bios", "platform", "platform_version", "kernel_version", "ip", "mac",
			"client", "site", "department", "last_logged_in_user", "last_logged_in_user_at"})
		for _, x := range rows {
			_ = out.Write([]string{x.Hostname, x.OS, x.Arch, x.Manufacturer, x.Model, x.Serial,
				x.CPUModel, num(int64(x.CPUCores)), num(x.MemTotalByte), x.GPU, x.Storage,
				num(x.StorageBytes), x.BIOS, x.Platform, x.PlatformVer, x.Kernel, x.IP, x.MAC,
				x.Client, x.Site, x.Department, x.LastLoggedInUser, timeCell(x.LastLoggedInUserAt)})
		}
	case "machines":
		rows, err := s.store.MachinesReport(r.Context(), filter)
		if err != nil {
			_ = out.Write([]string{"EXPORT FAILED", err.Error()})
			return
		}
		_ = out.Write([]string{"hostname", "os", "arch", "status", "agent_version", "cpu_model",
			"mem_total_bytes", "ip", "mac", "client", "site", "department", "logged_in_user", "last_seen", "collected_at"})
		for _, x := range rows {
			_ = out.Write([]string{x.Hostname, x.OS, x.Arch, x.Status, x.AgentVersion, x.CPUModel,
				num(x.MemTotalByte), x.IP, x.MAC, x.Client, x.Site, x.Department, x.LoggedInUser,
				timeCell(x.LastSeen), timeCell(x.CollectedAt)})
		}
	default:
		writeErr(w, http.StatusBadRequest, "unknown report_type")
	}
}

// timeCell renders a nullable timestamp for a spreadsheet: empty rather than
// a zero date, which sorts and reads as a real value.
func timeCell(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
