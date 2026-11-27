# Device operations profile

The Operations tab contains human-maintained reference information for one device. It follows Inventory, Jobs, Checks and Scheduled tasks. Existing Notes and inventory remain separate.

Purpose is limited to 2,000 Unicode characters. Applications, dependencies and backup notes each allow 8,000; the maintenance window allows 1,000. Text is plain text, with tabs and line breaks allowed but other control and invisible formatting characters rejected. Criticality is `unknown`, `low`, `normal`, `high` or `critical`. The maintenance timezone is empty or an IANA identifier (maximum 100 characters), never the server-local `Local` setting.

Maintenance text documents an operator's intentions. It neither schedules work nor authorizes commands, patching or restarts. Backup notes do not prove that backups work. A blank verification date means unverified. Operators enter `backup_verified_on` only after manually checking a backup; the value is a calendar date in `YYYY-MM-DD`, with no timezone conversion. The application does not perform backup verification.

`GET /api/devices/{id}/operations-profile` requires `devices.view` and device scope. `PUT` requires `devices.manage` and the same scope. An existing device without a saved profile returns empty fields, unknown criticality, a null backup date and version zero. A nonexistent device returns 404 for unrestricted callers; scoped callers cannot probe devices outside their scope.

PUT replaces the editable fields and requires a nonnegative `expected_version`. Clients must use the last returned version, including zero for initial creation. Concurrent creation or editing returns 409 to the losing writer. Reloading explicitly replaces a working copy; a conflict retains local edits. Unknown fields, duplicate JSON keys, invalid dates, spoofed metadata and bodies over 160,000 bytes are rejected. The server owns version, updated_at and updated_by. There is no write tool for AI.

Migration 041 adds `device_operations_profiles` with a cascading device foreign key. Each successful update and its `device.operational_profile.update` audit commit in one transaction. An audit failure rolls back the profile. The audit stores actor, target, hostname, version and changed field names, never full notes. Rejected writes are not audited as successful changes. Audit ordering uses the existing upstream migration 039; this feature does not replace the audit function or rewrite historical records.

AI inventory retains its existing `agent_id`, `data` and `collected_at` fields. The additional `operational_profile` wrapper identifies `source: operator_entered`, includes the profile as reference data and explicitly denies instruction or authorization authority. Scope is checked before any inventory or profile read. Missing profiles use defaults; database failures remain errors. Agent protocol and endpoint collection are unchanged.
