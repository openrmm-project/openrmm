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
  **self-updates** from the server (publish with `make agent-dist`).
- **AI chat with tool-calling** — the model reads fleet state and proposes actions.
- **Guardrails by design** — AI never executes directly; every action is a job that
  a human approves, with dry-run preview and an immutable audit log.
- **Pluggable remote access** — `native` (built-in relay + CGO-free media sidecar,
  tile-delta codec; Windows and Linux/Xorg incl. the login screen, with
  cross-platform file transfer), `terminal` (an **elevated** PTY/ConPTY shell in
  the browser, tech-only), and `meshcentral` — all behind one interface. Swap freely.
- **BYO AI model** — any OpenAI-compatible endpoint (OpenAI, Ollama, vLLM, …).
- **Classic RMM, too** — Client→Site→Agent hierarchy, RBAC users, script library,
  monitoring checks + alerts, scheduled tasks, and inherited policies. The agent
  installs as a system service and reports rich inventory.
- **Access control** — `admin`/`tech`/`readonly` roles, optional **TOTP 2FA**, and
  **per-user access scope** (Company/Site/device) enforced on read, write, and the
  AI/MCP surface. AI reports stay in chat with a client-side **CSV download** —
  never written to an endpoint.
- **MCP server** — connect external AI (Claude, ChatGPT, …) to manage the fleet,
  reusing the same tools and the same human-approval guardrail. Secured with
  scoped revocable tokens + an IP allowlist; deploy privately (Tailscale) or
  behind TLS.

## Architecture at a glance

```
        UI (you build) ── REST/WS API ── AI chat (tool-calling loop)
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
cp .env.example .env          # set RMM_ADMIN_TOKEN (openssl rand -hex 32) and AI keys
docker compose up -d          # postgres + server
# or run locally:
make build && RMM_ADMIN_TOKEN=... ./bin/rmm-server
```

Then enroll an agent:

```bash
# 1. create an enrollment token
curl -X POST localhost:8080/api/enroll-tokens \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -d '{"uses":5}'

# 2. run the agent with that token (first run only)
./bin/rmm-agent -server ws://localhost:8080/ws/agent -enroll-token <TOKEN>
```

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
| [13-security-rollout](docs/13-security-rollout.md) | Staged hardening: signed updates, TLS enforcement, CI checks, CSP staging |
| [14-frontend-design-brief](docs/14-frontend-design-brief.md) | Frontend brief: product context, screens, design language, full API contract |

To build the frontend: [`docs/14-frontend-design-brief.md`](docs/14-frontend-design-brief.md).

## License

This project is intended to be released as open source. The native remote engine
is a homegrown, CGO-free implementation (no third-party fork), so it adds no extra
licensing constraints. See [`docs/08-remote-access.md`](docs/08-remote-access.md)
for the engine notes.
