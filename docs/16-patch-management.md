# 16 — Patch Management

> **Status: implemented (all phases).** Missing-patch inventory, a catalog with
> approval/classification, patch policies (maintenance windows, auto-approve,
> exclusions), reboot orchestration and a compliance report are built — backend,
> agent, scheduler, AI tool and UI. The per-OS scanners/installers shell out to
> the native tools (WUA / apt / dnf / `softwareupdate`); their exact parsing
> should be **validated on real endpoints** (same caveat as the macOS remote
> engine). Requires an agent build ≥ **0.4.2** on the endpoint.

## Goal

A first-class patch-management subsystem covering:

- **Inventory of available/missing patches** (Windows KBs, Linux advisories/CVEs)
  — not just installed software.
- **Catalog + per-patch approval + classification** (security / critical /
  important / moderate / optional).
- **Patch policy**: maintenance windows, auto-approve security patches after N
  days, exclusions.
- **Post-patch reboot orchestration**.
- **Compliance reporting** (who is up to date, who failed).

## Principles (inherited from the existing architecture)

- **Scanning/reading is free; installing is gated** — nothing reaches an endpoint
  without approval (manual or policy-driven), the same guardrail as jobs.
- **Per-user scope + RBAC + audit** on everything (`user_scopes`,
  `readonly/tech/admin`, `audit_log`).
- **CGO-free agent** — per-OS scanners shell out (`exec`) to PowerShell/WUA, apt,
  dnf, `softwareupdate`; cross-compiles (incl. arm64).
- **Maximum reuse** — `jobs` for install history, `policy_assignments` for
  inheritance, the Reports infra for compliance, the scheduler for windows.

## Data model (sketch)

- `patches` — catalog deduped by `(os, identifier)`: `identifier` (KB / package /
  advisory), `title`, `classification`, `kb`, `cves[]`, `reboot_required`,
  `first_seen`, `last_seen`.
- `agent_patches` — missing patch per device: `agent_id`, `patch_id`, `status`
  (missing/approved/installing/installed/failed/excluded), `available_version`,
  `detected_at`, `installed_at`, `error`.
- `patch_approvals` — `patch_id`, `scope_type/scope_id`, `decision`, `actor`,
  `at`, `source` (manual/policy).
- `patch_policies` + `patch_policy_assignments` — maintenance window, auto-approve
  rules, exclusions, reboot policy; assigned to client/site/agent.
- Reuse: `jobs` gains `kind=patch`; `agents` gains `reboot_pending`,
  `last_patch_scan`.

## Protocol additions (`internal/protocol`)

- **Server → agent**: `patch_scan`, `patch_install` (approved `patch_id`s),
  `reboot` (coordinated).
- **Agent → server**: `patch_report` (missing patches + metadata), `patch_result`
  (per-patch outcome + `reboot_required`).

## Phased plan

| Phase | Scope | Verifies |
|-------|-------|----------|
| **1 — Missing-patch inventory** | Per-OS scanners (Windows WUA / apt+`apt-check` / `dnf updateinfo cves` / `softwareupdate -l`); `patches`+`agent_patches`; scheduled + on-demand `patch_scan`; read APIs. | Scan a Windows + a Linux host and see missing patches with KB/severity/CVE. |
| **2 — Catalog, classification & manual approval** | Normalized `classification`; `patch_approvals` (approve/decline, scoped, audited); catalog API with counts. | Approve a patch → eligible; decline → removed from the queue. |
| **3 — Install orchestration** | `patch_install` → agent installs → `patch_result`; install = audited `job` (`kind=patch`, batched per company); OS-aware `apply_updates` AI/MCP tool (gated). **Open design risk:** WUA installs can exceed the 10-min job timeout → needs async/progress execution. | Approve→install a real patch; status `installed`, result recorded. |
| **4 — Patch policies** | `patch_policies` (maintenance window; auto-approve `classification` after N days since `first_seen`; exclusions); scheduler auto-approval engine + in-window dispatch. | Policy auto-approves a security patch older than N days and installs it in the window; exclusion blocks. |
| **5 — Reboot orchestration** | `reboot_required` → `agents.reboot_pending`; policy decides auto-reboot in window / defer / force after N hours; reboot via the existing path; user notice (optional, via the native chat window). | A reboot-requiring patch schedules and reboots in the window; state clears after. |
| **6 — Compliance reporting** | Aggregate per device/site/client (missing by class, last scan, last patched, pending reboot, failures) → status (compliant/at-risk/critical), reusing the Reports infra (CSV, scope); stale-scan / N+ missing-security alerts; AI `patch_report`. | Per-tenant compliance report + alert raise/resolve + CSV. |
| **7 — UI** | Patches tab (catalog + approve/decline, per-device missing, policy editor, compliance dashboard); a Patches sub-tab in the device drawer. Sliceable alongside each phase. | Click-through of catalog, approval, policy and compliance. |

## Cross-cutting

- **RBAC**: approve/install = `tech`/`admin`; policies = `admin`. Scope enforced on
  read and write.
- **Audit**: `patch.scan`, `patch.approve/decline`, `patch.install`,
  `patch.policy.*`, `reboot`.
- **Security**: the agent runs as SYSTEM/root (can install); server-side
  validation of identifiers (anti-injection), like the high-level tools.

## Open design decisions (to settle before coding)

1. **Long installs** (WUA) beyond the job timeout → async / progress streaming
   (Phase 3; affects the protocol).
2. **Metadata varies by OS**: CVE is rich on `dnf`, weak on `apt` / Windows-app /
   macOS → normalized, best-effort classification + a `source` field.
3. **Window vs. timezone**: maintenance window in the agent's TZ vs. the server.
4. **Safe reboot**: avoid rebooting during an active remote session / business
   hours.

## Suggested MVP cut

- **Functional MVP** = Phases **1 → 2 → 3** (scan + approve + install manually,
  with `apply_updates` in chat).
- **Automation** = Phases **4 → 5** (policies + reboot).
- **Visibility** = Phase **6** (+ **7** UI, sliced along the way).
