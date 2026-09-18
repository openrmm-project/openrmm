package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool puts an executable of that name first on PATH. It appends whatever
// arrives on stdin to a transcript, so a test can assert both that the right
// tool was chosen and that the text reached it intact.
func fakeTool(t *testing.T, dir, name, transcript string, exitCode int) {
	t.Helper()
	// /bin/cat by absolute path: PATH is narrowed to the fake tools below, so a
	// bare "cat" would not resolve. printf is a shell builtin and always does.
	script := "#!/bin/sh\nprintf '%s:' " + name + " >> " + transcript + "\n/bin/cat >> " + transcript +
		"\nprintf '\\n' >> " + transcript + "\nexit " + itoa(exitCode) + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

func withPath(t *testing.T, dir string) {
	t.Helper()
	previous := os.Getenv("PATH")
	t.Setenv("PATH", dir)
	t.Cleanup(func() { _ = os.Setenv("PATH", previous) })
}

func TestClipboardPrefersWaylandTool(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "log")
	// Both installed: on a Wayland session the X11 tools write to Xwayland's
	// clipboard, which the compositor's applications never read.
	fakeTool(t, dir, "wl-copy", transcript, 0)
	fakeTool(t, dir, "xclip", transcript, 0)
	withPath(t, dir)

	if err := setClipboard("hello from the technician"); err != nil {
		t.Fatalf("setClipboard: %v", err)
	}
	body, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.HasPrefix(got, "wl-copy:") {
		t.Fatalf("expected wl-copy to be used first, got %q", got)
	}
	if !strings.Contains(got, "hello from the technician") {
		t.Fatalf("the text never reached the tool: %q", got)
	}
	if strings.Contains(got, "xclip:") {
		t.Fatal("xclip ran even though wl-copy succeeded")
	}
}

func TestClipboardFallsBackWhenAToolFails(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "log")
	fakeTool(t, dir, "wl-copy", transcript, 1) // no Wayland display, say
	fakeTool(t, dir, "xclip", transcript, 0)
	withPath(t, dir)

	if err := setClipboard("second choice"); err != nil {
		t.Fatalf("setClipboard: %v", err)
	}
	body, _ := os.ReadFile(transcript)
	if !strings.Contains(string(body), "xclip:second choice") {
		t.Fatalf("xclip did not receive the text: %q", body)
	}
}

func TestClipboardReportsWhenNothingIsInstalled(t *testing.T) {
	withPath(t, t.TempDir())
	err := setClipboard("nowhere to go")
	if err == nil {
		t.Fatal("a paste with no clipboard tool must be reported, not silently dropped")
	}
	if !strings.Contains(err.Error(), "wl-clipboard") {
		t.Fatalf("the error should name what to install, got %q", err)
	}
}

// The paste keystroke travels as ordinary key events so it goes through
// whichever backend is injecting input — uinput or XTEST — rather than a second
// code path that could drift from it.
func TestPasteKeyEventsAreAControlVChord(t *testing.T) {
	if len(pasteKeyEvents) != 4 {
		t.Fatalf("expected press and release of two keys, got %d events", len(pasteKeyEvents))
	}
	want := []string{
		`{"t":"k","code":17,"down":true}`,
		`{"t":"k","code":86,"down":true}`,
		`{"t":"k","code":86,"down":false}`,
		`{"t":"k","code":17,"down":false}`,
	}
	for i, event := range pasteKeyEvents {
		if string(event) != want[i] {
			t.Errorf("event %d = %s, want %s", i, event, want[i])
		}
	}
	// Control must be released last, or it stays down on the user's keyboard.
	if !strings.Contains(string(pasteKeyEvents[3]), `"code":17`) {
		t.Error("the modifier is not released last")
	}
}
