# 13 — Security Hardening Rollout

This is the low-risk rollout path for the supply-chain and transport hardening
controls. It is intentionally staged so existing labs keep working while
production deployments can become strict.

## 1. CI dependency checks

CI now verifies Go module checksums and runs `govulncheck` in advisory mode.
`govulncheck` is `continue-on-error` at first so the project gets vulnerability
visibility without blocking every existing PR immediately.

Run the same checks locally with:

```bash
make security-check
```

Dependabot is enabled for:

- Go modules
- Docker base images
- GitHub Actions

Treat those as reviewed PRs, not automatic production deploys.

## 2. Sign agent and sidecar artifacts

The agent can verify detached Ed25519 signatures for anything it downloads from
`/dl`:

- `rmm-agent-<os>-<arch>[.exe]`
- `rmm-remote-<os>-<arch>[.exe]`

Generate a signing keypair:

```bash
make signer
bin/rmm-sign -gen-key
```

Store `RMM_SIGNING_KEY` as a release secret. Do not commit it. Distribute
`RMM_UPDATE_PUBLIC_KEY` to agents, either by environment/configuration or by
embedding it into the agent build:

```bash
RMM_UPDATE_PUBLIC_KEY=<public-key> make agent-dist
make remote-dist
RMM_SIGNING_KEY=<private-key> make sign-dist
```

The signature file format is:

```text
rmm-ed25519-sha256-v1 <base64-signature>
```

The signature covers the SHA-256 digest of the artifact.

## 3. Rollout modes

`RMM_UPDATE_VERIFY` controls enforcement on the agent:

| Mode | Behavior |
|------|----------|
| `warn` | Default. Log missing/invalid signatures but keep compatibility. |
| `strict` | Refuse unsigned or untrusted agent/sidecar artifacts. |
| `off` | Disable signature checks. Lab/debug only. |

Recommended rollout:

1. Publish signed artifacts while agents are still in `warn`.
2. Confirm logs show `security: verified update signature`.
3. Reinstall or update agents with `RMM_UPDATE_PUBLIC_KEY` available.
4. Set `RMM_UPDATE_VERIFY=strict` for production agents.

In `strict`, a bad or missing `.sig` blocks the update and leaves the running
agent/sidecar untouched.

## 4. Require TLS for agents

The agent now supports:

```bash
RMM_REQUIRE_TLS=1
# or
rmm-agent -require-tls -server wss://rmm.example.com/ws/agent
```

The installers support the same control:

```bash
curl -fsSL https://rmm.example.com/install.sh | sudo bash -s -- \
  --server https://rmm.example.com \
  --token <ENROLL> \
  --require-tls
```

```powershell
& ([scriptblock]::Create((iwr -useb https://rmm.example.com/install.ps1))) `
  -Server https://rmm.example.com `
  -Token <ENROLL> `
  -RequireTls
```

Leave this off for local `http://localhost` labs. Enable it for production so
agent control, self-update, and sidecar downloads derive from WSS/HTTPS.

## 5. Frontend CSP staging

The nginx config now emits security headers plus a
`Content-Security-Policy-Report-Only` header. It does not block the current
no-build frontend, because the prototype still uses inline scripts, Babel, and
CDNs. Use browser/proxy reports to identify what must be vendored before moving
to an enforcing CSP.

Production target:

- bundle or self-host React, xterm, QRCode, Tailwind, and fonts
- remove browser Babel and Tailwind CDN
- switch from `Content-Security-Policy-Report-Only` to `Content-Security-Policy`
- remove `'unsafe-inline'` and `'unsafe-eval'`

## 6. Remaining hardening

Still recommended for a future pass:

- pin Docker images by digest once the target registry digests are chosen
- add a blocking container-image scan after the first advisory period
- bind `/ws/remote` viewer tokens to the technician principal and target agent
