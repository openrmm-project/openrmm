package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

const patchScanOutputLimit = 4 << 20

var errPatchOutputLimit = errors.New("output_limit")

type patchCommandRunner func(context.Context, string, ...string) ([]byte, error)

type boundedPatchOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	exceeded bool
}

func (b *boundedPatchOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len()+len(p) > patchScanOutputLimit {
		b.exceeded = true
		return 0, errPatchOutputLimit
	}
	return b.buf.Write(p)
}

// runPatchCommand bounds both streams and waits for process-group cancellation
// before the caller removes its private catalog. Stderr never reaches reports.
func runPatchCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	out := &boundedPatchOutput{}
	cmd.Stdout = out
	// stderr also needs a bound, even though it is never returned.
	stderr := &boundedPatchOutput{}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	cleanup := configurePatchProcess(cmd)
	defer cleanup()
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("timeout")
	}
	if out.exceeded || stderr.exceeded {
		return nil, errPatchOutputLimit
	}
	if errors.Is(err, os.ErrPermission) {
		return nil, errors.New("permission")
	}
	if err != nil {
		return nil, errors.New("scan_failed")
	}
	return out.buf.Bytes(), nil
}

func patchScanError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.New("timeout")
	}
	if errors.Is(err, os.ErrPermission) {
		return errors.New("permission")
	}
	switch err.Error() {
	case "timeout", "permission", "output_limit":
		return errors.New(err.Error())
	}
	return errors.New(fallback)
}

// scanAptWithRunner reads a new private catalog only when explicitly requested.
// No command installs packages, changes host lists, or executes update hooks.
func scanAptWithRunner(ctx context.Context, refresh bool, run patchCommandRunner, tempParent string) ([]protocol.PatchItem, error) {
	args := []string{}
	if refresh {
		dir, err := os.MkdirTemp(tempParent, "openrmm-scan-")
		if err != nil {
			return nil, patchScanError(err, "refresh_failed")
		}
		defer os.RemoveAll(dir)
		if err = os.Chmod(dir, 0755); err != nil {
			return nil, patchScanError(err, "refresh_failed")
		}
		lists := filepath.Join(dir, "lists")
		if err = os.Mkdir(lists, 0755); err != nil {
			return nil, patchScanError(err, "refresh_failed")
		}
		// Mkdir's mode is masked by the service umask; _apt needs traversal.
		if err = os.Chmod(lists, 0755); err != nil {
			return nil, patchScanError(err, "refresh_failed")
		}
		conf := filepath.Join(dir, "scan.conf")
		if err = os.WriteFile(conf, []byte("#clear APT::Update::Pre-Invoke;\n#clear APT::Update::Post-Invoke;\n#clear APT::Update::Post-Invoke-Success;\n"), 0644); err != nil {
			return nil, patchScanError(err, "refresh_failed")
		}
		args = []string{"-c", conf, "-o", "Dir::State::lists=" + lists, "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "-o", "Acquire::Retries=0", "-o", "Acquire::http::Timeout=25", "-o", "Acquire::https::Timeout=25", "-o", "APT::Update::Error-Mode=any"}
		if out, err := run(ctx, "apt-get", append(append([]string{}, args...), "update")...); err != nil {
			return nil, patchScanError(err, "refresh_failed")
		} else if len(out) > patchScanOutputLimit {
			return nil, errPatchOutputLimit
		}
	}
	out, err := run(ctx, "apt-get", append(args, "-s", "-o", "Debug::NoLocking=true", "upgrade")...)
	if err != nil {
		return nil, patchScanError(err, "scan_failed")
	}
	if len(out) > patchScanOutputLimit {
		return nil, errPatchOutputLimit
	}
	return parseAptUpgrade(string(out)), nil
}
