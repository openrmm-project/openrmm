# Frontend Design Brief — OpenRMM

Use this to design and build the web frontend (e.g. in Claude Design / Claude
Artifacts), then wire it to the Go backend in this repo. It is self-contained:
product context, screens, design language, and the full API contract.

---

## 1. Product in one paragraph

OpenRMM is a self-hosted **Remote Monitoring & Management** tool whose primary
interface is an **AI chat**. An IT admin manages fleets of computers in natural
language — *"install 7-zip on the finance group"*, *"set up printer X at IP Y on
host Z"*, *"give me a report of offline devices"*. The AI inspects the fleet and
**proposes** scripts/actions; a human **approves** before anything runs. Think
"modern Tactical RMM, AI-native, privacy-first."

## 2. Audience & tone

IT admins / MSP technicians. The UI should feel **modern, fast, dense but
calm** — closer to Linear / Vercel dashboards than to legacy RMM consoles.
Dark-mode first, light mode supported. Keyboard-friendly. Trustworthy: this tool
runs code on real machines, so destructive/AI actions must feel deliberate.

## 3. Design language

- **Layout:** left sidebar nav + main content area; the AI chat is a first-class
  destination (not a hidden widget) and also available as a docked side panel.
- **Color:** neutral slate base; one accent (indigo/violet). Semantic colors:
  green=online/completed, amber=awaiting approval/pending, red=failed/offline,
  blue=running.
- **Type:** clean sans (Inter/Geist). Monospace for scripts, IDs, logs, output.
- **Components:** cards, data tables with status pills, slide-over drawers for
  detail, a prominent **diff/preview** block for scripts, toasts for async
  results.
- **Motion:** subtle; status transitions animate (awaiting → running → done).

## 4. Screens

### 4.1 Dashboard
- Tiles: total devices, online/offline, jobs awaiting approval (highlight),
  recent failures.
- Recent activity feed (from the audit log).
- Quick "Ask the AI" input that opens the chat with the typed prompt.

### 4.2 Devices
- Table: hostname, OS (icon), arch, status pill, last seen.
- Filters: OS, status, group, tags. Search by hostname.
- Row click → **Device detail** drawer/page:
  - Header: hostname, OS, status, last seen; **Remote** button (provider dropdown:
    native / terminal / meshcentral) → calls remote API, then opens the returned
    `connect_url` (`terminal` is an elevated, tech-only shell — see
    [`08-remote-access.md`](08-remote-access.md)).
  - Tabs: **Inventory** (pretty-print the JSON), **Jobs** (this device's history),
    **Run script** (manual: shell selector + editor → creates an approval-gated job).

### 4.3 Jobs & Approval queue  ⭐ (the safety-critical screen)
- Two views: **Awaiting approval** (default, prominent) and **All jobs**.
- Each awaiting job shows: target device, description, **the full script in a
  monospace preview block (dry-run)**, who/what created it (note `ai:` prefix for
  AI-proposed), created time.
- Actions: **Approve** (primary, requires a deliberate click/confirm) and
  **Reject**. After approve, the row animates to running → completed/failed.
- Completed jobs show exit code + stdout/stderr (collapsible, monospace).

### 4.4 AI Chat  ⭐ (the differentiator)
- Conversation list (sessions) + message thread.
- Messages: user, assistant, and **tool steps** (render `tool` messages as
  collapsible "the AI looked up X / proposed job Y" cards — don't hide them;
  transparency builds trust).
- When a reply includes `created_job_ids`, render an inline **"Proposed job"**
  card with the script preview and an Approve/Reject action (deep-links to the
  job). Make it obvious nothing has run yet.
- Composer with send; show a typing/working indicator while the loop runs.

### 4.5 Audit log
- Filterable table: time, actor, action, target, detail. Read-only.

### 4.6 Settings
- AI provider status (model, base URL — read-only display of what's configured).
- API tokens (list/create — when the backend exposes it).
- Enrollment: button to create an enroll token + the agent install command
  snippet to copy.

## 5. Key UX rules (do not violate)

1. **Never present an AI action as already done.** AI-proposed jobs are
   *proposals*; the UI must show them as pending approval with the script visible.
2. **Approval is deliberate** — primary button, ideally a confirm step for
   destructive-looking scripts.
3. **Show the script before running it** (dry-run preview), every time.
4. **Surface the audit trail** — make it easy to see who approved what.

## 6. API contract

Base URL from config (default `http://localhost:8080`). All `/api/*` calls need:
```
Authorization: Bearer <token>
Content-Type: application/json
```

### Auth model (frontend)
Username + password → `POST /api/auth/login` returns `{token, role}`; store it in
`localStorage` and attach as the bearer on every `/api/*` call. See §9 for roles
and first-run bootstrap. TOTP 2FA and per-user access scope are backend-enforced —
the UI just surfaces them.

### Endpoints & types — canonical sources

To avoid drift, this brief no longer inlines the endpoint list or the TypeScript
shapes. Use the single sources of truth:

- **Full REST contract** → [`04-api-reference.md`](04-api-reference.md).
- **TypeScript types** (kept in sync with the running UI) →
  [`../openrmm/handoff/apiClient.ts`](../openrmm/handoff/apiClient.ts).

Remote providers are `native`, `terminal`, `meshcentral`, and an optional
`rustdesk` (off by default; enable in Settings) — see
[`08-remote-access.md`](08-remote-access.md).

### Status → color map

| status | color |
|--------|-------|
| online, completed | green |
| awaiting_approval, pending | amber |
| running, sent | blue |
| failed, offline, rejected | red |

## 7. Suggested stack

React + TypeScript + Vite, Tailwind, a component kit (shadcn/ui), TanStack Query
for data fetching, a code/diff viewer (Monaco or CodeMirror) for script previews.
Polling is fine for the MVP (e.g. refetch jobs every few seconds); a job/result
WebSocket can come later.

## 8. First build target (MVP UI)

1. Login (paste token) → Devices list → Device detail (inventory + run script).
2. Jobs / Approval queue with script preview + approve/reject.
3. AI Chat with proposed-job cards.
4. Audit log.

That covers the core loop. The full backend (below) is broader.

---

# Part 2 — Full RMM UI (backend is implemented & verified)

The backend now includes the "classic RMM" feature set inspired by Tactical RMM.
Design these additional areas. Background and semantics:
`docs/10-monitoring-policies.md`.

## 9. Authentication & roles

- **Login screen**: username + password → `POST /api/auth/login` → store the
  returned `token` + `role`; attach as bearer. (`POST /api/auth/bootstrap` for
  first-run admin creation when the instance is empty — show a setup screen if
  login reports no users.)
- **Roles** gate the UI: `readonly` (view only), `tech` (run/approve/ack),
  `admin` (manage everything). Hide/disable actions above the user's role; the
  API also enforces this (403). Read the current principal from `GET /api/me`.

## 10. New screens

### 10.1 Organization (Clients → Sites)
Tree or master-detail of Clients → Sites. Create/delete (admin). Show device
counts. From a site, copy an **enrollment command** (create a site-bound enroll
token via `POST /api/enroll-tokens {site_id}` and show the agent install snippet).
Device detail gains a **site selector** (`PATCH /api/devices/{id}/site`).

### 10.2 Script library
Table (name, category, shell). Editor with a code editor for `body` (Monaco/
CodeMirror, syntax by shell). "Run on device" action → pick device → creates an
approval-gated job. Admin CRUD.

### 10.3 Monitoring (Checks) ⭐
- Per device **and** per policy: list checks with their **live status pill**
  (passing/warning/failing) from the latest result.
- Create/edit check: type (diskspace/memory/cpuload/ping/winservice/script),
  type-specific config (path / host / service / inline script), warn/err
  thresholds, interval. Note the threshold direction per type (disk/mem = alert
  when low; cpu = alert when high).
- Check detail: a **time-series / history** of results (value + status) from
  `GET /api/checks/{id}/results?agent_id=`.

### 10.4 Scheduled tasks
List + create/edit tasks (pick a library script or inline; schedule = interval or
daily-at-time; enabled toggle). Show last run + link to the jobs it produced
(`jobs.task_id`). Per device and per policy.

### 10.5 Policies ⭐
- Policy list; policy detail with its **checks** and **tasks** (reuse 10.3/10.4
  in "policy mode"), and an **Assignments** panel: assign to a client, site, or
  agent. Communicate clearly that assigning to a client/site pushes these
  checks/tasks to **all agents in scope** (inheritance).

### 10.6 Alerts center ⭐
- Dashboard-prominent. Table of alerts: severity (info/warning/error), source
  (check/task/agent), status (new/acked/resolved), message, device, time.
- Filters by status/severity. Actions: **Ack**, **Resolve** (tech+). Alerts
  auto-resolve when the condition clears — reflect that with live status.
- Surface the open-alert count as a badge in the nav and on the dashboard.

### 10.7 Users & settings
Admin: list/create/delete users with role. Show AI provider + remote provider
config (read-only). Enrollment token management.

**API tokens / MCP connectors** (admin): list/create/revoke API tokens
(`GET/POST/DELETE /api/api-tokens`). On create, show the secret **once** with a
copy button and the `/mcp` connector URL, so the user can paste both into Claude/
ChatGPT. Explain that the token is role-scoped (`tech`) and revocable.

### 10.8 Destructive actions (must confirm)
- **Delete device** (`DELETE /api/devices/{id}`) and **delete group**
  (`DELETE /api/groups/{id}`) are admin-only and require an explicit **confirm
  dialog** (type-to-confirm for devices). These are deliberate human actions —
  never triggered by the AI. Device groups: `GET/POST /api/groups`, assign via
  `PATCH /api/devices/{id}/group`.
- Approval is mandatory for every script run; the UI never offers a
  "run without approval" path unless the backend reports it is enabled.

## 11. Additional API & types — canonical sources

The "classic RMM" endpoints (auth, users, org, scripts, checks, tasks, policies,
alerts) are documented in full in [`04-api-reference.md`](04-api-reference.md),
with the semantics in [`10-monitoring-policies.md`](10-monitoring-policies.md) —
not duplicated here.

All request/response **TypeScript types** (`User`, `Client`, `Site`, `Script`,
`Check`, `CheckResult`, `Task`, `Policy`, `PolicyAssignment`, `Alert`, plus
`Agent.site_id` / `Agent.description`) live in
[`../openrmm/handoff/apiClient.ts`](../openrmm/handoff/apiClient.ts), kept in sync
with the running UI. Use that as the source of truth rather than copying types
into this brief.

## 13. Recommended IA / navigation

`Dashboard · Devices · Alerts · Checks · Tasks · Policies · Scripts · AI Chat ·
Clients · Audit · Users/Settings`. Alerts and the AI Chat are the two
attention-grabbing destinations; everything else is management.
