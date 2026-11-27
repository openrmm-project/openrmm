package main

// Registry checks are observational only. No pull, login, container mutation or
// Docker credential loading is involved in this code.
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

const dockerImageProjection = `{"id":{{json .Id}},"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json (index . "Variant")}},"repo_digests":{{json (index . "RepoDigests")}}}`
const dockerUpdateCacheLimit = 512

var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageRepoPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
var imageTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var platformPartPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

type imagePlatform struct {
	ID           string   `json:"id"`
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	Variant      string   `json:"variant"`
	RepoDigests  []string `json:"repo_digests,omitempty"`
}

func (p imagePlatform) key() string { return p.OS + "/" + p.Architecture + "/" + p.Variant }
func (p imagePlatform) valid() bool {
	return platformPartPattern.MatchString(p.OS) && platformPartPattern.MatchString(p.Architecture) && (p.Variant == "" || platformPartPattern.MatchString(p.Variant))
}
func (p imagePlatform) same(q imagePlatform) bool { return p.key() == q.key() }

type imageReference struct {
	host, repo, tag string
	pinned          bool
}

func parseImageReference(raw string) (r imageReference, code string) {
	if len(raw) > 512 || strings.TrimSpace(raw) != raw {
		return r, "invalid_reference"
	}
	if imageDigestPattern.MatchString(raw) || protocol.ValidDockerID(raw) {
		return r, "invalid_reference"
	}
	if strings.Contains(raw, "@") {
		parts := strings.Split(raw, "@")
		if len(parts) != 2 || !imageDigestPattern.MatchString(parts[1]) {
			return r, "invalid_reference"
		}
		r.pinned = true
		raw = parts[0]
	}
	first, rest, slash := strings.Cut(raw, "/")
	host := "docker.io"
	if slash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host = first
		raw = rest
	}
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		r.host = "registry-1.docker.io"
	case "ghcr.io", "quay.io":
		r.host = host
	default:
		return r, "registry_unsupported"
	}
	r.repo = raw
	r.tag = "latest"
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		r.repo = raw[:i]
		r.tag = raw[i+1:]
	}
	if r.host == "registry-1.docker.io" && !strings.Contains(r.repo, "/") {
		r.repo = "library/" + r.repo
	}
	if !imageRepoPattern.MatchString(r.repo) || !imageTagPattern.MatchString(r.tag) {
		return r, "invalid_reference"
	}
	return r, ""
}

type imageUpdateEntry struct {
	result  protocol.DockerImageUpdate
	expires time.Time
	base    string
}
type imageUpdateManager struct {
	mu      sync.Mutex
	cache   map[string]imageUpdateEntry // image ID + reference + inspected platform
	running bool
	now     func() time.Time
	inspect func(context.Context, string, string) (imagePlatform, string)
	check   func(context.Context, imageReference, string, imagePlatform) protocol.DockerImageUpdate
}

func newImageUpdateManager() *imageUpdateManager {
	return &imageUpdateManager{cache: map[string]imageUpdateEntry{}, now: time.Now, inspect: inspectImagePlatform, check: checkImageUpdate}
}

var processImageUpdates = newImageUpdateManager()

func inspectImagePlatform(ctx context.Context, id, reference string) (p imagePlatform, code string) {
	code = "platform_unavailable"
	observeLocalDocker(ctx, func(ctx context.Context, run dockerCommand) protocol.DockerSnapshot {
		out, err := run(ctx, "image", "inspect", "--format", dockerImageProjection, id)
		if err == nil {
			p, code = parseLocalImagePlatform(out, id, reference)
		}
		return protocol.DockerSnapshot{}
	})
	return
}

// Repository digests are local pull provenance. A local build can reuse a
// public image name; it must not be compared to an unrelated public image.
func parseLocalImagePlatform(out []byte, id, reference string) (p imagePlatform, code string) {
	if json.Unmarshal(out, &p) != nil || p.ID != id || !p.valid() {
		return p, "platform_unavailable"
	}
	r, code := parseImageReference(reference)
	if code != "" {
		return p, code
	}
	for _, digest := range p.RepoDigests {
		origin, errCode := parseImageReference(digest)
		if errCode == "" && origin.pinned && origin.host == r.host && origin.repo == r.repo {
			return p, ""
		}
	}
	return p, "local_image"
}
func imageUpdateBase(c protocol.DockerContainer) string { return c.ImageID + "\x00" + c.Image }
func initialImageUpdate(c protocol.DockerContainer, disabled bool) (*protocol.DockerImageUpdate, bool) {
	u := &protocol.DockerImageUpdate{Status: "unknown", VersionStatus: "unknown"}
	if disabled {
		u.Status = "unsupported"
		u.ErrorCode = "disabled"
		u.VersionStatus = "unsupported"
		u.VersionErrorCode = "disabled"
		return u, false
	}
	r, code := parseImageReference(c.Image)
	if code != "" {
		u.Status = "unsupported"
		u.ErrorCode = code
		u.VersionStatus = "unsupported"
		u.VersionErrorCode = code
		return u, false
	}
	if r.pinned {
		u.Status = "pinned"
		if _, stable := parseStableImageVersion(r.tag); !stable {
			u.VersionStatus = "unsupported"
			return u, false
		}
	}
	if !imageDigestPattern.MatchString(c.ImageID) {
		if !r.pinned {
			u.ErrorCode = "image_id_missing"
		}
		u.VersionErrorCode = "image_id_missing"
		return u, false
	}
	return u, true
}

// attach never waits for registry or Docker commands. One connection owns each
// worker's cancellation; completed cache entries survive connection changes.
func (m *imageUpdateManager) attach(ctx context.Context, s *protocol.DockerSnapshot) {
	if s.Status != protocol.DockerAvailable {
		return
	}
	disabled := os.Getenv("RMM_DOCKER_UPDATE_CHECKS") == "false"
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []protocol.DockerContainer
	queued := map[string]bool{}
	now := m.now()
	for i := range s.Containers {
		c := &s.Containers[i]
		u, eligible := initialImageUpdate(*c, disabled)
		c.Update = u
		if !eligible {
			continue
		}
		base := imageUpdateBase(*c)
		fresh := false
		for _, entry := range m.cache {
			if entry.base == base {
				v := entry.result
				c.Update = &v
				fresh = now.Before(entry.expires)
				break
			}
		}
		if !fresh && !queued[base] && len(jobs) < 8 {
			queued[base] = true
			jobs = append(jobs, *c)
		}
	}
	if disabled || m.running || len(jobs) == 0 || ctx.Err() != nil {
		return
	}
	m.running = true
	go func() {
		defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
		ctx, cancelSweep := context.WithTimeout(ctx, 90*time.Second)
		defer cancelSweep()
		seen := map[string]bool{}
		for _, c := range jobs {
			if ctx.Err() != nil {
				return
			}
			base := imageUpdateBase(c)
			if seen[base] {
				continue
			}
			seen[base] = true
			jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			p, code := m.inspect(jobCtx, c.ImageID, c.Image)
			r, _ := parseImageReference(c.Image)
			var u protocol.DockerImageUpdate
			if code != "" {
				u = protocol.DockerImageUpdate{Status: "unknown", ErrorCode: code, VersionStatus: "unknown", VersionErrorCode: code}
				if r.pinned {
					u.Status = "pinned"
					u.ErrorCode = ""
				}
			} else {
				u = m.check(jobCtx, r, c.ImageID, p)
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			checked := m.now().UTC()
			u.CheckedAt = &checked
			ttl := 6 * time.Hour
			if u.Status == "unknown" || u.VersionStatus == "unknown" || (u.VersionErrorCode != "" && u.VersionStatus != "unsupported") {
				ttl = 30 * time.Minute
			}
			m.mu.Lock()
			for k, e := range m.cache {
				if e.base == base {
					delete(m.cache, k)
				}
			}
			if len(m.cache) >= dockerUpdateCacheLimit {
				oldKey := ""
				var oldest time.Time
				for k, e := range m.cache {
					if oldKey == "" || e.expires.Before(oldest) {
						oldKey = k
						oldest = e.expires
					}
				}
				delete(m.cache, oldKey)
			}
			m.cache[base+"\x00"+p.key()] = imageUpdateEntry{result: u, expires: checked.Add(ttl), base: base}
			m.mu.Unlock()
		}
	}()
}

type registrySession struct {
	client   *http.Client
	ref      imageReference
	token    string
	requests int
}

func registryHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxResponseHeaderBytes = 64 * 1024
	tr.MaxIdleConns = 8
	tr.MaxIdleConnsPerHost = 2
	tr.ResponseHeaderTimeout = 8 * time.Second
	return &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var publicRegistryClient = registryHTTPClient()

func registryHTTPCode(status int) string {
	switch status {
	case 401, 403:
		return "authentication_required"
	case 404:
		return "manifest_not_found"
	case 429:
		return "registry_rate_limited"
	default:
		return "registry_unavailable"
	}
}
func (s *registrySession) request(ctx context.Context, raw string, token string) ([]byte, http.Header, int, string) {
	s.requests++
	if s.requests > 24 {
		return nil, nil, 0, "tag_search_incomplete"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, nil, 0, "registry_unavailable"
	}
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil || isTimeout(err) {
			return nil, nil, 0, "registry_timeout"
		}
		return nil, nil, 0, "registry_unavailable"
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if len(b) > 2*1024*1024 {
		return nil, nil, resp.StatusCode, "response_too_large"
	}
	if err != nil {
		return nil, nil, resp.StatusCode, "registry_unavailable"
	}
	return b, resp.Header, resp.StatusCode, ""
}
func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	e, ok := err.(timeout)
	return ok && e.Timeout()
}

var bearerFieldPattern = regexp.MustCompile(`(?:^|,)\s*(realm|service|scope)="([^"\r\n]*)"\s*`)

func (s *registrySession) authenticate(ctx context.Context, challenge string) string {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "authentication_required"
	}
	fields := map[string]string{}
	remaining := strings.TrimPrefix(challenge, "Bearer ")
	for remaining != "" {
		m := bearerFieldPattern.FindStringSubmatchIndex(remaining)
		if m == nil || m[0] != 0 {
			return "authentication_failed"
		}
		key := remaining[m[2]:m[3]]
		if _, ok := fields[key]; ok {
			return "authentication_failed"
		}
		fields[key] = remaining[m[4]:m[5]]
		remaining = remaining[m[1]:]
	}
	realm, service := "", ""
	switch s.ref.host {
	case "registry-1.docker.io":
		realm = "https://auth.docker.io/token"
		service = "registry.docker.io"
	case "ghcr.io":
		realm = "https://ghcr.io/token"
		service = "ghcr.io"
	case "quay.io":
		realm = "https://quay.io/v2/auth"
		service = "quay.io"
	}
	if fields["realm"] != realm || (fields["service"] != "" && fields["service"] != service) {
		return "authentication_failed"
	}
	scope := "repository:" + s.ref.repo + ":pull"
	if fields["scope"] != "" && fields["scope"] != scope {
		return "authentication_failed"
	}
	q := url.Values{"service": {service}, "scope": {scope}}
	b, _, status, code := s.request(ctx, realm+"?"+q.Encode(), "")
	if code != "" {
		return code
	}
	if status != 200 {
		return "authentication_failed"
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(b, &t) != nil {
		return "authentication_failed"
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" || len(t.Token) > 16384 || strings.ContainsAny(t.Token, "\r\n") {
		return "authentication_failed"
	}
	s.token = t.Token
	return ""
}
func (s *registrySession) get(ctx context.Context, path string) ([]byte, http.Header, string) {
	raw := "https://" + s.ref.host + path
	b, h, status, code := s.request(ctx, raw, s.token)
	if code != "" {
		return nil, nil, code
	}
	if status == 401 && s.token == "" {
		if code = s.authenticate(ctx, h.Get("WWW-Authenticate")); code != "" {
			return nil, nil, code
		}
		b, h, status, code = s.request(ctx, raw, s.token)
		if code != "" {
			return nil, nil, code
		}
	}
	if status != 200 {
		return nil, nil, registryHTTPCode(status)
	}
	return b, h, ""
}

type registryDescriptor struct {
	Digest    string        `json:"digest"`
	MediaType string        `json:"mediaType"`
	Platform  imagePlatform `json:"platform"`
}
type registryManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Manifests     []registryDescriptor `json:"manifests"`
	Config        registryDescriptor   `json:"config"`
}

func (s *registrySession) manifest(ctx context.Context, reference string, p imagePlatform) (string, string) {
	path := "/v2/" + s.ref.repo + "/manifests/"
	b, _, code := s.get(ctx, path+reference)
	if code != "" {
		return "", code
	}
	var m registryManifest
	if json.Unmarshal(b, &m) != nil || m.SchemaVersion != 2 {
		return "", "manifest_invalid"
	}
	selected := false
	if len(m.Manifests) > 0 {
		digest := ""
		count := 0
		for _, d := range m.Manifests {
			if d.Platform.same(p) {
				digest = d.Digest
				count++
			}
		}
		if count != 1 || !imageDigestPattern.MatchString(digest) {
			return "", "platform_unsupported"
		}
		b, _, code = s.get(ctx, path+digest)
		if code != "" {
			return "", code
		}
		m = registryManifest{}
		if json.Unmarshal(b, &m) != nil || m.SchemaVersion != 2 || len(m.Manifests) != 0 {
			return "", "manifest_invalid"
		}
		selected = true
	}
	if !imageDigestPattern.MatchString(m.Config.Digest) {
		return "", "manifest_invalid"
	}
	if !selected {
		b, _, code = s.get(ctx, "/v2/"+s.ref.repo+"/blobs/"+m.Config.Digest)
		if code != "" {
			return "", code
		}
		var config imagePlatform
		if json.Unmarshal(b, &config) != nil || !config.valid() {
			return "", "manifest_invalid"
		}
		if !config.same(p) {
			return "", "platform_unsupported"
		}
	}
	return m.Config.Digest, ""
}

type stableImageVersion struct {
	numbers        [3]int
	count          int
	prefix, suffix string
}

var stableTagPattern = regexp.MustCompile(`^(v?)(0|[1-9][0-9]{0,2})(?:\.(0|[1-9][0-9]{0,2}))?(?:\.(0|[1-9][0-9]{0,2}))?(-[a-z0-9.-]+)?$`)
var stableFlavorPattern = regexp.MustCompile(`^-(?:alpine(?:[0-9]+(?:\.[0-9]+)?)?|slim|bookworm|bullseye|trixie|buster|jammy|noble|bookworm-slim|bullseye-slim|trixie-slim|slim-bookworm|slim-bullseye|slim-trixie)$`)

func parseStableImageVersion(tag string) (v stableImageVersion, ok bool) {
	m := stableTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return v, false
	}
	v.prefix = m[1]
	v.suffix = m[5]
	if v.suffix != "" && !stableFlavorPattern.MatchString(v.suffix) {
		return v, false
	}
	for i := 0; i < 3; i++ {
		if m[i+2] != "" {
			v.numbers[i], _ = strconv.Atoi(m[i+2])
			v.count++
		}
	}
	return v, true
}
func (v stableImageVersion) newer(other stableImageVersion) bool {
	for i, n := range v.numbers {
		if n != other.numbers[i] {
			return n > other.numbers[i]
		}
	}
	return false
}
func (v stableImageVersion) kind(other stableImageVersion) string {
	for i, n := range v.numbers {
		if n != other.numbers[i] {
			return []string{"major", "minor", "patch"}[i]
		}
	}
	return ""
}
func (s *registrySession) versions(ctx context.Context, p imagePlatform, u *protocol.DockerImageUpdate) {
	current, ok := parseStableImageVersion(s.ref.tag)
	if !ok {
		u.VersionStatus = "unsupported"
		u.VersionErrorCode = "tag_format_unsupported"
		return
	}
	type candidate struct {
		tag string
		v   stableImageVersion
	}
	candidates := []candidate{}
	seenTags := map[string]bool{}
	complete := false
	path := "/v2/" + s.ref.repo + "/tags/list?n=100"
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		if seen[path] {
			u.VersionErrorCode = "tag_search_incomplete"
			break
		}
		seen[path] = true
		tags, next, code := s.tagPage(ctx, path, page+1)
		for _, tag := range tags {
			if seenTags[tag] {
				continue
			}
			seenTags[tag] = true
			v, ok := parseStableImageVersion(tag)
			if ok && v.count == current.count && v.prefix == current.prefix && v.suffix == current.suffix && v.newer(current) {
				candidates = append(candidates, candidate{tag, v})
			}
		}
		if code != "" {
			u.VersionErrorCode = code
			break
		}
		if next == "" {
			complete = true
			break
		}
		path = next
	}
	if !complete && u.VersionErrorCode == "" {
		u.VersionErrorCode = "tag_search_incomplete"
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].v.newer(candidates[j].v) })
	for i, c := range candidates {
		if i >= 3 {
			u.VersionErrorCode = "tag_search_incomplete"
			break
		}
		_, code := s.manifest(ctx, c.tag, p)
		if code == "" {
			u.VersionStatus = "available"
			u.LatestTag = c.tag
			u.VersionKind = c.v.kind(current)
			return
		}
		if code != "platform_unsupported" && code != "manifest_not_found" {
			u.VersionErrorCode = code
			return
		}
	}
	if complete && u.VersionErrorCode == "" {
		u.VersionStatus = "not_found"
	}
}

// Docker Hub's recent-first public catalog avoids spending the bounded search
// on hundreds of obsolete, lexically early tags. No registry token is sent to Hub.
func (s *registrySession) tagPage(ctx context.Context, path string, page int) ([]string, string, string) {
	if s.ref.host == "registry-1.docker.io" {
		ns, repo, ok := strings.Cut(s.ref.repo, "/")
		if !ok || strings.Contains(repo, "/") {
			return nil, "", "tag_search_incomplete"
		}
		raw := "https://hub.docker.com/v2/namespaces/" + ns + "/repositories/" + repo + "/tags?page_size=100&page=" + strconv.Itoa(page) + "&ordering=last_updated"
		b, _, status, code := s.request(ctx, raw, "")
		if code != "" {
			return nil, "", code
		}
		if status != 200 {
			return nil, "", registryHTTPCode(status)
		}
		var list struct {
			Results *[]struct {
				Name string `json:"name"`
			} `json:"results"`
			Next json.RawMessage `json:"next"`
		}
		if json.Unmarshal(b, &list) != nil || list.Results == nil || len(list.Next) == 0 || len(*list.Results) > 1000 {
			return nil, "", "manifest_invalid"
		}
		tags := make([]string, 0, len(*list.Results))
		for _, r := range *list.Results {
			if !imageTagPattern.MatchString(r.Name) {
				return nil, "", "manifest_invalid"
			}
			tags = append(tags, r.Name)
		}
		next := ""
		if string(list.Next) != "null" {
			var marker string
			if json.Unmarshal(list.Next, &marker) != nil || marker == "" {
				return tags, "", "manifest_invalid"
			}
			next = "hub-page-" + strconv.Itoa(page+1)
		}
		return tags, next, ""
	}
	b, h, code := s.get(ctx, path)
	if code != "" {
		return nil, "", code
	}
	var list struct {
		Tags json.RawMessage `json:"tags"`
	}
	var tags []string
	if json.Unmarshal(b, &list) != nil || len(list.Tags) == 0 || json.Unmarshal(list.Tags, &tags) != nil || len(tags) > 1000 {
		return nil, "", "manifest_invalid"
	}
	for _, tag := range tags {
		if !imageTagPattern.MatchString(tag) {
			return nil, "", "manifest_invalid"
		}
	}
	if h.Get("Link") == "" {
		return tags, "", ""
	}
	next, code := s.nextTagsPath(h.Get("Link"))
	return tags, next, code
}
func (s *registrySession) nextTagsPath(link string) (string, string) {
	// Do not follow arbitrary Link targets, including another repository or host.
	if !strings.HasSuffix(link, `; rel="next"`) || !strings.HasPrefix(link, "<") {
		return "", "tag_search_incomplete"
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(link, "<"), `>; rel="next"`)
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "" && u.Scheme != "https") || (u.Host != "" && u.Host != s.ref.host) || u.Path != "/v2/"+s.ref.repo+"/tags/list" {
		return "", "tag_search_incomplete"
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) > 2 || len(q["last"]) != 1 || !imageTagPattern.MatchString(q.Get("last")) {
		return "", "tag_search_incomplete"
	}
	for k := range q {
		if k != "n" && k != "last" {
			return "", "tag_search_incomplete"
		}
	}
	q.Set("n", "100")
	return u.Path + "?" + q.Encode(), ""
}
func checkImageUpdate(ctx context.Context, r imageReference, id string, p imagePlatform) protocol.DockerImageUpdate {
	s := registrySession{client: publicRegistryClient, ref: r}
	return s.check(ctx, id, p)
}
func (s *registrySession) check(ctx context.Context, id string, p imagePlatform) protocol.DockerImageUpdate {
	u := protocol.DockerImageUpdate{Status: "unknown", VersionStatus: "unknown"}
	if s.ref.pinned {
		// The pinned digest remains intentional. Only discover other version tags;
		// a moved mutable tag is not a replacement recommendation for this image.
		u.Status = "pinned"
		s.versions(ctx, p, &u)
		return u
	}
	digest, code := s.manifest(ctx, s.ref.tag, p)
	if code != "" {
		u.ErrorCode = code
	} else if digest == id {
		u.Status = "up_to_date"
	} else {
		u.Status = "update_available"
	}
	s.versions(ctx, p, &u)
	return u
}
