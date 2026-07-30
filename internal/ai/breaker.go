package ai

import (
	"errors"
	"sync"
	"time"
)

// ErrProviderUnavailable is returned while the breaker is open. It is a
// sentinel so callers can tell "we did not even try" apart from a real provider
// error, and the message is written for the person in the chat window.
var ErrProviderUnavailable = errors.New(
	"the AI provider is not responding; requests are paused briefly — check the provider settings or try again shortly")

const (
	// Consecutive failures before the circuit opens. Three, not one: a single
	// timeout is normal noise on a busy provider, and opening on it would make
	// the assistant feel broken more often than the provider actually is.
	breakerThreshold = 3
	// How long to stay open before letting one request through to test.
	breakerCooldown = 30 * time.Second
)

// breaker is a circuit breaker for the upstream provider.
//
// The problem it solves is not error rate but occupancy: the client allows 120s
// per request, so a provider that hangs holds one goroutine — and one waiting
// user — per request for two minutes. Failing fast once the provider is known
// bad turns a pile-up into an immediate, explainable error.
type breaker struct {
	mu       sync.Mutex
	failures int
	openedAt time.Time
	probing  bool // a half-open trial is in flight
}

// allow reports whether a request may proceed.
//
// Closed lets everything through. Open rejects until the cooldown elapses, then
// admits exactly one probe — the rest keep failing fast, so a recovering
// provider gets one request rather than the whole backlog at once.
func (b *breaker) allow(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < breakerThreshold {
		return nil // closed
	}
	if now.Sub(b.openedAt) < breakerCooldown {
		return ErrProviderUnavailable
	}
	if b.probing {
		return ErrProviderUnavailable
	}
	b.probing = true
	return nil
}

// success closes the circuit. One good response is enough: the breaker exists to
// stop a pile-up, and keeping it open past recovery only prolongs the outage the
// user sees.
func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probing = false
	b.openedAt = time.Time{}
}

// abort releases a half-open slot without judging the provider, for failures
// that happen before any request leaves this process. Without it a local error
// on the probe path would leave probing set and wedge the circuit open forever.
func (b *breaker) abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

// failure counts a fault and opens the circuit at the threshold. A failed probe
// restarts the cooldown rather than letting the next caller through immediately.
func (b *breaker) failure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if b.failures < breakerThreshold {
		b.failures++
	}
	if b.failures >= breakerThreshold {
		b.openedAt = now
	}
}
