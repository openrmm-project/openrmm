package api

import (
	"encoding/json"
	"net/http"

	"github.com/p1ngu/rmm/internal/notify"
)

func (s *Server) getAlertTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
	c, err := notify.LoadAlertTemplates(r.Context(), s.store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "defaults": notify.DefaultAlertTemplates(), "variables": notify.AlertTemplateVariables})
}

func (s *Server) updateAlertTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	var c notify.AlertTemplates
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := notify.ValidateAlertTemplates(c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), notify.AlertTemplatesKey, string(data), false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Record the action, never the administrator's message text.
	_ = s.store.AddAudit(r.Context(), actorOf(r), "config.mail.templates.update", "app_settings", nil)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) previewAlertTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	var in struct {
		Config notify.AlertTemplates `json:"config"`
		Event  string                `json:"event"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	message, err := notify.RenderAlertTemplate(in.Config, in.Event, notify.SampleAlertTemplateData(in.Event, s.cfg.NotificationConsoleURL()))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"subject": message.Subject, "body": message.Body})
}
