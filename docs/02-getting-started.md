# 02 — Getting Started

## Prerequisites

- Go 1.23+ (to build) or Docker (to run via compose).
- PostgreSQL 14+.

## Run with Docker Compose

```bash
cp .env.example .env
# edit .env: set a strong RMM_ADMIN_TOKEN and your AI keys
docker compose up -d
curl localhost:8080/healthz      # -> ok
```

## Run locally

```bash
# start a Postgres (example)
docker run -d --name rmm-pg -e POSTGRES_USER=rmm -e POSTGRES_PASSWORD=rmm \
  -e POSTGRES_DB=rmm -p 5432:5432 postgres:16-alpine

export RMM_ADMIN_TOKEN=$(openssl rand -hex 32)
export RMM_DATABASE_URL="postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable"
export RMM_AI_API_KEY=sk-...        # or point RMM_AI_BASE_URL at Ollama/vLLM
make build
./bin/rmm-server
```

The schema is embedded and applied automatically on start (idempotent).

## Enroll an agent

```bash
# 1. Create an enrollment token (admin).
TOKEN=$(curl -s -X POST localhost:8080/api/enroll-tokens \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"lab","uses":10}' | jq -r .token)

# 2. Run the agent (first run consumes the enroll token and stores its identity).
./bin/rmm-agent -server ws://localhost:8080/ws/agent -enroll-token "$TOKEN"

# 3. Confirm it registered.
curl -s localhost:8080/api/devices -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq
```

Cross-compile the agent for endpoints with `make agent-all` (publish with
`make agent-dist`). Both **amd64 and arm64** are built for Windows and Linux,
plus macOS arm64 — the agent is CGO-free, so every target cross-compiles from any
host. The native remote sidecar (`make remote-dist`) likewise ships
Windows/Linux **amd64 + arm64** (macOS needs CGO — `remote-dist-darwin`, run on a
Mac). The `install.sh` / `install.ps1` scripts auto-detect the host architecture.

## Updating agents (self-update)

Agents **upgrade themselves**. Each connected agent periodically HEADs
`/dl/rmm-agent-<os>-<arch>` and compares the **ETag** with the build it last
applied (the same mechanism the remote sidecar uses). When a new binary is
published it downloads it, verifies the detached Ed25519 signature when
configured, validates it runs (`-print-version`), confirms it is not
byte-identical to the running one, atomically swaps it in, and reloads (re-exec
on Unix; a detached service restart on Windows).

So a fleet-wide upgrade is just **republish to `/dl`**:

```bash
make agent-dist        # rebuild + publish rmm-agent-<os>-<arch> into deploy/public (served at /dl)
# connected agents pick it up within RMM_UPDATE_INTERVAL (default 30m)
```

- Controls (agent env): `RMM_AUTO_UPDATE=0` disables it; `RMM_UPDATE_INTERVAL`
  sets the cadence (e.g. `15m`; minimum 5m).
- Signature controls: `RMM_UPDATE_VERIFY=warn|strict|off` and
  `RMM_UPDATE_PUBLIC_KEY=<base64-ed25519-public-key>`. See
  [13-security-rollout](13-security-rollout.md).
- **Safety:** a download that fails to execute or returns no version is discarded
  and the running agent is left untouched; the previous binary is kept as
  `*.old` until the next clean start.
- **Bootstrap:** self-update only works once an agent is already running a build
  that contains it. The first rollout onto self-updating agents is a normal
  (re)install via `install.sh` / `install.ps1` (served at `/install.sh`).

## Run a job (with approval)

```bash
AID=<agent id from /api/devices>

# Propose (lands in awaiting_approval)
JID=$(curl -s -X POST localhost:8080/api/jobs \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"agent_id\":\"$AID\",\"shell\":\"bash\",\"script\":\"echo hi && uname -a\",\"description\":\"smoke\"}" \
  | jq -r .id)

# Approve -> dispatched -> executed
curl -s -X POST localhost:8080/api/jobs/$JID/approve -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq

# Read the result
curl -s localhost:8080/api/jobs/$JID -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq '{status, exit_code, stdout}'
```

## Use the AI chat

```bash
SID=$(curl -s -X POST localhost:8080/api/chat/sessions \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -d '{"title":"ops"}' | jq -r .id)

curl -s -X POST localhost:8080/api/chat/sessions/$SID/messages \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"content":"List my Linux devices and propose a script to install htop on the first one."}' | jq
```

The reply includes `created_job_ids` for any jobs the AI proposed — surface those
in the UI for approval. (Requires a working AI provider; set `RMM_AI_API_KEY` or
point `RMM_AI_BASE_URL` at a local model.)
