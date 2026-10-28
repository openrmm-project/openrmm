package main

import (
	"encoding/json"
	"reflect"
	"runtime"
	"testing"
)

// Firmware fills unknown fields with placeholders; the inventory must not
// show them as if they were data.
func TestCleanHardwareStringDropsFirmwarePlaceholders(t *testing.T) {
	for in, want := range map[string]string{
		"  Dell Inc. \x00":       "Dell Inc.",
		"To Be Filled By O.E.M.": "",
		"to be filled by vendor": "",
		"Default string":         "",
		"System Serial Number":   "",
		"Not Specified":          "",
		"None":                   "",
		"unknown":                "",
		"":                       "",
		"PowerEdge R650":         "PowerEdge R650",
	} {
		if got := cleanHardwareString(in); got != want {
			t.Errorf("cleanHardwareString(%q) = %q, want %q", in, got, want)
		}
	}
	if got := firstHardwareValue("", "Default string", " ThinkPad X1 ", "later"); got != "ThinkPad X1" {
		t.Fatalf("firstHardwareValue = %q", got)
	}
	if got := firstHardwareValue("None", ""); got != "" {
		t.Fatalf("all placeholders = %q", got)
	}
}

func TestCompactHardwareMapRemovesEmptyBranches(t *testing.T) {
	var in map[string]any
	if err := json.Unmarshal([]byte(`{
		"vendor": "Lenovo", "serial": "To Be Filled By O.E.M.", "cores": 8, "nothing": null,
		"disks": [{"model": "Samsung SSD", "serial": "None"}, {"serial": "Default string"}, "unknown"],
		"bios": {"version": "Not Specified"},
		"empty": []
	}`), &in); err != nil {
		t.Fatal(err)
	}
	got := compactHardwareMap(in)
	want := map[string]any{
		"vendor": "Lenovo", "cores": float64(8),
		"disks": []any{map[string]any{"model": "Samsung SSD"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compactHardwareMap\n got %#v\nwant %#v", got, want)
	}
}

func TestHardwareSectionHelpers(t *testing.T) {
	if hardwareString("x") != "x" || hardwareString(float64(16)) != "16" || hardwareString(true) != "" {
		t.Fatal("hardwareString")
	}
	if len(hardwareSlice(nil)) != 0 || len(hardwareSlice("one")) != 1 || len(hardwareSlice([]any{1, 2})) != 2 {
		t.Fatal("hardwareSlice")
	}
	m := firstMapInSection([]any{"text", map[string]any{"a": 1}, map[string]any{"b": 2}})
	if m["a"] != 1 {
		t.Fatalf("firstMapInSection = %v", m)
	}
	if firstMapInSection("text") != nil {
		t.Fatal("no map in the section")
	}
}

func TestVirtualBlockDevicesAreNotDisks(t *testing.T) {
	for name, want := range map[string]bool{
		"loop0": true, "ram1": true, "zram0": true, "dm-0": true, "sr0": true, "fd0": true,
		"sda": false, "nvme0n1": false, "vda": false,
	} {
		if got := virtualBlockDevice(name); got != want {
			t.Errorf("virtualBlockDevice(%q) = %v", name, got)
		}
	}
}

func TestRegistrationErrorAndEnvironmentHelpers(t *testing.T) {
	if got := errFromData(json.RawMessage(`{"error":"device limit reached"}`)).Error(); got != "server rejected registration: device limit reached" {
		t.Fatalf("errFromData = %q", got)
	}
	if got := errFromData(json.RawMessage(`not json`)).Error(); got != "server rejected registration: " {
		t.Fatalf("unreadable error = %q", got)
	}
	t.Setenv("RMM_TEST_ENV", "")
	if envOr("RMM_TEST_ENV", "fallback") != "fallback" {
		t.Fatal("envOr empty")
	}
	t.Setenv("RMM_TEST_ENV", "set")
	if envOr("RMM_TEST_ENV", "fallback") != "set" {
		t.Fatal("envOr set")
	}
	for value, want := range map[string]bool{"1": true, "true": true, " YES ": true, "on": true, "0": false, "no": false, "": false, "maybe": false} {
		t.Setenv("RMM_TEST_ENV", value)
		if envBool("RMM_TEST_ENV") != want {
			t.Errorf("envBool(%q) != %v", value, want)
		}
	}
}

// The real collector, on the machine running the tests: it only reads the
// system. Linux only, where it is quick (macOS's system_profiler is not).
func TestCollectInventoryOnLinuxReadsTheBasics(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Linux collector")
	}
	var inv map[string]any
	if err := json.Unmarshal(collectInventory(), &inv); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hostname", "os", "arch", "num_cpu", "agent_version", "collected", "hardware"} {
		if _, ok := inv[key]; !ok {
			t.Errorf("inventory has no %q", key)
		}
	}
	if inv["os"] != "linux" || inv["agent_version"] != agentVersion {
		t.Fatalf("os %v, version %v", inv["os"], inv["agent_version"])
	}
}
