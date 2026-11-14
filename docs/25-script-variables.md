# Script variables and secrets

Script variables let one library script install products for different customers
without storing each customer's key in its source. This feature requires a server
and agent built with variable support (agent **0.11.0 or later**). It is separate
from interactive `OPENRMM_PARAM_*` parameters, whose values remain visible on jobs.
Do not use interactive parameters for credentials. Variables reach a device
when the saved script itself runs: from **Scripts ▸ Run on device**, from a
device's **Run script ▸ From library** while the script is unedited (the form
says it runs the saved script), or as a scheduled library task. **Run on device**
waits until unsaved changes in the editor are saved.

A one-off job from a device's **Run script** tab can also receive variables, but
only from someone who could bind them to a saved script: **Manage scripts** and
**Manage script variables** on an unrestricted user. For them, the tab shows a
**Script variables** picker; an edited copy of a library script keeps its names
there, and a script typed from scratch takes the ones picked. Such a job waits
for approval like any job, and the job card shows the approver which names it
receives, never their values, and marks it as a one-off script (from the
server's record, not its description, so it cannot pass for a saved library
script). Where the instance allows jobs without approval (Settings ▸ Configuration ▸ Jobs), a
person holding **Manage jobs** and **Manage configuration** may choose **Run
immediately**, with variables or a saved script's parameters as for any job. For anyone else, an edited copy, a pasted script and **Deploy
to many devices** carry no variables, and the console says so. Parameters
(`OPENRMM_PARAM_*`) only go with the saved script.

## Configure and run

1. Grant **Scripts → Manage script variables** (`scripts.variables`). Only the
   administrator receives it by default. Existing custom roles keep their grants.
2. Open **Scripts → Script variables**, create a name such as
   `HUNTRESS_ACCOUNT_KEY`, and mark it **Secret** before entering a credential.
3. Set a global value to reuse it across devices. Optionally override it for a
   client, site, department or individual device; configuring every level is not
   required. Inheritance, from least to most specific, is:
   **global → client → site → department → device**. Removing an override restores
   the next matching value; values from other clients are never a fallback.
4. Edit the library script and select the variables it may receive. The definition
   stores names only. Variable values are resolved for its target device at dispatch,
   including a delayed human-approved job or a scheduled library task.
5. Run the saved library script by ID. A run with variables or parameters waits
   for approval, unless the instance allows jobs without approval and the person
   holds `jobs.manage` and `settings.manage` and asks to skip it, as for any job.
   AI Chat and MCP never skip it. Existing explicitly released AI actions keep
   their separate autonomy checks; changing the declarations withdraws a release.

Names start with an ASCII letter or underscore and contain ASCII letters, digits
and underscores. Your choice of upper/lowercase is preserved: `customerKey` is a
valid name. Names differing only by letter case cannot coexist, because Windows
environment names are case-insensitive. The prefixes `RMM_`, `OPENRMM_` and
process-control names such as `PATH` are reserved regardless of case, and so are
names an interpreter or loader reads, because a value there decides what runs:
`PSModulePath`, `PSExecutionPolicyPreference`, `PS2`–`PS4`, `SHELLOPTS`,
`TMPDIR`, Windows locations such as `ProgramFiles` and `APPDATA`, and every name
starting with `COR_`, `CORECLR_`, `COMPLUS_`, `DOTNET_`, `APPDOMAIN_MANAGER_`,
`LD_`, `DYLD_`, `PYTHON`, `PERL5`, `RUBY`, `NODE_`, `GLIBC_`, `SSL_CERT_` or
`GIT_CONFIG`. On top of that list, the agent refuses a variable whose name, in
any case, the device's own environment already defines: the job fails before
the script runs and says which name to rename. Each script
can declare up to 32 names. Names and values have no field-specific character
maximum; values must be nonempty and cannot contain NUL or line breaks. Secrets
may be as short as one character and are masked in results too. Short secrets can
mask ordinary output more often, so avoid echoing credentials.

Request bodies remain limited to 1 MiB and the combined environment data has an
8 MiB safety budget. The target operating system also limits process arguments
and environment size; a value accepted by the vault may exceed that platform's
execution limit. A missing value, deleted definition, incapable agent or decryption
error fails the run rather than running without the requested value.

Only an unrestricted operator can create, change or delete global definitions or
set global values. A scoped variable manager can set/clear overrides only within
scopes they control. `scripts.view` exposes definitions for the editor; reading
values additionally requires `scripts.variables`. Secret values are always absent
from reads. Editing or binding a script that receives variables requires both
`scripts.manage` and `scripts.variables`. Approving an AI-proposed script with
variable bindings requires both permissions too; executing a reviewed saved script
does not grant the ability to retrieve its secrets.

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

CMD does not receive custom variables or built-in context. Its percent expansion
can turn a credential or device/client name into command syntax even when the
script is staged to a batch file. The API, job dispatch and agent reject CMD
variable payloads. Ordinary CMD scripts continue working without injected values;
use PowerShell for Windows scripts requiring variables.

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

The server snapshots the device scope revision before resolution and locks/rechecks
it at the final nonblocking send, including autonomous delivery. A concurrent move
(including away and back) fails the dispatch rather than sending former-client
credentials. Values already sent or cached on an endpoint cannot be revoked by a
move; rotate credentials as part of an ownership transfer.

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
| POST | `/api/script-variables/{id}/approve` | Approve a proposal from AI Chat or MCP; unrestricted principals only; audited `script_variable.approve`. Approving twice, or a definition a person created, is a **409** |

Create/update a library script with `variables: ["HUNTRESS_ACCOUNT_KEY"]`. Send an
empty array to detach all names. Omitting `variables` on update preserves its
current declaration. Run it via `POST /api/devices/{id}/run-script` with
`{ "script_id": "..." }`; callers never submit vault values with a run.

An ad hoc job asks for names with `POST /api/jobs` and
`{"agent_id": "...", "shell": "powershell", "script": "...", "variables": ["HUNTRESS_ACCOUNT_KEY"]}`.
It needs `jobs.create`, `scripts.manage` and `scripts.variables` and an
unrestricted principal (`403` otherwise), refuses CMD and unknown or proposed
names (`400`), checks that the agent is 0.11.0 or later and that every name has a
value for the device, and is created awaiting approval unless
`require_approval: false` comes from a caller who may skip approval (the
instance allows it, and `jobs.manage` and `settings.manage`), in which case it is
sent at once. The job stores the names, never a value, and its
`job.propose` audit entry lists them. AI Chat and MCP cannot ask for variables.

## AI Chat and MCP

An assistant can help define variables, but never sees or sets a value:

- `list_script_variables` (`scripts.view`) returns names, descriptions, the secret
  flag and whether a definition awaits review. No value, and no client, site or
  device that holds one, ever reaches the model.
- `propose_script_variable` (`scripts.variables`) creates a definition **awaiting
  review**, attributed to the assistant (`proposed_by` = `ai:<user>` or
  `mcp:<client>`). Until an unrestricted operator approves it in Scripts ▸ Script
  variables, it takes no value (**409**), no script can bind it, and no run
  resolves it. Rejecting is deleting it. Values are always set by a person in the
  console; a model is told never to ask for one in the chat.
- `propose_library_script` (`scripts.manage`) adds a script to the library
  **awaiting review**, in the category it names or *AI proposals*. It can bind
  approved variables only when the caller also holds `scripts.variables`. Until
  approved (`POST /api/scripts/{id}/approve`, audited `script.approve`), it cannot
  run (**409**), a scheduled task that points to it does not start, and it cannot
  be released for AI autonomy. A script approved and later released follows the
  usual hash-bound release rules ([23-ai-autonomy](23-ai-autonomy.md)).

Model-facing results for these three tools have a **64 KiB serialized JSON
budget**, including errors and proposal confirmations. Listing returns at most
200 definitions, clips descriptions to 1,024 Unicode characters and marks clipped
descriptions. Names longer than 4,096 bytes are omitted rather than shortened;
identifiers that are returned are exact. The result reports matching counts and
omissions: refine the search or use the console for large definitions. If a
proposal's metadata exceeds the budget, its ID and review status are retained;
do not repeat the mutation. These transport budgets do not limit stored names,
descriptions or values, or the console's variable API.

Both proposals are global definitions, so a principal confined to some clients
cannot make either, as in the REST API. Neither tool can approve, release or run
anything; a person reads the proposal first.

## Validation

Unit tests cover encryption compatibility, name validation, release hashes,
version gates, environment-only delivery, redaction failures, output truncation,
the durable outbox, and the console's write-only forms. CI additionally runs
Postgres upgrade/inheritance/RBAC and full router/job-lifecycle integration tests,
real Chrome create/bind/run UI flow, and Windows PowerShell execution plus CMD refusal. These tests use
synthetic keys. Real vendor installers and policies still need a staging trial.
