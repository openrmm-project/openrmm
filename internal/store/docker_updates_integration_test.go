package store

import (
	"context"
	"fmt"
	"github.com/p1ngu/rmm/internal/protocol"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDockerUpdateFilterPartitionsAndFreshness(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	s := newMigrationTestStore(t, url)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(ctx, "updates-host", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE agents SET status='online' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	now, old, future := time.Now().UTC(), time.Now().UTC().Add(-27*time.Hour), time.Now().UTC().Add(time.Hour)
	updates := []*protocol.DockerImageUpdate{
		{Status: "up_to_date", CheckedAt: &now, VersionStatus: "not_found"},
		{Status: "update_available", CheckedAt: &now, VersionStatus: "unsupported"},
		{Status: "up_to_date", CheckedAt: &now, VersionStatus: "available", LatestTag: "17", VersionKind: "major"},
		{Status: "pinned", CheckedAt: &now, VersionStatus: "available", LatestTag: "17", VersionKind: "major"},
		nil,
		{Status: "update_available", CheckedAt: &old, VersionStatus: "unsupported"},
		{Status: "up_to_date", CheckedAt: &now, VersionStatus: "unknown", VersionErrorCode: "tag_search_incomplete"},
		{Status: "pinned", CheckedAt: &now, VersionStatus: "unsupported"},
		{Status: "update_available", CheckedAt: &future, VersionStatus: "unsupported"},
		{Status: "up_to_date", CheckedAt: &now, VersionStatus: "unsupported", VersionErrorCode: "tag_format_unsupported"},
	}
	report := protocol.DockerSnapshot{Status: protocol.DockerAvailable}
	for i, u := range updates {
		report.Containers = append(report.Containers, protocol.DockerContainer{ID: fmt.Sprintf("%064x", i+1), Name: fmt.Sprintf("web%d", i), Image: "postgres:16", ImageID: "sha256:" + strings.Repeat("a", 64), State: "running", Health: "none", Update: u})
	}
	if err = s.RecordDockerSnapshot(ctx, a.ID, report); err != nil {
		t.Fatal(err)
	}
	check := func(filter string, want int64) {
		t.Helper()
		got, e := s.DockerFleet(ctx, ReportFilter{}, DockerFilter{Update: filter, Limit: 1})
		if e != nil || got.Total != want {
			t.Fatalf("filter %s: got %+v err=%v want %d", filter, got, e, want)
		}
		if len(got.Containers) > 1 {
			t.Fatal("pagination ignored")
		}
	}
	check("available", 3)
	check("current", 2)
	check("unknown", 5)
	inv, err := s.DockerInventory(ctx, a.ID)
	if err != nil || inv.Containers[2].Update.LatestTag != "17" {
		t.Fatalf("roundtrip: %+v %v", inv, err)
	}
	if _, err = s.DockerFleet(ctx, ReportFilter{}, DockerFilter{Update: "arbitrary"}); err == nil {
		t.Fatal("unknown filter accepted")
	}
	for _, sql := range []string{`UPDATE agents SET status='offline' WHERE id=$1`, `UPDATE agents SET status='online' WHERE id=$1;`} {
		if _, err = s.pool.Exec(ctx, sql, a.ID); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sql, "offline") {
			check("available", 0)
			check("current", 0)
			check("unknown", 10)
		}
	}
	if _, err = s.pool.Exec(ctx, `UPDATE docker_inventory SET last_success_at=now()-interval '2 minutes' WHERE agent_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	check("available", 0)
	check("current", 0)
	check("unknown", 10)
	if err = s.RecordDockerSnapshot(ctx, a.ID, report); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDockerSnapshot(ctx, a.ID, protocol.DockerSnapshot{Status: protocol.DockerUnavailable, ErrorCode: "collection_failed"}); err != nil {
		t.Fatal(err)
	}
	check("available", 0)
	check("unknown", 10)
}
