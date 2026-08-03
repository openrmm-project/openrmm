package patches

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

func TestDispatchInstallClaimsApprovedPatchesAndRequeuesOffline(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newPatchTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	agent, err := st.CreateAgent(ctx, "patch-agent", "linux", "amd64", "1.0.0", "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPatchReport(ctx, agent.ID, &protocol.PatchReportMsg{
		OS: "linux",
		Patches: []protocol.PatchItem{
			{Identifier: "openssl", Title: "OpenSSL", Classification: protocol.PatchSecurity},
			{Identifier: "vim", Title: "Vim", Classification: protocol.PatchOptional},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListAgentPatches(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var approvedPatchID string
	for _, row := range rows {
		if row.Identifier == "openssl" {
			approvedPatchID = row.PatchID
		}
	}
	if approvedPatchID == "" {
		t.Fatal("openssl patch was not persisted")
	}
	if err := st.SetApproval(ctx, approvedPatchID, models.ScopeAgent, agent.ID, "approve", "manual", "operator"); err != nil {
		t.Fatal(err)
	}

	liveHub := hub.New()
	svc := New(st, liveHub)
	if err := svc.DispatchInstall(ctx, agent.ID, nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline DispatchInstall error = %v, want ErrOffline", err)
	}
	assertPatchStatus(t, st, ctx, agent.ID, "openssl", models.PatchStatusApproved)
	assertPatchStatus(t, st, ctx, agent.ID, "vim", models.PatchStatusMissing)

	outbound := liveHub.Add(agent.ID, nil)
	if err := svc.DispatchInstall(ctx, agent.ID, []string{"openssl", "vim"}); err != nil {
		t.Fatalf("online DispatchInstall error: %v", err)
	}
	select {
	case raw := <-outbound:
		var envelope protocol.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != protocol.MsgPatchInstall {
			t.Fatalf("frame type = %q, want %q", envelope.Type, protocol.MsgPatchInstall)
		}
		var install protocol.PatchInstallMsg
		if err := json.Unmarshal(envelope.Data, &install); err != nil {
			t.Fatal(err)
		}
		if install.RequestID == "" || len(install.Identifiers) != 1 || install.Identifiers[0] != "openssl" {
			t.Fatalf("install payload = %#v, want only the approved openssl patch", install)
		}
	case <-time.After(time.Second):
		t.Fatal("online agent did not receive patch install")
	}
	assertPatchStatus(t, st, ctx, agent.ID, "openssl", models.PatchStatusInstalling)
	if err := svc.DispatchInstall(ctx, agent.ID, nil); !errors.Is(err, ErrNoApproved) {
		t.Fatalf("second DispatchInstall error = %v, want ErrNoApproved", err)
	}
}

func assertPatchStatus(t *testing.T, st *store.Store, ctx context.Context, agentID, identifier, want string) {
	t.Helper()
	rows, err := st.ListAgentPatches(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Identifier == identifier {
			if row.Status != want {
				t.Fatalf("patch %s status = %q, want %q", identifier, row.Status, want)
			}
			return
		}
	}
	t.Fatalf("patch %s not found", identifier)
}

func newPatchTestStore(t *testing.T, databaseURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "patches_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	database, err := url.Parse(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
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
