# Production deployment

This runbook describes the implemented single-server Compose deployment. It uses
three path-restricted nginx listeners, PostgreSQL on an internal network,
file-backed secrets, a prebuilt frontend bundle, and an external trusted TLS
edge.

To run the published release images instead of building them, with Caddy as an
optional TLS edge, see [QUICKSTART.md](QUICKSTART.md). That setup is the same
deployment: everything below still applies.

## Supported topology

```text
Internet / private clients
          |
          v
trusted TLS edge: certificates + source ACLs
          |
          +-- agents hostname  -> 127.0.0.1:8443
          +-- console hostname -> 127.0.0.1:9443
          +-- MCP hostname     -> 127.0.0.1:7443
                                      |
                           nginx container listeners
                           8080 / 8081 / 8082
                                      |
                    server:8080 <-> postgres:5432
```

The published host listeners bind to loopback by default. This prevents direct
bypass of the TLS edge; it does **not** create an internal UFW policy. Source-IP
ACLs belong on the trusted TLS edge, before it forwards to these listeners. UFW
rules against loopback-only `8443`/`9443`/`7443` do not implement the intended
client ACL and are not the documented control.

The three nginx doors expose only:

| Door | Host default | Container | Routes |
|---|---:|---:|---|
| agents | `127.0.0.1:8443` | `8080` | `/ws/*`, `/dl/*`, installers, `/healthz` |
| console | `127.0.0.1:9443` | `8081` | built SPA/base path, `/api/*`, `/ws/remote` |
| MCP | `127.0.0.1:7443` | `8082` | `/mcp`, `/.well-known/*`, `/oauth/*`, `/healthz` |
| app | not published | `8080` | API, WebSockets, scheduler, MCP/OAuth |
| PostgreSQL | not published | `5432` | database only |

Only **one `server` replica is supported**. Agent connections, remote relay
pairs, and the scheduler are process-local; there is no shared hub, relay bus,
or scheduler leader election. The migration advisory lock makes simultaneous
startup safe, but does not make the application highly available. Do not scale
`server` horizontally and do not describe this topology as HA or a distributed
hub.

## 1. Prerequisites

- Linux host with Docker Engine and Docker Compose v2.
- A trusted TLS edge (Caddy, Traefik, nginx, a tunnel, or a load balancer) on the
  same host by default.
- Three HTTPS hostnames, or equivalent edge routing, for agents, console, and MCP.
- `openssl` for secret provisioning; `age` and `rclone` when those backup options
  are enabled.
- Enough local storage for PostgreSQL and an independent backup destination.

## 2. Configuration and secret files

From the repository root:

```bash
cp deploy/.env.example deploy/.env
deploy/ops/provision-secrets.sh
```

`provision-secrets.sh` creates `deploy/secrets/` mode `0700` and, without
overwriting existing values, creates these random 32-byte hex secrets as files
mode `0444`:

- `postgres_password`
- `admin_token`
- `settings_encryption_key`

Compose mounts them under `/run/secrets` and sets the corresponding `*_FILE`
variables. Do not put `$(openssl ...)` in `deploy/.env`: Compose env files do not
execute shell substitutions. Do not commit `deploy/.env`, `deploy/secrets/`, age
identities, signing keys, or backup credentials.

`RMM_SETTINGS_ENCRYPTION_KEY` protects encrypted values in `app_settings`, such
as AI/MeshCentral credentials and TOTP secrets. Production sets
`RMM_REQUIRE_SETTINGS_ENCRYPTION_KEY=true` and reads the dedicated
`RMM_SETTINGS_ENCRYPTION_KEY_FILE`; the key must contain at least 32 characters
and must remain stable across upgrades and restores. Losing or replacing it
makes existing encrypted settings unreadable. The development configuration can
derive a key from `RMM_ADMIN_TOKEN`, but production must not rely on that fallback.

Edit only non-secret values and secret-file paths in `deploy/.env`:

```dotenv
RMM_PUBLIC_URL=https://agents.rmm.example.com
RMM_MCP_PUBLIC_URL=https://mcp.rmm.example.com
RMM_MCP_OAUTH_ENABLED=false
OPENRMM_BASE_PATH=/

AGENTS_BIND_ADDRESS=127.0.0.1
AGENTS_PORT=8443
CONSOLE_BIND_ADDRESS=127.0.0.1
CONSOLE_PORT=9443
MCP_BIND_ADDRESS=127.0.0.1
MCP_PORT=7443

RMM_TRUST_PROXY_HEADERS=true
RMM_ENFORCE_CONSOLE_RELAY_INGRESS=true
RMM_MCP_ALLOWED_IPS=
RMM_ALLOW_LEGACY_AGENT_RELAY=false
RMM_ALLOW_UNAPPROVED_JOBS=false
```

`OPENRMM_BASE_PATH` is a URL path with leading and trailing slash, for example
`/console/`. It cannot contain an origin, query, fragment, `.` or `..` segment.
The frontend is built for this path, nginx serves it there, and nginx generates
`${OPENRMM_BASE_PATH}config.js` at runtime with
`window.__API_BASE__ = location.origin`.

`RMM_PUBLIC_URL` is the HTTPS agents origin. `RMM_MCP_PUBLIC_URL` is the HTTPS MCP
origin and is mandatory when OAuth is enabled. Neither is the raw app address.

The optional AI secret is also file-backed. Set `RMM_AI_API_KEY_FILE` to a
protected file path; when empty, Compose mounts `/dev/null` and the server starts
without an AI key.

Validate and start:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml config --quiet
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml up -d --build --wait
```

## 3. Trusted TLS edge and ACLs

Terminate TLS at the trusted edge and forward each hostname to its loopback
listener. Apply source ACLs there:

- Agents door: normally reachable by all managed endpoints.
- Console door: office/VPN/operator source ranges only, unless public exposure is
  an explicit risk decision.
- MCP door: known client ranges when possible; cloud connectors have dynamic
  egress and normally require public reachability plus OAuth, least privilege,
  rate limiting, and the approval guardrail.

The edge must preserve WebSocket upgrades, set `X-Forwarded-For` to the client
it accepted (replacing or appending to an untrusted client-supplied value), and
forward the original host and HTTPS scheme. The bundled nginx takes the client
address from `X-Forwarded-For` only when the connection comes from the edge
network (`EDGE_SUBNET`, which the published ports arrive from) or from
`OPENRMM_TRUSTED_PROXY`, and forwards that single address to the server.
Without this every client would share the edge's address, and the per-address
login limit would lock everyone out at once. Enable `RMM_TRUST_PROXY_HEADERS`
only for this trusted chain.

`EDGE_SUBNET` pins the Docker network's range; change it if it collides with a
route on the host. Changing it, or upgrading from a release that did not pin
it, recreates that network: run `docker compose ... down` before `up -d`.

If the TLS edge runs on another machine, changing a bind address from loopback is
an explicit topology change. Bind only to the private interface used by that
edge, set `OPENRMM_TRUSTED_PROXY` to that edge's address, enforce source/network
ACLs there, and prevent direct public access. Never
publish the app `8080` or PostgreSQL `5432`.

The nginx security policy is enforcing, not report-only. The console CSP permits
scripts/styles/fonts only from the bundled origin (with inline style attributes
for current React styling), blocks objects and framing, and contains no
`unsafe-eval`. HSTS is emitted because this config assumes that clients reached
nginx through HTTPS at the trusted edge.

## 4. Agent artifacts and installation

Production installers require all of the following:

- an `https://` server URL;
- OpenSSL **3.x** on the endpoint for Ed25519 verification;
- the release's base64 Ed25519 update public key;
- an enrollment token;
- administrator/root privileges.

They verify `SHA256SUMS`, its detached signature, the selected artifact checksum,
and the artifact signature. They install transactionally, reject downgrades, wait
for an authenticated healthy connection, and restore the prior binary/service/
identity on failure. The installers themselves must be obtained from a trusted
release or verified before piping them to a shell.

Linux/macOS:

```bash
curl -fsSL https://agents.rmm.example.com/install.sh | sudo bash -s -- \
  --server https://agents.rmm.example.com \
  --token '<ENROLL_TOKEN>' \
  --update-public-key '<BASE64_ED25519_PUBLIC_KEY>' \
  --require-tls
```

Windows, from elevated PowerShell:

```powershell
& ([scriptblock]::Create((iwr -UseBasicParsing https://agents.rmm.example.com/install.ps1))) `
  -Server https://agents.rmm.example.com `
  -Token '<ENROLL_TOKEN>' `
  -UpdatePublicKey '<BASE64_ED25519_PUBLIC_KEY>' `
  -RequireTls
```

Normal reinstall preserves device identity. After revocation, use a fresh
enrollment token and add `--reset-identity` or `-ResetIdentity` intentionally.

Plain HTTP or an omitted update key is accepted only with explicit `--lab-mode`
or `-LabMode`. Lab mode is for isolated disposable environments, sets update
verification to `off` when no key is supplied, and must not be used as a
production workaround.

## 5. Release inputs and verification

The release workflow runs only for semantic `v*` tags whose commit is on `main`.
The tag becomes the agent `VERSION`; release builds require a matching
`RMM_SIGNING_KEY`/`RMM_UPDATE_PUBLIC_KEY` pair. It builds agents, Windows/Linux
sidecars, and macOS sidecars on native runners, then publishes:

- binaries and installers;
- `SHA256SUMS`;
- detached Ed25519 signatures for binaries and `SHA256SUMS`;
- `SBOM.spdx.json` generated by Syft;
- GitHub build-provenance attestations.

For a local signed artifact set, pass the version explicitly:

```bash
RMM_SIGNING_KEY='<PRIVATE_KEY>' \
RMM_UPDATE_PUBLIC_KEY='<PUBLIC_KEY>' \
make VERSION=v0.4.5 dist-signed
```

`make dist-signed` does not generate an SBOM or GitHub attestation; those are CI
release steps. The current workflow also does **not** Authenticode-sign Windows
executables and does **not** Apple code-sign/notarize/staple macOS artifacts.
Ed25519 update signatures protect OpenRMM's download path, but do not remove
SmartScreen, Gatekeeper, TCC, or enterprise application-control warnings.

Container images are published to GHCR by separate workflows:
`openrmm-server` and `openrmm-frontend` (`images.yml`: the version from the tag,
the minor line, `latest` for releases, `main` for every push to main) and
`openrmm-artifacts` (`release.yml`: the signed `/dl` set, for Kubernetes; see
[`docs/02-getting-started.md`](../docs/02-getting-started.md#serving-dl-on-kubernetes)).
This compose file builds the server and frontend from source instead. The
server image carries its version, shown in Settings ▸ Configuration and printed
by `rmm-server -version`: the workflow passes the tag's version as the
Dockerfile's `VERSION` build arg, and a build without it reports `dev` — pass
`--build-arg VERSION=0.6.2` (or set it under `build.args` in the compose file)
when building one yourself.

## 6. Database migrations

Server startup applies immutable, embedded, forward-only migrations in one
transaction under a PostgreSQL advisory lock — `internal/store/migrations`,
from `001_baseline` to `019_oidc_browser_handoff` today.

`rmm-server -migrate-only` applies them and exits. Use it to keep the schema
owner's credential out of the serving process. This compose file's database URL
is the owner (`POSTGRES_USER`, password from the `postgres_password` secret), so
a one-shot run applies them:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml run --rm --no-deps \
  server -migrate-only
```

The server can then serve as the `openrmm_app` role that migration 012 creates,
which cannot update or delete the audit trail: give that role a login and point
the `server` service's `RMM_DATABASE_URL` and password secret at it, keeping the
owner for the one-shot run. This compose file serves as the owner by default; the server says so
at every start. See
[`docs/07-guardrails-security.md`](../docs/07-guardrails-security.md) for setting
up the restricted role.

`schema_migrations` records the contiguous version, immutable name, and SHA-256
checksum. A database history must be an exact prefix of the migrations embedded
in the binary. Startup rejects gaps, unknown future versions, name changes, and
checksum changes. The short-lived pre-checksum v1/v2 format is adopted once and
backfilled; a compatible legacy pre-migration schema can be adopted, while a
partial/incompatible schema is rejected.

Never edit an applied migration. Add the next numbered migration and keep
`db/schema.sql` as the current canonical snapshot. A binary rollback after a new
migration is not generally compatible: the older binary rejects an unknown
database migration. Roll back application **and database together** by restoring
the verified pre-upgrade backup.

## 7. Backup, verification, and recovery

Configure `BACKUP_*`, `VERIFY_FULL_RESTORE`, and `RESTORE_*` in `deploy/.env`.
Encryption is required by default:

```dotenv
BACKUP_DIR=/var/backups/openrmm
BACKUP_REQUIRE_ENCRYPTION=true
BACKUP_AGE_RECIPIENT=age1...
BACKUP_AGE_IDENTITY_FILE=/root/.config/age/openrmm.txt
BACKUP_RCLONE_DEST=remote:openrmm
BACKUP_OFFSITE_REQUIRED=true
VERIFY_FULL_RESTORE=true
```

Create a custom-format PostgreSQL backup, validate its archive, encrypt it with
age, write a SHA-256 sidecar, and copy both files with rclone when configured:

```bash
deploy/ops/backup.sh
```

`BACKUP_OFFSITE_REQUIRED=true` fails early unless an rclone destination is set;
it does not independently prove retention, immutability, or later readability.
Monitor the command and the remote destination.

Verify checksum, age decryption, archive structure, and by default a complete
restore into a temporary database:

```bash
deploy/ops/verify-backup.sh /var/backups/openrmm/rmm-TIMESTAMP.dump.age
```

Restore is destructive. It verifies the archive, stops `frontend` and `server`,
creates and verifies a quiescent safety backup, replaces the database, and waits
for writer health. If replacement/startup fails, it attempts automatic database
recovery from the safety backup and records events in `restore.log`:

```bash
RESTORE_CONFIRM=rmm deploy/ops/restore.sh /var/backups/openrmm/rmm-TIMESTAMP.dump.age
```

Automatic rollback is a recovery attempt, not a substitute for an operator. If
both restore and rollback fail, writers remain stopped; preserve the logs and
temporary evidence, repair PostgreSQL/storage, and restore the last separately
verified offsite backup. Back up `deploy/secrets/settings_encryption_key`
separately through a protected secret-management path; the database dump alone
cannot decrypt runtime secrets.

### 7.1 Point-in-time recovery (PITR)

The nightly dump alone means losing up to a day. Continuous WAL archiving cuts
that to `POSTGRES_ARCHIVE_TIMEOUT` (default 300s) — `archive_timeout` matters
because an idle database can otherwise sit on an unarchived segment for hours,
making the real recovery point far worse than the configured one.

The segments land in the `rmm_pgwal` volume. Before postgres starts, the
one-shot `postgres-wal-init` service hands that volume to postgres's uid; a
volume Docker creates belongs to root, and postgres could not archive into it.
Check that segments are being archived and none has failed (with your
`POSTGRES_USER` and `POSTGRES_DB` if they are not `rmm`):

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml exec -T postgres \
  psql -U rmm -d rmm -c 'SELECT archived_count, failed_count, last_failed_wal FROM pg_stat_archiver'
```

PITR needs a **physical** base backup; the logical dump cannot replay WAL:

```bash
deploy/ops/basebackup.sh     # weekly is usually enough
deploy/ops/ship-wal.sh       # hourly: copies WAL offsite and prunes
```

`ship-wal.sh` prunes against the **oldest** retained base backup, never the
newest — pruning to the newest would silently destroy the ability to recover to
any point before it. Recover with:

```bash
RESTORE_CONFIRM=rmm PITR_TARGET_TIME='2026-07-27 03:20:00+00' \
  deploy/ops/restore-pitr.sh /var/backups/openrmm/base/base-TIMESTAMP.tar.gz.age
```

Omit `PITR_TARGET_TIME` to replay as far forward as the archive allows. The
script replaces the data volume but never the WAL volume, and holds the
application down until the cluster leaves recovery.

### 7.2 Recovery drill (the RTO number)

`verify-backup.sh` proves an archive is readable; the drill proves you can
rebuild from it and times the work:

```bash
deploy/ops/drill-restore.sh          # isolated, never touches production
```

It appends `elapsed_seconds` to `drill.log`. That covers database recovery only
— full RTO also includes host provisioning, secret restore and stack startup, so
time those by hand once and record the total in `docs/19`.

### 7.3 Warm standby (optional)

A second PostgreSQL streaming from the primary turns recovery from "restore a
base backup" into "promote", at the cost of one more container:

```bash
openssl rand -base64 33 > deploy/secrets/replication_password
chmod 600 deploy/secrets/replication_password
deploy/ops/standby-bootstrap.sh
docker compose -f docker-compose.prod.yml -f docker-compose.standby.yml up -d
```

Failover is deliberately manual (`SELECT pg_promote()`, then repoint
`RMM_DATABASE_URL`). Automatic promotion needs fencing, and a split brain where
two servers accept agent traffic is worse than minutes of downtime.

**Replication is not a backup.** It copies a mistaken `DELETE` to the standby
faithfully and instantly. Keep the dump and PITR regardless.

## 8. Upgrade and rollback runbook

1. Read release notes and verify the tag, `SHA256SUMS` signature, checksums, SBOM,
   and provenance as applicable.
2. Run `backup.sh`, then `verify-backup.sh` against the resulting archive.
3. Preserve `deploy/.env`, secret files, and the exact previous image/source tag.
4. Fetch/checkout the intended release tag and run Compose `config --quiet`.
5. Build/start with `up -d --build --wait`; migrations run before the server
   listener starts.
6. Check service health/logs, console login, scoped reads, an agent heartbeat, and
   a controlled approval-gated job before broad use.
7. Publish signed agent/sidecar artifacts only after the control plane is healthy.

For an application-only failure before a migration, return to the previous
images/tag. Once a new migration was applied, stop writers and use `restore.sh`
with the verified pre-upgrade backup before starting the previous application.
Do not manually delete `schema_migrations` rows or change their checksums.

## 9. Incident runbook

1. Restrict or disable affected hostnames at the TLS edge; preserve operator
   access through a trusted path. Do not expose raw backend ports as a shortcut.
2. Capture container status/logs, edge logs, audit exports, release hashes, and
   database/host timestamps before rotation or cleanup.
3. Revoke affected API tokens/OAuth access, sessions, or device identities.
   Suspected agent credential compromise requires revoke and fresh enrollment,
   not credential rotation.
4. Rotate exposed secrets. Keep the settings encryption key available until all
   encrypted settings are deliberately replaced; changing it first loses access
   to those values.
5. Verify a clean signed release and a recent backup, rebuild/recover, then test
   scoped authentication, agent control, and approvals before reopening ingress.
6. Review `audit_log` and externalize relevant records. The database audit table
   is immutable through the application, but is not tamper-proof against a
   database administrator.

## 10. Health and observability

Compose healthchecks cover PostgreSQL `pg_isready`, the server's `/readyz`
(which pings the database), and the frontend's built `index.html` plus
`assets/app.js`. Useful checks include:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml logs server frontend postgres
curl -fsS https://agents.rmm.example.com/healthz
curl -fsS https://mcp.rmm.example.com/healthz
```

The agents door must return `404` for `/api/*`; the MCP door must return `404`
for console routes; OAuth discovery exists only when enabled. Test the console
from both an allowed and a denied source at the TLS edge.

`/healthz` is only process liveness; `/readyz` adds a database ping and nothing
else. Neither checks the scheduler, agents, remote relay, AI provider, backup
freshness, or offsite replication.

`/metrics` serves a small Prometheus text snapshot (online agents, database pool,
monitoring errors, audit write failures, the alert-mail outbox) and stays **404**
until `RMM_METRICS_TOKEN` is set; a scraper then sends it as a bearer token. This
compose file does not pass that variable and the bundled nginx does not route
`/metrics` through any door: add `RMM_METRICS_TOKEN` to the `server`
environment and scrape `server:8080/metrics` from the internal network rather
than publishing it. The series are listed in
[`docs/04-api-reference.md`](../docs/04-api-reference.md#health) and what to alert
on in [`docs/20-email-notifications.md`](../docs/20-email-notifications.md).

There is no distributed tracing or HA failover. Alert mail is built in (Settings ▸
Email; see `docs/20-email-notifications.md`), but it reports fleet events, not the
platform's own health. Production operators must collect stdout/container and
edge logs, monitor PostgreSQL/storage/resources/certificate expiry, alert on
Compose health and backup verification, and use application alerts/audit as
domain signals rather than full platform observability.

Single sign-on (`RMM_OIDC_*`, see [`docs/21-single-sign-on.md`](../docs/21-single-sign-on.md))
is likewise not passed by this compose file; add the variables you use to the
`server` environment, with the client secret as a file secret
(`RMM_OIDC_CLIENT_SECRET_FILE`).
