package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: anonymous read-only requests to public registries. Never
// contacts Docker, reads credentials, pulls images or modifies a container.
func TestDockerUpdatesPublicRegistry(t *testing.T) {
	if os.Getenv("RMM_TEST_PUBLIC_REGISTRY") != "1" {
		t.Skip("set RMM_TEST_PUBLIC_REGISTRY=1 for anonymous registry checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ref, code := parseImageReference("postgres:16")
	if code != "" {
		t.Fatal(code)
	}
	platform := imagePlatform{OS: "linux", Architecture: "amd64"}
	session := registrySession{client: publicRegistryClient, ref: ref}
	digest, code := session.manifest(ctx, ref.tag, platform)
	if code != "" {
		t.Fatalf("public platform manifest: %s", code)
	}
	if !imageDigestPattern.MatchString(digest) {
		t.Fatal("invalid public digest")
	}
	current := checkImageUpdate(ctx, ref, digest, platform)
	if current.Status != "up_to_date" {
		t.Fatalf("current image: %+v", current)
	}
	if current.VersionStatus != "available" || current.VersionKind != "major" || current.LatestTag == "" {
		t.Fatalf("newer numbered version: %+v", current)
	}
	previous := checkImageUpdate(ctx, ref, "sha256:"+strings.Repeat("0", 64), platform)
	if previous.Status != "update_available" {
		t.Fatalf("changed image: %+v", previous)
	}
	t.Logf("Public postgres:16 linux/amd64: current digest recognized; synthetic older digest distinguished; newer candidate %s (%s), partial search=%t", current.LatestTag, current.VersionKind, current.VersionErrorCode != "")
}
