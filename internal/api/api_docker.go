package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/p1ngu/rmm/internal/store"
)

func (s *Server) getDeviceDocker(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	data, err := s.store.DockerInventory(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), "Unable to read Docker inventory.")
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) listDocker(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.checkedReportFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.DockerFilter{Query: strings.TrimSpace(q.Get("q")), State: q.Get("state"), Health: q.Get("health"), AgentID: q.Get("agent_id"), Limit: 100}
	if len(f.Query) > 256 || strings.ContainsFunc(f.Query, unicode.IsControl) {
		writeErr(w, http.StatusBadRequest, "query is too long")
		return
	}
	switch f.State {
	case "", "created", "running", "paused", "restarting", "removing", "exited", "dead":
	default:
		writeErr(w, http.StatusBadRequest, "invalid state")
		return
	}
	switch f.Health {
	case "", "none", "starting", "healthy", "unhealthy":
	default:
		writeErr(w, http.StatusBadRequest, "invalid health")
		return
	}
	for key, dest := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if q.Get(key) != "" {
			n, err := strconv.Atoi(q.Get(key))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid pagination")
				return
			}
			*dest = n
		}
	}
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 || f.Offset > 1000000 {
		writeErr(w, http.StatusBadRequest, "invalid pagination")
		return
	}
	if f.AgentID != "" && !s.requireAgent(w, r, f.AgentID) {
		return
	}
	data, err := s.store.DockerFleet(r.Context(), scope, f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read Docker inventory.")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
