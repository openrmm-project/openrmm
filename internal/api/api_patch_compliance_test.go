package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// The router fixture uses storetest.NewSchema: exercise the real aggregate
// query and JSON response, including its permission and access-scope middleware.
func TestPatchComplianceDescriptionsAndScopeIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "Elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "Private site")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, description := range []string{"Reception server 2", "", "Private finance server"} {
		var siteID *string
		if i == 2 {
			siteID = &site.ID
		}
		a, err := f.store.CreateAgent(ctx, "server-"+string(rune('A'+i)), "linux", "amd64", "1.0", "token-"+string(rune('A'+i)), siteID, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
		if err := f.store.SetAgentMeta(ctx, a.ID, description, "private note"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.UpsertPatchReport(ctx, a.ID, &protocol.PatchReportMsg{OS: "linux", Patches: []protocol.PatchItem{{Identifier: "test-security", Title: "Software patch title", Classification: protocol.PatchSecurity}}}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(target string) []models.ComplianceRow {
		t.Helper()
		rec := f.do(http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", target, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "private note") {
			t.Fatal("detail-only note exposed")
		}
		var body struct {
			Rows []models.ComplianceRow `json:"rows"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Rows
	}
	rows := read("/api/reports/compliance")
	if len(rows) != 3 {
		t.Fatalf("unrestricted rows: %+v", rows)
	}
	for i, want := range []string{"Reception server 2", "", "Private finance server"} {
		r := rows[i]
		if r.AgentID != ids[i] || r.Description != want || r.MissingTotal != 1 || r.MissingSec != 1 || r.Status != "critical" || r.LastScan == nil {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	if rows[0].ClientID != "" || rows[0].SiteID != "" || rows[1].ClientID != "" || rows[1].SiteID != "" {
		t.Fatalf("unassigned devices acquired organization IDs: %+v", rows)
	}
	if rows[2].ClientID != client.ID || rows[2].SiteID != site.ID || rows[2].Client != client.Name || rows[2].Site != site.Name {
		t.Fatalf("stable organization IDs and labels: %+v", rows[2])
	}
	// A device-scoped role sees its own description, even when the other
	// devices have the same patch. Filtering another site cannot widen access.
	restore := f.asUser(t, "", []string{models.PermPatchesView}, []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: ids[0]}})
	rows = read("/api/reports/compliance")
	if len(rows) != 1 || rows[0].AgentID != ids[0] || rows[0].Description != "Reception server 2" {
		t.Fatalf("scoped rows: %+v", rows)
	}
	rec := f.do(http.MethodGet, "/api/reports/compliance?scope=site&id="+site.ID, "")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Private finance server") {
		t.Fatalf("out-of-scope report: %d %s", rec.Code, rec.Body)
	}
	restore()
	restore = f.asUser(t, "", []string{models.PermPatchesView}, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	defer restore()
	rows = read("/api/reports/compliance")
	if len(rows) != 1 || rows[0].AgentID != ids[2] || rows[0].Description != "Private finance server" || rows[0].ClientID != client.ID || rows[0].SiteID != site.ID {
		t.Fatalf("client-scoped rows: %+v", rows)
	}
}

// Labels are not identities: identically named clients and sites must remain
// separate in the tree, and a scope cannot pull the other one's device rows.
func TestPatchComplianceOrganizationIDsWithDuplicateNamesIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	clients, sites, agents := []string{}, []string{}, []string{}
	for i := 0; i < 2; i++ {
		client, err := f.store.CreateClient(ctx, "Same name")
		if err != nil {
			t.Fatal(err)
		}
		site, err := f.store.CreateSite(ctx, client.ID, "Same site")
		if err != nil {
			t.Fatal(err)
		}
		agent, err := f.store.CreateAgent(ctx, "server-"+string(rune('A'+i)), "linux", "amd64", "1.0", "token-"+string(rune('A'+i)), &site.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client.ID)
		sites = append(sites, site.ID)
		agents = append(agents, agent.ID)
	}
	read := func() []models.ComplianceRow {
		t.Helper()
		rec := f.do(http.MethodGet, "/api/reports/compliance", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("report: %d %s", rec.Code, rec.Body)
		}
		var body struct {
			Rows []models.ComplianceRow `json:"rows"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Rows
	}
	rows := read()
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	for i, row := range rows {
		if row.AgentID != agents[i] || row.ClientID != clients[i] || row.SiteID != sites[i] || row.Client != "Same name" || row.Site != "Same site" {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	restore := f.asUser(t, "", []string{models.PermPatchesView}, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: sites[1]}})
	defer restore()
	rows = read()
	if len(rows) != 1 || rows[0].ClientID != clients[1] || rows[0].SiteID != sites[1] || rows[0].AgentID != agents[1] {
		t.Fatalf("duplicate labels crossed access scope: %+v", rows)
	}
}
