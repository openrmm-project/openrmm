package api

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/exports"
)

// CI-only PostgreSQL; real authenticated router and persistence. Text an
// endpoint controls (the hostname its agent reports) or that is free-form (an
// audit target) must reach the streamed CSV guarded against spreadsheet
// formulas, while ordinary values and UUIDs come out exactly as stored.
func TestStreamedCSVExportsGuardEndpointTextE2E(t *testing.T) {
	f := newPolicyAPIFixture(t)
	cfg := &config.Config{APIRateLimit: 10000, SettingsEncryptionKey: "synthetic-export-test-key"}
	f.handler = (&Server{cfg: cfg, store: f.store}).Router()
	ctx := context.Background()

	// split: a ';' list separator (pt-BR, de-DE, es-ES Excel) would make a cell of "=2+2".
	hostile, split, control := "=1+1", "SPLIT;=2+2", "WINDOWS-TEST"
	// The logged-in user is endpoint text too: the agent reports it.
	hostileUser, plainUser := "@cmd|' /C calc'!A0", `ACME\maria`
	for _, host := range []string{hostile, split, control} {
		a, err := f.store.CreateAgent(ctx, host, "windows", "amd64", "0.10.0", "hash-"+uuid.NewString(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		user := plainUser
		if host == hostile {
			user = hostileUser
		}
		if err := f.store.TouchAgentSession(ctx, a.ID, &user); err != nil {
			t.Fatal(err)
		}
	}
	freeTarget, idTarget := "@SUM(A1)", uuid.NewString()
	for _, target := range []string{freeTarget, idTarget} {
		if err := f.store.AddAudit(ctx, f.admin.ID, "csv.export_test", target, []byte(`{"note":"synthetic"}`)); err != nil {
			t.Fatal(err)
		}
	}

	get := func(claims exports.Claims) (int, [][]string) {
		t.Helper()
		token, err := exports.Mint(cfg, claims)
		if err != nil {
			t.Fatal(err)
		}
		r := f.do(http.MethodGet, "/api/exports/csv?token="+url.QueryEscape(token), "")
		if r.Code != http.StatusOK {
			return r.Code, nil
		}
		if ct := r.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
			t.Fatalf("content type %q", ct)
		}
		reader := csv.NewReader(strings.NewReader(r.Body.String()))
		reader.FieldsPerRecord = -1
		rows, err := reader.ReadAll()
		if err != nil {
			t.Fatalf("export is not valid CSV: %v", err)
		}
		return r.Code, rows
	}
	column := func(rows [][]string, i int) map[string]bool {
		out := map[string]bool{}
		for _, row := range rows[1:] {
			if len(row) > i {
				out[row[i]] = true
			}
		}
		return out
	}

	code, machines := get(exports.Claims{Kind: "inventory", Params: map[string]string{"report_type": "machines"}, User: f.admin.ID})
	if code != http.StatusOK || len(machines) < 4 || machines[0][0] != "hostname" {
		t.Fatalf("machines export=%d rows=%v", code, machines)
	}
	hosts := column(machines, 0)
	if !hosts["'"+hostile] || hosts[hostile] {
		t.Fatalf("hostile hostname reached the CSV unguarded: %v", hosts)
	}
	if !hosts["SPLIT;'=2+2"] || hosts[split] {
		t.Fatalf("a formula after a separator reached the CSV unguarded: %v", hosts)
	}
	if !hosts[control] {
		t.Fatalf("ordinary hostname changed: %v", hosts)
	}
	userCol := slices.Index(machines[0], "logged_in_user")
	if userCol < 0 {
		t.Fatalf("machines export has no logged_in_user column: %v", machines[0])
	}
	users := column(machines, userCol)
	if !users["'"+hostileUser] || users[hostileUser] || !users[plainUser] {
		t.Fatalf("logged-in users in the CSV: %v", users)
	}

	code, audit := get(exports.Claims{Kind: "audit", Params: map[string]string{"action": "csv.export_test"}, User: f.admin.ID})
	if code != http.StatusOK || len(audit) != 3 || audit[0][3] != "target" {
		t.Fatalf("audit export=%d rows=%v", code, audit)
	}
	targets := column(audit, 3)
	if !targets["'"+freeTarget] || targets[freeTarget] || !targets[idTarget] {
		t.Fatalf("audit targets=%v", targets)
	}

	// The link belongs to whoever asked for it, and a forged one is refused.
	if code, _ := get(exports.Claims{Kind: "audit", User: uuid.NewString()}); code != http.StatusForbidden {
		t.Fatalf("another account's link=%d", code)
	}
	if r := f.do(http.MethodGet, "/api/exports/csv?token=forged.signature", ""); r.Code != http.StatusBadRequest {
		t.Fatalf("forged link=%d", r.Code)
	}
}
