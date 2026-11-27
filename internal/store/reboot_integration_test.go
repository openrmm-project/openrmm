package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestRebootObservationsAndDispatchReservations(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "reboot-host", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func() {
		t.Helper()
		a, err = st.GetAgent(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	observe := func(status, boot string) {
		t.Helper()
		if err := st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: status, Reason: "kernel", BootID: boot}); err != nil {
			t.Fatal(err)
		}
		get()
	}
	get()
	if a.RebootStatus != protocol.RebootUnknown || a.RebootCheckedAt != nil {
		t.Fatalf("initial: %+v", a)
	}
	observe(protocol.RebootRequired, "boot-one")
	if !a.RebootPending || a.RebootPendingSince == nil || a.RebootCheckedAt == nil {
		t.Fatalf("positive: %+v", a)
	}
	since, checked := *a.RebootPendingSince, *a.RebootCheckedAt
	time.Sleep(time.Millisecond)
	observe(protocol.RebootRequired, "boot-one")
	if !a.RebootPendingSince.Equal(since) || !a.RebootCheckedAt.After(checked) {
		t.Fatal("repeat changed first detection or did not refresh")
	}
	if err = st.ObserveReboot(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	get()
	if !a.RebootPending {
		t.Fatal("legacy report cleared pending")
	}
	observe(protocol.RebootUnknown, "boot-one")
	if !a.RebootPending || !a.RebootPendingSince.Equal(since) {
		t.Fatal("unknown cleared pending/since")
	}
	eligible, err := st.RebootEligibleAgents(ctx)
	if err != nil || len(eligible) != 0 {
		t.Fatalf("unknown eligible: %v %v", eligible, err)
	}
	observe(protocol.RebootRequired, "boot-one")
	eligible, err = st.RebootEligibleAgents(ctx)
	if err != nil || len(eligible) != 1 {
		t.Fatalf("fresh positive not eligible: %v %v", eligible, err)
	}
	token, err := st.ClaimRebootRequest(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	get()
	if !a.RebootPending || a.RebootRequestedAt == nil || a.RebootObservedAt != nil {
		t.Fatal("request is not an observation")
	}
	if _, err = st.ClaimRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate claim: %v", err)
	}
	if err = st.ReleaseRebootRequest(ctx, a.ID, "wrong-token"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("another claim released reservation")
	}
	if err = st.ReleaseRebootRequest(ctx, a.ID, token); err != nil {
		t.Fatal(err)
	}
	get()
	if a.RebootRequestedAt != nil || !a.RebootPending {
		t.Fatal("failed send did not release safely")
	}
	if _, err = st.ClaimRebootRequest(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	observe(protocol.RebootRequired, "boot-one")
	if a.RebootObservedAt != nil {
		t.Fatal("same boot produced a boot-change observation")
	}
	observe(protocol.RebootUnknown, "boot-two")
	if a.RebootObservedAt == nil || !a.RebootPending || !a.RebootPendingSince.Equal(since) {
		t.Fatal("boot observation cleared an unknown pending requirement")
	}
	observed := *a.RebootObservedAt
	observe(protocol.RebootNotRequired, "boot-two")
	if a.RebootPending || a.RebootPendingSince != nil || !a.RebootObservedAt.Equal(observed) {
		t.Fatal("OS clear or observation stability failed")
	}
	observe(protocol.RebootNotRequired, "boot-unrequested")
	if !a.RebootObservedAt.After(observed) {
		t.Fatal("a later boot was not observed independently of the request")
	}
	observe(protocol.RebootRequired, "boot-two")
	if _, err = st.pool.Exec(ctx, `UPDATE agents SET reboot_requested_at=now()-interval '16 minutes',reboot_checked_at=now()-interval '3 minutes' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	eligible, err = st.RebootEligibleAgents(ctx)
	if err != nil || len(eligible) != 0 {
		t.Fatal("stale observation eligible")
	}
	observe(protocol.RebootRequired, "boot-two")
	if _, err = st.ClaimRebootRequest(ctx, a.ID); err != nil {
		t.Fatalf("retry after cooldown: %v", err)
	}
	if _, err = st.pool.Exec(ctx, `UPDATE agents SET revoked_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	observe(protocol.RebootNotRequired, "boot-three")
	if !a.RebootPending {
		t.Fatal("revoked agent changed observation")
	}
}

func TestRebootPatchSourcesAndSingleAutomaticEpisode(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, osName := range []string{"darwin", "linux-dnf", "legacy", "windows"} {
		t.Run(osName, func(t *testing.T) {
			a, err := st.CreateAgent(ctx, osName, osName, "amd64", "test", "hash-"+osName, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{RequestID: "install-A", RebootRequired: true}); err != nil {
				t.Fatal(err)
			}
			if osName != "legacy" {
				if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "unknown"}); err != nil {
					t.Fatal(err)
				}
			}
			current, err := st.GetAgent(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.RebootStatus != "unknown" {
				t.Fatal("patch source invented OS observation")
			}
			if osName == "legacy" && current.RebootCheckedAt != nil {
				t.Fatal("legacy patch result invented OS timestamp")
			}
			if osName == "windows" {
				if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required"}); err != nil {
					t.Fatal(err)
				}
			}
			token, err := st.ClaimAutomaticRebootRequest(ctx, a.ID)
			if err != nil {
				t.Fatalf("patch source is not eligible: %v", err)
			}
			if err = st.ReleaseRebootRequest(ctx, a.ID, token); err != nil {
				t.Fatal(err)
			}
			if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
				t.Fatalf("failed send cannot retry: %v", err)
			}
			if _, err = st.pool.Exec(ctx, `UPDATE agents SET reboot_pending_since=now()-interval '1 hour',reboot_patch_required_at=now()-interval '1 hour',reboot_requested_at=now()-interval '16 minutes' WHERE id=$1`, a.ID); err != nil {
				t.Fatal(err)
			}
			if osName == "windows" {
				if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("persistent marker automatically rebooted again: %v", err)
			}
			manual, err := st.ClaimRebootRequest(ctx, a.ID)
			if err != nil {
				t.Fatalf("explicit retry after cooldown denied: %v", err)
			}
			if err = st.ReleaseRebootRequest(ctx, a.ID, manual); err != nil {
				t.Fatal(err)
			}
			if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
				t.Fatal("failed manual retry erased delivered automatic episode")
			}

			// A second real install may need a reboot while the OS remains
			// unknown. Do not inject a negative observation unavailable on
			// macOS/DNF/legacy agents, and keep the original first detection.
			before, err := st.GetAgent(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{RequestID: "install-B", RebootRequired: true}); err != nil {
				t.Fatal(err)
			}
			current, err = st.GetAgent(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !current.RebootPendingSince.Equal(*before.RebootPendingSince) {
				t.Fatal("new install changed original first detection")
			}
			if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
				t.Fatalf("new distinct install not eligible: %v", err)
			}
			// Advance past cooldown without changing relative generation order.
			if _, err = st.pool.Exec(ctx, `UPDATE agents SET reboot_patch_required_at=now()-interval '20 minutes',reboot_requested_at=now()-interval '16 minutes' WHERE id=$1`, a.ID); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"install-A", "install-B", "install-A"} {
				if err = st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{RequestID: id, RebootRequired: true}); err != nil {
					t.Fatal(err)
				}
				if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
					t.Fatalf("replayed %s reopened eligibility: %v", id, err)
				}
			}

		})
	}
}

func TestRebootMigrationPreservesLegacyPatchSource(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.migrate(ctx, migrations[:35]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO agents (id,token_hash,reboot_pending,reboot_pending_since) VALUES ('00000000-0000-4000-8000-000000000001','hash',TRUE,now()),('00000000-0000-4000-8000-000000000002','hash2',FALSE,NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := st.RebootEligibleAgents(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("backfill: %v %v", ids, err)
	}
	a, err := st.GetAgent(ctx, "00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if a.RebootStatus != "unknown" || a.RebootCheckedAt != nil {
		t.Fatal("migration invented OS observation")
	}
}

func TestRebootObservationDoesNotAttributeBootToRequest(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "independent-boot", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, boot := range []string{"before", "after-unrequested-reboot"} {
		if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "unknown", BootID: boot}); err != nil {
			t.Fatal(err)
		}
	}
	a, err = st.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.RebootObservedAt == nil || a.RebootRequestedAt != nil {
		t.Fatalf("boot without request: %+v", a)
	}
	observed := *a.RebootObservedAt
	if _, err = st.ClaimRebootRequest(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "unknown", BootID: "after-unrequested-reboot"}); err != nil {
		t.Fatal(err)
	}
	a, err = st.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.RebootObservedAt.Equal(observed) || !a.RebootObservedAt.Before(*a.RebootRequestedAt) {
		t.Fatal("request relabeled an earlier boot")
	}
}

func TestRebootBootChangeDoesNotRestartAutomaticEpisode(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "persistent-kernel-marker", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required", BootID: "boot-before"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.pool.Exec(ctx, `UPDATE agents SET reboot_pending_since=now()-interval '1 hour',reboot_patch_required_at=now()-interval '1 hour',reboot_requested_at=now()-interval '16 minutes' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required", BootID: "boot-after"}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.RebootEligibleAgents(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("persistent post-boot marker eligible again: %v %v", ids, err)
	}
	if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-boot automatic retry: %v", err)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "not_required", BootID: "boot-after"}); err != nil {
		t.Fatal(err)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required", BootID: "boot-after"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
		t.Fatalf("new OS episode cannot reboot: %v", err)
	}
}

func TestRebootPatchReplayCannotResurrectClearedRequirement(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "deduplicated-results", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(id string) {
		t.Helper()
		if err := st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{RequestID: id, RebootRequired: true}); err != nil {
			t.Fatal(err)
		}
	}
	apply("A")
	apply("B")
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "not_required"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "A"} {
		apply(id)
		a, err = st.GetAgent(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.RebootPending || a.RebootPendingSince != nil {
			t.Fatalf("replay %s resurrected pending", id)
		}
	}
	var count int
	if err = st.pool.QueryRow(ctx, `SELECT count(*) FROM agent_reboot_patch_results WHERE agent_id=$1`, a.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("identities not durable/deduplicated: %d", count)
	}
	apply("C")
	if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
		t.Fatalf("new result after clear: %v", err)
	}
	if _, err = st.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.pool.QueryRow(ctx, `SELECT count(*) FROM agent_reboot_patch_results WHERE agent_id=$1`, a.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("result identities must cascade with device: %d %v", count, err)
	}
}

func TestRebootPatchMissingIdentityHasOneConservativeGeneration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "missing-result-id", "darwin", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply := func() {
		t.Helper()
		if err := st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{RebootRequired: true}); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.pool.Exec(ctx, `UPDATE agents SET reboot_pending_since=now()-interval '1 hour',reboot_patch_required_at=now()-interval '1 hour',reboot_requested_at=now()-interval '16 minutes' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		apply()
		if _, err = st.ClaimAutomaticRebootRequest(ctx, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing identity reopened automatic eligibility: %v", err)
		}
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "not_required"}); err != nil {
		t.Fatal(err)
	}
	apply()
	a, err = st.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.RebootPending {
		t.Fatal("anonymous replay resurrected cleared pending")
	}
}
