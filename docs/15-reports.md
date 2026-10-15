# 15 — Reports (fleet inventory)

Fleet-wide inventory reports — **software**, **hardware** and **machines** —
aggregated across the fleet and groupable/filterable by **Client → Site →
Department**. Computation is **deterministic, in the database** (SQL over the
inventory JSONB), never by the AI: the same store functions back both the REST
endpoints and the AI tool, so there is a single source of truth.

This complements per-device inspection: use `get_inventory` (or the device
detail screen) for **one** machine; use a report for an aggregate across a
client/site/department.

## Data source

Each agent upserts one `inventory` row (`agent_id` PK, `data JSONB`,
`collected_at`) — the **latest snapshot** (no history; see *Future work*). The
JSONB carries `cpu_model`, `cpu_cores`, `mem_total_bytes`, `platform`,
`platform_version`, `kernel_version`, `disks[]`, `network_interfaces[]`,
`software[]` (each `{name, version, manufacturer}`, collected per-OS) and a nested
`hardware{}` object (`manufacturer`, `system_model`, `serial_number`, `bios`,
`motherboard{}`, `storage_devices[]`, `gpus[]`). The hardware report flattens
that object — joining GPU and storage-device names into single cells (`gpu` /
`storage`) and, separately, **summing every storage device's `size_bytes`** into
a total-capacity `storage_bytes` column (rendered with a byte formatter). A GIN
index (`idx_inventory_data`) backs the JSONB lookups.

> The nested `hardware{}` object requires an agent build that collects it
> (`collectHardware`, agent ≥ 0.4.1). Older agents report only the top-level
> scalars + `disks`, so the manufacturer/model/serial/GPU/storage columns stay
> blank until the fleet self-updates (republish with `make agent-dist`).

## Reports

| Report | One row per | Columns |
|--------|-------------|---------|
| `software` | (package, machine) | name, version, manufacturer, hostname, client, site, department |
| `hardware` | machine with inventory | hostname, os, arch, manufacturer, model, serial_number, cpu_model, cpu_cores, mem_total_bytes, gpu, storage, storage_bytes, bios, platform, platform_version, kernel_version, ip, mac, client, site, department |
| `machines` | machine (online or not) | hostname, os, arch, status, agent_version, cpu_model, mem_total_bytes, ip, mac, client, site, department, last_seen, collected_at |

The UI labels the `hardware` tab **Hardware & System** and the `machines` tab
**Agents** (the report keys `hardware` / `machines` are unchanged). The Agents
tab is scoped to fleet-management columns (host, status, agent, client/site/
department, last seen); the per-machine detail (OS, arch, CPU, RAM, IP, MAC)
lives on Hardware & System. Both reports still *return* every field over REST —
only the column set each tab exposes to the picker differs.

`software`/`hardware` are based on `inventory` (only machines that have
reported); `machines` is based on `agents` (LEFT JOIN inventory), so a device
that never reported still appears. The machines `ip`/`mac` columns aggregate
every up, non-loopback network interface (`network_interfaces[]`), comma-joined —
so a multi-homed host shows all its addresses.

## REST API

```
GET /api/reports/software   ?scope=client|site|group&id=<uuid>
GET /api/reports/hardware   ?scope=client|site|group&id=<uuid>
GET /api/reports/machines   ?scope=client|site|group&id=<uuid>
```

- `scope`/`id` are optional; omit them for the whole in-scope fleet.
- Each returns `{ "rows": [ ... ] }`.
- Available to any authenticated principal (read-only); **CSV export is
  client-side** from this JSON (a browser `Blob` — nothing is written on the
  server or an endpoint), mirroring the AI chat report download.

### Access scope (enforced)

Every report is filtered to the caller's **per-user access scope**
(`user_scopes`) using the same predicate as the device list. The scope and the
`?scope=` filter are **ANDed**, so a restricted user filtering an out-of-scope
client gets an empty result — never a cross-tenant leak. Admin / unscoped
tokens see the whole fleet. (Verified end-to-end: a user scoped to one client
sees only that client's rows and 0 when filtering another.)

## AI / MCP tool

The same engine is exposed to the AI (and MCP clients) as `inventory_report` when
the caller has `reports.view`:

```
inventory_report(report_type: "software"|"hardware"|"machines",
                 mode?: "summary"|"rows", group_by?, match?,
                 scope_type?: "client"|"site"|"group", scope_id?: <uuid>)
export_inventory(report_type, scope_type?, scope_id?)
```

It calls the identical store functions with the caller's scope. By default
(`mode: summary`) it **counts in the database**, grouped by `group_by` (software:
name, version, manufacturer or client; hardware: os, manufacturer, model, platform
or client; machines: os, status, client, site, department or version) and
narrowed by `match` — the right answer to "how many machines have X". `mode: rows`
returns individual records, **capped at 1000** with a `truncated` flag, so a large
fleet cannot overflow the context window.

For the complete data the model calls `export_inventory`, which returns a signed
link to `GET /api/exports/csv`: the CSV streams from the database to the
operator's browser without passing through the model, and the link works only for
the account that asked, re-checked against its current scope. MCP and AI Chat do
not advertise or execute either tool without `reports.view`. Read-only: neither
creates a job.

## Frontend

The **Reports** tab (`openrmm/src/screens-reports.jsx`) has three sub-tabs
(Software / Hardware / Machines), a cascading **Client → Site → Department**
filter, a row search, a **Columns** picker (show/hide columns, persisted per tab
in `localStorage`), and a **Download CSV** button (shared `toCSV` /
`downloadText` helpers in `ui.jsx`). The CSV export and search honor the
currently visible columns.

## Future work — point-in-time history

Today only the latest snapshot is stored. A trend report ("software added/removed
over time", "RAM growth") would need an append-only `inventory_history` table
plus retention/pruning. The report functions are written to accept an `as_of`
parameter when/if that lands. Deferred — evaluate against the storage cost first.
