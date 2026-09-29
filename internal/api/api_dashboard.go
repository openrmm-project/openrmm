package api

import (
	"net/http"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

const (
	// dashboardFailedWindow is what "recent failures" means on the dashboard.
	dashboardFailedWindow = 24 * time.Hour
	// dashboardAwaitingShown is how many waiting jobs the dashboard lists; the
	// count covers all of them.
	dashboardAwaitingShown = 5
)

// getDashboard returns the dashboard's numbers, counted by the database over
// what the caller may see. Each part is present only with the permission that
// guards the full list: a principal with neither gets an empty object.
func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	uid := s.scopeUserID(r)
	out := map[string]any{}
	if hasPermission(r, models.PermDevicesView) {
		c, err := s.store.DashboardDeviceCounts(r.Context(), uid)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out["devices"] = c
	}
	if hasPermission(r, models.PermJobsView) {
		c, err := s.store.DashboardJobCounts(r.Context(), uid, time.Now().Add(-dashboardFailedWindow))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		awaiting, err := s.store.AwaitingJobSummaries(r.Context(), uid, dashboardAwaitingShown)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out["jobs"] = map[string]any{
			"awaiting":       c.Awaiting,
			"failed":         c.Failed,
			"failed_window":  int(dashboardFailedWindow.Hours()),
			"awaiting_items": awaiting,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
