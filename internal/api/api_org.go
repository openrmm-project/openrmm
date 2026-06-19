package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	var clients []models.Client
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		clients, err = s.store.ListClientsForUser(r.Context(), uid)
	} else {
		clients, err = s.store.ListClients(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	c, err := s.store.CreateClient(r.Context(), in.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if err := s.store.RenameClient(r.Context(), chi.URLParam(r, "id"), in.Name); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteClient(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	var sites []models.Site
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		sites, err = s.store.ListSitesForUser(r.Context(), uid, r.URL.Query().Get("client_id"))
	} else {
		sites, err = s.store.ListSites(r.Context(), r.URL.Query().Get("client_id"))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientID string `json:"client_id"`
		Name     string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.ClientID == "" || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "client_id and name required")
		return
	}
	site, err := s.store.CreateSite(r.Context(), in.ClientID, in.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, site)
}

func (s *Server) updateSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if err := s.store.RenameSite(r.Context(), chi.URLParam(r, "id"), in.Name); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSite(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) setDeviceSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID *string `json:"site_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetAgentSite(r.Context(), chi.URLParam(r, "id"), in.SiteID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// updateDeviceMeta sets the operator-editable identification fields: description
// (shown in the device list) and note (a private observation, detail-only).
func (s *Server) updateDeviceMeta(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	var in struct {
		Description string `json:"description"`
		Note        string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetAgentMeta(r.Context(), id, in.Description, in.Note); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, _ := s.store.GetAgent(r.Context(), id)
	writeJSON(w, http.StatusOK, out)
}
