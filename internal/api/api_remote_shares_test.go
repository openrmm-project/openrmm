package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

func TestCleanGuestName(t *testing.T) {
	for in, want := range map[string]string{
		"  Maria  Souza ":               "Maria Souza",
		"Maria\tSouza\n":                "Maria Souza",
		"Ma\x00ria\x1b[31m":             "Maria[31m",
		"   ":                           "",
		"José da Silva — Suporte":       "José da Silva — Suporte",
		strings.Repeat("a", 200):        strings.Repeat("a", guestNameMax),
		"\u202eevil\u202c name":         "evil name",
		"Ana" + strings.Repeat(" ", 80): "Ana",
	} {
		if got := cleanGuestName(in); got != want {
			t.Errorf("cleanGuestName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanHeaderValue(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes of two-byte characters
	got := cleanHeaderValue(long, guestUserAgentMax)
	if !utf8.ValidString(got) || len(got) > guestUserAgentMax {
		t.Fatalf("cut a character in half or kept too much: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got := cleanHeaderValue("Mozilla\xff\xfe/5.0\x01", 100); got != "Mozilla/5.0" {
		t.Fatalf("invalid bytes and controls kept: %q", got)
	}
	if got := cleanHeaderValue("203.0.113.9", guestIPMax); got != "203.0.113.9" {
		t.Fatalf("a plain address changed: %q", got)
	}
}

// One session per guestBrokerInterval per share: an accepted guest cannot make
// the device start sidecar after sidecar.
func TestGuestBrokerInterval(t *testing.T) {
	g := newGuestSessions()
	now := time.Now()
	if !g.allowBroker("s1", now) {
		t.Fatal("the first session was refused")
	}
	if g.allowBroker("s1", now.Add(time.Second)) {
		t.Fatal("a second session one second later was allowed")
	}
	if !g.allowBroker("s2", now.Add(time.Second)) {
		t.Fatal("another share was held back by this one")
	}
	if !g.allowBroker("s1", now.Add(guestBrokerInterval+time.Millisecond)) {
		t.Fatal("a reconnect after the interval was refused")
	}
}

func TestGuestShareCode(t *testing.T) {
	six := regexp.MustCompile(`^[0-9]{6}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := guestShareCode()
		if err != nil {
			t.Fatal(err)
		}
		if !six.MatchString(code) {
			t.Fatalf("code %q is not six digits", code)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 codes gave only %d distinct values", len(seen))
	}
	// The hash binds the code to its link: the same code under another token
	// does not match, so a leaked table cannot be reversed code by code.
	if guestCodeHash("token-a", "123456") == guestCodeHash("token-b", "123456") {
		t.Fatal("the code hash does not depend on the link")
	}
}

func TestGuestCredentialHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/guest/request", nil)
	req.Header.Set("Authorization", "Guest abc")
	if got := guestCredential(req); got != "abc" {
		t.Fatalf("guestCredential = %q", got)
	}
	req.Header.Set("Authorization", "Bearer abc")
	if got := guestCredential(req); got != "" {
		t.Fatalf("an operator token was read as a guest credential: %q", got)
	}
	ws := httptest.NewRequest(http.MethodGet, "/ws/remote", nil)
	ws.Header.Set("Sec-WebSocket-Protocol", "rmm-v1, rmm-ticket.t, rmm-guest.cred")
	if got := guestRelayCredential(ws); got != "cred" {
		t.Fatalf("guestRelayCredential = %q", got)
	}
}

// The registry keeps one session per share, lets a reconnect replace the
// previous one, and does not let a stale session's end forget the new one.
func TestGuestSessionsRegistry(t *testing.T) {
	g := newGuestSessions()
	if prev := g.start(&guestSession{shareID: "s1", sessionID: "a", agentID: "dev", sponsor: "tech"}); prev != nil {
		t.Fatal("a first session replaced something")
	}
	if prev := g.start(&guestSession{shareID: "s1", sessionID: "b", agentID: "dev", sponsor: "tech"}); prev == nil || prev.sessionID != "a" {
		t.Fatal("a reconnect did not replace the previous session")
	}
	g.finish("s1", "a")
	if !g.owns("s1", "b") {
		t.Fatal("the stale session's end forgot the live one")
	}
	g.start(&guestSession{shareID: "s2", sessionID: "c", agentID: "dev", sponsor: "other"})
	if got := g.sponsoredBy("dev", "tech"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("sponsoredBy = %v", got)
	}
	if gs := g.take("s1"); gs == nil || g.connected("s1") {
		t.Fatal("take did not remove the session")
	}
	var nilRegistry *guestSessions
	if nilRegistry.connected("s1") || nilRegistry.owns("s1", "b") || nilRegistry.take("s1") != nil {
		t.Fatal("a nil registry claimed a session")
	}
}

// The whole invitation, against a real database: issue, burn, decide, connect,
// remove — and each refusal along the way.
func TestRemoteShareLifecycle(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st := newTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	agent, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "windows", "amd64", "1.0.0", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	tech, err := st.CreateUser(ctx, "tech-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	other, err := st.CreateUser(ctx, "other-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	techToken, otherToken := auth.NewToken(), auth.NewToken()
	must(st.CreateSession(ctx, tech.ID, auth.Hash(techToken), time.Now().Add(time.Hour)))
	must(st.CreateSession(ctx, other.ID, auth.Hash(otherToken), time.Now().Add(time.Hour)))

	h := hub.New()
	agentFrames := h.Add(agent.ID, func() {})
	srv := New(&config.Config{APIRateLimit: 100000}, st, h, nil, remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), nil, nil, nil)
	handler := srv.Router()
	call := func(method, path, authz string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.RemoteAddr = "198.51.100." + uuid.NewString()[:2] + ":1234"
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	asTech, asOther := "Bearer "+techToken, "Bearer "+otherToken
	type created struct {
		Share models.RemoteShare `json:"share"`
		Token string             `json:"token"`
		Code  string             `json:"code"`
	}
	creator := &asTech
	create := func(body map[string]any) created {
		t.Helper()
		rec := call(http.MethodPost, "/api/devices/"+agent.ID+"/remote-shares", *creator, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		var c created
		must(json.Unmarshal(rec.Body.Bytes(), &c))
		return c
	}

	// Off by default: nothing can be shared until an administrator allows it.
	if rec := call(http.MethodPost, "/api/devices/"+agent.ID+"/remote-shares", asTech, map[string]any{"mode": "view"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "guest_access_disabled") {
		t.Fatalf("an invitation was created with guest access off: %d %s", rec.Code, rec.Body)
	}
	on := true
	var save settings.SaveRequest
	save.Remote.GuestAccess = &on
	_, err = settings.Save(ctx, st, srv.cfg, save)
	must(err)

	if rec := call(http.MethodPost, "/api/devices/"+agent.ID+"/remote-shares", asTech, map[string]any{"mode": "admin"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown mode was accepted: %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/devices/"+agent.ID+"/remote-shares", asTech, map[string]any{"minutes": 100000}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unbounded lifetime was accepted: %d", rec.Code)
	}

	inv := create(map[string]any{"mode": "view", "minutes": 15})
	if len(inv.Token) != 64 || len(inv.Code) != 6 || inv.Share.Status != models.RemoteShareOpen {
		t.Fatalf("unexpected invitation: %+v", inv)
	}

	// The link names the technician, and nothing about the device.
	rec := call(http.MethodPost, "/api/guest/lookup", "", map[string]any{"token": inv.Token})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tech.Username) || strings.Contains(rec.Body.String(), agent.Hostname) {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body)
	}

	// A wrong code is refused and counted; the right one burns the link.
	if rec := call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": inv.Token, "code": wrongCode(inv.Code), "name": "Maria"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a wrong code was accepted: %d %s", rec.Code, rec.Body)
	}
	rec = call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": inv.Token, "code": inv.Code, "name": "  Maria  Souza "})
	if rec.Code != http.StatusOK {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	var joined struct {
		Credential string `json:"guest_credential"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &joined))
	asGuest := "Guest " + joined.Credential
	if rec := call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": inv.Token, "code": inv.Code, "name": "Second"}); rec.Code != http.StatusNotFound {
		t.Fatalf("a burned link admitted a second guest: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/guest/request", asGuest, nil); !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("guest request: %d %s", rec.Code, rec.Body)
	}

	// Only the technician who sent it decides, and only from their own session.
	if rec := call(http.MethodPost, "/api/remote-shares/"+inv.Share.ID+"/accept", asOther, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another technician accepted the guest: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/remote-shares/"+inv.Share.ID+"/accept", asTech, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "sponsor_session_required") {
		t.Fatalf("accepted without the technician's session open: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/guest/session", asGuest, nil); rec.Code != http.StatusConflict {
		t.Fatalf("a waiting guest got a session: %d %s", rec.Code, rec.Body)
	}
	leaveTech := srv.presence.Join(remote.Viewer{SessionID: "tech-tab", AgentID: agent.ID, User: tech.ID, Kind: "native"})
	if rec := call(http.MethodGet, "/api/remote-shares/pending?agent_id="+agent.ID, asTech, nil); !strings.Contains(rec.Body.String(), "Maria Souza") {
		t.Fatalf("the waiting guest is not shown to the technician: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/remote-shares/"+inv.Share.ID+"/accept", asTech, nil); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}

	// The guest's own session: a native one, owned by the guest principal,
	// told to the endpoint without file transfer.
	rec = call(http.MethodPost, "/api/guest/session", asGuest, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("guest session: %d %s", rec.Code, rec.Body)
	}
	var sess struct {
		ID     string `json:"id"`
		Ticket string `json:"viewer_ticket"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &sess))
	select {
	case frame := <-agentFrames:
		var env protocol.Envelope
		must(json.Unmarshal(frame, &env))
		var start protocol.RemoteStartMsg
		must(json.Unmarshal(env.Data, &start))
		if start.SessionID != sess.ID || start.AllowFiles == nil || *start.AllowFiles {
			t.Fatalf("the endpoint was asked for %+v", start)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the endpoint was never asked to start the guest's session")
	}
	meta, err := st.GetRemoteSessionForAuthorization(ctx, sess.ID)
	must(err)
	if meta.CreatedBy != models.GuestPrincipal(inv.Share.ID) || meta.Provider != "native" {
		t.Fatalf("guest session recorded as %+v", meta)
	}
	if !srv.guests.owns(inv.Share.ID, sess.ID) {
		t.Fatal("the guest's session is not tracked")
	}

	// The technician leaves: the guest's session ends with them, the share
	// stays accepted, and no new session is brokered until they are back.
	leaveTech()
	srv.sponsorLeft(agent.ID, tech.ID)
	if srv.guests.connected(inv.Share.ID) {
		t.Fatal("the guest stayed on after the technician left")
	}
	if rec := call(http.MethodPost, "/api/guest/session", asGuest, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "sponsor_absent") {
		t.Fatalf("a guest got a session without the technician: %d %s", rec.Code, rec.Body)
	}

	// Removing the guest ends the share for good.
	if rec := call(http.MethodDelete, "/api/remote-shares/"+inv.Share.ID, asTech, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ended"`) {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/guest/request", asGuest, nil); !strings.Contains(rec.Body.String(), `"status":"ended"`) {
		t.Fatalf("a removed guest is not told: %s", rec.Body)
	}

	// Five wrong codes revoke a link, the right code included afterwards.
	burned := create(map[string]any{"mode": "control", "minutes": 60})
	for i := 0; i < guestCodeAttempts; i++ {
		call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": burned.Token, "code": wrongCode(burned.Code), "name": "Guess"})
	}
	if rec := call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": burned.Token, "code": burned.Code, "name": "Late"}); rec.Code != http.StatusNotFound {
		t.Fatalf("a link survived five wrong codes: %d %s", rec.Code, rec.Body)
	}

	// A guest whose technician is disabled loses the screen within one sweep,
	// and a guest cannot ask for session after session.
	swept := create(map[string]any{"mode": "view", "minutes": 60})
	rec = call(http.MethodPost, "/api/guest/join", "", map[string]any{"token": swept.Token, "code": swept.Code, "name": "Swept"})
	must(json.Unmarshal(rec.Body.Bytes(), &joined))
	asSwept := "Guest " + joined.Credential
	srv.presence.Join(remote.Viewer{SessionID: "tech-tab-2", AgentID: agent.ID, User: tech.ID, Kind: "native"})
	if rec := call(http.MethodPost, "/api/remote-shares/"+swept.Share.ID+"/accept", asTech, nil); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/guest/session", asSwept, nil); rec.Code != http.StatusCreated {
		t.Fatalf("guest session: %d %s", rec.Code, rec.Body)
	}
	<-agentFrames
	if rec := call(http.MethodPost, "/api/guest/session", asSwept, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a second session right away was not held back: %d %s", rec.Code, rec.Body)
	}
	_, err = st.SetUserDisabled(ctx, tech.ID, true)
	must(err)
	srv.sweepGuests(ctx)
	if srv.guests.connected(swept.Share.ID) {
		t.Fatal("the guest kept the screen after their technician was disabled")
	}

	// Turning guest access off withdraws every open invitation.
	creator = &asOther
	open := create(map[string]any{"mode": "view", "minutes": 60})
	body, _ := json.Marshal(map[string]any{"remote": map[string]any{"guest_access": false}})
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body)))
	req.Header.Set("Authorization", asOther)
	req.Header.Set("Content-Type", "application/json")
	cfgRec := httptest.NewRecorder()
	handler.ServeHTTP(cfgRec, req)
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("config update: %d %s", cfgRec.Code, cfgRec.Body)
	}
	if sh, err := st.GetRemoteShare(ctx, open.Share.ID); err != nil || sh.Status != models.RemoteShareRevoked {
		t.Fatalf("an invitation survived guest access being turned off: %+v %v", sh, err)
	}

	for _, action := range []string{"remote.share.create", "remote.share.code_failed", "remote.share.redeem", "remote.share.accept", "remote.share.guest_start", "remote.share.remove", "remote.share.revoke"} {
		entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: action, Target: agent.ID, Limit: 5})
		must(err)
		if len(entries) == 0 {
			t.Errorf("no %s audit record", action)
		}
	}
}

// wrongCode is a six-digit code that is not the right one.
func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}
