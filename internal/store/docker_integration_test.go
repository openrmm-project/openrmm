package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestDockerSnapshotPersistenceAndPagination(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	s := newMigrationTestStore(t, url)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(ctx, "docker-host", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.DockerInventory(ctx, a.ID)
	if err != nil || initial.Status != "unknown" || !initial.Stale || initial.LastAttemptAt != nil {
		t.Fatalf("legacy: %+v %v", initial, err)
	}
	report := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{{ID: strings.Repeat("a", 64), Name: "web", Image: "image:web", State: "running", Health: "healthy"}, {ID: strings.Repeat("b", 64), Name: "db", Image: "image:db", State: "exited", Health: "none"}}}
	if err = s.RecordDockerSnapshot(ctx, a.ID, report); err != nil {
		t.Fatal(err)
	}
	good, err := s.DockerInventory(ctx, a.ID)
	if err != nil || good.LastSuccessAt == nil || len(good.Containers) != 2 {
		t.Fatalf("good: %+v %v", good, err)
	}
	success := *good.LastSuccessAt
	if err = s.RecordDockerSnapshot(ctx, a.ID, protocol.DockerSnapshot{Status: protocol.DockerUnavailable, ErrorCode: "engine_unavailable"}); err != nil {
		t.Fatal(err)
	}
	failed, err := s.DockerInventory(ctx, a.ID)
	if err != nil || len(failed.Containers) != 2 || !failed.LastSuccessAt.Equal(success) || !failed.Stale {
		t.Fatalf("failed collection discarded history: %+v %v", failed, err)
	}
	fleet, err := s.DockerFleet(ctx, ReportFilter{}, DockerFilter{Limit: 1, Offset: 1})
	if err != nil || fleet.Total != 2 || len(fleet.Containers) != 1 || len(fleet.Hosts) != 1 {
		t.Fatalf("paged: %+v %v", fleet, err)
	}
	filtered, err := s.DockerFleet(ctx, ReportFilter{}, DockerFilter{Limit: 10, State: "running", Health: "healthy", Query: "web"})
	if err != nil || filtered.Total != 1 || filtered.Containers[0].Name != "web" {
		t.Fatalf("filtered: %+v %v", filtered, err)
	}
	if err = s.RecordDockerSnapshot(ctx, a.ID, protocol.DockerSnapshot{Status: protocol.DockerAvailable}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.DockerInventory(ctx, a.ID)
	if err != nil || len(empty.Containers) != 0 {
		t.Fatalf("successful empty retained removed containers: %+v %v", empty, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE agents SET revoked_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDockerSnapshot(ctx, a.ID, report); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked report accepted: %v", err)
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM docker_inventory WHERE agent_id=$1`, a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade: %d %v", n, err)
	}
}

func TestDockerFreshnessUsesServerReceiptAndAgentPresence(t *testing.T) {
	now := time.Now()
	fresh, old := now.Add(-30*time.Second), now.Add(-91*time.Second)
	for _, tc := range []struct {
		name, status, presence string
		success                *time.Time
		stale                  bool
	}{
		{"fresh", protocol.DockerAvailable, "online", &fresh, false},
		{"aged", protocol.DockerAvailable, "online", &old, true},
		{"offline", protocol.DockerAvailable, "offline", &fresh, true},
		{"failed", protocol.DockerUnavailable, "online", &fresh, true},
		{"legacy", "unknown", "online", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DockerInventory{Status: tc.status, AgentStatus: tc.presence, LastSuccessAt: tc.success}
			d.freshness(now)
			if d.Stale != tc.stale {
				t.Fatalf("stale=%v want %v", d.Stale, tc.stale)
			}
		})
	}
}
