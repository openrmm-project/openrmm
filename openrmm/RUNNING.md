# Running the frontend against the backend

`src/mockapi.jsx` is the real browser API client. Production uses the npm-built
`dist/` bundle; source `.jsx` files are not loaded by the browser.

## Development Compose

From the repository root:

```bash
cp .env.example .env
# Replace RMM_ADMIN_TOKEN with a random value of at least 32 characters.
docker compose up -d --build
curl -fsS http://localhost:3005/healthz
```

Create the first user only while the database has no users:

```bash
curl -X POST http://localhost:3005/api/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<a-strong-password>"}'
```

Open `http://localhost:3005`. This Compose proxy serves the built console and
API from the same origin.

## Local build

Start PostgreSQL and the Go server, then build and serve `dist/`:

```bash
export RMM_ADMIN_TOKEN="$(openssl rand -hex 32)"
export RMM_SETTINGS_ENCRYPTION_KEY="$(openssl rand -hex 32)"
export RMM_DATABASE_URL='postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable'
make build
./bin/rmm-server
```

Build and inspect the artifact:

```bash
npm --prefix openrmm ci
npm --prefix openrmm test
npm --prefix openrmm run build
python3 -m http.server 5500 --directory openrmm/dist
```

The Python server only serves static output; it does not proxy `/api` or
`/ws/remote`, so it is not a complete connected deployment. The enforcing CSP
uses `connect-src 'self'`; use a same-origin reverse proxy (as Compose does) for a
functional console. Changing `dist/config.js` to a cross-origin backend is not
enough: that topology also requires a deliberate CSP change and matching
`RMM_CORS_ORIGINS`. The direct server uses `:8080`; the development Compose proxy
uses `:3005`.

## Non-root base path

Build and serve with the same canonical path:

```bash
OPENRMM_BASE_PATH=/console/ npm --prefix openrmm run build
```

The output is under `openrmm/dist/console/`. A static server/proxy must serve
that directory at `/console/`, serve `/console/config.js`, and route `/api/*`
plus `/ws/remote` to the backend. Production Compose wires this automatically
from `OPENRMM_BASE_PATH` in `deploy/.env`.

## Verification

```bash
npm --prefix openrmm run smoke
```

The Docker/Compose healthcheck verifies built `index.html` and
`assets/app.js`; it is not a backend dependency or browser-functional readiness
probe. Verify login, a scoped list, an approval-gated job, and remote WebSocket
through the actual edge as separate checks.

## Current integration limitations

- The session bearer is in `sessionStorage`, while non-secret preferences use
  `localStorage`.
- The built-in `readonly` role includes remote desktop permission.
- Check status causes one latest-result request per check.
- The 2FA backend safely stages a pending secret and now requires current
  password (plus current TOTP when rotating/disabling). The current frontend card
  does not collect/send those reauthentication fields, so those UI actions are
  not functional yet. Use the REST API with the documented bodies.
- Live CPU load and osquery-grade inventory are not implemented.
