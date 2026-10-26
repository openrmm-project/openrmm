# 02 — Getting Started

## Prerequisites

- Go 1.26 (to build; `go.mod` pins the toolchain, currently go1.26.10) or Docker
  (to run via compose).
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

Immutable migrations (`internal/store/migrations`, `001_baseline` through
`019_oidc_browser_handoff` today) are embedded and applied before the listener
starts; recorded names and checksums are validated on every start.
`db/schema.sql` is a current snapshot, not the migration runner.

In production the serving role should not own the schema — the audit trail's
append-only guarantee depends on it being unable to delete from its own table.
Run the migrations first with the owner's credentials, then start the server
with the restricted ones:

```bash
RMM_DATABASE_URL=<owner dsn> ./bin/rmm-server -migrate-only
```

A server whose role cannot change the schema stops with that instruction instead
of a bare permission error. With Compose, `make migrate` runs the same thing in a
one-shot `server` container, using the owner DSN in `RMM_MIGRATE_DATABASE_URL`
(see `.env.example`).
The remaining direct-server examples use port `8080`; replace it with `3005` when
using the bundled Docker Compose frontend/proxy.

## Create the first console user

The console signs in with a username and password (plus a second factor once you
enable one) or single sign-on — never with `RMM_ADMIN_TOKEN`. Create the first
administrator with that token; the call works only while no user exists:

```bash
curl -s -X POST localhost:8080/api/auth/bootstrap \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"<at least 12 characters>"}' | jq '{role, user: .user.username}'
```

Then open the console (`http://localhost:3005` with Compose), sign in, and turn on
two-factor authentication in Settings ▸ Users. Further users are created there,
or through `POST /api/users`. The admin token stays valid for the API and
automation, as in the examples below; keep it out of day-to-day use.

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

The served installers default to production mode and require HTTPS, an
enrollment token, and the release's Ed25519 public update key. `install.sh`
uses only the system's tools, and checks the Ed25519 signatures when the
system's OpenSSL is 3. Otherwise, on every Mac (LibreSSL) and on Ubuntu 20.04
and RHEL 8 (OpenSSL 1.1), it checks the manifest's ECDSA P-256 signature
instead, with the key in `--manifest-public-key`. The console adds that flag
when the server has `RMM_MANIFEST_PUBLIC_KEY` set, so a server that enrols Macs
needs it. For example:

```bash
curl -fsSL https://agents.rmm.example.com/install.sh | sudo bash -s -- \
  --server https://agents.rmm.example.com --token "$TOKEN" \
  --update-public-key '<BASE64_ED25519_PUBLIC_KEY>' \
  --manifest-public-key '<BASE64_P256_PUBLIC_KEY>' --require-tls
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

**Windows — Microsoft Defender.** Add `-DefenderExclusion` to exclude
`C:\ProgramData\rmm-agent` from Defender at install time. It is **opt-in**: an
antivirus exclusion is a security decision an operator makes, not something an
installer does behind their back. Without it, remote-desktop sessions on some
hosts fail with a black screen, because Defender quarantines the sidecar the
agent downloads — see
[08-remote-access](08-remote-access.md#when-no-screen-arrives-at-all) for why,
and [`scripts/windows-defender-exclusion.ps1`](../scripts/windows-defender-exclusion.ps1)
to apply or revert it on endpoints that are already installed.

**Windows — Programs and Features.** The **agent** registers itself under
`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\OpenRMMAgent` at every
service start, so it appears as *OpenRMM Agent* there, in Settings ▸ Apps and to
inventory tools. Registering a Windows service alone leaves no such entry, and
software nobody can see on a managed endpoint is a support and audit problem. It
is not a tamper control either way: a local administrator can stop the service
regardless, so omitting the entry buys friction rather than protection.

The service does it rather than the installer because **agents update themselves
and installers do not re-run**: an endpoint that self-updated past the release
introducing the entry would never get one, and where one existed its version
froze at whatever first wrote it. The `uninstall.ps1` its `UninstallString`
points at is embedded in the agent and written out when it differs — it stops
and deletes the service, removes the binaries and enrolment state, drops the
Defender exclusion if one was added, and removes the registry entry itself.
`-action uninstall` on its own only removes the service.

**Where the agent is installed, and why the directory matters.** Linux keeps
`/usr/local/bin/rmm-agent`; **macOS uses `/usr/local/libexec/openrmm/`**, because
Homebrew hands `/usr/local` to the logged-in administrator, and a service running
as root must not execute from a directory another account can write to — a
signature is checked at download and says nothing about a swap afterwards. The
installer takes ownership of its directory, refuses to run if the target is not
root-owned or is group/world-writable, and moves an endpoint installed at the old
macOS path over on the next run. On Windows the same reasoning applies to
`C:\ProgramData\rmm-agent`: a standard user may create folders under
`C:\ProgramData` and owns what they create, so the installer breaks ACL
inheritance and rewrites the entry list (SYSTEM and Administrators full control,
Users read and execute) on every run, which is also what repairs an endpoint
installed by an older installer.

## Removing an agent

Run [`uninstall.sh`](../deploy/public/uninstall.sh) (Linux/macOS, served at
`/uninstall.sh`) or `C:\ProgramData\rmm-agent\uninstall.ps1` (Windows). Both stop
and remove the service, the binaries and `/etc/rmm-agent` (Windows:
`C:\ProgramData\rmm-agent`) — which holds credentials that still authenticate to
the server — and on macOS also the remote-desktop bundle, its LaunchAgent and the
privacy grants recorded for it. `--keep-state` keeps the enrolment so a reinstall
returns as the same device.

```sh
curl -fsSL https://agents.rmm.example.com/uninstall.sh | sudo bash
```

**Removal is local only.** It revokes nothing: delete the device in the console
so its credentials stop being accepted.

## Updating agents (self-update)

Agents **upgrade themselves**. Each connected agent periodically HEADs
`/dl/rmm-agent-<os>-<arch>` and compares the **ETag** with the build it last
applied (the same mechanism the remote sidecar uses). When a new binary is
published it downloads it, verifies the detached Ed25519 signature when
configured, validates it runs (`-print-version`), confirms it is not
byte-identical to the running one, atomically swaps it in, and reloads (re-exec
on Unix; a detached service restart on Windows).

### Serving `/dl` on Kubernetes

Compose bind-mounts `deploy/public` into the web container, which is why `/dl`
works out of the box there. A Kubernetes install has no such directory, and the
web image carries none: the route answers and has nothing to serve, so no agent
can install or update.

The `artifacts` build target packages a published `deploy/public` into an image
that copies itself into a volume:

```bash
docker build --target artifacts -t openrmm-artifacts:v0.8.0 .
```

Run it as an initContainer writing to a volume the web container mounts at
`/usr/share/nginx/dl`, read-only — nothing that serves HTTP should be able to
rewrite what endpoints are about to install. Shared storage is not needed, and
the **image tag becomes the release identity**, so promoting one installation is
a single value rather than a file sync nobody can reconstruct afterwards.

The image is a transport, not a trust boundary: every artifact carries its
Ed25519 signature and the agent verifies it against the key pinned at install
time, so a compromised registry still cannot deliver an accepted update.

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

In CI, the *Database migrations* workflow runs them against PostgreSQL 16 for the
packages that have DB-backed tests (store, settings, api, notify, ai, mcpserver,
patches, scheduler), on pull requests and pushes that touch those paths. The
main CI job runs without a database, so they skip there. Run them locally before
trusting a change to anything security-relevant.

Each DB-backed test gets a **schema of its own** (`storetest.NewSchema`), created
and dropped around the test, so packages running in parallel cannot see each
other's fixtures. Add new DB tests that way rather than deleting or truncating
shared tables: a truncate reaches into whatever another package is doing at the
time, which shows up as a vanished fixture or a deadlock rather than as an
obvious mistake.
