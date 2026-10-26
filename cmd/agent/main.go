// Command agent is the cross-platform endpoint agent. It maintains a persistent
// WebSocket to the server, sends heartbeats + rich inventory, runs approved jobs,
// and executes monitoring checks. It can install itself as a system service.
//
// Remote desktop (the native provider's media sidecar) is a separate module that
// rides on the relay; this agent handles enrollment, telemetry, jobs and checks.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

// agentVersion is overwritten at release build time from the tag (see
// AGENT_LDFLAGS in the Makefile). The compiled-in default must still name a
// release whose capabilities this source actually has: the server gates features
// on this string, so an agent that under-reports is refused work it can do, and
// one that over-reports is handed work it cannot.
var agentVersion = "0.8.0"

type state struct {
	AgentID               string `json:"agent_id"`
	AgentToken            string `json:"agent_token"`
	PendingRotationID     string `json:"pending_rotation_id,omitempty"`
	PreviousAgentToken    string `json:"previous_agent_token,omitempty"`
	PendingCancellationID string `json:"pending_cancellation_id,omitempty"`
}

type agent struct {
	wsURL           string
	enrollToken     string
	allowEnrollment bool
	statePath       string
	healthPath      string
	st              state
	send            chan []byte
	quit            chan struct{}
	mu              sync.Mutex
	connMu          sync.Mutex
	conn            *websocket.Conn
	running         map[string]context.CancelFunc // job_id -> cancel, for in-flight jobs
	patchMu         sync.Mutex                    // scan/install/reboot must not overlap
	checkMu         sync.Mutex
	runningChecks   map[string]struct{} // execution_id -> running, prevents duplicate dispatch work

	// outbox holds job results the server hasn't acked yet, keyed by job_id, and
	// is persisted to disk. Results are re-sent on every (re)connect and on a
	// timer until acked, so a dropped connection (or an agent restart) can't lose
	// a finished job's result and strand it in "sent" server-side.
	obmu            sync.Mutex
	outbox          map[string]protocol.JobResultMsg
	outboxPath      string
	checkOutbox     map[string]protocol.CheckResultMsg
	checkOutboxPath string

	// started records, durably and before anything runs, every job this agent
	// has begun and not yet had its result acked (guarded by obmu). The server
	// re-sends a job still in "sent" on every reconnect — delivery is
	// at-least-once — so without this a job that was running, or whose result
	// was still on its way, would run a second time: one more install, reboot
	// or account change than the operator asked for.
	started     map[string]time.Time
	startedPath string
}

// program adapts the agent to the service interface.
type program struct{ a *agent }

func (p *program) Start(_ service.Service) error {
	startPlatformServices(p.a.quit)
	go p.a.loop()
	return nil
}
func (p *program) Stop(_ service.Service) error { close(p.a.quit); return nil }

func main() {
	wsURL := flag.String("server", envOr("RMM_SERVER_WS", "ws://localhost:8080/ws/agent"), "server agent WebSocket URL")
	enroll := flag.String("enroll-token", os.Getenv("RMM_ENROLL_TOKEN"), "one-time enrollment token (used only with -enroll-only)")
	statePath := flag.String("state", envOr("RMM_AGENT_STATE", "agent-state.json"), "path to the agent identity file")
	enrollOnly := flag.Bool("enroll-only", false, "enroll once, persist identity, and exit")
	action := flag.String("action", "", "service action: install | uninstall | start | stop | restart (empty = run)")
	requireTLS := flag.Bool("require-tls", envBool("RMM_REQUIRE_TLS"), "require a wss:// server URL")
	labMode := flag.Bool("lab-mode", envBool("RMM_LAB_MODE"), "allow insecure lab-only transport/update settings")
	updateKey := flag.String("update-public-key", os.Getenv("RMM_UPDATE_PUBLIC_KEY"), "base64 or hex Ed25519 update public key(s); comma-separated to trust several during key rotation")
	updateVerify := flag.String("update-verify", envOr("RMM_UPDATE_VERIFY", "strict"), "update verification mode: strict, warn, or off")
	healthPath := flag.String("health-file", os.Getenv("RMM_HEALTH_FILE"), "write a health marker after authenticated connection")
	printVer := flag.Bool("print-version", false, "print the agent version and exit (used to validate a self-update download)")
	checkUpgradeFrom := flag.String("check-upgrade-from", "", "exit unsuccessfully if this build is older than the supplied version")
	emergencyUpdateRollback := flag.Bool("emergency-update-rollback", false, "restore only the previous binary recorded by a signed pending update")
	verifyOwnSignature := flag.String("verify-own-signature", "", "check this binary against the given detached signature under -update-public-key and exit (used by install.sh when its OpenSSL cannot check Ed25519)")
	flag.Parse()

	if *printVer {
		fmt.Println(agentVersion)
		return
	}
	if *checkUpgradeFrom != "" {
		if err := validateUpgradeVersion(*checkUpgradeFrom, agentVersion); err != nil {
			log.Print(err)
			os.Exit(2)
		}
		return
	}
	runtimeUpdatePublicKey = strings.TrimSpace(*updateKey)
	runtimeUpdateVerify = strings.TrimSpace(*updateVerify)
	if *verifyOwnSignature != "" {
		if err := verifyOwnArtifactSignature(*verifyOwnSignature); err != nil {
			log.Print(err)
			os.Exit(2)
		}
		return
	}
	if *emergencyUpdateRollback {
		if err := emergencyRollback(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if recoverInterruptedUpdateAtStartup() {
		return
	}
	if *action == "" || *action == "install" {
		if err := validateRuntimeSecurity(*wsURL, *labMode, *requireTLS); err != nil {
			log.Fatal(err)
		}
	}

	cleanupUpdateTemps()

	a := &agent{
		wsURL: *wsURL, enrollToken: *enroll, allowEnrollment: *enrollOnly,
		statePath: *statePath, healthPath: *healthPath, quit: make(chan struct{}),
		running: map[string]context.CancelFunc{}, runningChecks: map[string]struct{}{},
		outbox: map[string]protocol.JobResultMsg{}, outboxPath: *statePath + ".outbox",
		checkOutbox: map[string]protocol.CheckResultMsg{}, checkOutboxPath: *statePath + ".check-outbox",
		started: map[string]time.Time{}, startedPath: *statePath + ".started",
	}
	hardenStateDir(*statePath)
	a.loadState()
	a.loadOutbox()
	a.loadCheckOutbox()
	a.loadStarted()
	if a.st.AgentID != "" && a.st.AgentToken != "" {
		// A legacy service may still pass -enroll-token, but an enrolled agent must
		// never retain or reuse that bootstrap capability at runtime.
		a.enrollToken = ""
	}
	if *enrollOnly {
		if a.st.AgentID != "" || a.st.AgentToken != "" {
			log.Fatal("enroll-only: remove the existing state file only when intentionally re-enrolling")
		}
		if *enroll == "" {
			log.Fatal("enroll-only: -enroll-token is required")
		}
		if err := a.enrollOnce(); err != nil {
			log.Fatalf("enroll-only: %v", err)
		}
		log.Printf("enrolled as %s", a.st.AgentID)
		return
	}
	if *action == "install" && (a.st.AgentID == "" || a.st.AgentToken == "") {
		log.Fatal("service install requires an enrolled identity; run with -enroll-only first")
	}

	svcArgs := []string{"-server", *wsURL, "-state", *statePath}
	if *labMode {
		svcArgs = append(svcArgs, "-lab-mode")
	} else {
		svcArgs = append(svcArgs, "-require-tls")
	}
	svcArgs = append(svcArgs, "-update-verify", updateVerifyMode())
	if key := effectiveUpdatePublicKey(); key != "" {
		svcArgs = append(svcArgs, "-update-public-key", key)
	}
	if *healthPath != "" {
		svcArgs = append(svcArgs, "-health-file", *healthPath)
	}
	svcConfig := &service.Config{
		Name:        "rmm-agent",
		DisplayName: "OpenRMM Agent",
		Description: "OpenRMM endpoint agent: telemetry, jobs and monitoring checks.",
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
	setupServiceLog()
	if err := svc.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}

// Reconnect pacing. A fixed delay makes the whole fleet retry in lockstep after
// a server outage — every endpoint failed at the same instant, so every endpoint
// comes back at the same instant, hammering the server exactly when it is least
// able to cope. Backing off bounds the sustained load and the jitter spreads the
// arrivals; without jitter, backoff alone just synchronises the herd on a longer
// period.
const (
	reconnectBase    = 5 * time.Second
	reconnectMax     = 60 * time.Second
	healthySessionAt = 60 * time.Second // a connection this long counts as real
)

// reconnectDelay grows the wait with consecutive failures, then spreads it over
// the upper half of the window. Equal jitter rather than full jitter keeps a
// floor under the delay, so a struggling server is not immediately retried by
// the unlucky end of the distribution.
func reconnectDelay(failures int) time.Duration {
	delay := reconnectBase
	for i := 0; i < failures && delay < reconnectMax; i++ {
		delay *= 2
	}
	if delay > reconnectMax {
		delay = reconnectMax
	}
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// loop reconnects forever until quit is closed.
func (a *agent) loop() {
	failures := 0
	for {
		select {
		case <-a.quit:
			return
		default:
		}
		started := time.Now()
		err := a.runOnce()
		// A session that lasted resets the backoff: it proves the server is
		// reachable, so a later drop is a fresh incident rather than a
		// continuation of the one we were backing off from.
		if time.Since(started) >= healthySessionAt {
			failures = 0
		}
		if err != nil {
			if rollbackUnhealthyUpdate() {
				return
			}
		}
		delay := reconnectDelay(failures)
		failures++
		if err != nil {
			log.Printf("connection ended: %v; retrying in %s", err, delay.Round(time.Millisecond))
		}
		select {
		case <-a.quit:
			return
		case <-time.After(delay):
		}
	}
}

// enrollOnce is used by the installers before creating the long-lived service.
// The enrollment token never needs to be retained in service arguments, so a
// revoked endpoint cannot turn an old bootstrap token into a new identity.
func (a *agent) enrollOnce() error {
	conn, _, err := websocket.DefaultDialer.Dial(a.wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	return a.handshake(conn)
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
	a.connMu.Lock()
	a.conn = conn
	a.connMu.Unlock()
	defer func() {
		a.connMu.Lock()
		if a.conn == conn {
			a.conn = nil
		}
		a.connMu.Unlock()
	}()
	log.Printf("registered as %s", a.st.AgentID)
	if err := a.writeHealthMarker(); err != nil {
		log.Printf("health: could not write marker: %v", err)
	}

	a.send = make(chan []byte, 64)
	done := make(chan struct{})
	go a.writePump(conn, done)
	go a.telemetryLoop(done)
	go a.updateLoop(done)
	go confirmHealthyUpdate(done)
	a.flushCredentialRotationCancellation()

	// Re-send any results the server never acked (e.g. lost on a previous
	// disconnect or an agent restart) as soon as the link is back.
	a.flushOutbox()

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
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	reg := protocol.RegisterReq{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  agentVersion,
	}
	if st.AgentID != "" && st.AgentToken != "" {
		reg.AgentID = st.AgentID
		reg.AgentToken = st.AgentToken
	} else if a.allowEnrollment && a.enrollToken != "" {
		reg.EnrollToken = a.enrollToken
	} else {
		return fmt.Errorf("agent has no identity; enroll with -enroll-only before starting the long-lived service")
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
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.AgentID = resp.AgentID
	if resp.AgentToken != "" {
		a.st.AgentToken = resp.AgentToken
	}
	if resp.CancelCredentialRotationID != "" {
		return a.prepareCredentialRotationCancellationLocked(resp.CancelCredentialRotationID)
	}
	// Reconnecting with a persisted replacement is the recovery commit when the
	// earlier persistence acknowledgement could not reach the server.
	a.st.PendingRotationID = ""
	a.st.PreviousAgentToken = ""
	a.st.PendingCancellationID = ""
	return a.saveState()
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
	case protocol.MsgJobCancel:
		var c protocol.JobCancelMsg
		if json.Unmarshal(env.Data, &c) == nil {
			a.cancelJob(c.JobID)
		}
	case protocol.MsgJobResultAck:
		var ack protocol.JobResultAckMsg
		if json.Unmarshal(env.Data, &ack) == nil {
			a.ackResult(ack.JobID)
		}
	case protocol.MsgCheckResultAck:
		var ack protocol.CheckResultAckMsg
		if json.Unmarshal(env.Data, &ack) == nil {
			a.ackCheckResult(ack.ExecutionID)
		}
	case protocol.MsgRunCheck:
		var chk protocol.RunCheckMsg
		if json.Unmarshal(env.Data, &chk) == nil {
			go a.runCheck(chk)
		}
	case protocol.MsgRemoteStart:
		var rs protocol.RemoteStartMsg
		if json.Unmarshal(env.Data, &rs) == nil {
			if rs.Kind == protocol.RemoteKindTerminal {
				go a.startTerminalSession(rs)
			} else {
				go a.startRemoteSession(rs)
			}
		}
	case protocol.MsgCredentialRotate:
		var rotate protocol.CredentialRotateMsg
		if json.Unmarshal(env.Data, &rotate) == nil && rotate.RotationID != "" && rotate.AgentToken != "" {
			a.rotateCredential(rotate)
		}
	case protocol.MsgCredentialRotationCancel:
		var cancel protocol.CredentialRotationCancelMsg
		if json.Unmarshal(env.Data, &cancel) == nil && cancel.RotationID != "" {
			a.cancelCredentialRotation(cancel)
		}
	case protocol.MsgCredentialRotationCanceledAck:
		var ack protocol.CredentialRotationCancelMsg
		if json.Unmarshal(env.Data, &ack) == nil && ack.RotationID != "" {
			a.ackCredentialRotationCancellation(ack.RotationID)
		}
	case protocol.MsgRemoteQuery:
		var q protocol.RemoteQueryMsg
		if json.Unmarshal(env.Data, &q) == nil {
			go a.handleRemoteQuery(q)
		}
	case protocol.MsgPatchScan:
		go a.runPatchScan()
	case protocol.MsgPatchInstall:
		var m protocol.PatchInstallMsg
		if json.Unmarshal(env.Data, &m) == nil {
			go a.handlePatchInstall(m)
		}
	case protocol.MsgReboot:
		var m protocol.RebootMsg
		if json.Unmarshal(env.Data, &m) == nil {
			go a.handlePatchReboot(m)
		}
	}
}

// runJob executes a script and reports the result. The job is registered so a
// job_cancel from the server can kill the still-running process.
// runJobScript is the script runner, replaceable in tests.
var runJobScript = runScriptCtx

func (a *agent) runJob(job protocol.JobMsg) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	// Claiming the id in running and checking it happen under one lock, so two
	// copies of the same job arriving together cannot both start.
	a.mu.Lock()
	if _, busy := a.running[job.JobID]; busy {
		a.mu.Unlock()
		cancel()
		log.Printf("job %s is already running; ignoring the repeated dispatch", job.JobID)
		return
	}
	a.running[job.JobID] = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.running, job.JobID)
		a.mu.Unlock()
	}()
	if !a.beginJob(job.JobID) {
		return
	}

	log.Printf("running job %s (%s)", job.JobID, job.Shell)
	exitCode, stdout, stderr := runJobScript(ctx, job.Shell, job.Script)
	if ctx.Err() == context.Canceled {
		stderr = strings.TrimSpace(stderr + "\n[canceled by operator]")
		if exitCode == 0 {
			exitCode = -1
		}
	}
	a.reportResult(protocol.JobResultMsg{
		JobID: job.JobID, ExitCode: exitCode, Stdout: stdout, Stderr: stderr, Success: exitCode == 0,
	})
}

// reportResult persists a job result to the outbox and sends it. The result
// stays in the outbox — re-sent on reconnect and on the heartbeat timer — until
// the server acks it (job_result_ack), giving at-least-once delivery.
func (a *agent) reportResult(res protocol.JobResultMsg) {
	a.obmu.Lock()
	if a.outbox == nil {
		a.outbox = map[string]protocol.JobResultMsg{}
	}
	a.outbox[res.JobID] = res
	a.saveOutbox()
	a.obmu.Unlock()
	a.queue(protocol.MsgJobResult, res)
}

// beginJob decides whether a dispatched job may run, and records that it
// started before it does. A job seen before is never run again:
//   - its result is still in the outbox: flushOutbox delivers it, nothing to do;
//   - it started under a previous process and never finished (the agent
//     stopped mid-run): what it did is unknown, so it is reported as failed
//     and left to the operator, instead of being repeated blindly.
func (a *agent) beginJob(jobID string) bool {
	a.obmu.Lock()
	defer a.obmu.Unlock()
	if _, pending := a.outbox[jobID]; pending {
		log.Printf("job %s already finished; its result is waiting in the outbox", jobID)
		return false
	}
	if a.started == nil {
		a.started = map[string]time.Time{}
	}
	if since, seen := a.started[jobID]; seen {
		log.Printf("job %s started at %s and was interrupted; not running it again", jobID, since.Format(time.RFC3339))
		res := protocol.JobResultMsg{JobID: jobID, ExitCode: -1,
			Stderr: "[the agent stopped while this job was running; it was not run again, to avoid running it twice]"}
		if a.outbox == nil {
			a.outbox = map[string]protocol.JobResultMsg{}
		}
		a.outbox[jobID] = res
		a.saveOutbox()
		a.queue(protocol.MsgJobResult, res)
		return false
	}
	a.started[jobID] = time.Now().UTC()
	a.saveStarted()
	return true
}

// ackResult drops an acked result from the outbox so it stops being re-sent.
// The server has recorded the job as finished, so it will not dispatch it
// again and its start record can go too.
func (a *agent) ackResult(jobID string) {
	a.obmu.Lock()
	if _, ok := a.outbox[jobID]; ok {
		delete(a.outbox, jobID)
		a.saveOutbox()
	}
	if _, ok := a.started[jobID]; ok {
		delete(a.started, jobID)
		a.saveStarted()
	}
	a.obmu.Unlock()
}

// startedRetention bounds the start records: a job deleted server-side is never
// acked, and its record would otherwise stay forever. A week is far past any
// job's ten-minute limit and any realistic offline spell before a reconnect.
const startedRetention = 7 * 24 * time.Hour

func (a *agent) loadStarted() {
	b, err := os.ReadFile(a.startedPath)
	if err != nil {
		return
	}
	loaded := map[string]time.Time{}
	if err := json.Unmarshal(b, &loaded); err != nil || loaded == nil {
		log.Printf("warning: could not load job start records: %v", err)
		return
	}
	cutoff := time.Now().Add(-startedRetention)
	for id, at := range loaded {
		if id == "" || at.Before(cutoff) {
			delete(loaded, id)
		}
	}
	a.started = loaded
}

// saveStarted persists the start records; callers hold a.obmu.
func (a *agent) saveStarted() {
	b, err := json.Marshal(a.started)
	if err == nil {
		err = writeDurableFile(a.startedPath, b)
	}
	if err != nil {
		log.Printf("warning: could not persist job start records: %v", err)
	}
}

// flushOutbox re-sends every unacked result. Called on each (re)connect and on
// the heartbeat timer; usually a no-op. Sends happen outside the lock, and a
// full send buffer just leaves the item to retry on the next pass.
func (a *agent) flushOutbox() {
	a.obmu.Lock()
	pending := make([]protocol.JobResultMsg, 0, len(a.outbox))
	for _, res := range a.outbox {
		pending = append(pending, res)
	}
	checkPending := make([]protocol.CheckResultMsg, 0, len(a.checkOutbox))
	for _, res := range a.checkOutbox {
		checkPending = append(checkPending, res)
	}
	a.obmu.Unlock()
	for _, res := range pending {
		a.queue(protocol.MsgJobResult, res)
	}
	for _, res := range checkPending {
		a.queue(protocol.MsgCheckResult, res)
	}
}

func (a *agent) loadOutbox() {
	b, err := os.ReadFile(a.outboxPath)
	if err != nil {
		return
	}
	loaded := map[string]protocol.JobResultMsg{}
	if err := json.Unmarshal(b, &loaded); err != nil || loaded == nil {
		log.Printf("warning: could not load outbox: %v", err)
		return
	}
	for id, result := range loaded {
		if id == "" || result.JobID != id {
			delete(loaded, id)
		}
	}
	a.outbox = loaded
}

func (a *agent) loadCheckOutbox() {
	b, err := os.ReadFile(a.checkOutboxPath)
	if err != nil {
		return
	}
	loaded := map[string]protocol.CheckResultMsg{}
	if err := json.Unmarshal(b, &loaded); err != nil || loaded == nil {
		log.Printf("warning: could not load check outbox: %v", err)
		return
	}
	for id, result := range loaded {
		if id == "" || result.ExecutionID != id || result.CheckID == "" {
			delete(loaded, id)
		}
	}
	a.checkOutbox = loaded
}

// saveOutbox persists the outbox; callers hold a.obmu.
func (a *agent) saveOutbox() {
	b, err := json.Marshal(a.outbox)
	if err == nil {
		err = writeDurableFile(a.outboxPath, b)
	}
	if err != nil {
		log.Printf("warning: could not persist outbox: %v", err)
	}
}

func (a *agent) saveCheckOutbox() {
	b, err := json.Marshal(a.checkOutbox)
	if err == nil {
		err = writeDurableFile(a.checkOutboxPath, b)
	}
	if err != nil {
		log.Printf("warning: could not persist check outbox: %v", err)
	}
}

func (a *agent) reportCheckResult(res protocol.CheckResultMsg) {
	if res.ExecutionID == "" {
		// Compatibility with a pre-execution-id server. There is nothing stable to
		// acknowledge, so retain the legacy best-effort behavior.
		a.queue(protocol.MsgCheckResult, res)
		return
	}
	a.obmu.Lock()
	if a.checkOutbox == nil {
		a.checkOutbox = map[string]protocol.CheckResultMsg{}
	}
	a.checkOutbox[res.ExecutionID] = res
	a.saveCheckOutbox()
	a.obmu.Unlock()
	a.queue(protocol.MsgCheckResult, res)
}

func (a *agent) ackCheckResult(executionID string) {
	if executionID == "" {
		return
	}
	a.obmu.Lock()
	if _, ok := a.checkOutbox[executionID]; ok {
		delete(a.checkOutbox, executionID)
		a.saveCheckOutbox()
	}
	a.obmu.Unlock()
}

// cancelJob kills the process of an in-flight job, if any.
func (a *agent) cancelJob(jobID string) {
	a.mu.Lock()
	cancel := a.running[jobID]
	a.mu.Unlock()
	if cancel != nil {
		log.Printf("canceling job %s", jobID)
		cancel()
	}
}

// telemetryLoop sends periodic heartbeats and inventory snapshots.
func (a *agent) telemetryLoop(done <-chan struct{}) {
	// Collect inventory off the loop so software/network probing (which can take
	// seconds) never delays heartbeats.
	sendInventory := func() {
		go func() { a.queue(protocol.MsgInventory, protocol.InventoryMsg{Data: collectInventory()}) }()
	}
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
			a.flushOutbox() // retry any results still waiting for an ack
			a.flushCredentialRotationCancellation()
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

func (a *agent) queue(typ string, payload any) bool {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	select {
	case a.send <- frame:
		return true
	default:
		log.Printf("send buffer full, dropping %s", typ)
		return false
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

func (a *agent) saveState() error {
	b, _ := json.MarshalIndent(a.st, "", "  ")
	if err := writeDurableFile(a.statePath, b); err != nil {
		log.Printf("warning: could not persist agent state: %v", err)
		return err
	}
	return nil
}

// writeDurableFile replaces a small state file only after its new contents are
// on disk. A crash during a direct truncate/write could otherwise erase every
// unacknowledged result and turn at-least-once delivery back into best effort.
func writeDurableFile(path string, b []byte) error {
	if path == "" {
		return fmt.Errorf("empty state path")
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if chmodErr := f.Chmod(0o600); chmodErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return chmodErr
	}
	if _, err := f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Syncing the directory makes the rename durable on filesystems that support
	// it. Windows may reject directory Sync; the already-synced file is still safe.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (a *agent) rotateCredential(msg protocol.CredentialRotateMsg) {
	a.mu.Lock()
	if cancellation := a.st.PendingCancellationID; cancellation != "" {
		if cancellation == msg.RotationID {
			a.mu.Unlock()
			log.Printf("credential rotation %s ignored while cancellation is pending", msg.RotationID)
			a.flushCredentialRotationCancellation()
			return
		}
		// The server cannot stage a different rotation until it has recorded this
		// cancellation, so the new command also confirms the earlier completion.
		a.st.PendingCancellationID = ""
	}
	if a.st.PendingRotationID != "" {
		if a.st.PendingRotationID == msg.RotationID {
			a.mu.Unlock()
			a.confirmCredentialRotation(msg.RotationID)
			return
		}
		pending := a.st.PendingRotationID
		a.mu.Unlock()
		log.Printf("credential rotation %s ignored while %s is pending", msg.RotationID, pending)
		return
	}
	previous := a.st
	a.st.AgentToken = msg.AgentToken
	a.st.PendingRotationID = msg.RotationID
	a.st.PreviousAgentToken = previous.AgentToken
	err := a.saveState()
	if err != nil {
		a.st = previous
	}
	a.mu.Unlock()
	if err != nil {
		return
	}
	a.confirmCredentialRotation(msg.RotationID)
}

func (a *agent) cancelCredentialRotation(msg protocol.CredentialRotationCancelMsg) {
	a.mu.Lock()
	err := a.prepareCredentialRotationCancellationLocked(msg.RotationID)
	a.mu.Unlock()
	if err != nil {
		log.Printf("credential rotation cancellation %s not applied: %v", msg.RotationID, err)
		return
	}
	a.flushCredentialRotationCancellation()
}

// prepareCredentialRotationCancellationLocked restores the previous token, if
// needed, and keeps a durable confirmation record until the server acknowledges it.
// Callers hold a.mu.
func (a *agent) prepareCredentialRotationCancellationLocked(rotationID string) error {
	if a.st.PendingCancellationID != "" && a.st.PendingCancellationID != rotationID {
		return fmt.Errorf("cancellation %s is pending", a.st.PendingCancellationID)
	}
	if a.st.PendingRotationID != "" && a.st.PendingRotationID != rotationID {
		return fmt.Errorf("rotation %s is pending", a.st.PendingRotationID)
	}
	previous := a.st
	if a.st.PendingRotationID == rotationID {
		if a.st.PreviousAgentToken == "" {
			return fmt.Errorf("cannot restore a previous token")
		}
		a.st.AgentToken = a.st.PreviousAgentToken
		a.st.PendingRotationID = ""
		a.st.PreviousAgentToken = ""
	}
	a.st.PendingCancellationID = rotationID
	if err := a.saveState(); err != nil {
		a.st = previous
		return err
	}
	return nil
}

// flushCredentialRotationCancellation retries the durable confirmation after a
// reconnect and on heartbeats until the server replies with its acknowledgement.
func (a *agent) flushCredentialRotationCancellation() {
	a.mu.Lock()
	rotationID := a.st.PendingCancellationID
	a.mu.Unlock()
	if rotationID != "" {
		a.queue(protocol.MsgCredentialRotationCanceled, protocol.CredentialRotationCancelMsg{RotationID: rotationID})
	}
}

func (a *agent) ackCredentialRotationCancellation(rotationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st.PendingCancellationID != rotationID {
		return
	}
	a.st.PendingCancellationID = ""
	if err := a.saveState(); err != nil {
		a.st.PendingCancellationID = rotationID
	}
}

func (a *agent) confirmCredentialRotation(rotationID string) {
	// This acknowledgement records local durability; reconnecting with the
	// replacement is the server-side commit point. The bounded reconnect also
	// recovers if the acknowledgement cannot leave a broken control socket.
	if !a.queue(protocol.MsgCredentialRotated, protocol.CredentialRotatedMsg{RotationID: rotationID}) {
		a.reconnectControl()
		return
	}
	a.reconnectControlAfter(rotationID, 5*time.Second)
}

func (a *agent) reconnectControl() {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (a *agent) reconnectControlAfter(rotationID string, delay time.Duration) {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn == nil {
		return
	}
	time.AfterFunc(delay, func() {
		a.mu.Lock()
		pending := a.st.PendingRotationID == rotationID && a.st.PendingCancellationID == ""
		a.mu.Unlock()
		if !pending {
			return
		}
		a.connMu.Lock()
		current := a.conn == conn
		a.connMu.Unlock()
		if current {
			_ = conn.Close()
		}
	})
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

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func secureAgentURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "wss"
}

func validateRuntimeSecurity(wsURL string, labMode, requireTLS bool) error {
	mode := updateVerifyMode()
	if mode != "strict" && mode != "warn" && mode != "off" {
		return fmt.Errorf("invalid update verification mode %q", mode)
	}
	if !labMode {
		if !secureAgentURL(wsURL) {
			return fmt.Errorf("production mode requires a wss:// server URL; use -lab-mode explicitly only for isolated labs")
		}
		if mode != "strict" {
			return fmt.Errorf("production mode requires -update-verify strict")
		}
		if keys, err := configuredUpdatePublicKeys(); err != nil {
			return fmt.Errorf("invalid update public key: %w", err)
		} else if len(keys) == 0 {
			return fmt.Errorf("production mode requires -update-public-key or a build-time RMM_UPDATE_PUBLIC_KEY")
		}
	}
	if requireTLS && !secureAgentURL(wsURL) {
		return fmt.Errorf("-require-tls requires a wss:// server URL")
	}
	return nil
}

func (a *agent) writeHealthMarker() error {
	if a.healthPath == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{
		"agent_id":   a.st.AgentID,
		"version":    agentVersion,
		"healthy_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.healthPath), 0o755); err != nil {
		return err
	}
	tmp := a.healthPath + ".new"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.healthPath)
}

// ---------- script execution ----------

// exitNoInterpreter reports "the machine has no Python", not "the script
// failed". 127 is the shell convention for a command that could not be found,
// which is exactly the situation. Keeping it distinct is what separates a
// provisioning gap from a bug in the operator's script — they are fixed by
// different people in different places.
const exitNoInterpreter = 127

// pythonCandidates are tried in order. python3 first because on a system with
// both, "python" may still be Python 2 — running a Python 3 script under it
// fails in ways that read like a syntax error in the operator's code. The py
// launcher is last and Windows-only, where it is the reliable way to reach an
// interpreter that is installed but not on PATH.
var pythonCandidates = []struct {
	name string
	args []string
}{
	{name: "python3"},
	{name: "python"},
	{name: "py", args: []string{"-3"}},
}

// pythonInterpreter resolves the interpreter to run a script with.
//
// The error names every candidate that was tried: "python not found" sends an
// operator looking in the wrong place when the machine has py but no PATH entry.
func pythonInterpreter() (string, []string, error) {
	tried := make([]string, 0, len(pythonCandidates))
	for _, candidate := range pythonCandidates {
		if candidate.name == "py" && runtime.GOOS != "windows" {
			continue
		}
		tried = append(tried, strings.TrimSpace(candidate.name+" "+strings.Join(candidate.args, " ")))
		if path, err := exec.LookPath(candidate.name); err == nil {
			return path, candidate.args, nil
		}
	}
	return "", nil, fmt.Errorf("no Python interpreter found on this device (tried: %s)", strings.Join(tried, ", "))
}

// writeTempScript stages a script on disk and returns its path plus a cleanup.
//
// Mode 0600: the file can carry credentials the operator embedded in the
// script, and it lives in a world-readable temp directory. The cleanup runs even
// when the process is killed by a timeout or an operator cancel, because it is
// deferred in the caller rather than left to the child.
func writeTempScript(script, extension string) (string, func(), error) {
	f, err := os.CreateTemp("", "openrmm-script-*"+extension)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}

// runScriptCtx runs a script under the given context; canceling the context
// (timeout or operator cancel) kills the process — and on Windows its whole
// child tree (taskkill /T), so a stuck downloader/installer dies too.
func runScriptCtx(ctx context.Context, shell, script string) (int, string, string) {
	var cmd *exec.Cmd
	switch shell {
	case "powershell":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", script)
	case "sh":
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	case "bash":
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	case "python":
		interpreter, args, err := pythonInterpreter()
		if err != nil {
			return exitNoInterpreter, "", err.Error()
		}
		// Written to a file rather than passed with -c. Three reasons, all of
		// which bite in practice: a traceback names the file and a line instead
		// of "<string>", Windows caps a command line at ~32k characters, and a
		// script full of quotes and newlines stops being an argument-quoting
		// problem the moment it stops being an argument.
		path, cleanup, err := writeTempScript(script, ".py")
		if err != nil {
			return 2, "", "cannot stage the script: " + err.Error()
		}
		defer cleanup()
		cmd = exec.CommandContext(ctx, interpreter, append(args, path)...)
		// Ask Python for UTF-8 stdio. Without it a redirected interpreter writes
		// the system code page — cp1252 on a pt-BR Windows — whose accented bytes
		// are not valid UTF-8, and the JSON layer then replaces each one with
		// U+FFFD, losing it for good. Asking is lossless; decodeOutput is only
		// the fallback for interpreters that ignore this.
		//
		// The environment variable rather than -X utf8: PEP 540's flag needs
		// Python 3.7, and an older interpreter would fail the whole script with
		// "unknown option" instead of merely mis-encoding it. PYTHONIOENCODING
		// has been honoured since 2.6 and touches only stdio.
		cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	default:
		return 2, "", "unsupported shell: " + shell
	}
	if runtime.GOOS == "windows" {
		// Default ctx-cancel only kills the direct child; kill the tree instead.
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			}
			return nil
		}
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
	return exitCode,
		truncateRunes(decodeOutput(stdout.Bytes()), maxJobOutput),
		truncateRunes(decodeOutput(stderr.Bytes()), maxJobOutput)
}

// maxJobOutput bounds stdout/stderr captured per job (1 MiB each).
const maxJobOutput = 1 << 20

// ---------- monitoring checks ----------

// runCheck executes a check by type and reports the raw outcome; the server
// applies thresholds and decides passing/warning/failing.
func (a *agent) runCheck(chk protocol.RunCheckMsg) {
	if chk.ExecutionID != "" {
		// A server retry after a lost acknowledgement should resend the durable
		// result, not execute the check a second time.
		a.obmu.Lock()
		stored, complete := a.checkOutbox[chk.ExecutionID]
		a.obmu.Unlock()
		if complete {
			a.queue(protocol.MsgCheckResult, stored)
			return
		}
		a.checkMu.Lock()
		if a.runningChecks == nil {
			a.runningChecks = map[string]struct{}{}
		}
		if _, running := a.runningChecks[chk.ExecutionID]; running {
			a.checkMu.Unlock()
			return
		}
		a.runningChecks[chk.ExecutionID] = struct{}{}
		a.checkMu.Unlock()
		defer func() {
			a.checkMu.Lock()
			delete(a.runningChecks, chk.ExecutionID)
			a.checkMu.Unlock()
		}()
	}
	var value *float64
	var output string
	success := false
	timeout := checkExecutionTimeout(chk.TimeoutSec)
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), timeout)
	defer cancelCheck()

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
		success, output = doPing(checkCtx, cfg.Host)
	case "winservice":
		var cfg struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		success, output = winServiceRunning(checkCtx, cfg.Service)
	case "script":
		var cfg struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}
		_ = json.Unmarshal(chk.Config, &cfg)
		code, out, errOut := runScriptCtx(checkCtx, cfg.Shell, cfg.Script)
		fc := float64(code)
		value, output, success = &fc, out+errOut, code == 0
	default:
		output = "unknown check type: " + chk.Type
	}

	a.reportCheckResult(protocol.CheckResultMsg{
		CheckID: chk.CheckID, ExecutionID: chk.ExecutionID,
		Value: value, Output: truncateRunes(output, 4000), Success: success,
	})
}

func checkExecutionTimeout(timeoutSec int) time.Duration {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout < 5*time.Second || timeout > 24*time.Hour {
		// Older servers did not send a timeout. Keep their historical script
		// limit while bounding malformed values from a compromised controller.
		return 2 * time.Minute
	}
	return timeout
}

func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		return "C:"
	}
	return "/"
}

func doPing(ctx context.Context, host string) (bool, string) {
	if host == "" {
		return false, "no host configured"
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", "3000", host)
	} else {
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "3", host)
	}
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

func winServiceRunning(ctx context.Context, name string) (bool, string) {
	if runtime.GOOS != "windows" {
		return false, "winservice check only runs on Windows"
	}
	if name == "" {
		return false, "no service configured"
	}
	out, _ := exec.CommandContext(ctx, "sc", "query", name).CombinedOutput()
	running := strings.Contains(string(out), "RUNNING")
	return running, string(out)
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
	data["hardware"] = collectHardware()
	data["network_interfaces"] = collectNetwork()
	data["software"] = collectSoftware()
	raw, _ := json.Marshal(data)
	return raw
}

func collectHardware() map[string]any {
	switch runtime.GOOS {
	case "windows":
		return collectWindowsHardware()
	case "linux":
		return collectLinuxHardware()
	case "darwin":
		return collectDarwinHardware()
	}
	return map[string]any{}
}

func collectWindowsHardware() map[string]any {
	const ps = `$bios = Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue
$cs = Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue
$base = Get-CimInstance Win32_BaseBoard -ErrorAction SilentlyContinue
$disks = Get-CimInstance Win32_DiskDrive -ErrorAction SilentlyContinue | ForEach-Object { [pscustomobject]@{ name = $_.DeviceID; model = $_.Model; serial = $_.SerialNumber; size_bytes = [uint64]$_.Size; type = $_.MediaType; interface = $_.InterfaceType } }
$gpus = Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | ForEach-Object { [pscustomobject]@{ name = $_.Name; memory_bytes = [uint64]$_.AdapterRAM; driver_version = $_.DriverVersion } }
[pscustomobject]@{
  serial_number = $bios.SerialNumber
  service_tag = $bios.SerialNumber
  manufacturer = $cs.Manufacturer
  system_model = $cs.Model
  bios = $bios.SMBIOSBIOSVersion
  motherboard = [pscustomobject]@{ manufacturer = $base.Manufacturer; product = $base.Product; serial = $base.SerialNumber }
  storage_devices = @($disks)
  gpus = @($gpus)
} | ConvertTo-Json -Compress -Depth 5`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return map[string]any{}
	}
	var hw map[string]any
	if json.Unmarshal(bytes.TrimSpace(out), &hw) != nil {
		return map[string]any{}
	}
	return compactHardwareMap(hw)
}

func collectLinuxHardware() map[string]any {
	hw := map[string]any{}
	serial := firstHardwareValue(readDMI("product_serial"), readDMI("chassis_serial"), readDMI("board_serial"))
	setHardwareString(hw, "serial_number", serial)
	setHardwareString(hw, "service_tag", serial)
	setHardwareString(hw, "manufacturer", readDMI("sys_vendor"))
	setHardwareString(hw, "system_model", readDMI("product_name"))
	setHardwareString(hw, "bios", readDMI("bios_version"))

	mb := map[string]string{}
	setHardwareStringValue(mb, "manufacturer", readDMI("board_vendor"))
	setHardwareStringValue(mb, "product", readDMI("board_name"))
	setHardwareStringValue(mb, "version", readDMI("board_version"))
	setHardwareStringValue(mb, "serial", readDMI("board_serial"))
	if len(mb) > 0 {
		hw["motherboard"] = mb
	}
	if storage := collectLinuxStorageDevices(); len(storage) > 0 {
		hw["storage_devices"] = storage
	}
	if gpus := collectLinuxGPUs(); len(gpus) > 0 {
		hw["gpus"] = gpus
	}
	return hw
}

func collectDarwinHardware() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "system_profiler", "SPHardwareDataType", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return map[string]any{}
	}
	var report map[string]any
	if json.Unmarshal(out, &report) != nil {
		return map[string]any{}
	}
	hw := map[string]any{}
	setHardwareString(hw, "manufacturer", "Apple")
	if info := firstMapInSection(report["SPHardwareDataType"]); info != nil {
		setHardwareString(hw, "serial_number", hardwareString(info["serial_number"]))
		setHardwareString(hw, "service_tag", hardwareString(info["serial_number"]))
		setHardwareString(hw, "system_model", firstHardwareValue(hardwareString(info["machine_model"]), hardwareString(info["machine_name"])))
		setHardwareString(hw, "processor", firstHardwareValue(hardwareString(info["chip_type"]), hardwareString(info["processor_name"])))
		setHardwareString(hw, "bios", hardwareString(info["boot_rom_version"]))
	}
	var gpus []map[string]any
	for _, item := range hardwareSlice(report["SPDisplaysDataType"]) {
		if m, ok := item.(map[string]any); ok {
			gpu := map[string]any{}
			setHardwareString(gpu, "name", firstHardwareValue(hardwareString(m["sppci_model"]), hardwareString(m["_name"])))
			setHardwareString(gpu, "memory", hardwareString(m["spdisplays_vram"]))
			if len(gpu) > 0 {
				gpus = append(gpus, gpu)
			}
		}
	}
	if len(gpus) > 0 {
		hw["gpus"] = gpus
	}
	return hw
}

func collectLinuxStorageDevices() []map[string]any {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, e := range entries {
		name := e.Name()
		if virtualBlockDevice(name) {
			continue
		}
		base := "/sys/block/" + name
		dev := map[string]any{"name": "/dev/" + name}
		model := strings.TrimSpace(strings.Join([]string{readHardwareFile(base + "/device/vendor"), readHardwareFile(base + "/device/model")}, " "))
		setHardwareString(dev, "model", model)
		setHardwareString(dev, "serial", readHardwareFile(base+"/device/serial"))
		if sectors, err := strconv.ParseUint(readTextFile(base+"/size"), 10, 64); err == nil && sectors > 0 {
			dev["size_bytes"] = sectors * 512
		}
		rotational := readTextFile(base + "/queue/rotational")
		switch {
		case strings.HasPrefix(name, "nvme"):
			dev["type"] = "NVMe"
		case rotational == "1":
			dev["type"] = "HDD"
		case rotational == "0":
			dev["type"] = "SSD"
		}
		out = append(out, dev)
	}
	return out
}

func collectLinuxGPUs() []map[string]any {
	if _, err := exec.LookPath("lspci"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lspci").Output()
	if err != nil {
		return nil
	}
	var gpus []map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "vga compatible controller") && !strings.Contains(lower, "3d controller") && !strings.Contains(lower, "display controller") {
			continue
		}
		name := strings.TrimSpace(line)
		if i := strings.Index(lower, "controller:"); i >= 0 {
			name = strings.TrimSpace(line[i+len("controller:"):])
		}
		if name = cleanHardwareString(name); name != "" {
			gpus = append(gpus, map[string]any{"name": name})
		}
	}
	return gpus
}

func virtualBlockDevice(name string) bool {
	for _, p := range []string{"loop", "ram", "fd", "sr", "zram", "dm-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func readDMI(name string) string { return readHardwareFile("/sys/class/dmi/id/" + name) }

func readTextFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readHardwareFile(path string) string { return cleanHardwareString(readTextFile(path)) }

func setHardwareString(m map[string]any, key, value string) {
	if value = cleanHardwareString(value); value != "" {
		m[key] = value
	}
}

func setHardwareStringValue(m map[string]string, key, value string) {
	if value = cleanHardwareString(value); value != "" {
		m[key] = value
	}
}

func cleanHardwareString(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "\x00"))
	lower := strings.ToLower(s)
	if lower == "" || lower == "none" || lower == "unknown" || lower == "not specified" || lower == "default string" || lower == "system serial number" || lower == "to be filled by o.e.m." || strings.Contains(lower, "to be filled") {
		return ""
	}
	return s
}

func firstHardwareValue(vals ...string) string {
	for _, v := range vals {
		if v = cleanHardwareString(v); v != "" {
			return v
		}
	}
	return ""
}

func compactHardwareMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if clean, ok := compactHardwareValue(v); ok {
			out[k] = clean
		}
	}
	return out
}

func compactHardwareValue(v any) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		if s := cleanHardwareString(x); s != "" {
			return s, true
		}
	case []any:
		var out []any
		for _, item := range x {
			if clean, ok := compactHardwareValue(item); ok {
				out = append(out, clean)
			}
		}
		return out, len(out) > 0
	case map[string]any:
		out := compactHardwareMap(x)
		return out, len(out) > 0
	default:
		return v, true
	}
	return nil, false
}

func hardwareString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func hardwareSlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	if v != nil {
		return []any{v}
	}
	return nil
}

func firstMapInSection(v any) map[string]any {
	for _, item := range hardwareSlice(v) {
		if m, ok := item.(map[string]any); ok {
			return m
		}
	}
	return nil
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

// collectSoftware returns installed software (name + version + manufacturer),
// per OS. Bounded by a timeout so a slow query never stalls telemetry; capped
// in size.
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
	const ps = `Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*','HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName } | Select-Object DisplayName,DisplayVersion,Publisher | Sort-Object DisplayName -Unique | ConvertTo-Json -Compress`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return nil
	}
	return parseWindowsSoftware(out)
}

func parseWindowsSoftware(out []byte) []map[string]string {
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "{") { // single entry -> wrap as array
		trimmed = "[" + trimmed + "]"
	}
	var arr []struct{ DisplayName, DisplayVersion, Publisher string }
	if json.Unmarshal([]byte(trimmed), &arr) != nil {
		return nil
	}
	var sw []map[string]string
	for _, a := range arr {
		if a.DisplayName == "" {
			continue
		}
		sw = append(sw, map[string]string{"name": a.DisplayName, "version": a.DisplayVersion, "manufacturer": a.Publisher})
	}
	return capSoftware(sw)
}

func softwareLinux(ctx context.Context) []map[string]string {
	if out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Package}\t${Version}\t${Maintainer}\n").Output(); err == nil {
		return parseTabbed(out)
	}
	if out, err := exec.CommandContext(ctx, "rpm", "-qa", "--qf", "%{NAME}\t%{VERSION}\t%{VENDOR}\n").Output(); err == nil {
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
		parts := strings.SplitN(line, "\t", 3)
		version, manufacturer := "", ""
		if len(parts) > 1 {
			version = parts[1]
		}
		if len(parts) > 2 {
			manufacturer = parts[2]
		}
		sw = append(sw, map[string]string{"name": parts[0], "version": version, "manufacturer": manufacturer})
	}
	return capSoftware(sw)
}

func softwareDarwin(ctx context.Context) []map[string]string {
	out, err := exec.CommandContext(ctx, "system_profiler", "-json", "SPApplicationsDataType").Output()
	if err != nil {
		return nil
	}
	return parseDarwinSoftware(out)
}

func parseDarwinSoftware(out []byte) []map[string]string {
	var doc struct {
		Apps []struct {
			Name         string `json:"_name"`
			Version      string `json:"version"`
			SignedBy     any    `json:"signed_by"`
			ObtainedFrom string `json:"obtained_from"`
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
		manufacturer := a.ObtainedFrom
		for _, signer := range hardwareSlice(a.SignedBy) {
			if value := strings.TrimSpace(hardwareString(signer)); value != "" {
				manufacturer = value
				break
			}
		}
		sw = append(sw, map[string]string{"name": a.Name, "version": a.Version, "manufacturer": manufacturer})
	}
	return capSoftware(sw)
}
