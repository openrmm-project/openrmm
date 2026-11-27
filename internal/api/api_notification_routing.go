package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func routingScope(r *http.Request) (string, string) {
	scope := models.ScopeSite
	if strings.HasPrefix(r.URL.Path, "/api/clients/") {
		scope = models.ScopeClient
	}
	return scope, chi.URLParam(r, "id")
}
func (s *Server) requireRoutingScope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	scope, id := routingScope(r)
	if _, err := uuid.Parse(id); err != nil {
		writeErr(w, 400, "invalid scope ID")
		return scope, id, false
	}
	return scope, id, s.requireScope(w, r, scope, id)
}
func (s *Server) getNotificationRouting(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.requireRoutingScope(w, r)
	if !ok {
		return
	}
	result, err := s.store.GetNotificationRouting(r.Context(), scope, id)
	if err != nil {
		writeErr(w, httpStatusFor(err), "unable to read notification routing")
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) notificationRouteRecipientOptions(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.requireRoutingScope(w, r)
	if !ok {
		return
	}
	query := r.URL.Query().Get("q")
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 200 {
		writeErr(w, 400, "recipient search too long")
		return
	}
	users, more, err := s.store.NotificationRouteRecipientOptions(r.Context(), scope, id, query)
	if err != nil {
		writeErr(w, httpStatusFor(err), "unable to read recipient options")
		return
	}
	writeJSON(w, 200, map[string]any{"recipients": users, "has_more": more})
}

func decodeNotificationRouting(r io.Reader) (models.NotificationRouteUpdate, int64, error) {
	var in struct {
		models.NotificationRouteUpdate
		ExpectedVersion *int64 `json:"expected_version"`
	}
	bad := errors.New("invalid notification routing request")
	raw, err := io.ReadAll(io.LimitReader(r, 20001))
	if err != nil {
		return in.NotificationRouteUpdate, 0, err
	}
	if len(raw) > 20000 || !utf8.Valid(raw) {
		return in.NotificationRouteUpdate, 0, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return in.NotificationRouteUpdate, 0, bad
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return in.NotificationRouteUpdate, 0, bad
		}
		key, ok := token.(string)
		if !ok || seen[key] || (key != "mode" && key != "user_ids" && key != "expected_version") {
			return in.NotificationRouteUpdate, 0, bad
		}
		seen[key] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return in.NotificationRouteUpdate, 0, bad
		}
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return in.NotificationRouteUpdate, 0, bad
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return in.NotificationRouteUpdate, 0, bad
	}
	if in.ExpectedVersion == nil || *in.ExpectedVersion < 0 {
		return in.NotificationRouteUpdate, 0, bad
	}
	if err = in.Validate(); err != nil {
		return in.NotificationRouteUpdate, 0, err
	}
	return in.NotificationRouteUpdate, *in.ExpectedVersion, nil
}
func (s *Server) putNotificationRouting(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.requireRoutingScope(w, r)
	if !ok {
		return
	}
	update, version, err := decodeNotificationRouting(r.Body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	result, err := s.store.UpdateNotificationRouting(r.Context(), scope, id, actorOf(r), version, update)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "notification routing changed; reload before saving")
		return
	}
	if errors.Is(err, store.ErrNotificationRecipients) {
		writeErr(w, 400, "some recipients are unavailable for this scope")
		return
	}
	if err != nil {
		writeErr(w, httpStatusFor(err), "unable to save notification routing")
		return
	}
	writeJSON(w, 200, result)
}
