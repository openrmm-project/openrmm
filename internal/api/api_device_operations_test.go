package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func TestOperationsProfileStrictRequest(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"expected_version":0,"criticality":"unknown","Purpose":"alias"}`, `{"criticality":"unknown"}`, `{"expected_version":-1,"criticality":"unknown"}`,
		`{"expected_version":0,"criticality":"unknown","version":2}`, `{"expected_version":0,"criticality":"unknown","updated_by":"admin"}`,
		`{"expected_version":0,"criticality":"unknown","updated_at":"2026-01-01"}`,
		`{"expected_version":0,"expected_version":1,"criticality":"unknown"}`, `{"expected_version":null,"criticality":"unknown"}`,
		`{"expected_version":0,"criticality":"unknown","purpose":null}`, `{"expected_version":0,"criticality":"unknown"} {}`,
		`{"expected_version":0,"criticality":"unknown","purpose":"` + strings.Repeat("a", 160001) + `"}`,
	} {
		if _, _, err := decodeOperationsUpdate(strings.NewReader(body)); err == nil {
			t.Errorf("accepted %s", body[:min(len(body), 100)])
		}
	}
	if _, version, err := decodeOperationsUpdate(strings.NewReader(`{"expected_version":0,"criticality":"unknown","backup_verified_on":null}`)); err != nil || version != 0 {
		t.Fatalf("valid request %d %v", version, err)
	}
}

func TestOperationsProfileScopeAndAPIIntegration(t *testing.T) {
	f := newBulkFixture(t)
	path := "/api/devices/" + f.a1 + "/operations-profile"
	body := map[string]any{"expected_version": 0, "criticality": "high", "purpose": "reference only"}
	wantStatus(t, f.do(f.admin, http.MethodGet, path, nil), 200)
	out := f.do(f.scoped, http.MethodPut, path, body)
	wantStatus(t, out, 200)
	var p models.DeviceOperationsProfile
	if err := json.Unmarshal(out.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Version != 1 || p.UpdatedAt == nil || p.UpdatedBy == "" || p.Purpose != "reference only" {
		t.Fatalf("saved %+v", p)
	}
	wantStatus(t, f.do(f.admin, http.MethodPut, path, body), 409)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		wantStatus(t, f.do(f.scoped, method, "/api/devices/"+f.b1+"/operations-profile", body), 403)
	}
	viewer, err := f.st.CreateUser(f.ctx, "viewer-"+uuid.NewString(), "hash", models.RoleReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := f.st.CreateSession(f.ctx, viewer.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do(token, http.MethodGet, path, nil), 200)
	wantStatus(t, f.do(token, http.MethodPut, path, body), 403)
	body["expected_version"] = 1
	body["updated_by"] = "spoof"
	wantStatus(t, f.do(f.admin, http.MethodPut, path, body), 400)
	wantStatus(t, f.do(f.admin, http.MethodGet, "/api/devices/"+uuid.NewString()+"/operations-profile", nil), 404)
	if rows := f.audits("device.operational_profile.update", f.a1); len(rows) != 1 || rows[0].Actor != p.UpdatedBy {
		t.Fatalf("audits %+v", rows)
	}
}

func TestOperationsProfileDatabaseErrorsAreServerErrors(t *testing.T) {
	if operationsErrorStatus(errors.New("synthetic database failure")) != http.StatusInternalServerError {
		t.Fatal("database failure reported as invalid client input")
	}
	if operationsErrorStatus(store.ErrNotFound) != http.StatusNotFound {
		t.Fatal("missing device not reported as 404")
	}
}
