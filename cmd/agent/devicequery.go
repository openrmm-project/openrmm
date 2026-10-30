package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"github.com/p1ngu/rmm/internal/protocol"
)

// Device queries: the agent's built-in, read-only reports (job type
// device_query, docs/23-ai-autonomy.md).
//
// Each one is fixed code. The request names a query from a closed catalog and
// carries parameters from closed sets, checked again here (the server checked
// them first). A name filter is applied in Go to the data read, never placed on
// a command line; the only parameter that reaches a command is a whole number
// formatted here. Nothing here writes to the device.

// runDeviceQuery is a variable so tests can stand in for it.
var runDeviceQuery = deviceQuery

func deviceQuery(ctx context.Context, script string) (int, string, string) {
	var req protocol.DeviceQueryRequest
	if err := json.Unmarshal([]byte(script), &req); err != nil {
		return 2, "", "invalid device query: " + err.Error()
	}
	req, err := protocol.NormalizeDeviceQuery(req)
	if err != nil {
		return 2, "", err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var result any
	switch req.Query {
	case protocol.DeviceQueryProcesses:
		result, err = queryProcesses(ctx, req)
	case protocol.DeviceQueryServices:
		result, err = queryServices(ctx, req)
	case protocol.DeviceQueryEvents:
		result, err = queryEvents(ctx, req)
	case protocol.DeviceQueryDisk:
		result, err = queryDisk(ctx)
	case protocol.DeviceQueryNetwork:
		result, err = queryNetwork(ctx, req)
	case protocol.DeviceQuerySystem:
		result, err = querySystem(ctx)
	case protocol.DeviceQuerySoftware:
		result, err = querySoftware(ctx, req)
	default:
		return 2, "", "unknown device query: " + req.Query
	}
	if err != nil {
		return 1, "", err.Error()
	}
	out, _ := json.Marshal(map[string]any{
		"query": req.Query, "params": req.Params, "os": runtime.GOOS,
		"collected": time.Now().UTC().Format(time.RFC3339), "result": result,
	})
	if len(out) > protocol.MaxDeviceQueryOutput {
		return 0, string(out[:protocol.MaxDeviceQueryOutput]), "[output truncated]"
	}
	return 0, string(out), ""
}

func matchesName(filter string, names ...string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), f) {
			return true
		}
	}
	return false
}

// ---- processes ----

type procRow struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpu_percent"`
	MemoryMB   float64 `json:"memory_mb"`
}

// queryProcesses samples CPU time twice, a second apart, so the figure is what
// each process uses now rather than its average since it started. Only the
// name is reported, never the command line, which can hold credentials.
func queryProcesses(ctx context.Context, req protocol.DeviceQueryRequest) (any, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	before := map[int32]float64{}
	for _, p := range procs {
		if t, err := p.TimesWithContext(ctx); err == nil {
			before[p.Pid] = t.User + t.System
		}
	}
	const window = time.Second
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(window):
	}
	cpus := float64(runtime.NumCPU())
	rows := make([]procRow, 0, len(procs))
	for _, p := range procs {
		name, err := p.NameWithContext(ctx)
		if err != nil || name == "" {
			continue
		}
		row := procRow{PID: p.Pid, Name: name}
		if t, err := p.TimesWithContext(ctx); err == nil {
			if start, ok := before[p.Pid]; ok {
				row.CPUPercent = round1((t.User + t.System - start) / window.Seconds() / cpus * 100)
			}
		}
		if m, err := p.MemoryInfoWithContext(ctx); err == nil && m != nil {
			row.MemoryMB = round1(float64(m.RSS) / (1 << 20))
		}
		rows = append(rows, row)
	}
	if req.Params["sort"] == "memory" {
		sort.Slice(rows, func(i, j int) bool { return rows[i].MemoryMB > rows[j].MemoryMB })
	} else {
		sort.Slice(rows, func(i, j int) bool { return rows[i].CPUPercent > rows[j].CPUPercent })
	}
	limit := protocol.DeviceQueryInt(req, "limit")
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return map[string]any{"processes": rows, "total": len(procs), "cpu_count": runtime.NumCPU()}, nil
}

func round1(f float64) float64 {
	if f < 0 {
		return 0
	}
	return float64(int64(f*10+0.5)) / 10
}

// ---- services ----

type serviceRow struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	State       string `json:"state"`
	Running     bool   `json:"running"`
	StartType   string `json:"start_type,omitempty"`
}

func queryServices(ctx context.Context, req protocol.DeviceQueryRequest) (any, error) {
	var all []serviceRow
	var err error
	switch runtime.GOOS {
	case "windows":
		all, err = servicesWindows(ctx)
	case "linux":
		all, err = servicesLinux(ctx)
	case "darwin":
		all, err = servicesDarwin(ctx)
	default:
		return nil, fmt.Errorf("services are not available on %s", runtime.GOOS)
	}
	if err != nil {
		return nil, err
	}
	state := req.Params["state"]
	out := []serviceRow{}
	for _, s := range all {
		if !matchesName(req.Params["name"], s.Name, s.DisplayName) {
			continue
		}
		if (state == "running" && !s.Running) || (state == "stopped" && s.Running) {
			continue
		}
		out = append(out, s)
	}
	total := len(out)
	const max = 200
	if len(out) > max {
		out = out[:max]
	}
	return map[string]any{"services": out, "matched": total}, nil
}

func servicesWindows(ctx context.Context) ([]serviceRow, error) {
	const ps = `Get-Service | Select-Object Name,DisplayName,@{n='Status';e={$_.Status.ToString()}},@{n='StartType';e={$_.StartType.ToString()}} | ConvertTo-Json -Compress`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return nil, fmt.Errorf("Get-Service: %w", err)
	}
	return parseWindowsServices(out)
}

func parseWindowsServices(out []byte) ([]serviceRow, error) {
	var raw []struct {
		Name, DisplayName, Status, StartType string
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) > 0 && trimmed[0] == '{' { // a single service is not an array
		trimmed = append(append([]byte{'['}, trimmed...), ']')
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, err
	}
	rows := make([]serviceRow, 0, len(raw))
	for _, s := range raw {
		rows = append(rows, serviceRow{Name: s.Name, DisplayName: s.DisplayName, State: s.Status,
			Running: s.Status == "Running", StartType: s.StartType})
	}
	return rows, nil
}

func servicesLinux(ctx context.Context) ([]serviceRow, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--all",
		"--no-pager", "--no-legend", "--plain").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl: %w", err)
	}
	return parseSystemctlUnits(out), nil
}

// parseSystemctlUnits reads `UNIT LOAD ACTIVE SUB DESCRIPTION` lines.
func parseSystemctlUnits(out []byte) []serviceRow {
	var rows []serviceRow
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasSuffix(f[0], ".service") {
			continue
		}
		rows = append(rows, serviceRow{Name: strings.TrimSuffix(f[0], ".service"),
			DisplayName: strings.Join(f[4:], " "), State: f[2] + "/" + f[3], Running: f[3] == "running"})
	}
	return rows
}

func servicesDarwin(ctx context.Context) ([]serviceRow, error) {
	out, err := exec.CommandContext(ctx, "launchctl", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("launchctl: %w", err)
	}
	return parseLaunchctlList(out), nil
}

// parseLaunchctlList reads `PID Status Label` lines; a running job has a PID.
func parseLaunchctlList(out []byte) []serviceRow {
	var rows []serviceRow
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[0] == "PID" {
			continue
		}
		running := f[0] != "-"
		state := "stopped"
		if running {
			state = "running"
		}
		rows = append(rows, serviceRow{Name: f[2], State: state + " (last exit " + f[1] + ")", Running: running})
	}
	return rows
}

// ---- events ----

type eventRow struct {
	Time    string `json:"time"`
	Log     string `json:"log,omitempty"`
	Source  string `json:"source"`
	ID      int    `json:"id,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

const maxEventMessage = 300

func shorten(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > maxEventMessage {
		s = string(r[:maxEventMessage]) + "…"
	}
	return s
}

func queryEvents(ctx context.Context, req protocol.DeviceQueryRequest) (any, error) {
	hours := protocol.DeviceQueryInt(req, "hours")
	var rows []eventRow
	var err error
	switch runtime.GOOS {
	case "windows":
		rows, err = eventsWindows(ctx, hours)
	case "linux":
		rows, err = eventsLinux(ctx, hours)
	case "darwin":
		rows, err = eventsDarwin(ctx, hours)
	default:
		return nil, fmt.Errorf("events are not available on %s", runtime.GOOS)
	}
	if err != nil {
		return nil, err
	}
	out := []eventRow{}
	for _, e := range rows {
		if matchesName(req.Params["source"], e.Source) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	matched := len(out)
	if limit := protocol.DeviceQueryInt(req, "limit"); len(out) > limit {
		out = out[:limit]
	}
	return map[string]any{"events": out, "matched": matched, "hours": hours, "levels": "errors and critical"}, nil
}

func eventsWindows(ctx context.Context, hours int) ([]eventRow, error) {
	// hours is a validated whole number, formatted here: the only parameter
	// any query places on a command line.
	ps := `$h=` + strconv.Itoa(hours) + `; Get-WinEvent -FilterHashtable @{LogName='System','Application'; Level=1,2; StartTime=(Get-Date).AddHours(-$h)} -MaxEvents 500 -ErrorAction SilentlyContinue | ` +
		`Select-Object @{n='Time';e={$_.TimeCreated.ToUniversalTime().ToString('o')}},@{n='Log';e={$_.LogName}},@{n='Source';e={$_.ProviderName}},@{n='Id';e={$_.Id}},@{n='Level';e={$_.LevelDisplayName}},@{n='Message';e={$_.Message}} | ConvertTo-Json -Compress`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return nil, fmt.Errorf("Get-WinEvent: %w", err)
	}
	return parseWindowsEvents(out)
}

func parseWindowsEvents(out []byte) ([]eventRow, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil // no events in the window
	}
	if trimmed[0] == '{' {
		trimmed = append(append([]byte{'['}, trimmed...), ']')
	}
	var raw []struct {
		Time, Log, Source, Level, Message string
		ID                                int `json:"Id"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, err
	}
	rows := make([]eventRow, 0, len(raw))
	for _, e := range raw {
		rows = append(rows, eventRow{Time: e.Time, Log: e.Log, Source: e.Source, ID: e.ID, Level: e.Level, Message: shorten(e.Message)})
	}
	return rows, nil
}

func eventsLinux(ctx context.Context, hours int) ([]eventRow, error) {
	out, err := exec.CommandContext(ctx, "journalctl", "--since="+strconv.Itoa(hours)+" hours ago",
		"--priority=err", "--output=json", "--no-pager", "--lines=500").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	return parseJournal(out), nil
}

var journalLevels = map[string]string{"0": "emergency", "1": "alert", "2": "critical", "3": "error"}

// parseJournal reads journalctl's one-object-per-line JSON. MESSAGE can be an
// array of bytes when it is not valid UTF-8.
func parseJournal(out []byte) []eventRow {
	var rows []eventRow
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		var msg string
		switch m := e["MESSAGE"].(type) {
		case string:
			msg = m
		case []any:
			b := make([]byte, 0, len(m))
			for _, v := range m {
				if f, ok := v.(float64); ok {
					b = append(b, byte(f))
				}
			}
			msg = string(bytes.ToValidUTF8(b, []byte("?")))
		}
		ts := ""
		if us, ok := e["__REALTIME_TIMESTAMP"].(string); ok {
			if n, err := strconv.ParseInt(us, 10, 64); err == nil {
				ts = time.UnixMicro(n).UTC().Format(time.RFC3339)
			}
		}
		src, _ := e["SYSLOG_IDENTIFIER"].(string)
		if src == "" {
			src, _ = e["_COMM"].(string)
		}
		prio, _ := e["PRIORITY"].(string)
		rows = append(rows, eventRow{Time: ts, Source: src, Level: journalLevels[prio], Message: shorten(msg)})
	}
	return rows
}

func eventsDarwin(ctx context.Context, hours int) ([]eventRow, error) {
	out, err := exec.CommandContext(ctx, "log", "show", "--last", strconv.Itoa(hours)+"h",
		"--style", "ndjson", "--predicate", `messageType == error OR messageType == fault`).Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("log show: %w", err)
	}
	var rows []eventRow
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() && len(rows) < 2000 {
		var e struct {
			Timestamp    string `json:"timestamp"`
			MessageType  string `json:"messageType"`
			Process      string `json:"processImagePath"`
			EventMessage string `json:"eventMessage"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.EventMessage == "" {
			continue
		}
		src := e.Process
		if i := strings.LastIndexByte(src, '/'); i >= 0 {
			src = src[i+1:]
		}
		rows = append(rows, eventRow{Time: e.Timestamp, Source: src, Level: strings.ToLower(e.MessageType), Message: shorten(e.EventMessage)})
	}
	return rows, nil
}

// ---- disk ----

func queryDisk(ctx context.Context) (any, error) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	type volume struct {
		Mount       string  `json:"mount"`
		FSType      string  `json:"fstype"`
		TotalGB     float64 `json:"total_gb"`
		FreeGB      float64 `json:"free_gb"`
		UsedPercent float64 `json:"used_percent"`
	}
	out := []volume{}
	for _, p := range parts {
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, volume{p.Mountpoint, p.Fstype, round1(float64(u.Total) / (1 << 30)),
			round1(float64(u.Free) / (1 << 30)), round1(u.UsedPercent)})
	}
	return map[string]any{"volumes": out}, nil
}

// ---- network ----

func queryNetwork(ctx context.Context, req protocol.DeviceQueryRequest) (any, error) {
	switch req.Params["section"] {
	case "listening":
		return networkListening(ctx)
	case "connections":
		return networkConnections(ctx)
	default:
		return networkConfig(ctx)
	}
}

func networkConfig(ctx context.Context) (any, error) {
	ifaces, err := gnet.InterfacesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	type nic struct {
		Name      string   `json:"name"`
		MAC       string   `json:"mac,omitempty"`
		Up        bool     `json:"up"`
		Addresses []string `json:"addresses"`
	}
	out := []nic{}
	for _, ifc := range ifaces {
		n := nic{Name: ifc.Name, MAC: ifc.HardwareAddr, Addresses: []string{}}
		for _, f := range ifc.Flags {
			if f == "up" {
				n.Up = true
			}
		}
		for _, a := range ifc.Addrs {
			n.Addresses = append(n.Addresses, a.Addr)
		}
		out = append(out, n)
	}
	return map[string]any{"interfaces": out}, nil
}

func processNames(ctx context.Context, pids map[int32]bool) map[int32]string {
	names := map[int32]string{}
	for pid := range pids {
		if pid <= 0 {
			continue
		}
		if p, err := process.NewProcessWithContext(ctx, pid); err == nil {
			if n, err := p.NameWithContext(ctx); err == nil {
				names[pid] = n
			}
		}
	}
	return names
}

func networkListening(ctx context.Context) (any, error) {
	conns, err := gnet.ConnectionsWithContext(ctx, "inet")
	if err != nil {
		return nil, err
	}
	type listener struct {
		Protocol string `json:"protocol"`
		Address  string `json:"address"`
		Port     uint32 `json:"port"`
		PID      int32  `json:"pid,omitempty"`
		Process  string `json:"process,omitempty"`
	}
	pids := map[int32]bool{}
	var out []listener
	seen := map[string]bool{}
	for _, c := range conns {
		proto := "tcp"
		if c.Type == 2 { // SOCK_DGRAM
			proto = "udp"
		}
		if proto == "tcp" && c.Status != "LISTEN" {
			continue
		}
		if proto == "udp" && c.Raddr.IP != "" {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d|%d", proto, c.Laddr.IP, c.Laddr.Port, c.Pid)
		if seen[key] {
			continue
		}
		seen[key] = true
		pids[c.Pid] = true
		out = append(out, listener{Protocol: proto, Address: c.Laddr.IP, Port: c.Laddr.Port, PID: c.Pid})
	}
	names := processNames(ctx, pids)
	for i := range out {
		out[i].Process = names[out[i].PID]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Protocol < out[j].Protocol
	})
	if len(out) > 200 {
		out = out[:200]
	}
	return map[string]any{"listening": out}, nil
}

func networkConnections(ctx context.Context) (any, error) {
	conns, err := gnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	byState := map[string]int{}
	remotes := map[string]int{}
	for _, c := range conns {
		byState[c.Status]++
		if c.Status == "ESTABLISHED" && c.Raddr.IP != "" {
			remotes[fmt.Sprintf("%s:%d", c.Raddr.IP, c.Raddr.Port)]++
		}
	}
	type remote struct {
		Endpoint    string `json:"endpoint"`
		Connections int    `json:"connections"`
	}
	top := make([]remote, 0, len(remotes))
	for k, v := range remotes {
		top = append(top, remote{k, v})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Connections != top[j].Connections {
			return top[i].Connections > top[j].Connections
		}
		return top[i].Endpoint < top[j].Endpoint
	})
	if len(top) > 20 {
		top = top[:20]
	}
	return map[string]any{"total": len(conns), "by_state": byState, "top_remote_endpoints": top}, nil
}

// ---- system ----

func querySystem(ctx context.Context) (any, error) {
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return nil, err
	}
	pending, why := pendingReboot()
	out := map[string]any{
		"hostname": info.Hostname, "os": info.OS, "platform": info.Platform,
		"platform_version": info.PlatformVersion, "kernel": info.KernelVersion,
		"uptime_hours":   round1(float64(info.Uptime) / 3600),
		"boot_time":      time.Unix(int64(info.BootTime), 0).UTC().Format(time.RFC3339),
		"pending_reboot": pending, "agent_version": agentVersion,
	}
	if why != "" {
		out["pending_reboot_reason"] = why
	}
	return out, nil
}

// pendingRebootFile is where Debian-family systems flag a needed restart.
const pendingRebootFile = "/var/run/reboot-required"

func pendingRebootUnix() (any, string) {
	if runtime.GOOS != "linux" {
		return "unknown", ""
	}
	if _, err := os.Stat(pendingRebootFile); err == nil {
		return true, pendingRebootFile
	}
	return false, ""
}

// ---- software ----

func querySoftware(ctx context.Context, req protocol.DeviceQueryRequest) (any, error) {
	var all []map[string]string
	switch runtime.GOOS {
	case "windows":
		all = softwareWindows(ctx)
	case "linux":
		all = softwareLinux(ctx)
	case "darwin":
		all = softwareDarwin(ctx)
	}
	out := []map[string]string{}
	for _, sw := range all {
		if matchesName(req.Params["name"], sw["name"]) {
			out = append(out, sw)
		}
	}
	matched := len(out)
	if limit := protocol.DeviceQueryInt(req, "limit"); len(out) > limit {
		out = out[:limit]
	}
	return map[string]any{"software": out, "matched": matched}, nil
}

// ---- parameters of a released script ----

var envParamName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// paramEnv turns a released script's parameters into environment variables.
// The server validated them against the script's declared schema; the agent
// still refuses names and values no schema could have produced, and cmd,
// which would expand them into its command line.
func paramEnv(shell string, params map[string]string) ([]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	if shell == "cmd" {
		return nil, fmt.Errorf("cmd scripts cannot take parameters")
	}
	env := make([]string, 0, len(params))
	for name, value := range params {
		if !envParamName.MatchString(name) {
			return nil, fmt.Errorf("invalid parameter name %q", name)
		}
		if len(value) > 200 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid value for parameter %q", name)
		}
		env = append(env, "OPENRMM_PARAM_"+strings.ToUpper(name)+"="+value)
	}
	sort.Strings(env)
	return env, nil
}
