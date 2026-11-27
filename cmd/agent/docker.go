package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

type dockerCommand func(context.Context, ...string) ([]byte, error)

// The template is a closed projection. In particular, .Config and .State.Health
// must not be serialized wholesale: they contain secrets and health-check logs.
const dockerInspectProjection = `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Config.Image}},"state":{{json .State.Status}},"health":{{with index .State "Health"}}{{json .Status}}{{else}}"none"{{end}},"restart_count":{{.RestartCount}},"started_at":{{json .State.StartedAt}},"compose_project":{{json (index .Config.Labels "com.docker.compose.project")}},"compose_service":{{json (index .Config.Labels "com.docker.compose.service")}}}`

func dockerFailure(code string) protocol.DockerSnapshot {
	return protocol.DockerSnapshot{Status: protocol.DockerUnavailable, ErrorCode: code, Containers: []protocol.DockerContainer{}}
}

func collectDocker(ctx context.Context) protocol.DockerSnapshot {
	return observeLocalDocker(ctx, collectDockerWith)
}

func observeLocalDocker(ctx context.Context, observe func(context.Context, dockerCommand) protocol.DockerSnapshot) protocol.DockerSnapshot {
	if os.Getenv("RMM_DOCKER_MONITORING") == "false" {
		return protocol.DockerSnapshot{Status: protocol.DockerUnsupported, ErrorCode: "disabled"}
	}
	if runtime.GOOS != "linux" {
		return protocol.DockerSnapshot{Status: protocol.DockerUnsupported, ErrorCode: "platform_unsupported"}
	}
	program := ""
	for _, p := range []string{"/usr/bin/docker", "/usr/local/bin/docker", "/bin/docker"} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			program = p
			break
		}
	}
	if program == "" {
		return protocol.DockerSnapshot{Status: protocol.DockerNotInstalled, ErrorCode: "cli_missing"}
	}
	// Do not inherit the operator's remote context, TLS configuration or auth
	// store. The agent observes only the rootful engine of this Linux endpoint.
	cfg, err := os.MkdirTemp("", "openrmm-docker-")
	if err != nil {
		return dockerFailure("collection_failed")
	}
	defer os.RemoveAll(cfg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return observe(ctx, func(ctx context.Context, args ...string) ([]byte, error) {
		args = append([]string{"--config", cfg, "--host", "unix:///var/run/docker.sock"}, args...)
		return dockerCommandOutput(ctx, program, args...)
	})
}

func collectDockerWith(ctx context.Context, run dockerCommand) protocol.DockerSnapshot {
	fail := func(code string) protocol.DockerSnapshot {
		if ctx.Err() != nil {
			code = "collection_timeout"
		}
		return dockerFailure(code)
	}
	out, err := run(ctx, "container", "ls", "--all", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		return fail("engine_unavailable")
	}
	ids := strings.Fields(string(out))
	if len(ids) > protocol.MaxDockerContainers {
		return fail("limit_exceeded")
	}
	for _, id := range ids {
		if !protocol.ValidDockerID(id) {
			return fail("collection_failed")
		}
	}
	result := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{}}
	if len(ids) == 0 {
		return result
	}
	args := append([]string{"inspect", "--type", "container", "--format", dockerInspectProjection}, ids...)
	out, err = run(ctx, args...)
	if err != nil {
		return fail("collection_failed")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var c protocol.DockerContainer
		err := dec.Decode(&c)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail("collection_failed")
		}
		c.Name = strings.TrimPrefix(c.Name, "/")
		if c.StartedAt != nil && c.StartedAt.IsZero() {
			c.StartedAt = nil
		}
		result.Containers = append(result.Containers, c)
		if len(result.Containers) > protocol.MaxDockerContainers {
			return fail("limit_exceeded")
		}
	}
	if len(result.Containers) != len(ids) {
		return fail("collection_failed")
	}
	wanted := map[string]bool{}
	running := []string{}
	for _, id := range ids {
		wanted[id] = true
	}
	for _, c := range result.Containers {
		if !wanted[c.ID] {
			return fail("collection_failed")
		}
		if c.State == "running" {
			running = append(running, c.ID)
		}
	}
	if err := result.Validate(); err != nil {
		return fail("collection_failed")
	}
	if len(running) > 0 {
		// Do not request stopped-container samples: Docker represents them as zeroes,
		// which would incorrectly look like fresh resource measurements.
		statsArgs := append([]string{"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"}, running...)
		out, err = run(ctx, statsArgs...)
		if err != nil {
			return fail("collection_failed")
		}
		stats, err := parseDockerStats(out)
		if err != nil {
			return fail("collection_failed")
		}
		for i := range result.Containers {
			c := &result.Containers[i]
			if s, ok := stats[c.ID]; ok && c.State == "running" {
				c.CPUPercent = &s.cpu
				c.MemoryUsedBytes = &s.used
				if s.limit > 0 {
					c.MemoryLimitBytes = &s.limit
				}
			}
		}
	}
	if err := result.Validate(); err != nil {
		return fail("collection_failed")
	}
	return result
}

type dockerStats struct {
	cpu         float64
	used, limit int64
}

func parseDockerStats(out []byte) (map[string]dockerStats, error) {
	result := map[string]dockerStats{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var row struct{ ID, CPUPerc, MemUsage string }
		err := dec.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !protocol.ValidDockerID(row.ID) {
			return nil, errors.New("invalid stats identity")
		}
		if _, exists := result[row.ID]; exists {
			return nil, errors.New("duplicate stats identity")
		}
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(row.CPUPerc), "%"), 64)
		if err != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0 || cpu > 1e8 {
			return nil, errors.New("invalid CPU statistics")
		}
		used, limit, ok := strings.Cut(row.MemUsage, "/")
		if !ok {
			return nil, errors.New("invalid memory statistics")
		}
		u, err := dockerBytes(used)
		if err != nil {
			return nil, err
		}
		l, err := dockerBytes(limit)
		if err != nil {
			return nil, err
		}
		result[row.ID] = dockerStats{cpu: cpu, used: u, limit: l}
		if len(result) > protocol.MaxDockerContainers {
			return nil, errors.New("too many statistics")
		}
	}
	return result, nil
}

func dockerBytes(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	units := []struct {
		suffix string
		factor float64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(raw, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, u.suffix)), 64)
			n *= u.factor
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1<<53-1 {
				return 0, errors.New("invalid memory quantity")
			}
			return int64(math.Round(n)), nil
		}
	}
	return 0, errors.New("unknown memory unit")
}

type dockerCapture struct {
	buf      bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (b *dockerCapture) Write(p []byte) (int, error) {
	n := len(p)
	left := protocol.MaxDockerSnapshotBytes - b.buf.Len()
	if len(p) > left {
		b.buf.Write(p[:left])
		b.exceeded = true
		b.cancel()
	} else {
		b.buf.Write(p)
	}
	return n, nil
}
func dockerCommandOutput(ctx context.Context, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	capture := &dockerCapture{cancel: cancel}
	cmd.Stdout = capture
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if capture.exceeded {
		return nil, errors.New("Docker output budget exceeded")
	}
	return capture.buf.Bytes(), err
}

// A connection owns its output channel. An old collection finishing during a
// reconnect can never enqueue a stale snapshot on the new transport.
func (a *agent) dockerLoop(done <-chan struct{}, out chan []byte) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.dockerCollectMu.Lock()
		if ctx.Err() != nil {
			a.dockerCollectMu.Unlock()
			return
		}
		snapshot := collectDocker(ctx)
		if ctx.Err() != nil {
			a.dockerCollectMu.Unlock()
			return
		}
		data, _ := json.Marshal(snapshot)
		frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgDockerSnapshot, Data: data})
		select {
		case <-done:
		case out <- frame:
		default:
		}
		a.dockerCollectMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func dockerCheck(snapshot protocol.DockerSnapshot, checked, timeNow time.Time, raw json.RawMessage) (success, unknown bool, output string) {
	cfg, err := protocol.NormalizeDockerCheck(raw)
	if err != nil {
		return false, true, "Invalid Docker check configuration."
	}
	if snapshot.Status != protocol.DockerAvailable || checked.IsZero() || timeNow.Sub(checked) > protocol.DockerFreshFor || timeNow.Before(checked) {
		return false, true, "Docker collection is unavailable or stale."
	}
	for _, c := range snapshot.Containers {
		if c.Name == cfg.Container {
			if c.State != "running" {
				return false, false, fmt.Sprintf("Container %s is %s.", cfg.Container, c.State)
			}
			if cfg.Mode == "healthy" && c.Health != "healthy" {
				return false, false, fmt.Sprintf("Container %s health is %s.", cfg.Container, c.Health)
			}
			return true, false, fmt.Sprintf("Container %s satisfies %s.", cfg.Container, cfg.Mode)
		}
	}
	return false, false, fmt.Sprintf("Container %s is missing.", cfg.Container)
}

// Every scheduled check reads the requested container again. Reusing the minute
// snapshot would count one sample as several consecutive failures/recoveries.
func dockerCheckSnapshot(ctx context.Context, cfg protocol.DockerCheckConfig, run dockerCommand) protocol.DockerSnapshot {
	return collectDockerWith(ctx, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "stats" {
			return nil, nil
		} // checks need fresh state, not resource sampling
		if args[0] == "container" {
			args = append(args, "--filter", "name=^/"+regexp.QuoteMeta(cfg.Container)+"$")
		}
		return run(ctx, args...)
	})
}
func dockerCheckWith(ctx context.Context, raw json.RawMessage, run dockerCommand) (bool, bool, string) {
	cfg, err := protocol.NormalizeDockerCheck(raw)
	if err != nil {
		return false, true, "Invalid Docker check configuration."
	}
	snapshot := dockerCheckSnapshot(ctx, cfg, run)
	now := time.Now()
	return dockerCheck(snapshot, now, now, raw)
}
func runDockerCheck(ctx context.Context, raw json.RawMessage) (bool, bool, string) {
	cfg, err := protocol.NormalizeDockerCheck(raw)
	if err != nil {
		return false, true, "Invalid Docker check configuration."
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot := observeLocalDocker(ctx, func(ctx context.Context, run dockerCommand) protocol.DockerSnapshot {
		return dockerCheckSnapshot(ctx, cfg, run)
	})
	now := time.Now()
	return dockerCheck(snapshot, now, now, raw)
}
