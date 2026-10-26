package devicelimit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLevelFor(t *testing.T) {
	for _, c := range []struct {
		used, limit int
		want        Level
	}{
		{5, 0, LevelNone}, {79, 100, LevelOK}, {80, 100, LevelWarning}, {99, 100, LevelWarning},
		{100, 100, LevelLimit}, {130, 100, LevelLimit}, {0, 1, LevelOK}, {1, 1, LevelLimit},
		{3, 4, LevelOK}, {4, 5, LevelWarning},
	} {
		if got := LevelFor(c.used, c.limit); got != c.want {
			t.Errorf("LevelFor(%d, %d) = %s, want %s", c.used, c.limit, got, c.want)
		}
	}
}

func TestEnforced(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if (State{Limit: 0}).Enforced(now) {
		t.Error("no limit is never enforced")
	}
	if !(State{Limit: 100}).Enforced(now) {
		t.Error("a limit with no notice period is enforced at once")
	}
	if (State{Limit: 100, EnforceFrom: now.Add(time.Hour)}).Enforced(now) {
		t.Error("not enforced during the notice period")
	}
	if !(State{Limit: 100, EnforceFrom: now}).Enforced(now) {
		t.Error("enforced from the stated instant")
	}
}

// portal answers like the OpenRMM Cloud entitlement endpoint and records what
// it was sent.
func portal(t *testing.T, status int, reply string) (*httptest.Server, *http.Request, *map[string]int) {
	t.Helper()
	var seen http.Request
	var body map[string]int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.ReplaceAll(reply, "HOST", r.Host)))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen, &body
}

func TestFetch(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	srv, seen, body := portal(t, 200, `{"device_limit":100,"enforce_from":"2026-11-01T00:00:00Z","upgrade_url":"https://HOST/portal?plan=1"}`)
	c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/oidc/entitlement", ClientID: "client-1", ClientSecret: "s3cr3t/+="}
	st, err := c.Fetch(context.Background(), 87, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Limit != 100 || !st.EnforceFrom.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !st.FetchedAt.Equal(now) {
		t.Fatalf("unexpected state %+v", st)
	}
	if !strings.HasPrefix(st.UpgradeURL, "https://") || !strings.HasSuffix(st.UpgradeURL, "/portal?plan=1") {
		t.Errorf("upgrade url on the portal's own host is kept: %q", st.UpgradeURL)
	}
	if (*body)["devices_used"] != 87 {
		t.Errorf("the count is reported: %v", *body)
	}
	id, secret, ok := seen.BasicAuth()
	// Form-encoded inside Basic, as RFC 6749 asks and the portal decodes.
	if !ok || id != "client-1" || secret != "s3cr3t%2F%2B%3D" {
		t.Errorf("client credentials sent as Basic: %q %q %v", id, secret, ok)
	}
}

func TestFetchRefusesWhatItCannotTrust(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		status int
		reply  string
	}{
		"server error":         {500, `{"device_limit":100}`},
		"unauthorized":         {401, `{"error":"invalid_client"}`},
		"no limit field":       {200, `{"upgrade_url":"x"}`},
		"negative limit":       {200, `{"device_limit":-1}`},
		"absurd limit":         {200, `{"device_limit":1000000000}`},
		"not json":             {200, `<html>`},
		"invalid enforce_from": {200, `{"device_limit":100,"enforce_from":"tomorrow"}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _ := portal(t, c.status, c.reply)
			cl := &Client{HTTP: srv.Client(), Endpoint: srv.URL, ClientID: "id", ClientSecret: "secret"}
			if st, err := cl.Fetch(context.Background(), 1, now); err == nil {
				t.Fatalf("accepted %+v", st)
			}
		})
	}
}

func TestFetchZeroIsUnlimitedNotAnError(t *testing.T) {
	srv, _, _ := portal(t, 200, `{"device_limit":0}`)
	cl := &Client{HTTP: srv.Client(), Endpoint: srv.URL, ClientID: "id", ClientSecret: "secret"}
	st, err := cl.Fetch(context.Background(), 5, time.Now())
	if err != nil || st.Limit != 0 || st.Enforced(time.Now()) {
		t.Fatalf("zero is an explicit no-limit answer: %+v %v", st, err)
	}
}

func TestFetchKeepsLinksOnThePortal(t *testing.T) {
	for _, link := range []string{"https://evil.example/portal", "http://HOST/portal", "javascript:alert(1)", "https://user:pw@HOST/portal"} {
		srv, _, _ := portal(t, 200, `{"device_limit":10,"upgrade_url":"`+link+`"}`)
		cl := &Client{HTTP: srv.Client(), Endpoint: srv.URL, ClientID: "id", ClientSecret: "secret"}
		st, err := cl.Fetch(context.Background(), 1, time.Now())
		if err != nil || st.UpgradeURL != "" {
			t.Errorf("link %q must be dropped, got %q (%v)", link, st.UpgradeURL, err)
		}
	}
}

func TestFetchDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			leaked = true
		}
		_, _ = w.Write([]byte(`{"device_limit":999}`))
	}))
	defer elsewhere.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	cl := &Client{HTTP: srv.Client(), Endpoint: srv.URL, ClientID: "id", ClientSecret: "secret"}
	if _, err := cl.Fetch(context.Background(), 1, time.Now()); err == nil {
		t.Fatal("a redirect is an error, not an answer")
	}
	if leaked {
		t.Fatal("the credentials followed the redirect")
	}
}

func TestRefusalMessageSaysWhatToDo(t *testing.T) {
	msg := RefusalMessage(100)
	for _, want := range []string{"device limit reached", "100 devices", "increase the plan", "delete a device"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q lacks %q", msg, want)
		}
	}
}
