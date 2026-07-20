# OpenRMM

An **open-source, self-hosted RMM** (Remote Monitoring & Management) with a
**native AI assistant**. Inspired by Tactical RMM and MeshCentral, but with a
modern interface, a chat that connects to any AI model via API (BYO-model,
including local/private models), and pluggable remote access.

Ask the assistant in plain language — *"install 7-zip on the finance group"*,
*"configure printer model X at IP Y on host Z"*, *"generate a report of offline
devices"* — and it inspects the fleet, **proposes** the scripts/actions, and a
human approves before anything runs on an endpoint.

## Why this exists

The AI-copilot RMMs that exist today (Atera, SuperOps, Acronis, Neo Agent) are
all **closed SaaS**. There is no open-source, self-hosted RMM with native AI and
a pluggable model. That gap — self-hosted + AI-native + privacy-first — is what
this project fills.

## Highlights

- **Own Go agent** — single cross-platform binary (Windows/Linux/macOS), no runtime;
  **self-updates** from the server (`make agent-dist` for development; signed,
  versioned release artifacts are required for production).
- **AI chat with tool-calling** — the model reads fleet state and proposes actions.
- **Guardrails by design** — AI never executes directly; every action is a job that
  a human approves, with dry-run preview and an immutable audit log.
- **Pluggable remote access** — `native` (built-in relay + CGO-free media sidecar,
  tile-delta codec; Windows and Linux/Xorg incl. the login screen, with
  cross-platform file transfer), `terminal` (an **elevated** PTY/ConPTY shell in
  the browser, gated by `devices.terminal`), and `meshcentral` — all behind one
  interface. Swap freely.
- **BYO AI model** — any OpenAI-compatible endpoint (OpenAI, Ollama, vLLM, …).
- **Classic RMM, too** — Client→Site→Agent hierarchy, RBAC users, script library,
  monitoring checks + alerts, scheduled tasks, and inherited policies. The agent
  installs as a system service and reports rich inventory.
- **Access control** — built-in `admin`/`tech`/`readonly` roles plus custom roles,
  optional **TOTP 2FA**, and **per-user access scope** (Company/Site/Department/
  device) enforced on read, write, remote access, and the AI/MCP surface. AI/MCP
  tools are filtered by the caller's permissions and scope. AI reports stay in chat
  with a client-side **CSV download** —
  never written to an endpoint.
- The built-in `readonly` role is not purely observational: it includes
  `devices.remote` (remote desktop), but not file transfer or terminal. Create a
  custom role without `devices.remote` when screen access is not acceptable.
- **MCP server** — connect external AI (Claude, ChatGPT, …) to manage the fleet,
  reusing the same permission-filtered tools and the same human-approval guardrail.
  Secured with scoped revocable tokens + `mcp.use` + an IP allowlist; deploy
  privately (Tailscale) or behind TLS.

## Architecture at a glance

```
      bundled React UI ── REST/WS API ── AI chat (tool-calling loop)
                               │
                 ┌─────────────┼──────────────┐
                 │             │              │
            agent hub     job queue      remote relay
              (WS)        (Postgres)    (native provider)
                 │
        ┌────────┴─────────┐
        │   Go agent (endpoint)   │  +  remote client (native / MeshCentral)
        └──────────────────┘
```

Full detail: [`docs/01-architecture.md`](docs/01-architecture.md).

## Quick start

```bash
cp .env.example .env          # replace RMM_ADMIN_TOKEN with a generated value
docker compose up -d          # postgres + server
# or run locally:
export RMM_ADMIN_TOKEN="$(openssl rand -hex 32)"
make build && ./bin/rmm-server
```

Then enroll an agent:

```bash
# 1. create an enrollment token
curl -X POST localhost:3005/api/enroll-tokens \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -d '{"uses":1}'

# 2. enroll once, then start the long-lived agent without the bootstrap token
./bin/rmm-agent -server ws://localhost:3005/ws/agent -enroll-token <TOKEN> -enroll-only
./bin/rmm-agent -server ws://localhost:3005/ws/agent
```

The development Compose example uses its frontend/proxy on port `3005`; a directly-run server
uses port `8080` instead.

Production is intentionally different: provision file-backed secrets with
`deploy/ops/provision-secrets.sh`, terminate TLS at a trusted edge, and keep the
three published listeners bound to loopback. Agent installation requires HTTPS,
OpenSSL 3, and `--update-public-key`/`-UpdatePublicKey`; insecure installation is
available only through explicit lab mode. See
[`deploy/README-production.md`](deploy/README-production.md).

See [`docs/02-getting-started.md`](docs/02-getting-started.md) for the full walkthrough.

## Documentation

| Doc | Contents |
|-----|----------|
| [00-overview](docs/00-overview.md) | Vision, scope, what's built vs planned |
| [01-architecture](docs/01-architecture.md) | Components, data flow, package layout |
| [02-getting-started](docs/02-getting-started.md) | Run the server, enroll an agent, run a job |
| [03-database-schema](docs/03-database-schema.md) | Tables and relationships |
| [04-api-reference](docs/04-api-reference.md) | Every REST endpoint |
| [05-agent-protocol](docs/05-agent-protocol.md) | The agent WebSocket protocol |
| [06-ai-chat](docs/06-ai-chat.md) | Tool-calling loop, tools, prompts |
| [07-guardrails-security](docs/07-guardrails-security.md) | Approval, audit, threat model |
| [08-remote-access](docs/08-remote-access.md) | The native (desktop + file transfer), terminal, and MeshCentral providers |
| [09-roadmap](docs/09-roadmap.md) | Phased plan toward a complete RMM |
| [10-monitoring-policies](docs/10-monitoring-policies.md) | Org hierarchy, RBAC, scripts, checks, tasks, policies, alerts |
| [11-mcp](docs/11-mcp.md) | MCP server — connect external AI (Claude, ChatGPT) securely |
| [12-native-remote-fork](docs/12-native-remote-fork.md) | Historical: the abandoned RustDesk-fork plan (superseded by the homegrown engine in 08) |
| [13-security-rollout](docs/13-security-rollout.md) | Release/signing gates, strict installers, TLS/secrets, enforcing CSP, remaining limits |
| [14-frontend-design-brief](docs/14-frontend-design-brief.md) | Frontend brief: product context, screens, design language, full API contract |
| [15-reports](docs/15-reports.md) | Fleet inventory reports (software/hardware/machines) by client/site/department, scoped, CSV export, AI tool |
| [16-patch-management](docs/16-patch-management.md) | Patch inventory, approval/classification, policies (windows/auto-approve), reboot orchestration, compliance |
| [17-mcp-oauth](docs/17-mcp-oauth.md) | MCP OAuth 2.1 — OpenRMM as its own Authorization Server so cloud clients (Claude web, ChatGPT) connect natively |
| [18-update-signing-runbook](docs/18-update-signing-runbook.md) | Operator playbook for signed auto-update: key custody, cutting a signed release, publishing to `/dl/`, and staged key rotation via the trusted key set (no reinstall) |
| [19-production-readiness-plan](docs/19-production-readiness-plan.md) | Prioritized remediation plan to reach production: go-live blockers, observability, HA, container scanning, defense-in-depth |

For production deployment, see [`deploy/README-production.md`](deploy/README-production.md):
a **split-ingress topology** with loopback backend binds and source ACLs at the
trusted TLS edge. The supported deployment is one server replica; the in-memory
agent hub, relay and scheduler are not distributed or HA.

To build the frontend: [`docs/14-frontend-design-brief.md`](docs/14-frontend-design-brief.md).

## License

The native remote engine is a homegrown implementation (CGO-free on Windows and
Linux; macOS capture uses CGO) rather than a third-party fork. See
[`docs/08-remote-access.md`](docs/08-remote-access.md) for platform and maturity
limitations.
