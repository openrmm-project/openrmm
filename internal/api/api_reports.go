package api

import (
	"net/http"

	"github.com/p1ngu/rmm/internal/store"
)

// Inventory reports: deterministic, server-side aggregation of the inventory
// JSONB across the fleet, grouped/filtered by client / site / department and
// always constrained to the caller's access scope. CSV export is done
// client-side from this JSON (same pattern as the AI chat report download).

// reportFilter builds a store.ReportFilter from the request: the caller's access
// scope plus the optional ?scope=client|site|group&id=<uuid> filter.
func (s *Server) reportFilter(r *http.Request) store.ReportFilter {
	f := store.ReportFilter{ScopeUserID: s.scopeUserID(r)}
	switch t := r.URL.Query().Get("scope"); t {
	case "client", "site", "group":
		if id := r.URL.Query().Get("id"); id != "" {
			f.ScopeType = t
			f.ScopeID = id
		}
	}
	return f
}

func (s *Server) reportSoftware(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.SoftwareReport(r.Context(), s.reportFilter(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) reportHardware(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.HardwareReport(r.Context(), s.reportFilter(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) reportMachines(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.MachinesReport(r.Context(), s.reportFilter(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}
