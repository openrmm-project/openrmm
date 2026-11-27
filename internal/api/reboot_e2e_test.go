package api

import (
	"encoding/json"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"net/http"
	"testing"
)

func TestRebootHeartbeatToScopedAPI(t *testing.T) {
	f := newBulkFixture(t)
	s := &Server{store: f.st, hub: hub.New()}
	lease := s.hub.Add(f.a1, nil)
	heartbeat := func(data string) {
		t.Helper()
		raw, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgHeartbeat, Data: json.RawMessage(data)})
		s.handleAgentFrame(f.ctx, f.a1, lease, raw)
	}
	get := func() models.Agent {
		t.Helper()
		rr := f.do(f.scoped, http.MethodGet, "/api/devices/"+f.a1, nil)
		if rr.Code != 200 {
			t.Fatalf("device: %d %s", rr.Code, rr.Body.String())
		}
		var a models.Agent
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	heartbeat(`{"reboot":{"status":"required","reason":"kernel update","boot_id":"boot-one"}}`)
	a := get()
	if !a.RebootPending || a.RebootCheckedAt == nil || a.RebootPendingSince == nil || a.RebootReason != "kernel update" {
		t.Fatalf("observation absent: %+v", a)
	}
	since := *a.RebootPendingSince
	for _, data := range []string{`{}`, `{"reboot":null}`, `{"reboot":{"status":"unknown","reason":"access denied"}}`, `{"reboot":false}`} {
		heartbeat(data)
		a = get()
		if !a.RebootPending || !a.RebootPendingSince.Equal(since) {
			t.Fatalf("legacy/unknown cleared observation: %s", data)
		}
	}
	rr := f.do(f.scoped, http.MethodGet, "/api/devices", nil)
	var list struct {
		Devices []models.Agent `json:"devices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range list.Devices {
		if d.ID == f.b1 {
			t.Fatal("cross-client device leaked")
		}
		if d.ID == f.a1 {
			found = d.RebootPending && d.RebootStatus == "unknown"
		}
	}
	if !found {
		t.Fatal("list lost reboot observation")
	}
	if rr = f.do(f.scoped, http.MethodGet, "/api/devices/"+f.b1, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("scope: %d", rr.Code)
	}
	heartbeat(`{"reboot":{"status":"not_required","reason":"no marker","boot_id":"boot-one"}}`)
	a = get()
	if a.RebootPending || a.RebootPendingSince != nil || a.RebootStatus != "not_required" || a.RebootObservedAt != nil {
		t.Fatalf("negative observation: %+v", a)
	}
	rr = f.do(f.scoped, http.MethodGet, "/api/reports/compliance", nil)
	if rr.Code != 200 {
		t.Fatalf("compliance: %d %s", rr.Code, rr.Body.String())
	}
	var report struct {
		Rows []models.ComplianceRow `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, r := range report.Rows {
		if r.AgentID == f.b1 {
			t.Fatal("compliance scope leaked")
		}
		if r.AgentID == f.a1 {
			found = r.RebootStatus == "not_required" && r.RebootCheckedAt != nil
		}
	}
	if !found {
		t.Fatalf("compliance omitted observation: %s", rr.Body.String())
	}
	// A fresh negative OS report must not make an offline endpoint compliant.
	if err := f.st.UpsertPatchReport(f.ctx, f.a1, &protocol.PatchReportMsg{OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"online", "offline"} {
		if err := f.st.SetAgentStatus(f.ctx, f.a1, state); err != nil {
			t.Fatal(err)
		}
		rr = f.do(f.scoped, http.MethodGet, "/api/reports/compliance", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("compliance status %d", rr.Code)
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		for _, row := range report.Rows {
			if row.AgentID == f.a1 {
				want := "compliant"
				if state == "offline" {
					want = "unknown"
				}
				if row.AgentStatus != state || row.Status != want {
					t.Fatalf("fresh %s compliance: %+v", state, row)
				}
			}
		}
	}

}
