//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeLaunchd stands in for /bin/launchctl: it records every call and knows
// which jobs are loaded and which of them are running.
type fakeLaunchd struct {
	mu     sync.Mutex
	calls  []string
	loaded map[string]bool // label -> running
}

func (f *fakeLaunchd) run(args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	label := func(target string) string { return target[strings.LastIndexByte(target, '/')+1:] }
	switch args[0] {
	case "bootstrap":
		body, err := os.ReadFile(args[2])
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		if !strings.Contains(string(body), "<string>"+name+"</string>") {
			return nil, fmt.Errorf("plist label does not match its file")
		}
		f.loaded[name] = true // RunAtLoad: running at once
	case "print":
		running, ok := f.loaded[label(args[1])]
		if !ok {
			return []byte("Could not find service"), fmt.Errorf("exit status 113")
		}
		if running {
			return []byte("state = running"), nil
		}
		return []byte("state = not running"), nil
	case "bootout", "kickstart":
		delete(f.loaded, label(args[len(args)-1]))
	}
	return nil, nil
}

func (f *fakeLaunchd) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// A second session — a guest joining the technician's, or a second technician
// — must not stop the one already running. With one shared job restarted per
// session, each new session killed the other, and a guest's session looped.
func TestSessionJobsRunSideBySide(t *testing.T) {
	f := &fakeLaunchd{loaded: map[string]bool{}}
	old := launchctl
	launchctl = f.run
	t.Cleanup(func() { launchctl = old })
	uid := strconv.Itoa(os.Getuid())
	dir := t.TempDir()

	if err := startSessionJob(uid, dir, "wss://relay/a", "ticket-a", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := startSessionJob(uid, dir, "wss://relay/b", "ticket-b", false, false, true); err != nil {
		t.Fatal(err)
	}
	if len(f.loaded) != 2 {
		t.Fatalf("want two jobs running side by side, have %v", f.loaded)
	}
	for label, running := range f.loaded {
		if !running || !strings.HasPrefix(label, sessionJobPrefix) {
			t.Fatalf("job %s running=%v", label, running)
		}
	}
	if n := f.count("bootout") + f.count("kickstart"); n != 0 {
		t.Fatalf("starting a session stopped another one: %v", f.calls)
	}

	// Each job reads its own parameters, private to the session user.
	params, _ := filepath.Glob(filepath.Join(dir, sessionJobPrefix+"*.json"))
	if len(params) != 2 {
		t.Fatalf("parameter files: %v", params)
	}
	var tickets []string
	for _, p := range params {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %v", p, info.Mode().Perm())
		}
		body, _ := os.ReadFile(p)
		tickets = append(tickets, string(body))
		plist, err := os.ReadFile(strings.TrimSuffix(p, ".json") + ".plist")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plist), "<string>"+p+"</string>") || !strings.Contains(string(plist), "<key>RunAtLoad</key><true/>") {
			t.Fatalf("the plist does not start the sidecar on its own parameters:\n%s", plist)
		}
	}
	joined := strings.Join(tickets, "\n")
	if !strings.Contains(joined, "ticket-a") || !strings.Contains(joined, "ticket-b") || !strings.Contains(joined, `"no_chat":true`) {
		t.Fatalf("parameters: %s", joined)
	}
}

// The jobs of sessions that ended are removed, files and all, when the next one
// starts; a running one is kept.
func TestSessionJobsReapFinished(t *testing.T) {
	f := &fakeLaunchd{loaded: map[string]bool{}}
	old := launchctl
	launchctl = f.run
	t.Cleanup(func() { launchctl = old })
	uid := strconv.Itoa(os.Getuid())
	dir := t.TempDir()

	for _, ticket := range []string{"one", "two"} {
		if err := startSessionJob(uid, dir, "wss://relay", ticket, false, false, false); err != nil {
			t.Fatal(err)
		}
	}
	var finished, running string
	for label := range f.loaded {
		if finished == "" {
			finished = label
		} else {
			running = label
		}
	}
	f.loaded[finished] = false // its sidecar exited
	// A job launchd no longer knows (a reboot, a logout) only leaves files.
	stale := sessionJobPrefix + "0000000000000000"
	if err := os.WriteFile(filepath.Join(dir, stale+".plist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := startSessionJob(uid, dir, "wss://relay", "three", false, false, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.loaded[finished]; ok {
		t.Fatal("a finished session's job was not booted out")
	}
	if !f.loaded[running] {
		t.Fatal("a running session's job was stopped")
	}
	for _, label := range []string{finished, stale} {
		if _, err := os.Stat(filepath.Join(dir, label+".plist")); !os.IsNotExist(err) {
			t.Fatalf("%s.plist was not removed", label)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, running+".plist")); err != nil {
		t.Fatal("a running session's plist was removed")
	}
}
