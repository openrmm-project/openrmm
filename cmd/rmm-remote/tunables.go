package main

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Capture/encode tunables. All come from the environment of the agent that
// spawns the sidecar (the sidecar inherits it), so a deployment can profile for
// WAN (lower fps/quality) or LAN (higher) without rebuilding:
//
//	RMM_REMOTE_FPS       active capture rate, frames/s   (default 20, 1..60)
//	RMM_REMOTE_IDLE_FPS  idle capture rate, frames/s     (default 4,  1..60)
//	RMM_REMOTE_QUALITY   JPEG quality                    (default 65, 10..100)
//	RMM_REMOTE_TILE      delta-tile size, px             (default 64, 16..256)

func envInt(name string, def, lo, hi int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}

func captureInterval() time.Duration {
	return time.Second / time.Duration(envInt("RMM_REMOTE_FPS", 20, 1, 60))
}

func idleInterval() time.Duration {
	iv := time.Second / time.Duration(envInt("RMM_REMOTE_IDLE_FPS", 4, 1, 60))
	if act := captureInterval(); iv < act {
		iv = act // idle cadence never faster than the active one
	}
	return iv
}

func jpegQuality() int { return envInt("RMM_REMOTE_QUALITY", 65, 10, 100) }

func encoderTileSize() int {
	return envInt("RMM_REMOTE_TILE", tileSize, 16, 256) &^ 15 // multiple of 16
}

// videoBacklog is the queued-outbound threshold past which capture ticks are
// skipped: encoding frames the link can't drain only accumulates latency, which
// feels far worse than a lower frame rate. Skipping a tick BEFORE capture is
// free — the tile diff naturally folds the change into the next frame. (Never
// drop after encode: the encoder snapshots its prev frame unconditionally, so
// an encoded-but-unsent delta would leave the viewer with stale tiles until
// they happen to change again.)
const videoBacklog = 8

// pacer adapts the capture cadence: the active rate while the screen is
// changing or the user is interacting, the idle rate once the session has been
// quiet for idleAfter. markActivity is safe from any goroutine (input may be
// injected off the capture loop — see media_linux.go).
type pacer struct {
	active  time.Duration
	idle    time.Duration
	lastAct atomic.Int64 // UnixNano of the last sent frame / input event
}

const idleAfter = 2 * time.Second

func newPacer() *pacer {
	p := &pacer{active: captureInterval(), idle: idleInterval()}
	p.markActivity()
	return p
}

func (p *pacer) markActivity() { p.lastAct.Store(time.Now().UnixNano()) }

// interval returns the cadence for the next tick. sent reports whether the
// frame just captured produced a payload (i.e. the screen is changing).
func (p *pacer) interval(sent bool) time.Duration {
	if sent {
		p.markActivity()
		return p.active
	}
	if time.Duration(time.Now().UnixNano()-p.lastAct.Load()) > idleAfter {
		return p.idle
	}
	return p.active
}
