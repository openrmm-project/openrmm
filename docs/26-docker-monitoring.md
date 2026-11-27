# Docker inventory and container checks

The **Docker** screen shows containers across permitted devices. Each device also has a **Docker** tab. These views show the latest observations, not historical charts. They do not start, stop, restart, update, or otherwise control containers. The **Updates** column and filter distinguish a replacement image for the same tag from a newer numbered version.

## Requirements and limits

This first version observes a local, rootful Linux Docker engine at `/var/run/docker.sock`. The agent needs permission to read that socket and an executable Docker CLI at `/usr/bin/docker`, `/usr/local/bin/docker`, or `/bin/docker`. It does not install Docker or change socket permissions. `not_installed` / `cli_missing` means the CLI was not found in those supported locations; it does not prove that no Docker engine is installed. For example, a Snap-only CLI path is not discovered.

Remote engines, inherited Docker contexts, Docker TLS settings, rootless sockets, Windows and macOS engines are outside this version. The subprocess uses an empty temporary Docker configuration and a minimal environment, with an explicit local host. No additional inbound port is required.

Set `RMM_DOCKER_MONITORING=false` in the agent service environment to disable Docker collection and Docker checks. Restart the agent service for a changed environment to take effect. Disabled and non-Linux agents explicitly report `unsupported`. Agents from before this feature remain `unknown`.

## What is collected

A separate collector runs immediately after connection and then every 60 seconds. It does not block the normal heartbeat. Each collection has a 15-second deadline; reconnects cannot create overlapping inventory collections. Limits are 500 containers and 2 MiB per CLI output, below the server's existing 8 MiB WebSocket frame limit. Oversized, malformed or inaccessible observations are rejected as a whole.

For each container, the snapshot contains its full ID, name, image reference, state, health state, current CPU percentage, current memory usage and limit, restart count, start time, and the two Compose labels `com.docker.compose.project` and `com.docker.compose.service`. The CLI's inspect template projects only those metadata fields. Environment variables, commands, logs, mounts, health-check output and other labels are not collected or stored. Collection errors expose a fixed code, not raw CLI stderr.

Update-capable agents also report the immutable image ID and a bounded update-check result. The local image's OS, architecture, variant and repository digests are inspected internally to identify the correct platform and provenance; arbitrary image configuration is never reported.

CPU can exceed 100% on multiple cores. Linux memory usage follows [`docker stats`](https://docs.docker.com/reference/cli/docker/container/stats/), including its subtraction of inactive cache. Values retain the precision supplied by the CLI. Stopped containers and running containers without a returned sample show **Not sampled**, not a fabricated zero. A zero or absent memory limit is shown as unavailable. The restart count is the container's cumulative counter, not a restart rate or a permanent restart-loop alert.

The implementation uses [`docker inspect --type container --format`](https://docs.docker.com/reference/cli/docker/inspect/) and fixed argument arrays. Docker configuration precedence is described in the [CLI reference](https://docs.docker.com/reference/cli/docker/).

## Freshness and failures

The server records receipt time for the last attempt and the last successful snapshot. A failed attempt preserves the last successful inventory and marks it **Last observed**. Offline hosts and successful observations older than 90 seconds are also stale. A successful empty snapshot clears previous containers. A missing CLI, unavailable engine, unsupported platform, legacy agent and empty inventory are distinct states.

The global view filters and paginates on the server. Both endpoints require `devices.view` and apply the caller's client/site scope:

```
GET /api/devices/{id}/docker
GET /api/docker?q=web&state=running&health=healthy&limit=100&offset=0
```

The global endpoint accepts `agent_id` plus existing report scope filters. `limit` defaults to 100 and is capped at 200. It returns scoped host collection statuses, filtered total and container rows. Failed HTTP access hides previously displayed rows. Inventory is stored per agent in `docker_inventory` by migration 044, and deleted when the agent is deleted.

## Optional alerts

Image update detection is described in [Docker image updates](27-docker-image-updates.md). The container health checks below remain local and never contact a registry.

A user with `checks.manage` can create a **Docker container** check from the existing check editor. No checks are created automatically. Configuration uses an exact, case-sensitive container name:

```json
{"container":"web","mode":"running"}
```

`running` fails for missing, stopped, paused, dead or restarting containers. `healthy` also requires a real Docker health check in the `healthy` state; a running container without a health check fails that mode.

Each execution reads fresh projected state for only the named container, with a 10-second deadline and no resource statistics. It does not reuse the minute-old inventory sample as several consecutive observations. An unavailable engine, disabled collector or failed read yields **Unknown**, never a successful recovery. Existing failure/recovery thresholds, cooldowns, suppressions and notification preferences apply. An unknown result does not resolve an existing alert.

The server dispatches this check only after the current agent connection has reported a supported Docker snapshot. A replacement connection starts without that capability, so older agents do not receive an unknown check type. Windows and macOS targets are excluded. AI-proposed checks still require the existing review and approval flow before activation.

## Tests

The ordinary Go tests work without Docker installed. Protocol, command bounds, projection rendering, unit parsing, fresh check observations, connection capabilities, database retention, scoped APIs and failure/recovery behavior have dedicated tests. Frontend tests cover filtering, missing metrics, polling races, denied access and all nine locales. The Chrome smoke suite exercises fleet filters, errors, stale device inventory and creating a healthy-mode check.

An explicit opt-in Linux integration test uses the local Docker socket and a cached `alpine:latest` image:

```sh
OPENRMM_TEST_DOCKER=1 go test ./cmd/agent -run TestDockerLocalEngineIntegration -count=1 -v
```

It creates a uniquely named and labelled fixture with `--pull=never`, restricts its observations to that fixture, checks actual health and statistics, verifies that canary environment/label values do not enter the snapshot, stops its own container, and removes it on completion. It never controls existing containers. Run it only on a development Docker engine where creation of that temporary fixture is intended.
