package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestIsolatedAptScan(t *testing.T) {
	parent := t.TempDir()
	var calls [][]string
	var dir string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "apt-get" {
			t.Fatalf("command %s", name)
		}
		calls = append(calls, append([]string{}, args...))
		if len(calls) == 1 {
			if args[0] != "-c" {
				t.Fatalf("config must load after normal config: %v", args)
			}
			dir = filepath.Dir(args[1])
			for _, p := range []string{dir, filepath.Join(dir, "lists")} {
				info, err := os.Stat(p)
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("_apt permissions %s: %v", p, err)
				}
			}
			conf, err := os.ReadFile(args[1])
			if err != nil {
				t.Fatal(err)
			}
			want := "#clear APT::Update::Pre-Invoke;\n#clear APT::Update::Post-Invoke;\n#clear APT::Update::Post-Invoke-Success;\n"
			if string(conf) != want {
				t.Fatalf("unsafe hooks: %s", conf)
			}
			expected := []string{"-c", args[1], "-o", "Dir::State::lists=" + filepath.Join(dir, "lists"), "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "-o", "Acquire::Retries=0", "-o", "Acquire::http::Timeout=25", "-o", "Acquire::https::Timeout=25", "-o", "APT::Update::Error-Mode=any", "update"}
			if !reflect.DeepEqual(args, expected) {
				t.Fatalf("update args: %v", args)
			}
			return nil, nil
		}
		expected := append(append([]string{}, calls[0][:len(calls[0])-1]...), "-s", "-o", "Debug::NoLocking=true", "upgrade")
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("simulation did not reuse catalog: %v", args)
		}
		return []byte("Inst openssl [1.0] (1.1 distro-security [amd64])\n"), nil
	}
	items, err := scanAptWithRunner(context.Background(), true, run, parent)
	if err != nil || len(calls) != 2 || len(items) != 1 || items[0].Classification != protocol.PatchSecurity {
		t.Fatalf("result %+v %v calls=%d", items, err, len(calls))
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("catalog not removed")
	}
}

func TestIsolatedAptFailuresAreSafeAndClean(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		size int
		want string
	}{
		{"credential error", errors.New("https://alice:secret@repo.invalid private details"), 0, "refresh_failed"},
		{"timeout", context.DeadlineExceeded, 0, "timeout"},
		{"permission", os.ErrPermission, 0, "permission"},
		{"oversized", nil, patchScanOutputLimit + 1, "output_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			calls := 0
			_, err := scanAptWithRunner(context.Background(), true, func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return make([]byte, tc.size), tc.err
			}, parent)
			if err == nil || err.Error() != tc.want || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("temporary catalog leaked")
			}
		})
	}
	t.Run("simulation failure", func(t *testing.T) {
		parent := t.TempDir()
		calls := 0
		_, err := scanAptWithRunner(context.Background(), true, func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == 2 {
				return nil, errors.New("secret token")
			}
			return nil, nil
		}, parent)
		entries, _ := os.ReadDir(parent)
		if err == nil || err.Error() != "scan_failed" || len(entries) != 0 {
			t.Fatalf("%v %v", err, entries)
		}
	})
	t.Run("missing temp parent", func(t *testing.T) {
		_, err := scanAptWithRunner(context.Background(), true, func(context.Context, string, ...string) ([]byte, error) { t.Fatal("runner reached"); return nil, nil }, filepath.Join(t.TempDir(), "absent"))
		if err == nil || strings.Contains(err.Error(), "absent") {
			t.Fatal(err)
		}
	})
}

func TestCachedAptDoesNotRefresh(t *testing.T) {
	calls := 0
	_, err := scanAptWithRunner(context.Background(), false, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if !reflect.DeepEqual(args, []string{"-s", "-o", "Debug::NoLocking=true", "upgrade"}) {
			t.Fatal(args)
		}
		return nil, nil
	}, t.TempDir())
	if err != nil || calls != 1 {
		t.Fatalf("%v %d", err, calls)
	}
}

func TestPatchScanDeclinesBusyWithoutQueueing(t *testing.T) {
	a := &agent{send: make(chan []byte, 2)}
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	done := make(chan struct{})
	go func() {
		a.runPatchScan(protocol.PatchScanMsg{RequestID: "busy-request", RefreshMetadata: true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan waited behind install")
	}
	var envelope protocol.Envelope
	var report protocol.PatchReportMsg
	if err := json.Unmarshal(<-a.send, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, &report); err != nil {
		t.Fatal(err)
	}
	if report.RequestID != "busy-request" || report.ErrorCode != "busy" {
		t.Fatalf("%+v", report)
	}
}
