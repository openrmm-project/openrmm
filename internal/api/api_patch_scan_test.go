package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

func TestPatchScanAPIOutcomesAndScope(t *testing.T) {
	dsn := os.Getenv("RMM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "scan_api", dsn)
	h := hub.New()
	server := &Server{cfg: &config.Config{APIRateLimit: 10000}, store: st, hub: h, patches: patches.New(st, h)}
	admin, err := st.CreateUser(ctx, "scan-admin", "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err = st.CreateSession(ctx, admin.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f := &policyAPIFixture{t: t, store: st, handler: server.Router(), admin: admin, token: token}
	create := func(name string) *models.Agent {
		a, err := st.CreateAgent(ctx, name, "linux", "amd64", "test", name, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	online, offline, other := create("online"), create("offline"), create("other")
	outbound := h.Add(online.ID, nil)
	if rec := f.do(http.MethodPost, "/api/devices/"+online.ID+"/patches/scan", `{"refresh_metadata":"yes"}`); rec.Code != 400 {
		t.Fatalf("invalid payload: %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/api/devices/"+online.ID+"/patches/scan", `{"refresh_metadata":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch: %d %s", rec.Code, rec.Body)
	}
	var response struct {
		RequestID  string `json:"request_id"`
		Dispatched int    `json:"dispatched"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &response)
	if response.RequestID == "" || response.Dispatched != 1 {
		t.Fatalf("%s", rec.Body)
	}
	var frame protocol.Envelope
	var request protocol.PatchScanMsg
	_ = json.Unmarshal(<-outbound, &frame)
	_ = json.Unmarshal(frame.Data, &request)
	if !request.RefreshMetadata || request.RequestID != response.RequestID {
		t.Fatalf("wire request: %+v", request)
	}
	if rec = f.do(http.MethodPost, "/api/devices/"+online.ID+"/patches/scan", ""); rec.Code != http.StatusConflict {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
	if rec = f.do(http.MethodPost, "/api/devices/"+offline.ID+"/patches/scan", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline: %d", rec.Code)
	}
	state, err := st.GetPatchScanState(ctx, offline.ID)
	if err != nil || state.Status != "offline" {
		t.Fatalf("offline state: %+v %v", state, err)
	}
	// Read endpoints expose pending state separately from the patch inventory.
	rec = f.do(http.MethodGet, "/api/devices/"+online.ID+"/patches", "")
	var result struct {
		ScanState models.PatchScanState `json:"scan_state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if rec.Code != 200 || result.ScanState.Status != "pending" {
		t.Fatalf("device state: %d %s", rec.Code, rec.Body)
	}
	// A replaced transport cannot release the current pending request.
	replaced := h.Add(online.ID, nil)
	server.detachAgentConnection(ctx, online.ID, outbound)
	state, _ = st.GetPatchScanState(ctx, online.ID)
	if state.Status != "pending" {
		t.Fatal("replaced transport released work")
	}
	server.detachAgentConnection(ctx, online.ID, replaced)
	state, _ = st.GetPatchScanState(ctx, online.ID)
	if state.Status != "offline" {
		t.Fatal("disconnect did not release pending scan")
	}
	outbound = h.Add(online.ID, nil)
	rec = f.do(http.MethodPost, "/api/patches/scan", `{"agent_ids":["`+online.ID+`","`+offline.ID+`","`+online.ID+`"],"refresh_metadata":true}`)
	var counts map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &counts)
	if rec.Code != 202 || counts["dispatched"] != 1 || counts["offline"] != 1 {
		t.Fatalf("fleet: %d %s", rec.Code, rec.Body)
	}
	_ = <-outbound
	rec = f.do(http.MethodPost, "/api/patches/scan", `{"agent_ids":["`+online.ID+`"]}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &counts)
	if counts["busy"] != 1 || counts["dispatched"] != 0 {
		t.Fatalf("fleet busy: %s", rec.Body)
	}
	// Constrain this principal to one client and place the agents in distinct clients.
	clientA, _ := st.CreateClient(ctx, "client-a")
	clientB, _ := st.CreateClient(ctx, "client-b")
	siteA, _ := st.CreateSite(ctx, clientA.ID, "a")
	siteB, _ := st.CreateSite(ctx, clientB.ID, "b")
	if err = st.SetAgentSite(ctx, online.ID, &siteA.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAgentSite(ctx, other.ID, &siteB.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.ReplaceUserScopes(ctx, admin.ID, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: clientA.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/devices/" + other.ID + "/patches", "/api/devices/" + other.ID + "/patches/scan"} {
		method := http.MethodGet
		if target[len(target)-4:] == "scan" {
			method = http.MethodPost
		}
		if rec = f.do(method, target, ""); rec.Code != 403 {
			t.Fatalf("cross-client %s: %d %s", target, rec.Code, rec.Body)
		}
	}
	if rec = f.do(http.MethodPost, "/api/patches/scan", `{"agent_ids":["`+other.ID+`"]}`); rec.Code != 403 {
		t.Fatalf("cross-client fleet: %d", rec.Code)
	}
	rec = f.do(http.MethodGet, "/api/reports/compliance", "")
	var compliance struct {
		Rows []models.ComplianceRow `json:"rows"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &compliance)
	if rec.Code != 200 || len(compliance.Rows) != 1 || compliance.Rows[0].AgentID != online.ID || compliance.Rows[0].ScanState == nil {
		t.Fatalf("scoped compliance: %d %s", rec.Code, rec.Body)
	}
}

func TestPatchScanDistinguishesOfflineFromBackpressure(t *testing.T) {
	f := newScriptFixture(t)
	h := hub.New()
	f.handler = (&Server{
		cfg: &config.Config{APIRateLimit: 10000}, store: f.store,
		patches: patches.New(f.store, h),
	}).Router()
	device, err := f.store.CreateAgent(context.Background(), "scan-target", "linux", "amd64", "1.0", "synthetic-scan-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/devices/" + device.ID + "/patches/scan"
	scan := func(status int, wantKey, wantValue string) map[string]any {
		t.Helper()
		res := f.do(http.MethodPost, path, "")
		if res.Code != status {
			t.Fatalf("scan returned %d, want %d: %s", res.Code, status, res.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body[wantKey] != wantValue {
			t.Fatalf("response %v, want %s=%q", body, wantKey, wantValue)
		}
		return body
	}

	scan(http.StatusServiceUnavailable, "code", "device_offline")
	if state, err := f.store.GetPatchScanState(context.Background(), device.ID); err != nil || state.Status != "offline" {
		t.Fatalf("offline scan state: %+v %v", state, err)
	}
	if f.audit("patch.scan") != nil {
		t.Fatal("an offline request should not be recorded as dispatched")
	}

	frames := h.Add(device.ID, nil)
	for range cap(frames) {
		frames <- []byte("queued")
	}
	body := scan(http.StatusServiceUnavailable, "error", "internal server error")
	if body["code"] != nil {
		t.Fatalf("a full queue must not be reported as offline: %v", body)
	}
	if state, err := f.store.GetPatchScanState(context.Background(), device.ID); err != nil || state.Status != "failed" || state.ErrorCode != "scan_failed" {
		t.Fatalf("backpressure scan state: %+v %v", state, err)
	}
	fleet := f.do(http.MethodPost, "/api/patches/scan", `{"agent_ids":["`+device.ID+`"]}`)
	var counts map[string]int
	if err := json.Unmarshal(fleet.Body.Bytes(), &counts); err != nil || fleet.Code != http.StatusAccepted || counts["failed"] != 1 || counts["offline"] != 0 {
		t.Fatalf("backpressure fleet returned %d: %s (%v)", fleet.Code, fleet.Body, err)
	}
	for range cap(frames) {
		<-frames
	}

	scan(http.StatusAccepted, "status", "scanning")
	select {
	case frame := <-frames:
		var message protocol.Envelope
		if err := json.Unmarshal(frame, &message); err != nil || message.Type != protocol.MsgPatchScan {
			t.Fatalf("expected patch scan frame, got %q (%v)", frame, err)
		}
	default:
		t.Fatal("scan accepted without dispatch")
	}
	if f.audit("patch.scan") == nil {
		t.Fatal("dispatched scan was not audited")
	}
}
