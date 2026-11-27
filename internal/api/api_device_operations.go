package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func (s *Server) getDeviceOperationsProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	p, err := s.store.GetDeviceOperationsProfile(r.Context(), id)
	if err != nil {
		writeErr(w, operationsErrorStatus(err), "unable to read operations profile")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func decodeOperationsUpdate(r io.Reader) (models.DeviceOperationsFields, int64, error) {
	var in struct {
		models.DeviceOperationsFields
		ExpectedVersion *int64 `json:"expected_version"`
	}
	raw, err := io.ReadAll(io.LimitReader(r, 160001))
	if err != nil {
		return in.DeviceOperationsFields, 0, err
	}
	bad := errors.New("invalid operations profile request")
	if len(raw) > 160000 || !utf8.Valid(raw) {
		return in.DeviceOperationsFields, 0, bad
	}
	// Reject duplicate keys and null text rather than silently accepting ambiguous input.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return in.DeviceOperationsFields, 0, bad
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return in.DeviceOperationsFields, 0, bad
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return in.DeviceOperationsFields, 0, bad
		}
		switch key {
		case "expected_version", "purpose", "applications", "dependencies", "criticality", "maintenance_window", "maintenance_timezone", "backup_notes", "backup_verified_on":
		default:
			return in.DeviceOperationsFields, 0, bad
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return in.DeviceOperationsFields, 0, bad
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "backup_verified_on" {
			return in.DeviceOperationsFields, 0, bad
		}
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in.DeviceOperationsFields, 0, bad
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return in.DeviceOperationsFields, 0, bad
	}
	if in.ExpectedVersion == nil || *in.ExpectedVersion < 0 {
		return in.DeviceOperationsFields, 0, bad
	}
	if err := in.DeviceOperationsFields.Validate(); err != nil {
		return in.DeviceOperationsFields, 0, err
	}
	return in.DeviceOperationsFields, *in.ExpectedVersion, nil
}

func (s *Server) putDeviceOperationsProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	fields, version, err := decodeOperationsUpdate(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.store.UpdateDeviceOperationsProfile(r.Context(), id, actorOf(r), version, fields)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusConflict, "operations profile has changed; reload before saving")
		return
	}
	if err != nil {
		writeErr(w, operationsErrorStatus(err), "unable to save operations profile")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func operationsErrorStatus(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
