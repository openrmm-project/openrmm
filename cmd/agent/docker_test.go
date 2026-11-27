package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

var dockerTestID = strings.Repeat("a", 64)

func dockerTestProjection(state, health string) []byte {
	return []byte(`{"id":"` + dockerTestID + `","name":"/web.1","image":"repo/web:v1","state":"` + state + `","health":"` + health + `","restart_count":7,"started_at":"2026-01-02T03:04:05Z","compose_project":"site","compose_service":"web","Env":["CANARY_SECRET"],"labels":{"private":"CANARY_SECRET"}}`)
}

func TestDockerInspectTemplateRendersOnlyAllowedJSON(t *testing.T) {
	tmpl := template.Must(template.New("inspect").Option("missingkey=error").Funcs(template.FuncMap{"json": func(v any) string { b, _ := json.Marshal(v); return string(b) }}).Parse(dockerInspectProjection))
	for _, withHealth := range []bool{false, true} {
		state := map[string]any{"Status": "running", "StartedAt": "2026-01-02T03:04:05Z"}
		if withHealth {
			state["Health"] = map[string]any{"Status": "healthy", "Log": "CANARY_SECRET"}
		}
		fixture := map[string]any{"Id": dockerTestID, "Image": "sha256:" + dockerTestID, "Name": "/web.1", "RestartCount": 0, "State": state, "Config": map[string]any{"Image": "web:v1", "Env": []string{"CANARY_SECRET"}, "Labels": map[string]string{"private": "CANARY_SECRET", "com.docker.compose.project": "site", "com.docker.compose.service": "web"}}, "Mounts": "CANARY_SECRET"}
		var output bytes.Buffer
		if err := tmpl.Execute(&output, fixture); err != nil {
			t.Fatal(err)
		}
		var container protocol.DockerContainer
		if err := json.Unmarshal(output.Bytes(), &container); err != nil {
			t.Fatalf("invalid projected JSON: %v", err)
		}
		wantHealth := "none"
		if withHealth {
			wantHealth = "healthy"
		}
		if container.Health != wantHealth || container.ComposeService != "web" || strings.Contains(output.String(), "CANARY_SECRET") {
			t.Fatalf("unexpected projection: %s", output.String())
		}
	}
}

func TestDockerCollectionProjectionAndMetrics(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch args[0] {
		case "container":
			return []byte(dockerTestID + "\n"), nil
		case "inspect":
			return dockerTestProjection("running", "healthy"), nil
		case "stats":
			return []byte(`{"ID":"` + dockerTestID + `","CPUPerc":"125.5%","MemUsage":"2MiB / 1GiB"}`), nil
		}
		t.Fatalf("unexpected command %v", args)
		return nil, nil
	}
	s := collectDockerWith(context.Background(), run)
	if s.Status != protocol.DockerAvailable || len(s.Containers) != 1 {
		t.Fatalf("%+v", s)
	}
	c := s.Containers[0]
	if c.Name != "web.1" || c.RestartCount != 7 || c.CPUPercent == nil || *c.CPUPercent != 125.5 || *c.MemoryUsedBytes != 2<<20 || *c.MemoryLimitBytes != 1<<30 {
		t.Fatalf("%+v", c)
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), "CANARY_SECRET") {
		t.Fatal("unprojected secrets retained")
	}
	if calls[1][1] != "--type" || calls[1][2] != "container" || calls[1][4] != dockerInspectProjection {
		t.Fatalf("inspect not projected: %v", calls[1])
	}
	if calls[2][len(calls[2])-1] != dockerTestID {
		t.Fatal("statistics not bound to observed IDs")
	}
	for _, forbidden := range []string{"{{json .Config}}", "{{json .State}}", ".Env", ".Log", ".Mount", ".Cmd"} {
		if strings.Contains(dockerInspectProjection, forbidden) {
			t.Fatalf("unsafe projection %s", forbidden)
		}
	}
}

func TestDockerMissingErrorEmptyStoppedAndInvalid(t *testing.T) {
	ctx := context.Background()
	empty := collectDockerWith(ctx, func(context.Context, ...string) ([]byte, error) { return nil, nil })
	if empty.Status != protocol.DockerAvailable || len(empty.Containers) != 0 {
		t.Fatalf("empty engine: %+v", empty)
	}
	denied := collectDockerWith(ctx, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("CANARY_SECRET") })
	if denied.Status != protocol.DockerUnavailable || denied.ErrorCode != "engine_unavailable" {
		t.Fatalf("%+v", denied)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	timeout := collectDockerWith(cancelled, func(context.Context, ...string) ([]byte, error) { return nil, context.Canceled })
	if timeout.ErrorCode != "collection_timeout" {
		t.Fatalf("%+v", timeout)
	}
	stopped := collectDockerWith(ctx, func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "container":
			return []byte(dockerTestID), nil
		case "inspect":
			return dockerTestProjection("exited", "none"), nil
		default:
			t.Fatal("stopped container requested statistics")
			return nil, nil
		}
	})
	if stopped.Status != protocol.DockerAvailable || stopped.Containers[0].CPUPercent != nil || stopped.Containers[0].MemoryUsedBytes != nil {
		t.Fatalf("stopped samples fabricated: %+v", stopped)
	}
	bad := collectDockerWith(ctx, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "container" {
			return []byte(strings.Repeat(dockerTestID+"\n", 501)), nil
		}
		t.Fatal("oversized inventory inspected")
		return nil, nil
	})
	if bad.ErrorCode != "limit_exceeded" {
		t.Fatalf("%+v", bad)
	}
	for _, raw := range []string{`{"ID":"` + dockerTestID + `","CPUPerc":"NaN%","MemUsage":"0B / 1GiB"}`, `{"ID":"` + dockerTestID + `","CPUPerc":"1%","MemUsage":"InfB / 1GiB"}`, `{"ID":"bad","CPUPerc":"1%","MemUsage":"1B / 1B"}`, `{}`, `[]`} {
		if _, err := parseDockerStats([]byte(raw)); err == nil {
			t.Errorf("bad stats accepted %s", raw)
		}
	}
	for raw, want := range map[string]int64{"0B": 0, "1.5GiB": 1610612736, "2kB": 2000, "2 KiB": 2048} {
		got, err := dockerBytes(raw)
		if err != nil || got != want {
			t.Errorf("%s => %d %v", raw, got, err)
		}
	}
}

func TestDockerChecksUseFreshExactObservations(t *testing.T) {
	state, health, fail := "running", "healthy", false
	reads := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("unavailable")
		}
		switch args[0] {
		case "container":
			reads++
			if args[len(args)-1] != `name=^/web\.1$` {
				t.Fatalf("not exact escaped name: %v", args)
			}
			return []byte(dockerTestID), nil
		case "inspect":
			return dockerTestProjection(state, health), nil
		case "stats":
			t.Fatal("a check should not sample resources")
		}
		return nil, nil
	}
	cfg := json.RawMessage(`{"container":"web.1","mode":"healthy"}`)
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); !ok || unknown {
		t.Fatal("healthy failed")
	}
	health = "unhealthy"
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); ok || unknown {
		t.Fatal("fresh unhealthy not seen")
	}
	fail = true
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); ok || !unknown {
		t.Fatal("old observation reused during engine failure")
	}
	if reads != 2 {
		t.Fatalf("expected independent reads: %d", reads)
	}
	fail = false
	health = "none"
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); ok || unknown {
		t.Fatal("healthy mode without healthcheck passed")
	}
	cfg = json.RawMessage(`{"container":"web.1","mode":"running"}`)
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); !ok || unknown {
		t.Fatal("running mode needs no healthcheck")
	}
	state = "restarting"
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, run); ok || unknown {
		t.Fatal("restart loop passed")
	}
	if ok, unknown, _ := dockerCheckWith(context.Background(), cfg, func(context.Context, ...string) ([]byte, error) { return nil, nil }); ok || unknown {
		t.Fatal("missing container did not fail")
	}
	stale := protocol.DockerSnapshot{Status: protocol.DockerAvailable}
	now := time.Now()
	if ok, unknown, _ := dockerCheck(stale, now.Add(-2*time.Minute), now, cfg); ok || !unknown {
		t.Fatal("stale passed")
	}
}

func TestDockerCommandBoundsAndEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "tcp://forbidden.example:2375")
	t.Setenv("DOCKER_CONTEXT", "forbidden")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	out, err := dockerCommandOutput(context.Background(), executable, "-test.run=TestDockerCommandHelper", "--", "env")
	if err != nil || string(out) != "clean" {
		t.Fatalf("inherited remote config: %q %v", out, err)
	}
	if _, err = dockerCommandOutput(context.Background(), executable, "-test.run=TestDockerCommandHelper", "--", "overflow"); err == nil {
		t.Fatal("output bound not enforced")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = dockerCommandOutput(ctx, executable, "-test.run=TestDockerCommandHelper", "--", "sleep"); err == nil {
		t.Fatal("timeout not enforced")
	}
}

func TestDockerCommandHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "env":
		if os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" || os.Getenv("DOCKER_TLS_VERIFY") != "" {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("clean")
	case "overflow":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", protocol.MaxDockerSnapshotBytes+1))
	case "sleep":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestDockerDisabledAndUnsupportedOrMissingCLI(t *testing.T) {
	t.Setenv("RMM_DOCKER_MONITORING", "false")
	if s := collectDocker(context.Background()); s.Status != protocol.DockerUnsupported || s.ErrorCode != "disabled" {
		t.Fatalf("disabled: %+v", s)
	}
	t.Setenv("RMM_DOCKER_MONITORING", "")
	if runtime.GOOS != "linux" {
		if s := collectDocker(context.Background()); s.Status != protocol.DockerUnsupported || s.ErrorCode != "platform_unsupported" {
			t.Fatalf("platform: %+v", s)
		}
		return
	}
	for _, p := range []string{"/usr/bin/docker", "/usr/local/bin/docker", "/bin/docker"} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			t.Skip("CLI installed; missing-CLI case requires an ordinary Docker-free test environment")
		}
	}
	if s := collectDocker(context.Background()); s.Status != protocol.DockerNotInstalled || s.ErrorCode != "cli_missing" {
		t.Fatalf("missing CLI: %+v", s)
	}
}
