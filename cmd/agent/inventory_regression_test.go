package main

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

// A failed marshal used to become a successful queue of {data:null}. The
// server ignored it, leaving inventory empty while heartbeats kept working.
func TestQueueRejectsUnencodableInventory(t *testing.T) {
	for _, payload := range []any{
		map[string]any{"mem_used_percent": math.NaN()},
		protocol.InventoryMsg{Data: json.RawMessage(`{"broken"`)},
	} {
		a := &agent{send: make(chan []byte, 1)}
		if a.queue(protocol.MsgInventory, payload) {
			t.Error("unencodable inventory reported as queued")
		}
		if len(a.send) != 0 {
			t.Error("an invalid inventory frame was put on the wire")
		}
	}
}

// Exercise the wire payload as well as JSON encoding: a single invalid usage
// reading must leave the other device, disk and software fields available.
func TestInventoryUnknownUsageDoesNotDiscardSnapshot(t *testing.T) {
	for name, percent := range map[string]float64{
		"NaN": math.NaN(), "positive infinity": math.Inf(1), "negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			data := map[string]any{
				"hostname": "windows-11", "os": "windows",
				"mem_used_percent": inventoryPercent(percent),
				"disks": []map[string]any{
					{"mount": "C:", "total_bytes": uint64(100), "used_percent": inventoryPercent(25)},
					{"mount": "D:", "total_bytes": uint64(0), "used_percent": inventoryPercent(percent)},
				},
				"software": []map[string]string{{"name": "Example App"}},
			}
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			a := &agent{send: make(chan []byte, 1)}
			if !a.queue(protocol.MsgInventory, protocol.InventoryMsg{Data: raw}) {
				t.Fatal("snapshot was not queued")
			}
			var env protocol.Envelope
			if err := json.Unmarshal(<-a.send, &env); err != nil || env.Type != protocol.MsgInventory {
				t.Fatalf("inventory envelope: %v", err)
			}
			var msg protocol.InventoryMsg
			if err := json.Unmarshal(env.Data, &msg); err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				Hostname string   `json:"hostname"`
				Memory   *float64 `json:"mem_used_percent"`
				Disks    []struct {
					Mount string   `json:"mount"`
					Used  *float64 `json:"used_percent"`
				} `json:"disks"`
				Software []map[string]string `json:"software"`
			}
			if err := json.Unmarshal(msg.Data, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Hostname != "windows-11" || snapshot.Memory != nil || len(snapshot.Disks) != 2 || len(snapshot.Software) != 1 {
				t.Fatalf("incomplete snapshot: %s", msg.Data)
			}
			if snapshot.Disks[0].Used == nil || *snapshot.Disks[0].Used != 25 || snapshot.Disks[1].Used != nil {
				t.Fatalf("unknown usage must be null; known usage must be preserved: %s", msg.Data)
			}
		})
	}
	for _, percent := range []float64{0, 37.5, 100} {
		if got := inventoryPercent(percent); got != percent {
			t.Fatalf("finite usage %v changed to %v", percent, got)
		}
	}
}
