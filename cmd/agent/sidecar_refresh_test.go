//go:build linux

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The sidecar refresh end to end, against a real HTTP server: what is served
// under /dl/, when it is downloaded again, and above all that a bad update never
// takes away a sidecar that works. On Linux the artifact is the executable
// itself, so a shell script answering -print-version stands in for it; the
// macOS bundle path differs only in how the archive is unpacked.

type dlServer struct {
	mu     sync.Mutex
	name   string
	body   string
	etag   string
	status int
	gets   atomic.Int32
}

func (d *dlServer) set(version, etag string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.body = fmt.Sprintf("#!/bin/sh\n[ \"$1\" = \"-print-version\" ] && echo %s\n", version)
	d.etag = etag
	d.status = http.StatusOK
}

func (d *dlServer) broken(etag string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.body = "#!/bin/sh\nexit 3\n" // cannot report a version: not a sidecar
	d.etag = etag
	d.status = http.StatusOK
}

func (d *dlServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.URL.Path != "/dl/"+d.name {
		http.NotFound(w, r)
		return
	}
	if d.status != http.StatusOK {
		w.WriteHeader(d.status)
		return
	}
	w.Header().Set("ETag", d.etag)
	if r.Method == http.MethodHead {
		return
	}
	d.gets.Add(1)
	_, _ = w.Write([]byte(d.body))
}

// newRefreshFixture points an agent at a test server. The sidecar lives next to
// the running executable, which under `go test` is the test binary in its own
// temporary build directory; whatever is written there is removed afterwards.
func newRefreshFixture(t *testing.T) (*agent, *dlServer, string, string) {
	t.Helper()
	previous := runtimeUpdateVerify
	runtimeUpdateVerify = "off" // signatures have their own tests; this is about the refresh
	t.Cleanup(func() { runtimeUpdateVerify = previous })

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := sidecarExePath(filepath.Dir(exe))
	etagPath := filepath.Join(filepath.Dir(exe), sidecarName()+".etag")
	clean := func() { _ = os.Remove(path); _ = os.Remove(etagPath); _ = os.Remove(path + ".new") }
	clean()
	t.Cleanup(clean)

	d := &dlServer{name: sidecarDownloadName("")}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	a := &agent{wsURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/agent", statePath: filepath.Join(t.TempDir(), "state.json")}
	return a, d, path, etagPath
}

func installedVersion(t *testing.T, path string) string {
	t.Helper()
	v, err := binaryVersion(path)
	if err != nil {
		t.Fatalf("installed sidecar does not run: %v", err)
	}
	return v
}

func TestSidecarFirstSessionDownloads(t *testing.T) {
	a, d, path, etagPath := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	got, err := a.ensureSidecar()
	if err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}
	if got != path || installedVersion(t, path) != "1.0.0" {
		t.Fatalf("installed %s at %s", installedVersion(t, path), got)
	}
	if readETag(etagPath) != `"v1"` {
		t.Fatalf("etag recorded %q", readETag(etagPath))
	}
}

func TestSidecarUnchangedIsNotDownloadedAgain(t *testing.T) {
	a, d, _, _ := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	before := d.gets.Load()
	for i := 0; i < 3; i++ {
		if _, err := a.ensureSidecar(); err != nil {
			t.Fatal(err)
		}
	}
	if d.gets.Load() != before {
		t.Fatalf("an unchanged sidecar was downloaded %d more times", d.gets.Load()-before)
	}
}

func TestSidecarPublishedUpdateIsInstalled(t *testing.T) {
	a, d, path, etagPath := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	d.set("1.1.0", `"v2"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	if v := installedVersion(t, path); v != "1.1.0" {
		t.Fatalf("installed %s after publishing 1.1.0", v)
	}
	if readETag(etagPath) != `"v2"` {
		t.Fatalf("etag %q", readETag(etagPath))
	}
}

// Republishing the same version with different bytes is how a re-signed build
// reaches the fleet; equal versions must be accepted.
func TestSidecarSameVersionRebuildIsInstalled(t *testing.T) {
	a, d, _, etagPath := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	d.set("1.0.0", `"v1-resigned"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	if readETag(etagPath) != `"v1-resigned"` {
		t.Fatalf("a same-version rebuild was not taken: etag %q", readETag(etagPath))
	}
}

func TestSidecarDowngradeKeepsTheInstalledOne(t *testing.T) {
	a, d, path, etagPath := newRefreshFixture(t)
	d.set("2.0.0", `"v2"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	d.set("1.0.0", `"v1"`)
	got, err := a.ensureSidecar()
	if err != nil {
		t.Fatalf("a refused update must not fail the session: %v", err)
	}
	if got != path || installedVersion(t, path) != "2.0.0" {
		t.Fatalf("downgrade replaced the sidecar: %s", installedVersion(t, path))
	}
	// Left unmatched so a corrected publication is picked up at the next session.
	if readETag(etagPath) != `"v2"` {
		t.Fatalf("etag advanced to %q on a refused update", readETag(etagPath))
	}
}

func TestSidecarBrokenUpdateKeepsTheInstalledOne(t *testing.T) {
	a, d, path, _ := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	d.broken(`"bad"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatalf("a broken publication must not fail the session: %v", err)
	}
	if v := installedVersion(t, path); v != "1.0.0" {
		t.Fatalf("a broken publication replaced a working sidecar with %q", v)
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatalf("the rejected download was left behind: %v", err)
	}
}

func TestSidecarFirstSessionFailsWhenNothingIsPublished(t *testing.T) {
	a, d, path, _ := newRefreshFixture(t)
	d.mu.Lock()
	d.status = http.StatusNotFound
	d.mu.Unlock()
	if _, err := a.ensureSidecar(); err == nil {
		t.Fatal("with no sidecar installed and none published there is nothing to run")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("something was installed anyway: %v", err)
	}
}

func TestSidecarServerDownKeepsTheInstalledOne(t *testing.T) {
	a, d, path, _ := newRefreshFixture(t)
	d.set("1.0.0", `"v1"`)
	if _, err := a.ensureSidecar(); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.status = http.StatusServiceUnavailable
	d.mu.Unlock()
	got, err := a.ensureSidecar()
	if err != nil || got != path {
		t.Fatalf("an unreachable server must not cost a working sidecar: %v", err)
	}
}
