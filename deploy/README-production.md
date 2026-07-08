# Production deploy — split ingress (agents public, console firewalled)

A serious RMM has two audiences with opposite trust levels:

- **Agents** live on your clients' networks (untrusted). Their channel **must be
  reachable from anywhere** — you can't IP-allowlist it.
- **Console / REST API / MCP** is the high-value surface (login, config, delete,
  AI). You want it **restricted to your office / VPN IPs** (or, if you choose, public).

A single shared port can't do both: IP-restricting it would also cut the agents.
So this topology splits the ingress into **two doors on two ports**, and lets the
**firewall** do the cut — the application never needs to know your allowlist.

```
        Internet                         Your office / VPN
           │                                    │
           ▼                                    ▼
  ┌─────────────────┐                 ┌────────────────────┐
  │  Agents door    │  open to all    │   Console door     │  firewall: your IPs
  │  :AGENTS_PORT   │                 │   :CONSOLE_PORT    │
  │  /ws/ /dl/      │                 │  / /api /mcp       │
  │  /install.*     │                 │                    │
  └────────┬────────┘                 └─────────┬──────────┘
           └──────────────┬───────────────────-─┘
                          ▼
                nginx ──► server:8080 ──► postgres   (internal Docker network)
```

Files:
- [`nginx.prod.conf`](nginx.prod.conf) — the two `server` blocks (agents = `:80`,
  console = `:81` inside the container).
- [`docker-compose.prod.yml`](docker-compose.prod.yml) — publishes the two host
  ports and wires the server.

## 1. Configure

Create `deploy/.env`:

```bash
POSTGRES_PASSWORD=$(openssl rand -hex 24)
RMM_ADMIN_TOKEN=$(openssl rand -hex 32)      # min 32 chars

# The AGENTS door's public URL (what agents and the remote relay must reach).
RMM_PUBLIC_URL=https://agents.rmm.example.com

# Host ports for the two doors (governed by the firewall below).
AGENTS_PORT=8443     # public
CONSOLE_PORT=9443    # restricted

# Optional app-level allowlist for /mcp (defense in depth on the console door).
RMM_MCP_ALLOWED_IPS=203.0.113.10,100.64.0.0/10

# AI (optional; also settable later in the UI)
RMM_AI_API_KEY=
```

Bring it up:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml up -d --build
```

## 2. The firewall cut (this is where the restriction lives)

The firewall governs **ports + source IPs** (it can't see URL paths — that's why
the split is by port). Open the agents port to the world; restrict the console
port to your networks. Example with `ufw`:

```bash
ufw default deny incoming
ufw allow 8443/tcp                                          # agents door — world
ufw allow from 203.0.113.0/24 to any port 9443 proto tcp    # console — office
ufw allow from 100.64.0.0/10  to any port 9443 proto tcp    # console — VPN (Tailscale)
ufw enable
```

Traffic to `9443` from any other source is dropped in the kernel — it never
reaches nginx or the app. Postgres (`5432`) and the raw server (`8080`) are never
published; they stay on the internal Docker network.

> **Want a public console instead?** Just open the console port too
> (`ufw allow 9443/tcp`) — but then TLS is mandatory (below) and lean on the
> token + scope + approval guardrail, since an IP allowlist can't apply.

## 3. TLS (edge concern)

The doors speak plain HTTP inside the container; terminate HTTPS at the edge:

- **Cloudflare Tunnel** — no open inbound ports at all; map one hostname per door.
- **A front proxy** (Caddy/Traefik) doing auto-TLS, forwarding to `:AGENTS_PORT`
  and `:CONSOLE_PORT`.
- Or add `listen 443 ssl;` + `ssl_certificate*` to each `server` block in
  `nginx.prod.conf` and mount your certs.

Whatever you pick, `RMM_PUBLIC_URL` must be the **HTTPS URL of the agents door**.

## 4. Point the agents at the public door

Enrollment and self-update go through the agents door:

```bash
curl -fsSL https://agents.rmm.example.com/install.sh | sudo bash -s -- \
  --server https://agents.rmm.example.com \
  --token <ENROLL_TOKEN>
```

Agents authenticate with their own per-device hashed token (constant-time
compare), so the public door is an authenticated endpoint, not an open one.

## 5. Verify

```bash
# Agents door: agent paths OK, console refused.
curl -sf  https://agents.rmm.example.com/healthz            # -> ok
curl -s -o /dev/null -w '%{http_code}\n' \
     https://agents.rmm.example.com/api/health              # -> 404 (not exposed here)

# Console door (from an allowed IP): API answers; from a blocked IP it times out
# / is refused at the firewall.
curl -s -o /dev/null -w '%{http_code}\n' \
     https://console.rmm.example.com/api/health             # -> 401 (needs auth)
```

## Layered defense recap

1. **Firewall** — agents port public; console port to your IPs; DB + app cru never exposed.
2. **nginx** — TLS edge + path routing; the agents door refuses non-agent paths.
3. **App** — `RMM_MCP_ALLOWED_IPS` on `/mcp`, rate limiting, `mcp.use`, per-user
   scope, API-token expiry, and the human-approval guardrail as the final backstop.
