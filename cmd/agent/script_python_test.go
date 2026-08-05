package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requirePython guards the tests that need a real interpreter.
//
// On a developer machine without Python, skipping is right: failing there would
// blame the code for the machine. In CI it is the opposite. A Go skip counts as
// a pass, and the job runs without -v, so a runner missing Python would produce
// a green build that proves nothing — and the Windows and macOS runs exist
// precisely to cover the platform-specific parts of this path (the py -3
// lookup, path handling, killing the process tree) that cannot be exercised on
// the Linux machines this is written on. A silent skip there would mean claiming
// cross-platform coverage that never happened.
//
// So in CI the absence is a failure, loudly, instead of a pass nobody reads.
func requirePython(t *testing.T) {
	t.Helper()
	if _, _, err := pythonInterpreter(); err == nil {
		return
	} else if os.Getenv("CI") == "" {
		t.Skipf("no Python interpreter available: %v", err)
	} else {
		t.Fatalf("this runner has no Python, so the execution path was not tested here: %v", err)
	}
}

func TestPythonRunsMultiLineScripts(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Indentation and newlines survive, which is the reason the script is staged
	// in a file rather than passed as a -c argument.
	code, stdout, stderr := runScriptCtx(ctx, "python", "for i in range(3):\n    print('line', i)\n")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"line 0", "line 1", "line 2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q missing %q", stdout, want)
		}
	}
}

// The script's own exit status is what the operator sees, so a script that fails
// deliberately is not reported as a success.
func TestPythonPropagatesExitCode(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if code, _, _ := runScriptCtx(ctx, "python", "raise SystemExit(3)"); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if code, _, _ := runScriptCtx(ctx, "python", "print('fine')"); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

// A traceback names the staged file and the line. With -c every error points at
// "<string>", which tells an operator debugging a 200-line script nothing.
func TestPythonTracebackNamesTheStagedFile(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _, stderr := runScriptCtx(ctx, "python", "x = 1\nraise ValueError('boom')\n")
	if !strings.Contains(stderr, "openrmm-script-") {
		t.Errorf("stderr should name the staged script, got %q", stderr)
	}
	if !strings.Contains(stderr, "line 2") {
		t.Errorf("stderr should name the failing line, got %q", stderr)
	}
	if strings.Contains(stderr, "<string>") {
		t.Error("the traceback should not point at <string>")
	}
}

// The staged file must not outlive the run: scripts routinely carry credentials,
// and the temp directory is world-readable.
func TestPythonRemovesTheStagedFile(t *testing.T) {
	requirePython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	before := stagedScripts(t)
	if _, _, stderr := runScriptCtx(ctx, "python", "print('hi')"); stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if after := stagedScripts(t); len(after) != len(before) {
		t.Errorf("staged scripts leaked: %d before, %d after", len(before), len(after))
	}
}

// Cancellation (an operator cancel, or the job timeout) must clean up too — the
// removal is deferred in the agent rather than left to the child process.
func TestPythonRemovesTheStagedFileWhenCancelled(t *testing.T) {
	requirePython(t)
	before := stagedScripts(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code, _, _ := runScriptCtx(ctx, "python", "import time\ntime.sleep(30)\n")
	if code == 0 {
		t.Error("a cancelled script should not report success")
	}
	if after := stagedScripts(t); len(after) != len(before) {
		t.Errorf("staged scripts leaked after cancellation: %d before, %d after", len(before), len(after))
	}
}

func stagedScripts(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "openrmm-script-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// "No Python on this device" is a provisioning gap, not a broken script, and the
// two are fixed by different people. The exit code and the message keep them
// apart, and the message names what was looked for.
//
// An empty PATH makes the lookup fail wherever this runs, so the behaviour is
// tested on machines that do have Python — which is all of them in CI, and would
// otherwise mean this path was never exercised.
func TestPythonWithoutAnInterpreter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, _, err := pythonInterpreter()
	if err == nil {
		t.Fatal("an empty PATH should yield no interpreter")
	}
	for _, want := range []string{"python3", "python"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name the %q candidate", err, want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, _, stderr := runScriptCtx(ctx, "python", "print('never runs')")
	if code != exitNoInterpreter {
		t.Errorf("exit = %d, want %d so a missing interpreter is distinguishable from a failing script",
			code, exitNoInterpreter)
	}
	if !strings.Contains(stderr, "no Python interpreter found") {
		t.Errorf("stderr = %q, want it to say the device has no interpreter", stderr)
	}
}

// The candidate list is ordered on purpose: python3 first, because on a machine
// with both, "python" may still be Python 2 and a Python 3 script fails there in
// ways that read like a syntax error in the operator's code.
func TestPythonCandidateOrder(t *testing.T) {
	if len(pythonCandidates) == 0 || pythonCandidates[0].name != "python3" {
		t.Fatalf("python3 must be tried first, got %+v", pythonCandidates)
	}
	var sawPy bool
	for _, c := range pythonCandidates {
		if c.name == "py" {
			sawPy = true
			if len(c.args) == 0 || c.args[0] != "-3" {
				t.Errorf("the Windows py launcher must be pinned to -3, got %v", c.args)
			}
		}
	}
	if !sawPy && runtime.GOOS == "windows" {
		t.Error("Windows should try the py launcher")
	}
}

// An unknown interpreter is refused rather than silently run under a default.
func TestUnknownShellIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, _, stderr := runScriptCtx(ctx, "ruby", "puts 1")
	if code == 0 || !strings.Contains(stderr, "unsupported shell") {
		t.Errorf("code=%d stderr=%q, want a refusal", code, stderr)
	}
}
