package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDockerUpdateReportValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := DockerImageUpdate{Status: "up_to_date", CheckedAt: &now, VersionStatus: "available", LatestTag: "17-alpine", VersionKind: "major"}
	for _, tc := range []struct {
		name   string
		change func(*DockerImageUpdate)
	}{
		{"status", func(u *DockerImageUpdate) { u.Status = "installed" }},
		{"version status", func(u *DockerImageUpdate) { u.VersionStatus = "newest" }},
		{"missing timestamp", func(u *DockerImageUpdate) { u.CheckedAt = nil }},
		{"tag injection", func(u *DockerImageUpdate) { u.LatestTag = "17\nsecret" }},
		{"tag length", func(u *DockerImageUpdate) { u.LatestTag = strings.Repeat("a", 129) }},
		{"raw error", func(u *DockerImageUpdate) { u.VersionErrorCode = "password=secret" }},
		{"kind", func(u *DockerImageUpdate) { u.VersionKind = "compatible" }},
		{"candidate without result", func(u *DockerImageUpdate) { u.VersionStatus = "unknown" }},
		{"success with error", func(u *DockerImageUpdate) { u.ErrorCode = "registry_unavailable" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := valid
			tc.change(&u)
			if u.Validate() == nil {
				t.Fatal("accepted invalid report")
			}
		})
	}
	for _, u := range []*DockerImageUpdate{nil, &valid, {Status: "unknown", VersionStatus: "unknown", ErrorCode: "pending"}, {Status: "pinned", VersionStatus: "unsupported"}, {Status: "unknown", CheckedAt: &now, ErrorCode: "registry_rate_limited", VersionStatus: "available", LatestTag: "2.0", VersionKind: "major", VersionErrorCode: "tag_search_incomplete"}} {
		if err := u.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDockerUpdateRoundTripAndLegacyCompatibility(t *testing.T) {
	now := time.Now().UTC()
	c := DockerContainer{ID: strings.Repeat("a", 64), Name: "db", Image: "postgres:16", ImageID: "sha256:" + strings.Repeat("b", 64), State: "running", Health: "none", Update: &DockerImageUpdate{Status: "update_available", CheckedAt: &now, VersionStatus: "available", LatestTag: "17", VersionKind: "major"}}
	raw, err := json.Marshal(DockerSnapshot{Status: DockerAvailable, Containers: []DockerContainer{c}})
	if err != nil {
		t.Fatal(err)
	}
	var got DockerSnapshot
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err = got.Validate(); err != nil {
		t.Fatal(err)
	}
	if got.Containers[0].Update.LatestTag != "17" || got.Containers[0].ImageID != c.ImageID {
		t.Fatal("report lost metadata")
	}
	got.Containers[0].ImageID = "sha256:invalid"
	if got.Validate() == nil {
		t.Fatal("invalid image identity accepted")
	}
	got.Containers[0].ImageID = ""
	got.Containers[0].Update = nil
	if err = got.Validate(); err != nil {
		t.Fatalf("legacy report rejected: %v", err)
	}
}
