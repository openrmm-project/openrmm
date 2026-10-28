package supportlink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "ors_" + "abcdefghijABCDEFGHIJ0123456789-_abcdefghijk"

func TestValidKey(t *testing.T) {
	for key, want := range map[string]bool{
		testKey:                        true,
		"":                             false,
		"ors_short":                    false,
		"xyz_" + testKey[4:]:           false,
		testKey + "x":                  false,
		testKey[:len(testKey)-1] + "=": false,
		testKey[:len(testKey)-1] + " ": false,
	} {
		if got := ValidKey(key); got != want {
			t.Errorf("ValidKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestValidEndpoint(t *testing.T) {
	for endpoint, want := range map[string]bool{
		DefaultEndpoint:                       true,
		"https://staging.openrmm.io/api/x":    true,
		"http://openrmm.io/api/support/usage": false,
		"https://user:pw@openrmm.io/api":      false,
		"https://openrmm.io/api?x=1":          false,
		"https://openrmm.io/api#f":            false,
		"openrmm.io/api":                      false,
		"":                                    false,
	} {
		if got := ValidEndpoint(endpoint); got != want {
			t.Errorf("ValidEndpoint(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

// portal answers like the real one, and records what it was sent.
func portal(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request, *[]Report) {
	t.Helper()
	var reqs []*http.Request
	var reports []Report
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		var rep Report
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &rep)
		reports = append(reports, rep)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &reports
}

func TestSendReportsAndReadsTheAnswer(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	var srv *httptest.Server
	srv, reqs, reports := portal(t, 200, "")
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reqs = append(*reqs, r)
		var rep Report
		_ = json.NewDecoder(r.Body).Decode(&rep)
		*reports = append(*reports, rep)
		_, _ = io.WriteString(w, `{"status":"active","plan_devices":100,"billing_interval":"year",
			"paid_through":"2027-10-28T00:00:00Z","over_limit":true,"action_required_by":"2026-11-11T00:00:00Z",
			"portal_url":"`+srv.URL+`/portal"}`)
	})
	c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/api/support/usage", Key: testKey}
	st, err := c.Send(context.Background(), Report{DevicesUsed: 130, Version: "0.9.0", InstanceID: "inst-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("one request, got %d", len(*reqs))
	}
	r := (*reqs)[0]
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+testKey {
		t.Fatalf("method %s, authorization %q", r.Method, r.Header.Get("Authorization"))
	}
	if got := (*reports)[0]; got != (Report{DevicesUsed: 130, Version: "0.9.0", InstanceID: "inst-1"}) {
		t.Fatalf("sent %+v", got)
	}
	want := State{Status: "active", PlanDevices: 100, BillingInterval: "year", DevicesUsed: 130, OverLimit: true,
		PaidThrough: time.Date(2027, 10, 28, 0, 0, 0, 0, time.UTC), ActionRequiredBy: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		PortalURL: srv.URL + "/portal", CheckedAt: now}
	if st != want {
		t.Fatalf("state\n got %+v\nwant %+v", st, want)
	}
}

// Only three values leave the console: nothing about its devices, users or
// address.
func TestTheReportCarriesOnlyThreeValues(t *testing.T) {
	b, _ := json.Marshal(Report{DevicesUsed: 1, Version: "0.9.0", InstanceID: "x"})
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if len(fields) != 3 {
		t.Fatalf("the report has %d fields: %s", len(fields), b)
	}
}

func TestSendRefusesBadAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         `nope`,
		"no status":        `{"plan_devices":10,"billing_interval":"month"}`,
		"unknown status":   `{"status":"unlimited","plan_devices":10,"billing_interval":"month"}`,
		"no plan_devices":  `{"status":"active","billing_interval":"month"}`,
		"negative devices": `{"status":"active","plan_devices":-1,"billing_interval":"month"}`,
		"huge devices":     `{"status":"active","plan_devices":1000001,"billing_interval":"month"}`,
		"bad interval":     `{"status":"active","plan_devices":10,"billing_interval":"week"}`,
		"bad date":         `{"status":"active","plan_devices":10,"billing_interval":"month","paid_through":"tomorrow"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _ := portal(t, 200, body)
			c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/u", Key: testKey}
			if _, err := c.Send(context.Background(), Report{}, time.Now()); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSendReportsARejectedKey(t *testing.T) {
	for _, code := range []int{401, 403} {
		srv, _, _ := portal(t, code, `{"error":"unknown key"}`)
		c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/u", Key: testKey}
		if _, err := c.Send(context.Background(), Report{}, time.Now()); !errors.Is(err, ErrKeyRejected) {
			t.Fatalf("HTTP %d: %v", code, err)
		}
	}
	srv, _, _ := portal(t, 500, ``)
	c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/u", Key: testKey}
	if _, err := c.Send(context.Background(), Report{}, time.Now()); err == nil || errors.Is(err, ErrKeyRejected) {
		t.Fatalf("a server error is not a rejected key: %v", err)
	}
}

// The key never follows a redirect, and nothing is sent without HTTPS or a
// well-formed key.
func TestSendKeepsTheKeyWhereItBelongs(t *testing.T) {
	var elsewhere int
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
	}))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Endpoint: srv.URL + "/u", Key: testKey}
	if _, err := c.Send(context.Background(), Report{}, time.Now()); err == nil {
		t.Fatal("a redirect was taken as an answer")
	}
	if elsewhere != 0 {
		t.Fatal("the redirect was followed with the key")
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
	}))
	defer plain.Close()
	for _, c := range []*Client{
		{Endpoint: plain.URL + "/u", Key: testKey},
		{HTTP: srv.Client(), Endpoint: srv.URL + "/u", Key: "ors_bad"},
	} {
		if _, err := c.Send(context.Background(), Report{}, time.Now()); err == nil {
			t.Fatalf("sent to %s with key %q", c.Endpoint, c.Key)
		}
	}
	if elsewhere != 0 {
		t.Fatal("a request left without HTTPS or a valid key")
	}
}

func TestPortalLinkStaysOnThePortal(t *testing.T) {
	endpoint := "https://openrmm.io/api/support/usage"
	for raw, want := range map[string]string{
		"https://openrmm.io/portal":              "https://openrmm.io/portal",
		"https://OPENRMM.io/portal":              "https://OPENRMM.io/portal",
		"http://openrmm.io/portal":               "",
		"https://evil.example/portal":            "",
		"https://openrmm.io.evil.example/portal": "",
		"https://user:pw@openrmm.io/portal":      "",
		"javascript:alert(1)":                    "",
	} {
		if got := sameHostHTTPS(raw, endpoint); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
	if !strings.HasPrefix(DefaultEndpoint, "https://openrmm.io/") {
		t.Fatal("the default endpoint is the OpenRMM portal")
	}
}
