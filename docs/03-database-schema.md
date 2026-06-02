# 03 — Database Schema

PostgreSQL. The canonical schema is embedded in the server
(`internal/store/schema.sql`) and applied idempotently on every start. A copy
lives at `db/schema.sql` for manual `psql` use.

## Tables

### `agents`
Managed endpoints.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | agent identity |
| hostname, os, arch, version | text | reported at enrollment |
| status | text | `online` / `offline` |
| token_hash | text | SHA-256 of the agent's auth token |
| group_id | uuid FK → device_groups | nullable |
| tags | text[] | free-form labels |
| rustdesk_id, mesh_id | text | remote-provider identifiers (nullable) |
| last_seen | timestamptz | heartbeat |
| created_at | timestamptz | |

### `device_groups`
Logical grouping of agents (id, name, created_at).

### `enroll_tokens`
One-time / limited-use enrollment tokens (token, uses_left, expires_at).

### `api_tokens`
Hashed API tokens for the UI/integrations (token_hash, name, role). The
bootstrap `RMM_ADMIN_TOKEN` is also accepted without a row.

### `jobs`
Units of work targeted at an agent.
| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | |
| agent_id | uuid FK → agents | cascade delete |
| type | text | `run_script` (extensible) |
| description | text | human summary |
| shell | text | `bash`/`sh`/`powershell`/`cmd` |
| script | text | source to run |
| status | text | `awaiting_approval`→`pending`→`sent`→`running`→`completed`/`failed`, or `rejected` |
| requires_approval | bool | true for AI proposals |
| created_by | text | actor (`admin`, `api`, `ai:<actor>`) |
| approved_by | text | nullable |
| exit_code | int | nullable until finished |
| stdout, stderr | text | result output |
| created_at, updated_at | timestamptz | |

### `inventory`
Latest snapshot per agent (`agent_id` PK, `data` jsonb, `collected_at`).

### `remote_sessions`
Brokered remote-access sessions (id, agent_id, provider, status, token,
connect_url, created_by, expires_at).

### `audit_log`
Immutable record of sensitive actions (actor, action, target, detail jsonb,
created_at). Actions include `job.propose`, `job.approve`, `job.reject`,
`job.result`, `remote.start`.

### `chat_sessions` / `chat_messages`
Conversations with the AI. `chat_messages` stores `role`, `content`,
`tool_calls` (jsonb), `tool_call_id`, `name` — mirroring the OpenAI-compatible
schema so history can be replayed to the model verbatim.

## Relationships

```
device_groups 1───* agents 1───* jobs
                     agents 1───1 inventory
                     agents 1───* remote_sessions
chat_sessions 1───* chat_messages
```
