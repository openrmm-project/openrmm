package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBreakerOpensAfterThresholdAndFailsFast(t *testing.T) {
	var b breaker
	now := time.Now()
	for i := 0; i < breakerThreshold-1; i++ {
		b.failure(now)
		if err := b.allow(now); err != nil {
			t.Fatalf("after %d failures the circuit should still be closed: %v", i+1, err)
		}
	}
	b.failure(now)
	if err := b.allow(now); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("at the threshold the circuit should be open, got %v", err)
	}
	// Still open just before the cooldown elapses.
	if err := b.allow(now.Add(breakerCooldown - time.Millisecond)); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("circuit reopened early: %v", err)
	}
}

func TestBreakerAdmitsOneProbeAfterCooldown(t *testing.T) {
	var b breaker
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		b.failure(now)
	}
	later := now.Add(breakerCooldown)
	if err := b.allow(later); err != nil {
		t.Fatalf("the first caller after the cooldown should probe: %v", err)
	}
	// A recovering provider must get one request, not the whole backlog.
	if err := b.allow(later); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("a second concurrent caller should still fail fast, got %v", err)
	}

	// A failed probe restarts the cooldown rather than freeing the next caller.
	b.failure(later)
	if err := b.allow(later.Add(time.Second)); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("a failed probe should restart the cooldown, got %v", err)
	}
	// A successful probe closes the circuit for everyone.
	recovered := later.Add(breakerCooldown * 2)
	if err := b.allow(recovered); err != nil {
		t.Fatalf("probe after the second cooldown: %v", err)
	}
	b.success()
	if err := b.allow(recovered); err != nil {
		t.Fatalf("a successful probe should close the circuit: %v", err)
	}
}

// abort exists so a local error on the probe path cannot wedge the circuit open.
func TestBreakerAbortReleasesTheProbeSlot(t *testing.T) {
	var b breaker
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		b.failure(now)
	}
	later := now.Add(breakerCooldown)
	if err := b.allow(later); err != nil {
		t.Fatal(err)
	}
	b.abort()
	if err := b.allow(later); err != nil {
		t.Fatalf("after abort the slot should be free again: %v", err)
	}
}

// A hanging provider is the case this exists for: once it is known bad, callers
// must fail immediately instead of each waiting out the client timeout.
func TestChatStopsCallingAnUnhealthyProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	for i := 0; i < breakerThreshold; i++ {
		if _, err := c.Chat(context.Background(), nil, nil); err == nil {
			t.Fatal("expected the 502 to surface as an error")
		}
	}
	if got := calls.Load(); got != int32(breakerThreshold) {
		t.Fatalf("provider was called %d times before the circuit opened, want %d", got, breakerThreshold)
	}
	_, err := c.Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("once open, Chat should fail fast, got %v", err)
	}
	if got := calls.Load(); got != int32(breakerThreshold) {
		t.Fatalf("the open circuit still reached the provider (%d calls)", got)
	}
}

// A wrong API key is deterministic and already fails instantly, so tripping on
// it would only replace a useful message with a vague one.
func TestChatDoesNotTripOnClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	for i := 0; i < breakerThreshold+2; i++ {
		if _, err := c.Chat(context.Background(), nil, nil); errors.Is(err, ErrProviderUnavailable) {
			t.Fatalf("a 401 should keep reaching the provider, tripped on call %d", i+1)
		}
	}
	if got := calls.Load(); got != int32(breakerThreshold+2) {
		t.Fatalf("provider was called %d times, want every attempt to go through", got)
	}
}

// Changing provider settings is how an operator fixes what tripped the breaker.
func TestUpdateClosesTheCircuit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	for i := 0; i < breakerThreshold; i++ {
		_, _ = c.Chat(context.Background(), nil, nil)
	}
	if _, err := c.Chat(context.Background(), nil, nil); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected an open circuit, got %v", err)
	}
	c.Update(srv.URL, "new-key", "m")
	if err := c.breaker.allow(time.Now()); err != nil {
		t.Fatalf("Update should close the circuit: %v", err)
	}
}
