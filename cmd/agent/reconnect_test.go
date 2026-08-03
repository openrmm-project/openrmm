package main

import (
	"testing"
	"time"
)

// The delay must grow with consecutive failures and stop at the cap, so a long
// outage costs the server a bounded retry rate rather than one attempt per
// endpoint every five seconds.
func TestReconnectDelayBacksOffAndCaps(t *testing.T) {
	for _, tc := range []struct {
		failures       int
		low, high      time.Duration
		nominalSeconds int
	}{
		{failures: 0, low: 2500 * time.Millisecond, high: 5 * time.Second, nominalSeconds: 5},
		{failures: 1, low: 5 * time.Second, high: 10 * time.Second, nominalSeconds: 10},
		{failures: 2, low: 10 * time.Second, high: 20 * time.Second, nominalSeconds: 20},
		{failures: 8, low: 30 * time.Second, high: 60 * time.Second, nominalSeconds: 60},
		{failures: 99, low: 30 * time.Second, high: 60 * time.Second, nominalSeconds: 60},
	} {
		for i := 0; i < 200; i++ {
			got := reconnectDelay(tc.failures)
			if got < tc.low || got > tc.high {
				t.Fatalf("reconnectDelay(%d) = %s, want within [%s, %s] (nominal %ds)",
					tc.failures, got, tc.low, tc.high, tc.nominalSeconds)
			}
		}
	}
}

// Never exceed the cap, however many failures accumulate — an agent that has
// been offline for days must still come back within a minute of the server
// returning.
func TestReconnectDelayNeverExceedsMax(t *testing.T) {
	for failures := 0; failures < 1000; failures += 37 {
		if got := reconnectDelay(failures); got > reconnectMax {
			t.Fatalf("reconnectDelay(%d) = %s, over the %s cap", failures, got, reconnectMax)
		}
	}
}

// The point of the jitter: two agents that failed at the same instant must not
// retry at the same instant. Without it every draw would be identical.
func TestReconnectDelayIsSpreadAcrossTheFleet(t *testing.T) {
	const draws = 500
	seen := map[time.Duration]int{}
	for i := 0; i < draws; i++ {
		seen[reconnectDelay(0)]++
	}
	if len(seen) < draws/10 {
		t.Fatalf("only %d distinct delays across %d draws — the herd is still synchronised", len(seen), draws)
	}
	// And the spread must cover the window rather than clustering at one end.
	var min, max time.Duration = time.Hour, 0
	for d := range seen {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	if max-min < time.Second {
		t.Fatalf("delays span only %s, want them spread across the window", max-min)
	}
}
