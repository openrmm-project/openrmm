package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

func enrollTokenRequest(method, target, body, actor, scopeUser, id string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	ctx := context.WithValue(req.Context(), actorKey, actor)
	if scopeUser != "" {
		ctx = context.WithValue(ctx, scopeUserKey, scopeUser)
	}
	if id != "" {
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rc)
	}
	return req.WithContext(ctx)
}

// A token may enrol a fleet, but only within the bounds: anything outside is
// refused before the database is touched, including an hour count that would
// overflow time.Duration and come out small.
func TestCreateEnrollTokenRefusesOutOfBounds(t *testing.T) {
	s := &Server{}
	for name, body := range map[string]string{
		"too many uses":     `{"uses":10001}`,
		"negative uses":     `{"uses":-1}`,
		"too long":          `{"ttl_hours":2161}`,
		"negative validity": `{"ttl_hours":-5}`,
		"overflowing hours": `{"ttl_hours":9000000000000000000}`,
		"long description":  `{"description":"` + strings.Repeat("x", 201) + `"}`,
	} {
		rr := httptest.NewRecorder()
		s.createEnrollToken(rr, enrollTokenRequest(http.MethodPost, "/api/enroll-tokens", body, "admin", "", ""))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rr.Code, rr.Body.String())
		}
	}
}

func TestEnrollTokensForMassDeployment(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := newTestStore(t, url)
	s := &Server{cfg: &config.Config{}, store: st}
	suffix := uuid.NewString()[:8]
	client, err := st.CreateClient(ctx, "Mass "+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "HQ "+suffix)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateSite(ctx, client.ID, "Branch "+suffix)
	if err != nil {
		t.Fatal(err)
	}
	create := func(body, scopeUser string) (*httptest.ResponseRecorder, models.EnrollToken) {
		rr := httptest.NewRecorder()
		s.createEnrollToken(rr, enrollTokenRequest(http.MethodPost, "/api/enroll-tokens", body, "admin-"+suffix, scopeUser, ""))
		var tok models.EnrollToken
		_ = json.Unmarshal(rr.Body.Bytes(), &tok)
		return rr, tok
	}
	enrol := func(token, host string) error {
		_, _, _, err := s.registerAgent(ctx, &protocol.RegisterReq{EnrollToken: token, Hostname: host, OS: "linux", Arch: "amd64", Version: "0.10.0"})
		return err
	}

	t.Run("omitted means one computer for 24 hours", func(t *testing.T) {
		rr, tok := create(`{"site_id":"`+site.ID+`"}`, "")
		if rr.Code != http.StatusCreated || tok.UsesTotal != 1 || tok.ExpiresAt == nil {
			t.Fatalf("%d %+v", rr.Code, tok)
		}
		if d := time.Until(*tok.ExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
			t.Fatalf("expires in %v", d)
		}
	})

	var mass models.EnrollToken
	t.Run("one token enrols many computers, and only its hash is kept", func(t *testing.T) {
		rr, tok := create(`{"description":"GPO rollout","uses":3,"ttl_hours":2160,"site_id":"`+site.ID+`"}`, "")
		if rr.Code != http.StatusCreated || tok.Token == "" || tok.UsesTotal != 3 || tok.CreatedBy != "admin-"+suffix {
			t.Fatalf("%d %s", rr.Code, rr.Body.String())
		}
		mass = tok
		for i := 1; i <= 3; i++ {
			if err := enrol(tok.Token, "pc-"+suffix+"-"+string(rune('0'+i))); err != nil {
				t.Fatalf("computer %d: %v", i, err)
			}
		}
		if err := enrol(tok.Token, "pc-"+suffix+"-4"); err == nil {
			t.Fatal("a fourth computer enrolled with a three-use token")
		}
		got, err := st.GetEnrollToken(ctx, tok.ID)
		if err != nil || got.UsesLeft != 0 || got.UsesTotal != 3 {
			t.Fatalf("%+v %v", got, err)
		}
		if _, _, err := st.ConsumeEnrollToken(ctx, "not-"+tok.Token); err != store.ErrNotFound {
			t.Fatalf("a wrong token: %v", err)
		}
	})

	t.Run("the list shows what can still enrol, never the token", func(t *testing.T) {
		_, live := create(`{"description":"Intune","uses":50,"ttl_hours":720,"site_id":"`+site.ID+`"}`, "")
		rr := httptest.NewRecorder()
		s.listEnrollTokens(rr, enrollTokenRequest(http.MethodGet, "/api/enroll-tokens?site_id="+site.ID, "", "admin-"+suffix, "", ""))
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), live.Token) || strings.Contains(rr.Body.String(), `"token"`) {
			t.Fatalf("%d %s", rr.Code, rr.Body.String())
		}
		var list []models.EnrollToken
		_ = json.Unmarshal(rr.Body.Bytes(), &list)
		found := false
		for _, x := range list {
			if x.ID == mass.ID {
				t.Fatal("a used-up token is listed")
			}
			found = found || x.ID == live.ID
		}
		if !found {
			t.Fatalf("the live token is missing: %s", rr.Body.String())
		}

		rr = httptest.NewRecorder()
		s.revokeEnrollToken(rr, enrollTokenRequest(http.MethodDelete, "/api/enroll-tokens/"+live.ID, "", "admin-"+suffix, "", live.ID))
		if rr.Code != http.StatusOK {
			t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
		}
		if err := enrol(live.Token, "late-"+suffix); err == nil {
			t.Fatal("a revoked token still enrols")
		}
		rr = httptest.NewRecorder()
		s.revokeEnrollToken(rr, enrollTokenRequest(http.MethodDelete, "/api/enroll-tokens/"+live.ID, "", "admin-"+suffix, "", live.ID))
		if rr.Code != http.StatusConflict {
			t.Fatalf("second revoke: %d", rr.Code)
		}
		entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: "enroll_token.", Target: live.ID})
		if err != nil || len(entries) != 2 {
			t.Fatalf("audit for the token: %d entries, %v", len(entries), err)
		}
	})

	t.Run("a user limited to one site handles only that site's tokens", func(t *testing.T) {
		scoped, err := st.CreateUserWithScopes(ctx, "site-tech-"+suffix, "hash", models.RoleTech,
			[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}})
		if err != nil {
			t.Fatal(err)
		}
		_, otherTok := create(`{"uses":5,"site_id":"`+other.ID+`"}`, "")
		_, unbound := create(`{"uses":5}`, "")
		for name, req := range map[string]*http.Request{
			"list everything":       enrollTokenRequest(http.MethodGet, "/api/enroll-tokens", "", scoped.ID, scoped.ID, ""),
			"list another site":     enrollTokenRequest(http.MethodGet, "/api/enroll-tokens?site_id="+other.ID, "", scoped.ID, scoped.ID, ""),
			"revoke another site's": enrollTokenRequest(http.MethodDelete, "/", "", scoped.ID, scoped.ID, otherTok.ID),
			"revoke an unbound one": enrollTokenRequest(http.MethodDelete, "/", "", scoped.ID, scoped.ID, unbound.ID),
		} {
			rr := httptest.NewRecorder()
			if req.Method == http.MethodGet {
				s.listEnrollTokens(rr, req)
			} else {
				s.revokeEnrollToken(rr, req)
			}
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s: %d, want 403", name, rr.Code)
			}
		}
		if got, _ := st.GetEnrollToken(ctx, otherTok.ID); got.RevokedAt != nil {
			t.Fatal("the other site's token was revoked")
		}
		rr, own := create(`{"uses":20,"site_id":"`+site.ID+`"}`, scoped.ID)
		if rr.Code != http.StatusCreated {
			t.Fatalf("own site: %d %s", rr.Code, rr.Body.String())
		}
		rr = httptest.NewRecorder()
		s.revokeEnrollToken(rr, enrollTokenRequest(http.MethodDelete, "/", "", scoped.ID, scoped.ID, own.ID))
		if rr.Code != http.StatusOK {
			t.Fatalf("revoke own: %d %s", rr.Code, rr.Body.String())
		}
	})
}
