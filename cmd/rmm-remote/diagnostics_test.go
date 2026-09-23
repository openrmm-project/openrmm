package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rmm-remote.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateLog(path, 200)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a log under the limit was moved: %v", err)
	}
	if err := os.WriteFile(path+".1", []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateLog(path, 50)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an oversized log was left in place: %v", err)
	}
	b, err := os.ReadFile(path + ".1")
	if err != nil || len(b) != 100 {
		t.Fatalf("the previous file should now hold the rotated log, got %d bytes, %v", len(b), err)
	}
	rotateLog(filepath.Join(dir, "missing.log"), 1) // must not panic or create anything
}

type captured struct{ lines []string }

func (c *captured) logf(format string, args ...any) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func TestRepeatLogSummarisesInsteadOfRepeating(t *testing.T) {
	var out captured
	now := time.Unix(0, 0)
	r := &repeatLog{every: time.Minute, now: func() time.Time { return now }, logf: out.logf}

	// Twice a second for two and a half minutes: 300 failures.
	for i := 0; i < 300; i++ {
		r.fail("capture: denied")
		now = now.Add(500 * time.Millisecond)
	}
	if len(out.lines) != 3 {
		t.Fatalf("got %d lines for 300 identical failures, want the first plus one per minute:\n%s", len(out.lines), strings.Join(out.lines, "\n"))
	}
	if out.lines[0] != "capture: denied" || !strings.Contains(out.lines[1], "repeated 120 more times") {
		t.Fatalf("unexpected lines:\n%s", strings.Join(out.lines, "\n"))
	}

	r.ok()
	last := out.lines[len(out.lines)-1]
	if !strings.Contains(last, "recovered after 300 failures") || !strings.Contains(last, "capture: denied") {
		t.Fatalf("recovery line = %q", last)
	}
	before := len(out.lines)
	r.ok()
	if len(out.lines) != before {
		t.Fatal("ok() on a healthy stream must write nothing")
	}
}

func TestRepeatLogReportsPendingRepeatsWhenTheMessageChanges(t *testing.T) {
	var out captured
	now := time.Unix(0, 0)
	r := &repeatLog{every: time.Hour, now: func() time.Time { return now }, logf: out.logf}
	r.fail("a")
	r.fail("a")
	r.fail("a")
	r.fail("b")
	want := []string{"a", "a (repeated 2 more times)", "b"}
	if strings.Join(out.lines, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", out.lines, want)
	}
}

func TestBundleIdentity(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "RMMRemote.app")
	exe := filepath.Join(app, "Contents", "MacOS", "rmm-remote")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := bundleIdentity(exe); !strings.HasPrefix(got, "ad-hoc identity") {
		t.Fatalf("no marker: %q", got)
	}
	res := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	dr := `identifier "io.openrmm.remote" and certificate root = H"a2fb72d5"`
	if err := os.WriteFile(filepath.Join(res, "StableCodeIdentity"), []byte(dr+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bundleIdentity(exe); got != "certificate identity: "+dr {
		t.Fatalf("with marker: %q", got)
	}
	if got := bundleIdentity(filepath.Join(dir, "rmm-remote")); got != "not in an app bundle" {
		t.Fatalf("loose binary: %q", got)
	}
}

func TestDialHint(t *testing.T) {
	err := errors.New("dial tcp 10.0.2.2:3005: connect: no route to host")
	if h := dialHint("darwin", err); !strings.Contains(h, "Local Network") {
		t.Fatalf("macOS hint missing: %q", h)
	}
	if h := dialHint("linux", err); h != "" {
		t.Fatalf("a Linux routing error is a routing error: %q", h)
	}
	if h := dialHint("darwin", errors.New("connection refused")); h != "" {
		t.Fatalf("unrelated error got a hint: %q", h)
	}
	if h := dialHint("darwin", nil); h != "" {
		t.Fatal("nil error got a hint")
	}
}

func TestDescribePermissions(t *testing.T) {
	cases := map[string]permissionStart{
		"screen recording allowed, accessibility allowed":                                           {screen: true, input: true},
		"screen recording missing, accessibility missing, asked after Screen Recording is answered": {inputDeferred: true},
		"screen recording allowed, accessibility missing":                                           {screen: true},
	}
	for want, s := range cases {
		if got := describePermissions(s); got != want {
			t.Errorf("describePermissions(%+v) = %q, want %q", s, got, want)
		}
	}
}
