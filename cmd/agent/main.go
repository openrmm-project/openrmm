// Command agent is the cross-platform endpoint agent. It maintains a persistent
// WebSocket to the server, sends heartbeats + rich inventory, runs approved jobs,
// and executes monitoring checks. It can install itself as a system service.
//
// Remote desktop (native provider / RustDesk fork) is a separate module that
// rides on the relay; this agent handles enrollment, telemetry, jobs and checks.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kardianos/service"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"

	"github.com/p1ngu/rmm/internal/protocol"
)

const agentVersion = "0.2.1"

type state struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

type agent struct {
	wsURL       string
	enrollToken string
	statePath   string
	st          state
	send        chan []byte
	quit        chan struct{}
}

// program adapts the agent to the service interface.
type program struct{ a *agent }

func (p *program) Start(_ service.Service) error { go p.a.loop(); return nil }
func (p *program) Stop(_ service.Service) error  { close(p.a.quit); return nil }

func main() {
	wsURL := flag.String("server", envOr("RMM_SERVER_WS", "ws://localhost:8080/ws/agent"), "server agent WebSocket URL")
	enroll := flag.String("enroll-token", os.Getenv("RMM_ENROLL_TOKEN"), "one-time enrollment token (first run only)")
	statePath := flag.String("state", envOr("RMM_AGENT_STATE", "agent-state.json"), "path to the agent identity file")
	action := flag.String("action", "", "service action: install | uninstall | start | stop (empty = run)")
	flag.Parse()

	a := &agent{wsURL: *wsURL, enrollToken: *enroll, statePath: *statePath, quit: make(chan struct{})}
	a.loadState()

	svcArgs := []string{"-server", *wsURL, "-state", *statePath}
	if *enroll != "" {
		// Baked into the service definition so the first boot can enroll; once
		// state.json exists the agent re-auths with its token and ignores this.
		svcArgs = append(svcArgs, "-enroll-token", *enroll)
	}
	svcConfig := &service.Config{
		Name:        "rmm-agent",
		DisplayName: "RMM-AI Agent",
		Description: "RMM-AI endpoint agent: telemetry, jobs and monitoring checks.",
		Arguments:   svcArgs,
	}
	prg := &program{a: a}
	svc, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatalf("service: %v", err)
	}

	if *action != "" {
		if err := service.Control(svc, *action); err != nil {
			log.Fatalf("service %s: %v", *action, err)
		}
		log.Printf("service %s ok", *action)
		return
	}

	// Run (foreground when interactive, managed when installed as a service).
	if err := svc.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}

// loop reconnects forever until quit is closed.
func (a *agent) loop() {
	for {
		select {
		case <-a.quit:
			return
		default:
		}
		if err := a.runOnce(); err != nil {
			log.Printf("connection ended: %v; retrying in 5s", err)
		}
		select {
		case <-a.quit:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (a *agent) runOnce() error {
	conn, _, err := websocket.DefaultDialer.Dial(a.wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := a.handshake(conn); err != nil {
		return err
	}
	log.Printf("registered as %s", a.st.AgentID)

	a.send = make(chan []byte, 64)
	done := make(chan struct{})
	go a.writePump(conn, done)
	go a.telemetryLoop(done)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			close(done)
			return err
		}
		a.handleFrame(raw)
	}
}

// handshake registers (enroll on first run, otherwise re-auth) and stores the
// returned identity.
func (a *agent) handshake(conn *websocket.Conn) error {
	hostname, _ := os.Hostname()
	reg := protocol.RegisterReq{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  agentVersion,
	}
	if a.st.AgentID != "" && a.st.AgentToken != "" {
		reg.AgentID = a.st.AgentID
		reg.AgentToken = a.st.AgentToken
	} else {
		reg.EnrollToken = a.enrollToken
	}
	if err := writeEnvelope(conn, protocol.MsgRegister, reg); err != nil {
		return err
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if env.Type == protocol.MsgError {
		return errFromData(env.Data)
	}
	var resp protocol.RegisterResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		return err
	}
	a.st.AgentID = resp.AgentID
	if resp.AgentToken != "" {
		a.st.AgentToken = resp.AgentToken
	}
	a.saveState()
	return nil
}

func (a *agent) handleFrame(raw []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	switch env.Type {
	case protocol.MsgJob:
		var job protocol.JobMsg
		if json.Unmarshal(env.Data, &job) == nil {
			go a.runJob(job)
		}
	case protocol.MsgRunCheck:
		var chk protocol.RunCheckMsg
		if json.Unmarshal(env.Data, &chk) == nil {
			go a.runCheck(chk)
		}
	case protocol.MsgRemoteStart:
		var rs protocol.RemoteStartMsg
		if json.Unmarshal(env.Data, &rs) == nil {
			log.Printf("remote session requested: %s (relay %s) — media module not bundled in MVP", rs.SessionID, rs.RelayURL)
		}
	}
}

// runJob executes a script and reports the result.
func (a *agent) runJob(job protocol.JobMsg) {
	log.Printf("running job %s (%s)", job.JobID, job.Shell)
	exitCode, stdout, stderr := runScript(job.Shell, job.Script)
	a.queue(protocol.MsgJobResult, protocol.JobResultMsg{
		JobID: job.JobID, ExitCode: exitCode, Stdout: stdout, Stderr: stderr, Success: exitCode == 0,
	})
}

// telemetryLoop sends periodic heartbeats and inventory snapshots.
func (a *agent) telemetryLoop(done <-chan struct{}) {
	// Collect inventory off the loop so software/network probing (which can take
	// seconds) never delays heartbeats.
	sendInventory := func() { go func() { a.queue(protocol.MsgInventory, protocol.InventoryMsg{Data: collectInventory()}) }() }
	sendInventory()
	hb := time.NewTicker(20 * time.Second)
	inv := time.NewTicker(5 * time.Minute)
	defer hb.Stop()
	defer inv.Stop()
	for {
		select {
		case <-done:
			return
		case <-hb.C:
			a.queue(protocol.MsgHeartbeat, struct{}{})
		case <-inv.C:
			sendInventory()
		}
	}
}

func (a *agent) writePump(conn *websocket.Conn, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case msg := <-a.send:
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
}

func (a *agent) queue(typ string, payload any) {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	select {
	case a.send <- frame:
	default:
		log.Printf("send buffer full, dropping %s", typ)
	}
}

// ---------- state ----------

func (a *agent) loadState() {
	b, err := os.ReadFile(a.statePath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &a.st)
}

func (a *agent) saveState() {
	b, _ := json.MarshalIndent(a.st, "", "  ")
	if err := os.WriteFile(a.statePath, b, 0o600); err != nil {
		log.Printf("warning: could not persist agent state: %v", err)
	}
}

// ---------- helpers ----------

func writeEnvelope(conn *websocket.Conn, typ string, payload any) error {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	return conn.WriteMessage(websocket.TextMessage, frame)
}

func errFromData(data json.RawMessage) error {
	var m map[string]string
	_ = json.Unmarshal(data, &m)
	return fmt.Errorf("server rejected registration: %s", m["error"])
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---------- script execution ----------

func runScript(shell, script string) (int, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	switch shell {
	case "powershell":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", script)
	case "sh":
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	default: // bash
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
			stderr.WriteString("\n" + err.Error())
		}
	}
	// Cap captured output to avoid OOM / DB bloat from runaway scripts.
	return exitCode, truncate(stdout.String(), maxJobOutput), truncate(stderr.String(), maxJobOutput)
}

// maxJobOutput bounds stdout/stderr captured per job (1 MiB each).
const maxJobOutput = 1 << 20

// ---------- monitoring checks ----------

// runCheck executes a check by type and reports the raw outcome; the server
// applies thresholds and decides passing/warning/failing.
func (a *agent) runCheck(chk protocol.RunCheckMsg) {
	var value *float64
	var output string
	success := false

	switch chk.Type {
	case "diskspace":
		var cfg struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		if cfg.Path == "" {
			cfg.Path = defaultDiskPath()
		}
		if u, err := disk.Usage(cfg.Path); err == nil {
			free := 100 - u.UsedPercent
			value, output, success = &free, fmt.Sprintf("free %.1f%% on %s", free, cfg.Path), true
		} else {
			output = err.Error()
		}
	case "memory":
		if m, err := mem.VirtualMemory(); err == nil {
			free := 100 - m.UsedPercent
			value, output, success = &free, fmt.Sprintf("free memory %.1f%%", free), true
		} else {
			output = err.Error()
		}
	case "cpuload":
		if pcts, err := cpu.Percent(500*time.Millisecond, false); err == nil && len(pcts) > 0 {
			v := pcts[0]
			value, output, success = &v, fmt.Sprintf("cpu load %.1f%%", v), true
		} else {
			output = "cpu read failed"
		}
	case "ping":
		var cfg struct {
			Host string `json:"host"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		success, output = doPing(cfg.Host)
	case "winservice":
		var cfg struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		success, output = winServiceRunning(cfg.Service)
	case "script":
		var cfg struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		code, out, errOut := runScript(cfg.Shell, cfg.Script)
		fc := float64(code)
		value, output, success = &fc, out+errOut, code == 0
	default:
		output = "unknown check type: " + chk.Type
	}

	a.queue(protocol.MsgCheckResult, protocol.CheckResultMsg{
		CheckID: chk.CheckID, Value: value, Output: truncate(output, 4000), Success: success,
	})
}

func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		return "C:"
	}
	return "/"
}

func doPing(host string) (bool, string) {
	if host == "" {
		return false, "no host configured"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", "3000", host)
	} else {
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "3", host)
	}
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

func winServiceRunning(name string) (bool, string) {
	if runtime.GOOS != "windows" {
		return false, "winservice check only runs on Windows"
	}
	if name == "" {
		return false, "no service configured"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "sc", "query", name).CombinedOutput()
	running := strings.Contains(string(out), "RUNNING")
	return running, string(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// ---------- inventory ----------

func collectInventory() json.RawMessage {
	hostname, _ := os.Hostname()
	data := map[string]any{
		"hostname":      hostname,
		"os":            runtime.GOOS,
		"arch":          runtime.GOARCH,
		"num_cpu":       runtime.NumCPU(),
		"agent_version": agentVersion,
		"collected":     time.Now().UTC().Format(time.RFC3339),
	}
	if hi, err := host.Info(); err == nil {
		data["platform"] = hi.Platform
		data["platform_version"] = hi.PlatformVersion
		data["kernel_version"] = hi.KernelVersion
		data["uptime_sec"] = hi.Uptime
	}
	if ci, err := cpu.Info(); err == nil && len(ci) > 0 {
		data["cpu_model"] = ci[0].ModelName
		data["cpu_cores"] = ci[0].Cores
	}
	if m, err := mem.VirtualMemory(); err == nil {
		data["mem_total_bytes"] = m.Total
		data["mem_used_percent"] = m.UsedPercent
	}
	if parts, err := disk.Partitions(false); err == nil {
		var disks []map[string]any
		for _, p := range parts {
			if u, err := disk.Usage(p.Mountpoint); err == nil {
				disks = append(disks, map[string]any{
					"mount":        p.Mountpoint,
					"fstype":       p.Fstype,
					"total_bytes":  u.Total,
					"used_percent": u.UsedPercent,
				})
			}
		}
		data["disks"] = disks
	}
	data["network_interfaces"] = collectNetwork()
	data["software"] = collectSoftware()
	raw, _ := json.Marshal(data)
	return raw
}

// collectNetwork returns the up, non-loopback interfaces that have an IPv4.
func collectNetwork() []map[string]string {
	ifaces, err := gnet.Interfaces()
	if err != nil {
		return nil
	}
	var nics []map[string]string
	for _, ifc := range ifaces {
		up, loop := false, false
		for _, f := range ifc.Flags {
			switch f {
			case "up":
				up = true
			case "loopback":
				loop = true
			}
		}
		if !up || loop {
			continue
		}
		var ipv4 string
		for _, a := range ifc.Addrs {
			ip := a.Addr
			if i := strings.IndexByte(ip, '/'); i >= 0 {
				ip = ip[:i]
			}
			if strings.Contains(ip, ".") && !strings.HasPrefix(ip, "169.254") {
				ipv4 = ip
				break
			}
		}
		if ipv4 == "" {
			continue
		}
		nics = append(nics, map[string]string{"name": ifc.Name, "ipv4": ipv4, "mac": ifc.HardwareAddr})
	}
	return nics
}

// collectSoftware returns installed software (name + version), per OS. Bounded
// by a timeout so a slow query never stalls telemetry; capped in size.
func collectSoftware() []map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "windows":
		return softwareWindows(ctx)
	case "linux":
		return softwareLinux(ctx)
	case "darwin":
		return softwareDarwin(ctx)
	}
	return nil
}

func capSoftware(sw []map[string]string) []map[string]string {
	const max = 600
	if len(sw) > max {
		return sw[:max]
	}
	return sw
}

func softwareWindows(ctx context.Context) []map[string]string {
	const ps = `Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*','HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName } | Select-Object DisplayName,DisplayVersion | Sort-Object DisplayName -Unique | ConvertTo-Json -Compress`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "{") { // single entry -> wrap as array
		trimmed = "[" + trimmed + "]"
	}
	var arr []struct{ DisplayName, DisplayVersion string }
	if json.Unmarshal([]byte(trimmed), &arr) != nil {
		return nil
	}
	var sw []map[string]string
	for _, a := range arr {
		if a.DisplayName == "" {
			continue
		}
		sw = append(sw, map[string]string{"name": a.DisplayName, "version": a.DisplayVersion})
	}
	return capSoftware(sw)
}

func softwareLinux(ctx context.Context) []map[string]string {
	if out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Package}\t${Version}\n").Output(); err == nil {
		return parseTabbed(out)
	}
	if out, err := exec.CommandContext(ctx, "rpm", "-qa", "--qf", "%{NAME}\t%{VERSION}\n").Output(); err == nil {
		return parseTabbed(out)
	}
	return nil
}

func parseTabbed(out []byte) []map[string]string {
	var sw []map[string]string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		v := ""
		if len(parts) == 2 {
			v = parts[1]
		}
		sw = append(sw, map[string]string{"name": parts[0], "version": v})
	}
	return capSoftware(sw)
}

func softwareDarwin(ctx context.Context) []map[string]string {
	out, err := exec.CommandContext(ctx, "system_profiler", "-json", "SPApplicationsDataType").Output()
	if err != nil {
		return nil
	}
	var doc struct {
		Apps []struct {
			Name    string `json:"_name"`
			Version string `json:"version"`
		} `json:"SPApplicationsDataType"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	var sw []map[string]string
	for _, a := range doc.Apps {
		if a.Name == "" {
			continue
		}
		sw = append(sw, map[string]string{"name": a.Name, "version": a.Version})
	}
	return capSoftware(sw)
}
