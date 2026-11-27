package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

type registryRoundTrip func(*http.Request) (*http.Response, error)

func (f registryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func registryResponse(status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

var imageTestPlatform = imagePlatform{OS: "linux", Architecture: "amd64"}
var imageTestDigest = "sha256:" + strings.Repeat("a", 64)
var imageNextDigest = "sha256:" + strings.Repeat("b", 64)
var imageChildDigest = "sha256:" + strings.Repeat("c", 64)

func registryIndex(digest string) string {
	return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, digest)
}
func registryImage(digest string) string {
	return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q}}`, digest)
}
func testRegistrySession(tag string, handle registryRoundTrip) *registrySession {
	return &registrySession{ref: imageReference{host: "registry-1.docker.io", repo: "library/demo", tag: tag}, client: &http.Client{Transport: handle, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func TestImageReferenceBoundaries(t *testing.T) {
	for _, raw := range []string{"ubuntu", "nginx:1.2.3", "docker.io/library/nginx:latest", "ghcr.io/org/image:v1", "quay.io/org/image:2"} {
		r, code := parseImageReference(raw)
		if code != "" || r.repo == "" {
			t.Fatalf("%q: %+v %s", raw, r, code)
		}
	}
	for _, raw := range []string{"localhost:5000/image:1", "127.0.0.1/x", "private.example/x", "https://ghcr.io/x", "ghcr.io/../x", "ghcr.io/org//x", "ghcr.io/org/x:bad/tag", " x", "nginx@sha256:bad", "nginx:latest?x"} {
		if _, code := parseImageReference(raw); code == "" {
			t.Fatalf("accepted %q", raw)
		}
	}
	r, code := parseImageReference("nginx:1@" + imageTestDigest)
	if code != "" || !r.pinned {
		t.Fatalf("pinned: %+v %s", r, code)
	}
}
func TestImagePlatformProjectionOnly(t *testing.T) {
	tmpl := template.Must(template.New("image").Option("missingkey=error").Funcs(template.FuncMap{"json": func(v any) string { b, _ := json.Marshal(v); return string(b) }}).Parse(dockerImageProjection))
	var b bytes.Buffer
	err := tmpl.Execute(&b, map[string]any{"Id": imageTestDigest, "Os": "linux", "Architecture": "amd64", "Config": map[string]any{"Env": "CANARY_SECRET"}, "RootFS": "CANARY_SECRET"})
	if err != nil || strings.Contains(b.String(), "CANARY_SECRET") {
		t.Fatalf("unsafe projection %s %v", b.String(), err)
	}
	var p imagePlatform
	if json.Unmarshal(b.Bytes(), &p) != nil || p.ID != imageTestDigest || !p.same(imageTestPlatform) {
		t.Fatalf("%+v", p)
	}
}
func TestRegistryMultiArchAndIndependentVersion(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			digest := imageTestDigest
			if changed {
				digest = imageNextDigest
			}
			s := testRegistrySession("1.2.3", func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v2/library/demo/manifests/1.2.3":
					return registryResponse(200, registryIndex(imageChildDigest), nil), nil
				case "/v2/library/demo/manifests/" + imageChildDigest:
					return registryResponse(200, registryImage(digest), nil), nil
				case "/v2/library/demo/tags/list":
					return registryResponse(200, `{"tags":["1.2.3","2.0.0","9.0.0-rc1","2026.11.20","3.0.0-alpine","v8.0.0"]}`, nil), nil
				case "/v2/library/demo/manifests/2.0.0":
					return registryResponse(200, registryIndex(imageChildDigest), nil), nil
				default:
					t.Fatalf("unexpected request %s", r.URL)
					return nil, nil
				}
			})
			s.ref.host = "ghcr.io"
			u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
			want := "up_to_date"
			if changed {
				want = "update_available"
			}
			if u.Status != want || u.VersionStatus != "available" || u.LatestTag != "2.0.0" || u.VersionKind != "major" {
				t.Fatalf("%+v", u)
			}
		})
	}
}
func TestRegistryDirectManifestPlatformAndMissingPlatform(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "/blobs/") {
				return registryResponse(200, `{"os":"linux","architecture":"`+arch+`","config":{"Env":["CANARY_SECRET"]}}`, nil), nil
			}
			return registryResponse(200, registryImage(imageTestDigest), nil), nil
		})
		u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
		if arch == "amd64" && u.Status != "up_to_date" || arch == "arm64" && (u.Status != "unknown" || u.ErrorCode != "platform_unsupported") {
			t.Fatalf("%+v", u)
		}
		if u.VersionStatus != "unsupported" || u.VersionErrorCode != "tag_format_unsupported" {
			t.Fatalf("%+v", u)
		}
	}
	for _, platforms := range []string{`{"os":"linux","architecture":"arm64"}`, `{"os":"linux","architecture":"amd64","variant":"v3"}`} {
		s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
			return registryResponse(200, `{"schemaVersion":2,"manifests":[{"digest":"`+imageChildDigest+`","platform":`+platforms+`}]}`, nil), nil
		})
		_, code := s.manifest(context.Background(), "latest", imageTestPlatform)
		if code != "platform_unsupported" {
			t.Fatal(code)
		}
	}
	s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
		return registryResponse(200, `{"schemaVersion":2,"manifests":[{"digest":"`+imageChildDigest+`","platform":{"os":"linux","architecture":"amd64"}},{"digest":"`+imageNextDigest+`","platform":{"os":"linux","architecture":"amd64"}}]}`, nil), nil
	})
	_, code := s.manifest(context.Background(), "latest", imageTestPlatform)
	if code != "platform_unsupported" {
		t.Fatal(code)
	}
}
func TestRegistryAnonymousAuthenticationTrust(t *testing.T) {
	cases := []struct{ challenge, code string }{
		{`Basic realm="secret"`, "authentication_required"},
		{`Bearer realm="https://evil.example/token",service="registry.docker.io"`, "authentication_failed"},
		{`Bearer realm="http://auth.docker.io/token",service="registry.docker.io"`, "authentication_failed"},
		{`Bearer realm="https://auth.docker.io/token",service="evil"`, "authentication_failed"},
		{`Bearer realm="https://auth.docker.io/token",scope="repository:private/secrets:pull"`, "authentication_failed"},
	}
	for _, tc := range cases {
		s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "registry-1.docker.io" {
				t.Fatalf("untrusted token request %s", r.URL)
			}
			return registryResponse(401, "CANARY_SECRET", map[string]string{"WWW-Authenticate": tc.challenge}), nil
		})
		_, _, code := s.get(context.Background(), "/v2/library/demo/manifests/latest")
		if code != tc.code {
			t.Fatalf("%s: %s", tc.challenge, code)
		}
	}
	calls := 0
	s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "auth.docker.io" {
			if r.Header.Get("Authorization") != "" || r.URL.Query().Get("scope") != "repository:library/demo:pull" {
				t.Fatal("unsafe auth")
			}
			return registryResponse(200, `{"token":"CANARY_SECRET"}`, nil), nil
		}
		if r.Header.Get("Authorization") == "Bearer CANARY_SECRET" {
			return registryResponse(200, "{}", nil), nil
		}
		return registryResponse(401, "", map[string]string{"WWW-Authenticate": `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/demo:pull"`}), nil
	})
	_, _, code := s.get(context.Background(), "/v2/library/demo/manifests/latest")
	if code != "" || calls != 3 {
		t.Fatalf("%s %d", code, calls)
	}
}
func TestRegistryBoundedErrorsAndSecrets(t *testing.T) {
	for _, status := range []int{403, 404, 429, 500, 302} {
		calls := 0
		s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
			calls++
			return registryResponse(status, "CANARY_SECRET", map[string]string{"Location": "https://evil.example/leak"}), nil
		})
		u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
		b, _ := json.Marshal(u)
		if strings.Contains(string(b), "CANARY") || u.Status != "unknown" || calls != 1 {
			t.Fatalf("%s calls %d", b, calls)
		}
	}
	s := testRegistrySession("latest", func(*http.Request) (*http.Response, error) {
		return registryResponse(200, strings.Repeat("s", 2*1024*1024+1), nil), nil
	})
	_, _, code := s.get(context.Background(), "/v2/library/demo/manifests/latest")
	if code != "response_too_large" {
		t.Fatal(code)
	}
	s = testRegistrySession("latest", func(*http.Request) (*http.Response, error) {
		return nil, errors.New("CANARY_SECRET https://user:password@private")
	})
	u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
	if u.ErrorCode != "registry_unavailable" {
		t.Fatalf("%+v", u)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s = testRegistrySession("latest", func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	u = s.check(ctx, imageTestDigest, imageTestPlatform)
	if u.ErrorCode != "registry_timeout" {
		t.Fatalf("%+v", u)
	}
}
func TestStableImageVersions(t *testing.T) {
	for _, tag := range []string{"1", "1.2", "1.2.3", "v1.2.3", "1.2.3-alpine", "1.2.3-alpine3.20", "1.2-slim-bookworm", "2.3.4-noble"} {
		if _, ok := parseStableImageVersion(tag); !ok {
			t.Fatal(tag)
		}
	}
	for _, tag := range []string{"latest", "stable", "2026.11.20", "1.2.3-rc1", "1.2.3-beta", "1.2.3-custom", "01.2.3", "1.2.3.4", "v1.2.3+build"} {
		if _, ok := parseStableImageVersion(tag); ok {
			t.Fatal(tag)
		}
	}
	for _, tc := range []struct{ current, next, kind string }{{"1.2.3", "1.2.4", "patch"}, {"1.2.3", "1.3.0", "minor"}, {"1.2.3", "2.0.0", "major"}} {
		a, _ := parseStableImageVersion(tc.current)
		b, _ := parseStableImageVersion(tc.next)
		if !b.newer(a) || b.kind(a) != tc.kind {
			t.Fatal(tc)
		}
	}
}
func TestRegistryTagPaginationAndPartialCandidates(t *testing.T) {
	for _, partial := range []bool{false, true} {
		pages := 0
		s := testRegistrySession("v1.2.3-alpine", func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "/manifests/") {
				if strings.HasSuffix(r.URL.Path, imageChildDigest) {
					return registryResponse(200, registryImage(imageTestDigest), nil), nil
				}
				return registryResponse(200, registryIndex(imageChildDigest), nil), nil
			}
			pages++
			if pages == 1 {
				return registryResponse(200, `{"tags":["v1.2.4-alpine","v1.9.9-slim","2.0.0-alpine","v2-alpine"]}`, map[string]string{"Link": `</v2/library/demo/tags/list?n=100&last=v1.2.4-alpine>; rel="next"`}), nil
			}
			if partial {
				return registryResponse(429, "CANARY_SECRET", nil), nil
			}
			return registryResponse(200, `{"tags":["v1.3.0-alpine"]}`, nil), nil
		})
		s.ref.host = "ghcr.io"
		s.ref.host = "ghcr.io"
		u := protocol.DockerImageUpdate{VersionStatus: "unknown"}
		s.versions(context.Background(), imageTestPlatform, &u)
		want := "v1.3.0-alpine"
		if partial {
			want = "v1.2.4-alpine"
		}
		if u.VersionStatus != "available" || u.LatestTag != want || pages != 2 {
			t.Fatalf("%+v pages %d", u, pages)
		}
		if partial && u.VersionErrorCode != "registry_rate_limited" {
			t.Fatalf("partial completeness lost: %+v", u)
		}
	}
	s := testRegistrySession("1.2.3", func(r *http.Request) (*http.Response, error) {
		return registryResponse(200, `{"tags":["1.2.3"]}`, nil), nil
	})
	s.ref.host = "ghcr.io"
	u := protocol.DockerImageUpdate{VersionStatus: "unknown"}
	s.versions(context.Background(), imageTestPlatform, &u)
	if u.VersionStatus != "not_found" || u.VersionErrorCode != "" {
		t.Fatalf("%+v", u)
	}
	for _, link := range []string{`<https://evil.example/v2/library/demo/tags/list?last=x>; rel="next"`, `<http://registry-1.docker.io/v2/library/demo/tags/list?last=x>; rel="next"`, `</v2/other/tags/list?last=x>; rel="next"`, `</v2/library/demo/tags/list?last=../token>; rel="next"`, `</v2/library/demo/tags/list?last=x&secret=y>; rel="next"`} {
		if _, code := s.nextTagsPath(link); code != "tag_search_incomplete" {
			t.Fatal(link)
		}
	}
}
func waitImageWorker(t *testing.T, m *imageUpdateManager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker failed to finish")
}
func TestImageUpdateCacheAsyncDedupTTLAndIdentity(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := newImageUpdateManager()
	m.now = func() time.Time { return now }
	started, release := make(chan struct{}, 1), make(chan struct{})
	calls := 0
	m.inspect = func(context.Context, string, string) (imagePlatform, string) { return imageTestPlatform, "" }
	m.check = func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate {
		calls++
		if calls == 1 {
			started <- struct{}{}
			<-release
		}
		return protocol.DockerImageUpdate{Status: "up_to_date", VersionStatus: "unsupported", VersionErrorCode: "tag_format_unsupported"}
	}
	c := protocol.DockerContainer{Image: "demo:latest", ImageID: imageTestDigest}
	s := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{c, c}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.attach(ctx, &s)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	m.attach(ctx, &s)
	if s.Containers[0].Update.Status != "unknown" {
		t.Fatal("premature result")
	}
	close(release)
	waitImageWorker(t, m)
	m.attach(ctx, &s)
	if calls != 1 || s.Containers[0].Update.Status != "up_to_date" || s.Containers[0].Update.CheckedAt == nil {
		t.Fatalf("calls %d update %+v", calls, s.Containers[0].Update)
	}
	now = now.Add(5 * time.Hour)
	m.attach(ctx, &s)
	if calls != 1 {
		t.Fatal("cache refreshed too early")
	}
	now = now.Add(2 * time.Hour)
	m.attach(ctx, &s)
	waitImageWorker(t, m)
	if calls != 2 {
		t.Fatal("cache not refreshed")
	}
	s.Containers[0].ImageID = imageNextDigest
	m.attach(ctx, &s)
	if s.Containers[0].Update.Status != "unknown" {
		t.Fatal("new running image inherited result")
	}
	waitImageWorker(t, m)
	if calls != 3 {
		t.Fatal(calls)
	}
}
func TestImageUpdateFailureBackoffCancellationAndOptOut(t *testing.T) {
	now := time.Now()
	m := newImageUpdateManager()
	m.now = func() time.Time { return now }
	calls := 0
	m.inspect = func(context.Context, string, string) (imagePlatform, string) {
		calls++
		return imagePlatform{}, "platform_unavailable"
	}
	s := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{{Image: "demo:1.2.3", ImageID: imageTestDigest}}}
	m.attach(context.Background(), &s)
	waitImageWorker(t, m)
	now = now.Add(29 * time.Minute)
	m.attach(context.Background(), &s)
	if calls != 1 {
		t.Fatal("failure retry storm")
	}
	now = now.Add(2 * time.Minute)
	m.attach(context.Background(), &s)
	waitImageWorker(t, m)
	if calls != 2 {
		t.Fatal("no retry")
	}
	t.Setenv("RMM_DOCKER_UPDATE_CHECKS", "false")
	m.attach(context.Background(), &s)
	if s.Containers[0].Update.ErrorCode != "disabled" {
		t.Fatal("disabled cached result")
	}
	t.Setenv("RMM_DOCKER_UPDATE_CHECKS", "true")
	m = newImageUpdateManager()
	started := make(chan struct{})
	m.inspect = func(ctx context.Context, _, _ string) (imagePlatform, string) {
		close(started)
		<-ctx.Done()
		return imagePlatform{}, "platform_unavailable"
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.attach(ctx, &s)
	<-started
	cancel()
	waitImageWorker(t, m)
	if len(m.cache) != 0 {
		t.Fatal("cancellation poisoned cache")
	}
}
func TestImageUpdateCacheBound(t *testing.T) {
	m := newImageUpdateManager()
	m.inspect = func(context.Context, string, string) (imagePlatform, string) { return imageTestPlatform, "" }
	m.check = func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate {
		return protocol.DockerImageUpdate{Status: "up_to_date", VersionStatus: "not_found"}
	}
	for batch := 0; batch < 66; batch++ {
		s := protocol.DockerSnapshot{Status: protocol.DockerAvailable}
		for i := 0; i < 8; i++ {
			s.Containers = append(s.Containers, protocol.DockerContainer{Image: fmt.Sprintf("demo:%d", batch*8+i), ImageID: imageTestDigest})
		}
		m.attach(context.Background(), &s)
		waitImageWorker(t, m)
	}
	if len(m.cache) != dockerUpdateCacheLimit {
		t.Fatal(len(m.cache))
	}
}

func TestImageLocalProvenance(t *testing.T) {
	for _, tc := range []struct {
		digests []string
		code    string
	}{
		{nil, "local_image"},
		{[]string{"other@" + imageTestDigest}, "local_image"},
		{[]string{"docker.io/library/demo@" + imageTestDigest}, ""},
		{[]string{"registry-1.docker.io/library/demo@" + imageTestDigest}, ""},
		{[]string{"ghcr.io/library/demo@" + imageTestDigest}, "local_image"},
	} {
		p := imageTestPlatform
		p.ID = imageTestDigest
		p.RepoDigests = tc.digests
		b, _ := json.Marshal(p)
		_, code := parseLocalImagePlatform(b, imageTestDigest, "demo:latest")
		if code != tc.code {
			t.Fatalf("%v: %s", tc.digests, code)
		}
	}
	for _, ref := range []string{imageTestDigest, strings.Repeat("a", 64)} {
		_, eligible := initialImageUpdate(protocol.DockerContainer{ImageID: imageTestDigest, Image: ref}, false)
		if eligible {
			t.Fatalf("local image ID requested registry: %s", ref)
		}
	}
}
func TestImageCacheRecoversAfterInspectFailure(t *testing.T) {
	now := time.Now()
	m := newImageUpdateManager()
	m.now = func() time.Time { return now }
	fail := true
	m.inspect = func(context.Context, string, string) (imagePlatform, string) {
		if fail {
			return imagePlatform{}, "platform_unavailable"
		}
		return imageTestPlatform, ""
	}
	m.check = func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate {
		return protocol.DockerImageUpdate{Status: "up_to_date", VersionStatus: "unsupported"}
	}
	s := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{{Image: "demo:latest", ImageID: imageTestDigest}}}
	m.attach(context.Background(), &s)
	waitImageWorker(t, m)
	fail = false
	now = now.Add(time.Hour)
	m.attach(context.Background(), &s)
	waitImageWorker(t, m)
	m.attach(context.Background(), &s)
	if len(m.cache) != 1 || s.Containers[0].Update.Status != "up_to_date" {
		t.Fatalf("old failure survived: entries %d update %+v", len(m.cache), s.Containers[0].Update)
	}
}
func TestRegistryHubRecentCatalogAndNoCredentials(t *testing.T) {
	pages := 0
	s := testRegistrySession("16", func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "hub.docker.com" {
			pages++
			if r.Header.Get("Authorization") != "" || r.URL.Query().Get("ordering") != "last_updated" || r.URL.Query().Get("page_size") != "100" {
				t.Fatal("unsafe Hub request")
			}
			return registryResponse(200, `{"results":[{"name":"19"},{"name":"19-rc1"},{"name":"19.0"}],"next":"https://hub.docker.com/ignored"}`, nil), nil
		}
		if strings.HasSuffix(r.URL.Path, imageChildDigest) {
			return registryResponse(200, registryImage(imageNextDigest), nil), nil
		}
		return registryResponse(200, registryIndex(imageChildDigest), nil), nil
	})
	s.token = "CANARY_SECRET"
	u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
	if u.Status != "update_available" || u.VersionStatus != "available" || u.LatestTag != "19" || u.VersionErrorCode != "tag_search_incomplete" || pages != 5 {
		t.Fatalf("%+v pages %d", u, pages)
	}
}
func TestRegistryInvalidEmptyCatalogDoesNotClaimCurrent(t *testing.T) {
	for _, host := range []string{"ghcr.io", "registry-1.docker.io"} {
		s := testRegistrySession("1.2.3", func(*http.Request) (*http.Response, error) { return registryResponse(200, `{}`, nil), nil })
		s.ref.host = host
		u := protocol.DockerImageUpdate{VersionStatus: "unknown"}
		s.versions(context.Background(), imageTestPlatform, &u)
		if u.VersionStatus != "unknown" || u.VersionErrorCode != "manifest_invalid" {
			t.Fatalf("%s: %+v", host, u)
		}
	}
}
func TestImageWorkerSweepCapAndFairness(t *testing.T) {
	m := newImageUpdateManager()
	m.inspect = func(context.Context, string, string) (imagePlatform, string) { return imageTestPlatform, "" }
	calls := 0
	m.check = func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate {
		calls++
		return protocol.DockerImageUpdate{Status: "up_to_date", VersionStatus: "not_found"}
	}
	s := protocol.DockerSnapshot{Status: protocol.DockerAvailable}
	for i := 0; i < 20; i++ {
		s.Containers = append(s.Containers, protocol.DockerContainer{Image: fmt.Sprintf("demo:%d", i), ImageID: imageTestDigest})
	}
	for _, want := range []int{8, 16, 20} {
		m.attach(context.Background(), &s)
		waitImageWorker(t, m)
		if calls != want {
			t.Fatalf("worker did %d jobs want %d", calls, want)
		}
	}
}

func TestRegistrySameTagFailureDoesNotHideNewVersion(t *testing.T) {
	s := testRegistrySession("1.2.3", func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v2/library/demo/manifests/1.2.3":
			return registryResponse(404, "CANARY_SECRET", nil), nil
		case "/v2/library/demo/tags/list":
			return registryResponse(200, `{"tags":["2.0.0"]}`, nil), nil
		case "/v2/library/demo/manifests/2.0.0":
			return registryResponse(200, registryIndex(imageChildDigest), nil), nil
		case "/v2/library/demo/manifests/" + imageChildDigest:
			return registryResponse(200, registryImage(imageNextDigest), nil), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})
	s.ref.host = "ghcr.io"
	u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
	if u.Status != "unknown" || u.ErrorCode != "manifest_not_found" || u.VersionStatus != "available" || u.LatestTag != "2.0.0" {
		t.Fatalf("%+v", u)
	}
}
func TestRegistryTrustedTokenHosts(t *testing.T) {
	for _, host := range []string{"ghcr.io", "quay.io"} {
		realm := "https://" + host + "/token"
		if host == "quay.io" {
			realm = "https://quay.io/v2/auth"
		}
		tokens := 0
		s := testRegistrySession("latest", func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != host {
				t.Fatalf("wrong host %s", r.URL)
			}
			if strings.Contains(r.URL.Path, "token") || r.URL.Path == "/v2/auth" {
				tokens++
				if r.Header.Get("Authorization") != "" {
					t.Fatal("token endpoint received auth")
				}
				return registryResponse(200, `{"access_token":"anonymous-token"}`, nil), nil
			}
			if r.Header.Get("Authorization") == "Bearer anonymous-token" {
				return registryResponse(200, "{}", nil), nil
			}
			return registryResponse(401, "", map[string]string{"WWW-Authenticate": `Bearer realm="` + realm + `",service="` + host + `"`}), nil
		})
		s.ref.host = host
		_, _, code := s.get(context.Background(), "/v2/library/demo/manifests/latest")
		if code != "" || tokens != 1 {
			t.Fatalf("%s: %s tokens %d", host, code, tokens)
		}
	}
}
func TestImageUnsupportedNeverStartsWorker(t *testing.T) {
	m := newImageUpdateManager()
	m.inspect = func(context.Context, string, string) (imagePlatform, string) {
		t.Error("unsupported reference inspected")
		return imagePlatform{}, "platform_unavailable"
	}
	s := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{
		{Image: "nginx@" + imageTestDigest, ImageID: imageTestDigest},
		{Image: "private.example/image:1", ImageID: imageTestDigest},
		{Image: "nginx:1"},
	}}
	m.attach(context.Background(), &s)
	if m.running || s.Containers[0].Update.Status != "pinned" || s.Containers[1].Update.ErrorCode != "registry_unsupported" || s.Containers[2].Update.ErrorCode != "image_id_missing" {
		t.Fatal("unsupported status or worker")
	}
}

func TestRegistryHubRequiresExplicitPageCompletion(t *testing.T) {
	for _, body := range []string{`{"results":[]}`, `{"results":[],"next":""}`, `{"results":[],"next":false}`} {
		s := testRegistrySession("1.2.3", func(*http.Request) (*http.Response, error) { return registryResponse(200, body, nil), nil })
		u := protocol.DockerImageUpdate{VersionStatus: "unknown"}
		s.versions(context.Background(), imageTestPlatform, &u)
		if u.VersionStatus != "unknown" || u.VersionErrorCode != "manifest_invalid" {
			t.Fatalf("%s: %+v", body, u)
		}
	}
	s := testRegistrySession("1.2.3", func(*http.Request) (*http.Response, error) {
		return registryResponse(200, `{"results":[],"next":null}`, nil), nil
	})
	u := protocol.DockerImageUpdate{VersionStatus: "unknown"}
	s.versions(context.Background(), imageTestPlatform, &u)
	if u.VersionStatus != "not_found" {
		t.Fatalf("%+v", u)
	}
}

func TestImagePinnedNumericReferenceEligibility(t *testing.T) {
	for _, tc := range []struct {
		reference string
		eligible  bool
	}{
		{"postgres:15.20-trixie@" + imageTestDigest, true},
		{"henrygd/beszel:0.21.2@" + imageTestDigest, true},
		{"postgres@" + imageTestDigest, false},
		{"postgres:latest@" + imageTestDigest, false},
		{"postgres:16-rc1@" + imageTestDigest, false},
	} {
		u, eligible := initialImageUpdate(protocol.DockerContainer{Image: tc.reference, ImageID: imageTestDigest}, false)
		if eligible != tc.eligible || u.Status != "pinned" {
			t.Fatalf("%s: %+v eligible %t", tc.reference, u, eligible)
		}
		want := "unsupported"
		if eligible {
			want = "unknown"
		}
		if u.VersionStatus != want {
			t.Fatalf("%s: %+v", tc.reference, u)
		}
	}
	u, eligible := initialImageUpdate(protocol.DockerContainer{Image: "postgres:16@" + imageTestDigest}, false)
	if eligible || u.Status != "pinned" || u.VersionStatus != "unknown" || u.VersionErrorCode != "image_id_missing" || u.ErrorCode != "" {
		t.Fatalf("missing local identity: %+v eligible %t", u, eligible)
	}
}
func TestRegistryPinnedNumericOnlySearchesNewVersions(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		requests := 0
		s := testRegistrySession("15.20-trixie", func(r *http.Request) (*http.Response, error) {
			requests++
			switch r.URL.Path {
			case "/v2/library/demo/tags/list":
				if unavailable {
					return registryResponse(429, "CANARY_SECRET", nil), nil
				}
				return registryResponse(200, `{"tags":["15.20-trixie","16.1-trixie","16.1-bookworm"]}`, nil), nil
			case "/v2/library/demo/manifests/16.1-trixie":
				return registryResponse(200, registryIndex(imageChildDigest), nil), nil
			case "/v2/library/demo/manifests/" + imageChildDigest:
				return registryResponse(200, registryImage(imageNextDigest), nil), nil
			default:
				t.Fatalf("pinned check requested mutable current tag or unexpected endpoint: %s", r.URL)
				return nil, nil
			}
		})
		s.ref.host = "ghcr.io"
		s.ref.pinned = true
		u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
		if u.Status != "pinned" || u.ErrorCode != "" {
			t.Fatalf("pin lost: %+v", u)
		}
		if unavailable {
			if u.VersionStatus != "unknown" || u.VersionErrorCode != "registry_rate_limited" || requests != 1 {
				t.Fatalf("failure: %+v requests %d", u, requests)
			}
		} else if u.VersionStatus != "available" || u.LatestTag != "16.1-trixie" || u.VersionKind != "major" || requests != 3 {
			t.Fatalf("candidate: %+v requests %d", u, requests)
		}
	}
}
func TestRegistryUnversionedPinNeverRequestsNetwork(t *testing.T) {
	for _, tag := range []string{"latest", "stable", "16-rc1"} {
		s := testRegistrySession(tag, func(*http.Request) (*http.Response, error) {
			t.Error("non-numeric pin requested network")
			return nil, errors.New("unexpected")
		})
		s.ref.pinned = true
		u := s.check(context.Background(), imageTestDigest, imageTestPlatform)
		if u.Status != "pinned" || u.VersionStatus != "unsupported" {
			t.Fatalf("%s: %+v", tag, u)
		}
	}
}
func TestImagePinnedStatusSurvivesMetadataFailureAndCache(t *testing.T) {
	for _, code := range []string{"platform_unavailable", "local_image"} {
		m := newImageUpdateManager()
		inspections := 0
		m.inspect = func(context.Context, string, string) (imagePlatform, string) {
			inspections++
			return imagePlatform{}, code
		}
		m.check = func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate {
			t.Error("metadata failure requested registry")
			return protocol.DockerImageUpdate{}
		}
		s := protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{{Image: "demo:1.2.3@" + imageTestDigest, ImageID: imageTestDigest}}}
		m.attach(context.Background(), &s)
		waitImageWorker(t, m)
		m.attach(context.Background(), &s)
		u := s.Containers[0].Update
		if u.Status != "pinned" || u.VersionStatus != "unknown" || u.VersionErrorCode != code || u.ErrorCode != "" || u.CheckedAt == nil || inspections != 1 {
			t.Fatalf("%s: %+v inspections %d", code, u, inspections)
		}
		if err := u.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
