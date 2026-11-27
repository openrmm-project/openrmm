//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the test binary is executed; no host package manager is ever invoked.
func TestPatchProcessHelper(t *testing.T) {
	i := -1
	for n, arg := range os.Args {
		if arg == "patch-helper" {
			i = n
			break
		}
	}
	if i < 0 {
		return
	}
	mode := os.Args[i+1]
	switch mode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestPatchProcessHelper", "--", "patch-helper", "child", os.Args[i+2])
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Args[i+2]+".started", []byte("ready"), 0600)
		time.Sleep(10 * time.Second)
	case "child":
		time.Sleep(time.Second)
		_ = os.WriteFile(os.Args[i+2], []byte("unsafe descendant survived"), 0600)
	case "output":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", patchScanOutputLimit+1)))
	case "error":
		_, _ = os.Stderr.WriteString("https://alice:password@repo.invalid credential=secret")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPatchProcessCancellationKillsAcquireChildren(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runPatchCommand(ctx, os.Args[0], "-test.run=TestPatchProcessHelper", "--", "patch-helper", "parent", sentinel)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sentinel+".started") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !fileExists(sentinel + ".started") {
		t.Fatal("helper did not start")
	}
	cancel()
	if err := <-done; err == nil || err.Error() != "timeout" {
		t.Fatalf("%v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if fileExists(sentinel) {
		t.Fatal("child wrote after cancellation")
	}
}

func TestPatchCommandBoundsAndSanitizesOutput(t *testing.T) {
	for _, mode := range []string{"output", "error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out, err := runPatchCommand(ctx, os.Args[0], "-test.run=TestPatchProcessHelper", "--", "patch-helper", mode)
			want := "scan_failed"
			if mode == "output" {
				want = "output_limit"
			}
			if err == nil || err.Error() != want || len(out) != 0 {
				t.Fatalf("out=%d error=%v", len(out), err)
			}
		})
	}
}
