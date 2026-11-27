package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/protocol"
)

// Explicit opt-in: only uniquely labelled fixtures are listed, inspected,
// stopped and removed. Images are never pulled and no existing container is
// controlled or inspected by this test.
func TestDockerLocalEngineIntegration(t *testing.T) {
	if os.Getenv("OPENRMM_TEST_DOCKER") != "1" {
		t.Skip("set OPENRMM_TEST_DOCKER=1 for disposable local Docker fixtures")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux engine collector")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	config := t.TempDir()
	token := uuid.NewString()
	label := "io.openrmm.testcase=" + token
	name := "openrmm-docker-test-" + token
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		return dockerCommandOutput(ctx, binary, append([]string{"--config", config, "--host", "unix:///var/run/docker.sock"}, args...)...)
	}
	idBytes, err := run(ctx, "run", "--pull=never", "--detach", "--name", name, "--label", label, "--label", "private.canary=NEVER_COLLECT_THIS", "--env", "PRIVATE_CANARY=NEVER_COLLECT_THIS", "--label", "com.docker.compose.project=test-project", "--label", "com.docker.compose.service=test-service", "--health-cmd", "exit 0", "--health-interval", "1s", "--health-timeout", "1s", "--health-retries", "1", "alpine:latest", "sleep", "60")
	if err != nil {
		t.Fatalf("create local fixture (cached alpine:latest required): %v", err)
	}
	id := strings.TrimSpace(string(idBytes))
	if !protocol.ValidDockerID(id) {
		t.Fatal("invalid fixture identity")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := run(cleanup, "rm", "--force", id); err != nil {
			t.Errorf("remove own fixture %s: %v", id, err)
		}
	})
	diagnostic := map[string]string{}
	fixtureRun := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "container" {
			args = append(args, "--filter", "label="+label)
		}
		out, err := run(ctx, args...)
		if len(out) < 4096 {
			diagnostic[args[0]] = string(out)
		}
		return out, err
	}
	var snapshot protocol.DockerSnapshot
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
		snapshot = collectDockerWith(ctx, fixtureRun)
		if snapshot.Status == protocol.DockerAvailable && len(snapshot.Containers) == 1 && snapshot.Containers[0].Health == "healthy" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if snapshot.Status != protocol.DockerAvailable || len(snapshot.Containers) != 1 {
		t.Fatalf("fixture snapshot: %+v; fixture-only projected outputs: %+v", snapshot, diagnostic)
	}
	c := snapshot.Containers[0]
	if c.ID != id || c.Name != name || c.Health != "healthy" || c.CPUPercent == nil || c.MemoryUsedBytes == nil || c.ComposeProject != "test-project" || c.ComposeService != "test-service" {
		t.Fatalf("incomplete actual sample: %+v", c)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "NEVER_COLLECT_THIS") || strings.Contains(string(encoded), "private.canary") {
		t.Fatal("private environment/label was collected")
	}
	cfg, _ := json.Marshal(protocol.DockerCheckConfig{Container: name, Mode: "healthy"})
	if ok, unknown, _ := dockerCheckWith(ctx, cfg, fixtureRun); !ok || unknown {
		t.Fatal("actual healthy check failed")
	}
	if _, err := run(ctx, "stop", "--time", "1", id); err != nil {
		t.Fatal(err)
	}
	snapshot = collectDockerWith(ctx, fixtureRun)
	if snapshot.Status != protocol.DockerAvailable || len(snapshot.Containers) != 1 || snapshot.Containers[0].State != "exited" || snapshot.Containers[0].CPUPercent != nil {
		t.Fatalf("stopped fixture: %+v", snapshot)
	}
	if ok, unknown, _ := dockerCheckWith(ctx, cfg, fixtureRun); ok || unknown {
		t.Fatal("actual stopped check did not fail")
	}
}
