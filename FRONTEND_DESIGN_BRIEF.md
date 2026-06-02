# Frontend Design Brief — RMM-AI

Use this to design and build the web frontend (e.g. in Claude Design / Claude
Artifacts), then wire it to the Go backend in this repo. It is self-contained:
product context, screens, design language, and the full API contract.

---

## 1. Product in one paragraph

RMM-AI is a self-hosted **Remote Monitoring & Management** tool whose primary
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
    native / meshcentral / rustdesk) → calls remote API, then opens `connect_url`
    (native/meshcentral: open URL; rustdesk: trigger the `rustdesk://` URI).
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
For the MVP, the user pastes the admin token (or an issued API token) into a
login screen; store it in memory/localStorage and attach as the bearer. (Proper
login/session is a backend roadmap item.)

### Endpoints

| Action | Method & path | Body / notes |
|--------|---------------|--------------|
| Health | `GET /healthz` | → `ok` |
| List devices | `GET /api/devices` | → `{ devices: Agent[] }` |
| Get device | `GET /api/devices/{id}` | → `Agent` |
| Device inventory | `GET /api/devices/{id}/inventory` | → `{ agent_id, data, collected_at }` |
| Start remote | `POST /api/devices/{id}/remote` | `{ provider }` → `RemoteSession` (use `connect_url`) |
| List jobs | `GET /api/jobs?agent_id=&limit=` | → `{ jobs: Job[] }` |
| Create job | `POST /api/jobs` | `{ agent_id, shell, script, description, require_approval }` → `Job` |
| Get job | `GET /api/jobs/{id}` | → `Job` |
| Approve job | `POST /api/jobs/{id}/approve` | → `Job` |
| Reject job | `POST /api/jobs/{id}/reject` | → `Job` |
| Audit | `GET /api/audit?limit=` | → `{ entries: AuditEntry[] }` |
| Enroll token | `POST /api/enroll-tokens` | `{ description, uses, ttl_hours }` → token (shown once) |
| Chat sessions | `GET /api/chat/sessions` | → `{ sessions: ChatSession[] }` |
| New session | `POST /api/chat/sessions` | `{ title }` → `ChatSession` |
| Messages | `GET /api/chat/sessions/{id}/messages` | → `{ messages: ChatMessage[] }` |
| Send message | `POST /api/chat/sessions/{id}/messages` | `{ content }` → `{ content, created_job_ids }` |

### Shapes (TypeScript)

```ts
type Agent = {
  id: string; hostname: string; os: string; arch: string; version: string;
  status: "online" | "offline"; group_id?: string; tags: string[];
  rustdesk_id?: string; mesh_id?: string; last_seen?: string; created_at: string;
};

type Job = {
  id: string; agent_id: string; type: "run_script"; description: string;
  shell: "bash" | "sh" | "powershell" | "cmd"; script: string;
  status: "awaiting_approval" | "pending" | "sent" | "running" | "completed" | "failed" | "rejected";
  requires_approval: boolean; created_by: string; approved_by?: string;
  exit_code?: number; stdout: string; stderr: string;
  created_at: string; updated_at: string;
};

type RemoteSession = {
  id: string; agent_id: string; provider: "native" | "meshcentral" | "rustdesk";
  status: string; connect_url: string; expires_at: string;
};

type AuditEntry = {
  id: string; actor: string; action: string; target: string;
  detail: Record<string, unknown>; created_at: string;
};

type ChatSession = { id: string; title: string; created_at: string };

type ChatMessage = {
  id: string; session_id: string;
  role: "user" | "assistant" | "tool" | "system"; content: string;
  tool_calls?: unknown; tool_call_id?: string; name?: string; created_at: string;
};
```

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

## 11. Additional API (full reference: docs/04-api-reference.md)

| Area | Key calls |
|------|-----------|
| Auth | `POST /api/auth/login`, `/bootstrap`, `/logout`; `GET /api/me` |
| Users | `GET/POST /api/users`, `DELETE /api/users/{id}` |
| Org | `GET/POST /api/clients`, `GET/POST /api/sites?client_id=`, `PATCH /api/devices/{id}/site` |
| Scripts | `GET/POST/PUT/DELETE /api/scripts`, `POST /api/devices/{id}/run-script` |
| Checks | `GET/POST/PUT/DELETE /api/checks`, `GET /api/checks/{id}/results?agent_id=` |
| Tasks | `GET/POST/PUT/DELETE /api/tasks` |
| Policies | `GET/POST/DELETE /api/policies`, `GET/POST /api/policies/{id}/assignments`, `DELETE /api/policies/assignments/{aid}` |
| Alerts | `GET /api/alerts?status=`, `POST /api/alerts/{id}/ack`, `/resolve` |

## 12. Additional TypeScript shapes

```ts
type Role = "admin" | "tech" | "readonly";
type User = { id: string; username: string; role: Role; created_at: string };
type Client = { id: string; name: string; created_at: string };
type Site = { id: string; client_id: string; name: string; created_at: string };

type Script = {
  id: string; name: string; description: string; category: string;
  shell: "bash" | "sh" | "powershell" | "cmd"; body: string;
  default_args: string; created_at: string; updated_at: string;
};

type CheckType = "diskspace" | "memory" | "cpuload" | "ping" | "winservice" | "script";
type Check = {
  id: string; agent_id?: string; policy_id?: string; type: CheckType;
  name: string; config: Record<string, unknown>;
  warn_threshold?: number; err_threshold?: number;
  interval_sec: number; enabled: boolean; created_at: string;
};
type CheckResult = {
  id: string; check_id: string; agent_id: string;
  status: "passing" | "warning" | "failing"; value?: number;
  output: string; ran_at: string;
};

type Task = {
  id: string; agent_id?: string; policy_id?: string; name: string;
  script_id?: string; shell: string; script: string;
  schedule_kind: "interval" | "daily"; interval_sec: number;
  run_at_minute: number; enabled: boolean; created_at: string;
};

type Policy = { id: string; name: string; description: string; created_at: string };
type PolicyAssignment = {
  id: string; policy_id: string;
  scope_type: "client" | "site" | "agent"; scope_id: string; created_at: string;
};

type Alert = {
  id: string; agent_id?: string;
  source_type: "check" | "task" | "agent"; source_id?: string;
  severity: "info" | "warning" | "error";
  message: string; status: "new" | "acked" | "resolved";
  created_at: string; resolved_at?: string;
};
```

The `Agent` type also gains `site_id?: string` and `description: string`.

## 13. Recommended IA / navigation

`Dashboard · Devices · Alerts · Checks · Tasks · Policies · Scripts · AI Chat ·
Clients · Audit · Users/Settings`. Alerts and the AI Chat are the two
attention-grabbing destinations; everything else is management.
