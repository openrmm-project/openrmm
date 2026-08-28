# 02 — Getting Started

## Prerequisites

- Go 1.25+ (to build) or Docker (to run via compose).
- PostgreSQL 14+.
- Node/npm only when building the console outside Docker.

## Run with Docker Compose

```bash
cp .env.example .env
# replace RMM_ADMIN_TOKEN with a generated value; AI keys are optional
docker compose up -d
curl localhost:3005/healthz      # -> ok
```

## Run locally

```bash
# start a Postgres (example)
docker run -d --name rmm-pg -e POSTGRES_USER=rmm -e POSTGRES_PASSWORD=rmm \
  -e POSTGRES_DB=rmm -p 5432:5432 postgres:16-alpine

export RMM_ADMIN_TOKEN=$(openssl rand -hex 32)
export RMM_SETTINGS_ENCRYPTION_KEY=$(openssl rand -hex 32)
export RMM_DATABASE_URL="postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable"
export RMM_AI_API_KEY=sk-...        # or point RMM_AI_BASE_URL at Ollama/vLLM
make build
./bin/rmm-server
```

Immutable migrations are embedded and applied automatically before the listener
starts. Current versions are v1 `baseline` and v2 `pending_totp_secret`; recorded
names/checksums are validated on every startup. `db/schema.sql` is a current
snapshot, not the migration runner.
The remaining direct-server examples use port `8080`; replace it with `3005` when
using the bundled Docker Compose frontend/proxy.

## Enroll an agent

```bash
# 1. Create an enrollment token (admin).
TOKEN=$(curl -s -X POST localhost:8080/api/enroll-tokens \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"lab","uses":1}' | jq -r .token)

# 2. Enroll once (stores the identity), then run the long-lived agent without
# retaining the bootstrap token in its process/service arguments.
./bin/rmm-agent -server ws://localhost:8080/ws/agent -enroll-token "$TOKEN" -enroll-only
./bin/rmm-agent -server ws://localhost:8080/ws/agent

# 3. Confirm it registered.
curl -s localhost:8080/api/devices -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq
```

Cross-compile the agent for endpoints with `make agent-all` (publish with
`make agent-dist`). Both **amd64 and arm64** are built for Windows, Linux, and
macOS — the agent is CGO-free, so every target cross-compiles from any
host. The native remote sidecar (`make remote-dist`) likewise ships
Windows/Linux **amd64 + arm64** (macOS needs CGO — `remote-dist-darwin`, run on a
Mac). The `install.sh` / `install.ps1` scripts auto-detect the host architecture.
After a device identity is revoked, create a new enrollment token and use the
installer's explicit identity-reset option (`--reset-identity` on Linux/macOS or
`-ResetIdentity` on Windows); normal reinstalls preserve the existing identity.

### Production installer requirements

The served installers default to production mode and require HTTPS, OpenSSL 3,
an enrollment token, and the release's Ed25519 public update key. For example:

```bash
curl -fsSL https://agents.rmm.example.com/install.sh | sudo bash -s -- \
  --server https://agents.rmm.example.com --token "$TOKEN" \
  --update-public-key '<BASE64_ED25519_PUBLIC_KEY>' --require-tls
```

```powershell
& ([scriptblock]::Create((iwr -UseBasicParsing https://agents.rmm.example.com/install.ps1))) `
  -Server https://agents.rmm.example.com -Token '<ENROLL_TOKEN>' `
  -UpdatePublicKey '<BASE64_ED25519_PUBLIC_KEY>' -RequireTls
```

Plain HTTP or no key requires explicit `--lab-mode`/`-LabMode` and is only for an
isolated lab. The installer validates the signed checksum manifest and selected
artifact, rejects downgrades, and rolls back if the installed service does not
become healthy. See [13-security-rollout](13-security-rollout.md).

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

## Running the tests

```bash
make test          # unit tests only
cd openrmm && npm test
```

That is **not the whole suite**. Most of what verifies scope isolation, the
approval gates, migrations and the alert lifecycle is DB-backed, and those tests
`t.Skip` when there is no database. A green run without one proves much less than
it looks like it does:

```bash
# any Postgres 16 you are willing to have written to
export RMM_TEST_DATABASE_URL='postgres://rmm:rmm@localhost:5432/rmm_test?sslmode=disable'
make test
```

CI sets no `RMM_TEST_DATABASE_URL`, so the integration tests skip there. Run them
locally before trusting a change to anything security-relevant.

Each DB-backed test gets a **schema of its own** (`storetest.NewSchema`), created
and dropped around the test, so packages running in parallel cannot see each
other's fixtures. Add new DB tests that way rather than deleting or truncating
shared tables: a truncate reaches into whatever another package is doing at the
time, which shows up as a vanished fixture or a deadlock rather than as an
obvious mistake.
