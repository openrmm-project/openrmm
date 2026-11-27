# Docker image update observations

The **Updates** column is available on the fleet Docker screen and each device's Docker tab. The **Update found** filter includes a newer image for the current tag, a newer numbered version, or both. It respects the same client/site visibility and pagination as the rest of the Docker inventory.

This feature only observes metadata. It does not pull images, change Compose files, install packages, recreate containers or restart services. No action or automatic alert is enabled by discovering an update.

## Two independent results

**Same tag** compares the immutable image actually used by the container to the current registry image for its configured tag. An image downloaded locally after the container started does not incorrectly make the running container appear current. For a multi-platform image, the comparison uses the manifest for the image's OS, architecture and variant, not the index digest or the host's architecture.

**Newer version found** is a candidate tag with a larger numeric version. The comparison preserves the `v` prefix, number of numeric components and exact supported flavor suffix. For example, `16` can find `17`; `1.2.3-alpine` can find `1.3.0-alpine`, but does not switch to a different distribution. Stable plain numeric versions and explicitly recognized Alpine, Debian and Ubuntu flavors are supported. Prereleases, dates, hashes and arbitrary suffixes are not interpreted as stable semantic versions. `latest` can be checked for a replacement image, but does not establish a numbered baseline.

A larger major number is labeled as requiring migration review. Neither a larger number nor an unchanged flavor establishes application compatibility. A candidate is not described as the universally latest version: bounded searches can inspect only part of a repository's tags. If no candidate is found in an incomplete search, the result remains unknown, rather than claiming no updates exist.

The two observations are independent. A successful same-tag comparison can coexist with an unavailable version search; a newer version candidate can coexist with an unavailable current tag.

## Supported images and network behavior

The initial implementation uses anonymous HTTPS requests to public Docker Hub, GitHub Container Registry (`ghcr.io`) and Quay (`quay.io`). Docker Hub version discovery uses its recent-first public tag catalog; the candidate is then verified against the registry manifest for the local image's platform. Other supported registries use their Registry V2 tag catalogs.

Images pinned to an immutable digest are identified as pinned and never treated as following a mutable tag. When their reference also includes a supported numeric tag, such as `postgres:16@sha256:...`, the newer-version search still runs and can report a candidate such as `17`; the pin is preserved. Digest-only references and pinned nonnumeric tags have no comparable numbered baseline. Local image IDs, locally built images without matching repository-digest provenance, private repositories requiring credentials, arbitrary custom registries and ambiguous platforms are explicitly unknown or unsupported. The agent does not read Docker login files or credential helpers. It never forwards operator credentials.

Only fixed supported registry and token endpoints are contacted. Redirects are rejected. In particular, a single-platform manifest whose configuration blob requires a redirect may remain unknown; the agent does not relax its destination restrictions to follow that URL. Responses, requests, tag pages, candidate checks and worker time are bounded. Registry errors are exposed as fixed codes, without raw response bodies, credentials or command output.

Registry checks happen in one background worker and do not delay minute-by-minute Docker inventory or heartbeats. Cached results are retained across agent reconnects and keyed to image identity/reference/platform; a changed image does not reuse the previous image's result. Successful results expire after six hours. Unknown, failed or partial searches retry after at least thirty minutes, even when a candidate was found in the partial search. The cache is bounded to 512 entries, and each sweep handles at most eight images with a 90-second total deadline. A complete first pass can therefore take several collection cycles on a busy host.

Set `RMM_DOCKER_UPDATE_CHECKS=false` in the agent service environment to disable registry checks while retaining ordinary Docker inventory. A service restart applies environment changes. Disabling `RMM_DOCKER_MONITORING` also disables these checks. No inbound port is needed.

## Freshness and compatibility

The result shows its check time. Offline devices, failed or stale Docker collection, registry checks older than 26 hours, and timestamps more than five minutes in the future are classified as unknown by update filters. Previously observed results can remain visible as old observations. Unsupported or pinned images with no check timestamp are not presented as an old successful check.

Old agents have no update report and remain unknown. The server and frontend must support the optional report before deploying update-capable agents, otherwise older server code may discard the additional fields. No new database migration is required: reports extend the existing per-container JSON inventory.

The existing endpoint gains `update=available`, `update=current` or `update=unknown`, for example:

```http
GET /api/docker?update=available&limit=100&offset=0
```

`current` means the same-tag image was confirmed current and the version search either completed without a candidate or does not support that tag format. The UI keeps the numbered-version limitation visible; it is not a guarantee that no newer application release exists.

## Validation

Deterministic tests cover platform manifests, authentication destination restrictions, malformed responses, request and output bounds, candidate version selection, partial searches, cancellation, caching and strict projection. PostgreSQL tests cover JSON persistence, server-side filters, freshness and existing access scopes. Frontend and Chrome smoke tests cover both views, filters, candidate and major-version messages, and unknown/stale results.

An opt-in public integration test makes anonymous metadata requests without accessing a Docker engine or changing containers:

```sh
RMM_TEST_PUBLIC_REGISTRY=1 go test ./cmd/agent -run '^TestDockerUpdatesPublicRegistry$' -count=1 -v
```

It checks a real `postgres:16` platform manifest, distinguishes its current digest from a synthetic older digest, and verifies a newer numbered candidate. It requires external network access and can fail when the registry is unavailable or rate-limits the test.

The implementation follows the [Registry V2 specification](https://distribution.github.io/distribution/spec/api/), Docker's [manifest API](https://docs.docker.com/reference/api/registry/latest/operations/GetImageManifest/) and [public tag catalog](https://docs.docker.com/reference/api/hub/latest/operations/ListRepositoryTags/).
