# Running the frontend against the real backend

`src/mockapi.jsx` is now the **real fetch client** (`window.api`) talking to the
Go backend — the mock/in-memory data is gone. Verified end-to-end against the
running backend (auth, CORS, config, devices, alerts).

## 1. Start the backend
From the repo root (`..`):
```bash
cp .env.example .env            # set RMM_ADMIN_TOKEN; AI keys optional
docker compose up -d            # postgres + server on :8080
# (or: make build && RMM_ADMIN_TOKEN=... ./bin/rmm-server)
```

## 2. Create the first admin user
```bash
curl -X POST http://localhost:8080/api/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<a-strong-password>"}'
```
(Only works while no users exist. After this, sign in with these credentials.)

## 3. Serve the frontend
It's a no-build prototype (React + Babel via CDN). Serve the folder statically:
```bash
cd openrmm
python3 -m http.server 5500
# open http://localhost:5500
```
Then sign in with the admin credentials from step 2 (password mode).

## Pointing at a non-default backend URL
The client reads `window.__API_BASE__` (default `http://localhost:8080`). To
override without editing code, add this **before** the script tags in
`index.html`:
```html
<script>window.__API_BASE__ = 'https://rmm.yourhost.com';</script>
```
The backend allows browser origins via CORS (`RMM_CORS_ORIGINS`, default `*`;
tighten in production).

## Notes / known polish items (real backend vs prototype)
- **Inventory tab**: the real agent reports a flatter inventory than the
  prototype's mock. `getInventory` adapts it (CPU, memory, disks, platform);
  live CPU load, network interfaces and software list are not collected yet, so
  those sub-sections render empty. (Backend roadmap: osquery-grade inventory.)
- **Checks status pill**: `listChecks` fetches each check's latest result
  (one extra request per check) to show the pill, since the list endpoint
  doesn't embed it. Fine for typical fleets; revisit if you have hundreds.
- **Create user form**: the backend requires a `password` when creating a user
  (`POST /api/users`). Ensure the Users screen sends a password field.
- **Run-script (device detail)**: ad-hoc runs post to `/api/jobs` and are
  **approval-gated by default** (the backend ignores `require_approval:false`
  unless `RMM_ALLOW_UNAPPROVED_JOBS=true`).
- **AI Chat**: requires the backend AI provider configured (`RMM_AI_API_KEY` or
  a local OpenAI-compatible endpoint). Proposed jobs still require approval.
