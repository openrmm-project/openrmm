package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/supportlink"
)

const supportTestKey = "ors_" + "abcdefghijABCDEFGHIJ0123456789-_abcdefghijk"

// fakePortal answers support reports like the real portal and counts them.
type fakePortal struct {
	mu      sync.Mutex
	srv     *httptest.Server
	reports []supportlink.Report
	keys    []string
	status  int
}

func newFakePortal(t *testing.T) *fakePortal {
	t.Helper()
	p := &fakePortal{status: http.StatusOK}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var rep supportlink.Report
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &rep)
		p.reports = append(p.reports, rep)
		p.keys = append(p.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if p.status != http.StatusOK {
			w.WriteHeader(p.status)
			return
		}
		_, _ = io.WriteString(w, `{"status":"active","plan_devices":2,"billing_interval":"month",
			"paid_through":"2026-11-28T00:00:00Z","over_limit":false,"portal_url":"`+p.srv.URL+`/portal"}`)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePortal) setStatus(code int) {
	p.mu.Lock()
	p.status = code
	p.mu.Unlock()
}

func (p *fakePortal) last() (supportlink.Report, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.reports)
	if n == 0 {
		return supportlink.Report{}, "", 0
	}
	return p.reports[n-1], p.keys[n-1], n
}

func (p *fakePortal) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reports)
}

func supportServer(t *testing.T) (*Server, *fakePortal, context.Context) {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	portal := newFakePortal(t)
	s := &Server{
		cfg: &config.Config{
			AdminToken: strings.Repeat("a", 40), SettingsEncryptionKey: strings.Repeat("k", 40),
			SupportEndpoint: portal.srv.URL + "/api/support/usage", Version: "0.9.0",
		},
		store: newTestStore(t, url), supportHTTP: portal.srv.Client(),
	}
	return s, portal, ctx
}

func supportCall(t *testing.T, s *Server, method, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/support", strings.NewReader(body))
	switch method {
	case http.MethodGet:
		s.supportInfo(rec, req)
	case http.MethodPut:
		s.setSupportKey(rec, req)
	case http.MethodDelete:
		s.removeSupportKey(rec, req)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A console without a key never contacts the portal, and says it has no key.
func TestSupportLinkSendsNothingWithoutAKeyIntegration(t *testing.T) {
	s, portal, ctx := supportServer(t)
	s.reportSupport(ctx)
	if portal.count() != 0 {
		t.Fatalf("a console with no key reported %d times", portal.count())
	}
	code, out := supportCall(t, s, http.MethodGet, "")
	if code != http.StatusOK || out["configured"] != false || out["state"] != nil {
		t.Fatalf("no key: %d %v", code, out)
	}
}

func TestSupportKeyIsCheckedStoredEncryptedAndNeverShownIntegration(t *testing.T) {
	s, portal, ctx := supportServer(t)

	// Not a key: refused before anything is sent.
	if code, _ := supportCall(t, s, http.MethodPut, `{"key":"hello"}`); code != http.StatusBadRequest {
		t.Fatalf("malformed key: %d", code)
	}
	if portal.count() != 0 {
		t.Fatal("a malformed key was sent to the portal")
	}

	// A key the portal refuses is not stored.
	portal.setStatus(http.StatusUnauthorized)
	if code, _ := supportCall(t, s, http.MethodPut, `{"key":"`+supportTestKey+`"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected key: %d", code)
	}
	if key, _ := s.supportKey(ctx); key != "" {
		t.Fatal("a key the portal refused was stored")
	}

	// A good key: checked, stored encrypted, its first report answered.
	portal.setStatus(http.StatusOK)
	code, out := supportCall(t, s, http.MethodPut, `{"key":" `+supportTestKey+` "}`)
	if code != http.StatusOK || out["configured"] != true {
		t.Fatalf("good key: %d %v", code, out)
	}
	raw, err := s.store.GetAppSetting(ctx, supportlink.KeySetting)
	if err != nil {
		t.Fatal(err)
	}
	if !raw.IsSecret || !strings.HasPrefix(raw.Value, "enc:v1:") || strings.Contains(raw.Value, supportTestKey) {
		t.Fatalf("the key is not stored encrypted: secret=%v", raw.IsSecret)
	}
	last, sentKey, _ := portal.last()
	if last.Version != "0.9.0" || last.InstanceID == "" || last.DevicesUsed != 0 {
		t.Fatalf("report %+v", last)
	}
	if sentKey != supportTestKey {
		t.Fatal("the report did not carry the key")
	}

	code, out = supportCall(t, s, http.MethodGet, "")
	body, _ := json.Marshal(out)
	if code != http.StatusOK || out["configured"] != true || strings.Contains(string(body), supportTestKey) {
		t.Fatalf("info: %d %s", code, body)
	}
	state, _ := out["state"].(map[string]any)
	if state["status"] != "active" || state["plan_devices"] != float64(2) {
		t.Fatalf("state: %v", state)
	}

	// The periodic report uses the same console id.
	s.reportSupport(ctx)
	if again, _, n := portal.last(); n < 2 || again.InstanceID != last.InstanceID {
		t.Fatal("the console id changed between reports")
	}

	// A failed report keeps the last answer and says why.
	portal.setStatus(http.StatusInternalServerError)
	s.reportSupport(ctx)
	st, ok := s.supportState(ctx)
	if !ok || st.Status != "active" || st.LastError == "" {
		t.Fatalf("after a failure: %+v", st)
	}

	// Removing the key stops the reports.
	if code, out := supportCall(t, s, http.MethodDelete, ""); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("remove: %d %v", code, out)
	}
	before := portal.count()
	s.reportSupport(ctx)
	if portal.count() != before {
		t.Fatal("a console reported after its key was removed")
	}
}

func TestCloudConsoleHasNoSupportKeyIntegration(t *testing.T) {
	s, portal, _ := supportServer(t)
	saved := supportCloud
	supportCloud = func(*Server) bool { return true }
	t.Cleanup(func() { supportCloud = saved })
	if code, _ := supportCall(t, s, http.MethodPut, `{"key":"`+supportTestKey+`"}`); code != http.StatusConflict {
		t.Fatalf("a cloud console accepted a support key: %d", code)
	}
	if portal.count() != 0 {
		t.Fatal("a cloud console reported to the support endpoint")
	}
	if _, out := supportCall(t, s, http.MethodGet, ""); out["cloud"] != true {
		t.Fatalf("info: %v", out)
	}
}
