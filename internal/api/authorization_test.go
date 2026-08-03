package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func restrictedAdminRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	permissions := map[string]bool{}
	for _, permission := range models.DefaultRolePermissions(models.RoleAdmin) {
		permissions[permission] = true
	}
	ctx := context.WithValue(req.Context(), actorKey, "restricted-user")
	ctx = context.WithValue(ctx, roleKey, "tenant-admin")
	ctx = context.WithValue(ctx, scopeUserKey, "restricted-user")
	ctx = context.WithValue(ctx, permsKey, permissions)
	return req.WithContext(ctx)
}

func TestRestrictedPrincipalCannotCreateAdminUser(t *testing.T) {
	s := &Server{}
	req := restrictedAdminRequest(http.MethodPost, "/api/users",
		`{"username":"new-admin","password":"correct horse battery staple","role":"admin","scopes":[{"scope_type":"site","scope_id":"00000000-0000-0000-0000-000000000001"}]}`)
	rr := httptest.NewRecorder()

	s.createUser(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRestrictedPrincipalCannotCreateAdminAPIToken(t *testing.T) {
	s := &Server{}
	req := restrictedAdminRequest(http.MethodPost, "/api/api-tokens",
		`{"name":"escape","role":"admin","user_id":"restricted-user"}`)
	rr := httptest.NewRecorder()

	s.createAPIToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRestrictedPrincipalCannotMutateGlobalPolicy(t *testing.T) {
	s := &Server{}
	req := restrictedAdminRequest(http.MethodDelete, "/api/policies/policy-1", "")
	rr := httptest.NewRecorder()

	if s.requirePolicy(rr, req, "policy-1", true) {
		t.Fatal("restricted principal was allowed to mutate a global policy")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestRestrictedPrincipalCannotReadGlobalAudit(t *testing.T) {
	s := &Server{}
	req := restrictedAdminRequest(http.MethodGet, "/api/audit", "")
	rr := httptest.NewRecorder()

	s.listAudit(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestAgentLifecycleLocksArePerAgent(t *testing.T) {
	s := &Server{}
	unlockA := s.lockAgentLifecycle("agent-a")

	acquiredB := make(chan struct{})
	go func() {
		unlock := s.lockAgentLifecycle("agent-b")
		close(acquiredB)
		unlock()
	}()

	select {
	case <-acquiredB:
	case <-time.After(time.Second):
		t.Fatal("agent-b was blocked by agent-a lifecycle lock")
	}

	reacquiredA := make(chan struct{})
	go func() {
		unlock := s.lockAgentLifecycle("agent-a")
		close(reacquiredA)
		unlock()
	}()
	select {
	case <-reacquiredA:
		t.Fatal("a second lifecycle operation bypassed the agent-a fence")
	case <-time.After(25 * time.Millisecond):
	}
	unlockA()
	select {
	case <-reacquiredA:
	case <-time.After(time.Second):
		t.Fatal("agent-a lifecycle lock was not released")
	}
}

func TestRestrictedPrincipalCannotPromoteUserToAdminIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newAuthorizationTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	client, err := st.CreateClient(ctx, "tenant-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := st.CreateUserWithScopes(ctx, "tenant-admin-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateUserWithScopes(ctx, "target-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}})
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{store: st, cfg: &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}}
	req := restrictedAdminRequest(http.MethodPatch, "/api/users/"+target.ID, `{"role":"admin"}`)
	requestCtx := context.WithValue(req.Context(), actorKey, actor.ID)
	requestCtx = context.WithValue(requestCtx, scopeUserKey, actor.ID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", target.ID)
	requestCtx = context.WithValue(requestCtx, chi.RouteCtxKey, routeCtx)
	req = req.WithContext(requestCtx)
	rr := httptest.NewRecorder()
	s.updateUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	got, err := st.GetUserByID(ctx, target.ID)
	if err != nil || got.Role != models.RoleTech {
		t.Fatalf("target role = %v, %v; want tech", got, err)
	}

	unrestricted, err := st.CreateUser(ctx, "global-admin-"+suffix, "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, unrestricted.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	identity, err := s.authenticateToken(ctx, "/api/me", token)
	if err != nil || identity.scopeUser != "" {
		t.Fatalf("unrestricted identity scope=%q err=%v", identity.scopeUser, err)
	}
	if err := st.ReplaceUserScopes(ctx, unrestricted.ID,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}}); err != nil {
		t.Fatal(err)
	}
	identity, err = s.authenticateToken(ctx, "/api/me", token)
	if err != nil || identity.scopeUser != unrestricted.ID {
		t.Fatalf("scoped admin identity scope=%q err=%v", identity.scopeUser, err)
	}

	agent, err := st.CreateAgent(ctx, "relay-agent", "linux", "amd64", "1.0.0", "agent-token", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, "viewer-"+suffix, "hash", models.RoleReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken := auth.NewToken()
	if err := st.CreateSession(ctx, viewer.ID, auth.Hash(viewerToken), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	remoteSession := &models.RemoteSession{
		AgentID: agent.ID, Provider: "native", Status: "pending", ViewerTicket: "viewer-ticket",
		CreatedBy: viewer.ID, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := st.CreateRemoteSession(ctx, remoteSession); err != nil {
		t.Fatal(err)
	}
	roleKey := "no_remote_" + strings.ReplaceAll(suffix, "-", "")
	if _, err := st.CreateRole(ctx, &models.Role{Key: roleKey, Name: "No remote"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserRole(ctx, viewer.ID, roleKey); err != nil {
		t.Fatal(err)
	}
	relayReq := httptest.NewRequest(http.MethodGet,
		"http://console.example/ws/remote?session="+remoteSession.ID+"&role=viewer", nil)
	relayReq.Header.Set("Origin", "http://console.example")
	relayReq.Header.Set("Sec-WebSocket-Protocol", "rmm-ticket.viewer-ticket, rmm-auth."+viewerToken)
	relayRR := httptest.NewRecorder()
	s.handleRelayWS(relayRR, relayReq)
	if relayRR.Code != http.StatusForbidden {
		t.Fatalf("relay status = %d, want 403 after permission revocation; body=%s", relayRR.Code, relayRR.Body.String())
	}

	passwordHash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	mfaUser, err := st.CreateUser(ctx, "mfa-"+suffix, passwordHash, models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	setupReq := httptest.NewRequest(http.MethodPost, "/api/me/2fa/setup", bytes.NewBufferString(`{}`))
	setupReq = setupReq.WithContext(context.WithValue(setupReq.Context(), actorKey, mfaUser.ID))
	setupRR := httptest.NewRecorder()
	s.setup2FA(setupRR, setupReq)
	if setupRR.Code != http.StatusUnauthorized {
		t.Fatalf("MFA setup without password status=%d, want 401", setupRR.Code)
	}
	setupReq = httptest.NewRequest(http.MethodPost, "/api/me/2fa/setup",
		bytes.NewBufferString(`{"current_password":"correct horse battery staple"}`))
	setupReq = setupReq.WithContext(context.WithValue(setupReq.Context(), actorKey, mfaUser.ID))
	setupRR = httptest.NewRecorder()
	s.setup2FA(setupRR, setupReq)
	if setupRR.Code != http.StatusOK {
		t.Fatalf("MFA setup with password status=%d, body=%s", setupRR.Code, setupRR.Body.String())
	}
	pending, err := st.GetPendingUserTOTP(ctx, mfaUser.ID)
	if err != nil || pending == "" {
		t.Fatalf("pending MFA secret=%q err=%v", pending, err)
	}
	activeSecret, enabled, err := st.GetUserTOTP(ctx, mfaUser.ID)
	if err != nil || activeSecret != "" || enabled {
		t.Fatalf("pending MFA setup changed active factor: secret=%q enabled=%t err=%v", activeSecret, enabled, err)
	}
}

func TestRouterAuthAndTenantScopeIsolation(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newAuthorizationTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	allowedClient, err := st.CreateClient(ctx, "allowed-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	otherClient, err := st.CreateClient(ctx, "other-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	allowedSite, err := st.CreateSite(ctx, allowedClient.ID, "site-a")
	if err != nil {
		t.Fatal(err)
	}
	otherSite, err := st.CreateSite(ctx, otherClient.ID, "site-b")
	if err != nil {
		t.Fatal(err)
	}
	allowedAgent, err := st.CreateAgent(ctx, "allowed-agent", "linux", "amd64", "1.0.0", "agent-token", &allowedSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := st.CreateAgent(ctx, "other-agent", "linux", "amd64", "1.0.0", "other-agent-token", &otherSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUserWithScopes(ctx, "scoped-tech-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: allowedClient.ID}})
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{cfg: &config.Config{APIRateLimit: 1000}, store: st}).Router()
	request := func(target string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/clients", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated clients status = %d, want 401", unauthenticated.Code)
	}

	clientsResponse := httptest.NewRecorder()
	handler.ServeHTTP(clientsResponse, request("/api/clients"))
	if clientsResponse.Code != http.StatusOK {
		t.Fatalf("clients status = %d, body=%s", clientsResponse.Code, clientsResponse.Body.String())
	}
	var clients struct {
		Clients []models.Client `json:"clients"`
	}
	if err := json.NewDecoder(clientsResponse.Body).Decode(&clients); err != nil {
		t.Fatal(err)
	}
	if len(clients.Clients) != 1 || clients.Clients[0].ID != allowedClient.ID {
		t.Fatalf("visible clients = %#v, want only %s", clients.Clients, allowedClient.ID)
	}

	devicesResponse := httptest.NewRecorder()
	handler.ServeHTTP(devicesResponse, request("/api/devices"))
	if devicesResponse.Code != http.StatusOK {
		t.Fatalf("devices status = %d, body=%s", devicesResponse.Code, devicesResponse.Body.String())
	}
	var devices struct {
		Devices []models.Agent `json:"devices"`
	}
	if err := json.NewDecoder(devicesResponse.Body).Decode(&devices); err != nil {
		t.Fatal(err)
	}
	if len(devices.Devices) != 1 || devices.Devices[0].ID != allowedAgent.ID {
		t.Fatalf("visible devices = %#v, want only %s", devices.Devices, allowedAgent.ID)
	}

	outsideDevice := httptest.NewRecorder()
	handler.ServeHTTP(outsideDevice, request("/api/devices/"+otherAgent.ID))
	if outsideDevice.Code != http.StatusForbidden {
		t.Fatalf("outside device status = %d, want 403; body=%s", outsideDevice.Code, outsideDevice.Body.String())
	}
	outsideSite := httptest.NewRecorder()
	handler.ServeHTTP(outsideSite, request("/api/sites?client_id="+otherClient.ID))
	if outsideSite.Code != http.StatusForbidden {
		t.Fatalf("outside site status = %d, want 403; body=%s", outsideSite.Code, outsideSite.Body.String())
	}
}

func newAuthorizationTestStore(t *testing.T, databaseURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "api_authorization_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	database, err := url.Parse(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	query := database.Query()
	query.Set("search_path", schemaName)
	database.RawQuery = query.Encode()
	st, err := store.New(ctx, database.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return st
}
