# Script variables and secrets

Script variables let one library script install products for different customers
without storing each customer's key in its source. This feature requires a server
and agent built with variable support (agent **0.11.0 or later**). It is separate
from interactive `OPENRMM_PARAM_*` parameters, whose values remain visible on jobs.
Do not use interactive parameters for credentials. Copying library text into an
ad hoc editor or a raw bulk deployment does not carry its vault bindings: use a
saved library run or a scheduled library task for scripts that require variables.

## Configure and run

1. Grant **Scripts → Manage script variables** (`scripts.variables`). Only the
   administrator receives it by default. Existing custom roles keep their grants.
2. Open **Scripts → Script variables**, create a name such as
   `HUNTRESS_ACCOUNT_KEY`, and mark it **Secret** before entering a credential.
3. Set a global or scoped value. Inheritance, from least to most specific, is:
   **global → client → site → department → device**. Removing an override restores
   the next matching value; values from other clients are never a fallback.
4. Edit the library script and select the variables it may receive. The definition
   stores names only. Variable values are resolved for its target device at dispatch,
   including a delayed human-approved job or a scheduled library task.
5. Run the saved library script by ID. A script with variables uses the approval
   path, just like a parameterized script. Existing explicitly released AI actions
   keep their separate autonomy checks; changing the declarations withdraws a release.

Names start with an uppercase letter, contain uppercase ASCII letters, digits and
underscores, and are at most 41 characters. The prefixes `RMM_`, `OPENRMM_` and
process-control names such as `PATH` are reserved. Each script can declare up to
32 names. Values must be nonempty, at most 4096 bytes, without NUL or line breaks;
secrets need at least 8 bytes. A missing value, deleted definition, incapable agent
or decryption error fails the run rather than running without the requested value.

Only an unrestricted operator can create, change or delete global definitions or
set global values. A scoped variable manager can set/clear overrides only within
scopes they control. `scripts.view` exposes definitions for the editor; reading
values additionally requires `scripts.variables`. Secret values are always absent
from reads. Editing or binding a script that receives variables requires both
`scripts.manage` and `scripts.variables`; executing a reviewed saved script does
not grant the ability to retrieve its secrets.

## Access values as environment data

PowerShell:

```powershell
Write-Host "Installing on $env:RMM_AGENT_HOSTNAME"
# Pass $env:HUNTRESS_ACCOUNT_KEY to the vendor's supported secure interface.
```

Bash/sh:

```bash
printf 'Installing for %s\n' "$RMM_CLIENT_NAME"
# Use "$HUNTRESS_ACCOUNT_KEY" as data, never with eval or sh -c.
```

Python:

```python
import os
print('Installing on', os.environ['RMM_AGENT_HOSTNAME'])
account_key = os.environ['HUNTRESS_ACCOUNT_KEY']
```

CMD scripts receive `%HUNTRESS_ACCOUNT_KEY%` through the environment. When variables
are present, the agent stages the unchanged script in a temporary batch file and
executes its path with `/D /Q /C`, rather than putting the expanded script in the
initial `cmd.exe` command line. Interactive `OPENRMM_PARAM_*` parameters remain
unsupported for CMD. Batch expansion, `call`, delayed expansion and child command
lines can still interpret or expose values: prefer PowerShell for credentials and
use the vendor's stdin, file or environment interface when supported.

Built-in context is injected automatically into job script executions on capable
agents. Older agents continue running ordinary scripts without this context.

| Variable | Value |
| --- | --- |
| `RMM_AGENT_ID` | Target agent ID |
| `RMM_AGENT_HOSTNAME` | Current device hostname |
| `RMM_CLIENT_ID` / `RMM_CLIENT_NAME` | Client ID / name |
| `RMM_SITE_ID` / `RMM_SITE_NAME` | Site ID / name |

Unassigned relationships produce empty strings. These variables cannot be replaced
by user-defined names. They apply to jobs, including ad hoc scripts and scheduled
tasks; monitoring `script` checks are a separate execution protocol and do not
receive secret variables. Scheduled library tasks accept saved variable bindings;
interactive parameter schemas still require an interactive run.

## Secret storage and limits

Secret values use the same AES-GCM format and instance key as settings. Preserve
`RMM_SETTINGS_ENCRYPTION_KEY` with your recovery material; restoring the database
without that key prevents decryption. A public variable can become secret in a
transaction that encrypts all its values. A secret cannot become public. Previous
public copies in backups, API responses or logs are not retroactively removed.

The server sends only the selected names' resolved values over the authenticated
agent connection. Jobs store the names, not plaintext values. An encrypted
per-job snapshot retains the secrets needed to mask a result after value rotation
or redispatch. The agent replaces exact secret matches in stdout/stderr **before
truncation and its durable result outbox**. The server masks again before storing
job history and withholds output if the redaction snapshot cannot be read or
opened. Audit entries record names, scopes and secret flags, never values.

Masking is not a sandbox or a data-loss prevention guarantee. A trusted script,
endpoint administrator or compromised endpoint can read its own environment;
script code can transform, split, encode or transmit a secret, or pass it to a
child's command line. Review scripts before binding credentials or releasing an
AI action, avoid shell reparsing, and do not echo credentials. The model receives
stored job results after masking, not the variable vault. Existing hardcoded keys
and interactive parameter histories are not automatically converted or cleaned.

## API

All endpoints require authentication. Write endpoints require `scripts.variables`.

| Method | Path | Body / behavior |
| --- | --- | --- |
| GET | `/api/script-variables` | Definitions; visible values for authorized managers; secrets omitted |
| POST | `/api/script-variables` | `{name, description, secret}`; no value |
| PUT | `/api/script-variables/{id}` | `{description, secret}`; immutable name, no secret downgrade |
| DELETE | `/api/script-variables/{id}` | Delete definition and overrides |
| PUT | `/api/script-variables/{id}/values` | `{scope_type, scope_id, value}`; write-only for secrets |
| DELETE | `/api/script-variables/{id}/values?scope_type=...&scope_id=...` | Remove one override |

Create/update a library script with `variables: ["HUNTRESS_ACCOUNT_KEY"]`. Send an
empty array to detach all names. Omitting `variables` on update preserves its
current declaration. Run it via `POST /api/devices/{id}/run-script` with
`{ "script_id": "..." }`; callers never submit vault values with a run.

## Validation

Unit tests cover encryption compatibility, name validation, release hashes,
version gates, environment-only delivery, redaction failures, output truncation,
the durable outbox, and the console's write-only forms. CI additionally runs
Postgres upgrade/inheritance/RBAC and full router/job-lifecycle integration tests,
real Chrome create/bind/run UI flow, and Windows CMD execution. These tests use
synthetic keys. Real vendor installers and policies still need a staging trial.
