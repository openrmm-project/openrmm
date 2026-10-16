# Quick start: the published images

This runs OpenRMM on one Linux server from the release images on
`ghcr.io/openrmm-project`, with Caddy obtaining the TLS certificates. It is the
deployment that [README-production.md](README-production.md) describes, with
prebuilt images and an optional TLS edge. Read that guide for backups,
point-in-time recovery, a warm standby and upgrades.

## What you need

- A Linux x86-64 server with Docker Engine and Docker Compose 2.24 or later.
- Three DNS names pointing at it, for example `agents.rmm.example.com`,
  `console.rmm.example.com` and `mcp.rmm.example.com`.
- Ports 80 and 443 open to the internet, for the certificates and the agents.
- `git` and `openssl`.

## 1. Get the deployment files

```bash
git clone https://github.com/openrmm-project/openrmm.git
cd openrmm/deploy
```

`docker-compose.images.yml` pins the release images. The `deploy/` files of
the same checkout are the ones that go with them.

## 2. Configure

```bash
cp .env.example .env
```

In `.env`, set these lines. Some of them replace values the example already
has; others are commented out in it.

```bash
RMM_PUBLIC_URL=https://agents.rmm.example.com
RMM_MCP_PUBLIC_URL=https://mcp.rmm.example.com
# The key the release's agents and installers are signed with. With the
# published images, use exactly this value.
RMM_UPDATE_PUBLIC_KEY=MhH1ZCiPzlPMvHUBrx52yZ49Co2KlutYNC913Zkq0L0=

COMPOSE_FILE=docker-compose.prod.yml:docker-compose.images.yml:docker-compose.caddy.yml
AGENTS_HOST=agents.rmm.example.com
CONSOLE_HOST=console.rmm.example.com
MCP_HOST=mcp.rmm.example.com
ACME_EMAIL=you@example.com
# Recommended: let only your office or VPN reach the console.
# CONSOLE_ALLOWED_RANGES=203.0.113.0/24 2001:db8::/32
```

Then create the secrets: random values, kept in `secrets/`, which git ignores.

```bash
ops/provision-secrets.sh
```

## 3. Start

```bash
docker compose pull
docker compose up -d
docker compose ps
```

The first start takes about a minute: Caddy obtains the certificates, and the
server creates the database.

## 4. Create the first administrator

The admin token works for this exactly once, while the console has no users.

```bash
read -rsp 'Admin password (12+ characters, an uppercase letter and a symbol; no double quotes): ' PW; echo
curl -fsS -X POST https://console.rmm.example.com/api/auth/bootstrap \
  -H "Authorization: Bearer $(cat secrets/admin_token)" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" >/dev/null && echo created
unset PW
```

Then:

1. Sign in at `https://console.rmm.example.com`.
2. Turn on two-step verification for your account.
3. Optionally, set an AI provider under **Users & Settings ▸ Configuration**.

## 5. Add devices

1. In the console, open **Enroll new agents** and generate a token.
2. Copy the install command for Windows, Linux or macOS. The release key is
   already in it.

Agents install from `https://agents.rmm.example.com` and update themselves from
there, and they check every file against the release key.

## Upgrading

1. Take a backup first ([README-production.md](README-production.md), section 7).
2. Update the checkout and the containers:

   ```bash
   git pull
   docker compose pull
   docker compose up -d
   ```

Each release moves the pinned images in `docker-compose.images.yml`, and the
server migrates the database when it starts.

## Using your own TLS edge

If you already run nginx, Traefik, a tunnel or a load balancer:

1. Leave `docker-compose.caddy.yml` out of `COMPOSE_FILE`.
2. Point the edge at the loopback doors:
   - agents: `127.0.0.1:8443`;
   - console: `127.0.0.1:9443`;
   - MCP: `127.0.0.1:7443`.

[README-production.md](README-production.md), section 3, lists what the edge has
to forward.

## Keep it private

- `.env` and `secrets/` hold this installation's secrets. Git ignores both:
  never copy them into the repository or into a ticket.
- Back up `secrets/settings_encryption_key`. Without it, the settings the
  console stores encrypted (SMTP and AI keys) cannot be read.
- The admin token is for the first bootstrap and for emergencies. Day-to-day
  work uses console accounts.
- The server connects to PostgreSQL as the database owner, and says so in its
  log at every start. For an audit trail the server itself cannot erase, run it
  as the restricted `openrmm_app` role
  ([README-production.md](README-production.md), section 6).
