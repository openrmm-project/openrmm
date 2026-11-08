package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestAutonomousDispatchAuthorizationIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "autonomy_dispatch", url)
	sc, err := st.CreateScript(ctx, &models.Script{Name: "status", Shell: "sh", Body: "printf status"})
	if err != nil {
		t.Fatal(err)
	}
	hash := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params)
	release := func() {
		t.Helper()
		if err := st.SetScriptAIRelease(ctx, sc.ID, "admin", hash, sc.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	release()
	job := &models.Job{Autonomy: models.AutonomyPreapproved, ScriptID: &sc.ID, Shell: sc.Shell, Script: sc.Body}
	sends := 0
	send := func() bool { sends++; return true }
	if sent, err := st.WithAutonomousDispatch(ctx, job, hash, send); err != nil || !sent || sends != 1 {
		t.Fatalf("valid control: sent=%v err=%v count=%d", sent, err, sends)
	}
	if _, err := st.ClearScriptAIRelease(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if sent, err := st.WithAutonomousDispatch(ctx, job, hash, send); err == nil || sent || sends != 1 {
		t.Fatalf("withdrawn snapshot sent=%v err=%v count=%d", sent, err, sends)
	}
	release()
	if err := st.UpsertAppSetting(ctx, "ai.autonomy_paused", "true", false); err != nil {
		t.Fatal(err)
	}
	if sent, err := st.WithAutonomousDispatch(ctx, job, hash, send); err == nil || sent || sends != 1 {
		t.Fatalf("paused snapshot sent=%v err=%v count=%d", sent, err, sends)
	}
	if err := st.UpsertAppSetting(ctx, "ai.autonomy_paused", "false", false); err != nil {
		t.Fatal(err)
	}

	// The final send is the linearization point: withdrawal must wait until a
	// send already inside that boundary finishes, never finish before that send.
	entered, finish := make(chan struct{}), make(chan struct{})
	dispatched := make(chan error, 1)
	go func() {
		_, err := st.WithAutonomousDispatch(ctx, job, hash, func() bool {
			close(entered)
			select {
			case <-finish:
			case <-ctx.Done():
			}
			return true
		})
		dispatched <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	withdrawn := make(chan error, 1)
	go func() { _, err := st.ClearScriptAIRelease(ctx, sc.ID); withdrawn <- err }()
	select {
	case err := <-withdrawn:
		close(finish)
		t.Fatalf("withdrawal completed inside send boundary: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-dispatched; err != nil {
		t.Fatal(err)
	}
	if err := <-withdrawn; err != nil {
		t.Fatal(err)
	}
	if sent, err := st.WithAutonomousDispatch(ctx, job, hash, send); err == nil || sent {
		t.Fatalf("post-withdrawal snapshot sent=%v err=%v", sent, err)
	}

	// An edit cannot dispatch the snapshot read before it, even when re-released.
	release()
	sc.Body = "printf changed"
	if _, err := st.UpdateScript(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if sent, err := st.WithAutonomousDispatch(ctx, job, hash, send); err == nil || sent {
		t.Fatalf("edited snapshot sent=%v err=%v", sent, err)
	}
}
