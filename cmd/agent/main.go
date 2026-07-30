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

var agentVersion = "0.4.5"

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

	// outbox holds job results the server hasn't acked yet, keyed by job_id, and
	// is persisted to disk. Results are re-sent on every (re)connect and on a
	// timer until acked, so a dropped connection (or an agent restart) can't lose
	// a finished job's result and strand it in "sent" server-side.
	obmu       sync.Mutex
	outbox     map[string]protocol.JobResultMsg
	outboxPath string
}

// program adapts the agent to the service interface.
type program struct{ a *agent }

func (p *program) Start(_ service.Service) error { go p.a.loop(); return nil }
func (p *program) Stop(_ service.Service) error  { close(p.a.quit); return nil }

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

	a := &agent{wsURL: *wsURL, enrollToken: *enroll, allowEnrollment: *enrollOnly, statePath: *statePath, healthPath: *healthPath, quit: make(chan struct{}), running: map[string]context.CancelFunc{}, outbox: map[string]protocol.JobResultMsg{}, outboxPath: *statePath + ".outbox"}
	a.loadState()
	a.loadOutbox()
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
func (a *agent) runJob(job protocol.JobMsg) {
	log.Printf("running job %s (%s)", job.JobID, job.Shell)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	a.mu.Lock()
	a.running[job.JobID] = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.running, job.JobID)
		a.mu.Unlock()
	}()

	exitCode, stdout, stderr := runScriptCtx(ctx, job.Shell, job.Script)
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
	a.outbox[res.JobID] = res
	a.saveOutbox()
	a.obmu.Unlock()
	a.queue(protocol.MsgJobResult, res)
}

// ackResult drops an acked result from the outbox so it stops being re-sent.
func (a *agent) ackResult(jobID string) {
	a.obmu.Lock()
	if _, ok := a.outbox[jobID]; ok {
		delete(a.outbox, jobID)
		a.saveOutbox()
	}
	a.obmu.Unlock()
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
	a.obmu.Unlock()
	for _, res := range pending {
		a.queue(protocol.MsgJobResult, res)
	}
}

func (a *agent) loadOutbox() {
	b, err := os.ReadFile(a.outboxPath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &a.outbox)
}

// saveOutbox persists the outbox; callers hold a.obmu.
func (a *agent) saveOutbox() {
	b, _ := json.Marshal(a.outbox)
	if err := os.WriteFile(a.outboxPath, b, 0o600); err != nil {
		log.Printf("warning: could not persist outbox: %v", err)
	}
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
	tmp := a.statePath + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Printf("warning: could not persist agent state: %v", err)
		return err
	}
	if _, err := f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		log.Printf("warning: could not persist agent state: %v", err)
		return err
	}
	if err := os.Rename(tmp, a.statePath); err != nil {
		_ = os.Remove(tmp)
		log.Printf("warning: could not replace agent state: %v", err)
		return err
	}
	// Syncing the directory makes the rename durable on filesystems that support
	// it. Windows may reject directory Sync; the already-synced file is still safe.
	if dir, err := os.Open(filepath.Dir(a.statePath)); err == nil {
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
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		code, out, errOut := runScriptCtx(cctx, cfg.Shell, cfg.Script)
		ccancel()
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
