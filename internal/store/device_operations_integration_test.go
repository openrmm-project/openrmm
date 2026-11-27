package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
)

func operationsAgent(t *testing.T, ctx context.Context, st *Store) string {
	t.Helper()
	a, err := st.CreateAgent(ctx, "operations-host", "linux", "amd64", "1", "token-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestOperationsProfileAtomicConcurrentIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	id := operationsAgent(t, ctx, st)
	p, err := st.GetDeviceOperationsProfile(ctx, id)
	if err != nil || p.Version != 0 || p.Criticality != "unknown" || p.BackupVerifiedOn != nil {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	if _, err := st.GetDeviceOperationsProfile(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent: %v", err)
	}
	fields := p.DeviceOperationsFields
	fields.Purpose = "confidential purpose"
	fields.BackupNotes = "confidential backup"
	date := "2024-02-29"
	fields.BackupVerifiedOn = &date
	for _, version := range []int64{0, 1} {
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", version, fields)
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrConflict) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 7 {
			t.Fatalf("version %d: %d writes, %d conflicts", version, success, conflict)
		}
	}
	saved, err := st.GetDeviceOperationsProfile(ctx, id)
	if err != nil || saved.BackupVerifiedOn == nil || *saved.BackupVerifiedOn != date {
		t.Fatalf("calendar date did not round trip: %+v %v", saved, err)
	}
	rows, total, err := st.ListAudit(ctx, AuditFilter{Action: "device.operational_profile.update"})
	if err != nil || total != 2 {
		t.Fatalf("audit %d %v", total, err)
	}
	for _, row := range rows {
		detail := string(row.Detail)
		if strings.Contains(detail, "confidential") || !strings.Contains(detail, "operations-host") || row.Actor != "operator" {
			t.Fatalf("audit leaked notes or wrong actor: %+v", row)
		}
	}
	if _, err := st.pool.Exec(ctx, `UPDATE audit_log SET actor='bad'`); err == nil {
		t.Fatal("audit update accepted")
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM audit_log`); err == nil {
		t.Fatal("audit delete accepted")
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM device_operations_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade %d %v", count, err)
	}
}

func TestOperationsProfileAuditRollbackIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	id := operationsAgent(t, ctx, st)
	fields := models.DefaultDeviceOperationsProfile().DeviceOperationsFields
	// A failure after the profile write must undo it, including initial creation.
	_, err := st.pool.Exec(ctx, `CREATE FUNCTION fail_operations_audit() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER fail_operations_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_operations_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", 0, fields); err == nil {
		t.Fatal("write survived failed audit")
	}
	p, err := st.GetDeviceOperationsProfile(ctx, id)
	if err != nil || p.Version != 0 {
		t.Fatalf("profile not rolled back %+v %v", p, err)
	}
	if _, err := st.pool.Exec(ctx, `DROP TRIGGER fail_operations_audit ON audit_log`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", 0, fields); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `CREATE TRIGGER fail_operations_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_operations_audit()`); err != nil {
		t.Fatal(err)
	}
	fields.Purpose = "rollback me"
	if _, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", 1, fields); err == nil {
		t.Fatal("update survived failed audit")
	}
	p, err = st.GetDeviceOperationsProfile(ctx, id)
	if err != nil || p.Version != 1 || p.Purpose != "" {
		t.Fatalf("update not rolled back %+v %v", p, err)
	}
	if _, err := st.pool.Exec(ctx, `DROP TRIGGER fail_operations_audit ON audit_log; ALTER TABLE device_operations_profiles ADD CONSTRAINT test_reject_purpose CHECK (purpose <> 'rollback me')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", 1, fields); err == nil {
		t.Fatal("profile constraint ignored")
	}
	_, total, err := st.ListAudit(ctx, AuditFilter{})
	if err != nil || total != 1 {
		t.Fatalf("failed profile write audited: %d %v", total, err)
	}
}

func TestOperationsProfileMixedAuditChainIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	id := operationsAgent(t, ctx, st)
	// Hold the agent lock so the profile transaction starts before the ordinary audit.
	lock, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := st.UpdateDeviceOperationsProfile(ctx, id, "operator", 0, models.DefaultDeviceOperationsProfile().DeviceOperationsFields)
		done <- err
	}()
	// Observe the actual blocked statement, not an assumed goroutine schedule.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err := st.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT hostname FROM agents%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("profile did not wait on agent lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := st.AddAudit(ctx, "other", "ordinary.update", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := st.AddAudit(ctx, "other", "ordinary.update", id, nil); err != nil {
					t.Error(err)
				}
			} else {
				p, err := st.GetDeviceOperationsProfile(ctx, id)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = st.UpdateDeviceOperationsProfile(ctx, id, "operator", p.Version, p.DeviceOperationsFields)
				if err != nil && !errors.Is(err, ErrConflict) {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	broken, err := st.VerifyAuditChain(ctx)
	if err != nil || broken != nil {
		t.Fatalf("chain broken: %+v %v", broken, err)
	}
}
