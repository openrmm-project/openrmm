package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
)

// An installed 0.9.4 database has migrations 001..024. Its existing schedules
// must keep their UTC behavior and values when 025 adds explicit zones. Keep
// this test valid when later releases append more migrations after 025.
func TestScheduleTimezoneUpgradeFrom094Postgres(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newMigrationTestStore(t, url)
	if err := st.migrate(ctx, migrations[:24]); err != nil {
		t.Fatal(err)
	}
	taskID, policyID := uuid.NewString(), uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO tasks (id,name,shell,script,schedule_kind,run_at_minute,enabled)
 VALUES ($1,'legacy UTC task','sh','printf status','daily',700,false)`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO patch_policies (id,name,window_start_min,window_end_min,reboot)
 VALUES ($1,'legacy UTC window',1200,1380,'never')`, policyID); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Timezone != models.DefaultTimezone || task.RunAtMinute != 700 || task.Enabled {
		t.Fatalf("legacy task changed: %+v", task)
	}
	var tz, reboot string
	var start, end int
	if err := st.pool.QueryRow(ctx, `SELECT timezone,window_start_min,window_end_min,reboot FROM patch_policies WHERE id=$1`, policyID).Scan(&tz, &start, &end, &reboot); err != nil {
		t.Fatal(err)
	}
	if tz != models.DefaultTimezone || start != 1200 || end != 1380 || reboot != "never" {
		t.Fatalf("legacy window changed: timezone=%s start=%d end=%d reboot=%s", tz, start, end, reboot)
	}
	var name, checksum string
	if err := st.pool.QueryRow(ctx, `SELECT name,checksum FROM schema_migrations WHERE version=25`).Scan(&name, &checksum); err != nil {
		t.Fatal(err)
	}
	if name != "schedule_timezones" || checksum != migrationChecksum(migrations[24]) {
		t.Fatal("migration 025 is not the stable schedule-timezone migration")
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("reapply migration: %v", err)
	}
}
