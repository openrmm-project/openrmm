import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import WebSocket from 'ws';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const types = { '.css': 'text/css', '.html': 'text/html', '.js': 'text/javascript', '.png': 'image/png', '.woff2': 'font/woff2' };
const permissions = ['ai_chat.use', 'alerts.view', 'alerts.manage', 'jobs.view', 'jobs.create', 'jobs.manage', 'checks.view', 'checks.manage', 'tasks.view', 'tasks.manage', 'policies.view', 'policies.manage', 'scripts.view', 'scripts.manage', 'scripts.variables', 'patches.view', 'patches.manage', 'patches.policies.manage', 'devices.view', 'devices.manage', 'devices.delete', 'devices.remote', 'devices.files', 'devices.terminal', 'clients.view', 'clients.manage', 'reports.view', 'audit.view', 'settings.manage', 'enrollment.manage', 'api_tokens.manage', 'users.manage', 'roles.manage'];
const user = { id: 'user-1', username: 'smoke-admin', role: 'admin', permissions, created_at: '2026-01-01T00:00:00Z' };
const operationsProfile = { purpose: '', applications: '', dependencies: '', criticality: 'unknown', maintenance_window: '', maintenance_timezone: '', backup_notes: '', backup_verified_on: null, version: 0, updated_at: null, updated_by: '' };
let operationsConflict = true;
let operationsViewer = false;
const apiRequests = [];
// A pre-existing direct check to edit and then share through a policy.
const sharedPolicy = { id: 'pol-shared-e2e', name: 'Backup coverage', enabled: true, description: '' };
const sharedCheck = { id: 'check-share-e2e', name: 'Existing memory check', type: 'memory', config: {}, enabled: true,
  interval_sec: 300, attachments: [{ agent_id: 'dev-1', name: 'SMOKE-PC' }] };
const editProposal = { id: 'edit-check-e2e', kind: 'check', target_id: sharedCheck.id, target_name: sharedCheck.name,
  status: 'pending', reason: 'Make the memory check easier to identify', proposed_by: 'ai:smoke', created_at: '2026-11-12T00:00:00Z' };

const ruleChecks = [];
let overrideManager = true;
const extraChecks = Array.from({ length: 60 }, (_, i) => ({ id: 'sample-rule-' + (i + 1), name: 'Monitor ' + String(i + 1).padStart(2, '0'), type: 'ping', agent_id: 'dev-1', config: { host: '127.0.0.1' }, enabled: true, interval_sec: 300 }));
const ruleDefaults = Object.fromEntries(['connectivity', 'job_failure', 'patch_failure', 'reboot_pending', 'agent_outdated'].map((type) => [type, { type, config: type === 'connectivity' ? { mode: 'alert', severity: 'error', after_min: 3 } : { mode: 'ignore', severity: 'warning' } }]));
let rulePrefs = { mode: 'all', include_defaults: true, check_ids: [] };

const scriptVariables = [];
// Enough definitions that the Variables tab and the editor's picker must scroll and be searched.
const extraVariables = Array.from({ length: 199 }, (_, i) => ({ id: 'variable-extra-' + i, name: 'SAMPLE_VAR_' + String(i).padStart(3, '0'), description: 'Synthetic variable ' + i, secret: i % 2 === 0, values: [],
  // One of them is a proposal from an assistant, awaiting review.
  ...(i === 198 ? { pending: true, proposed_by: 'mcp:claude' } : {}) }));
const libraryScripts = [{ id: 'script-e2e', name: 'Variable installer', description: '', category: 'Tests', shell: 'sh', body: 'printf \'%s\' "$customerKey"', params: [], variables: [] },
  // Proposed by an assistant over MCP: reviewed and approved in the console.
  { id: 'script-proposal', name: 'Proposed cleanup', description: 'Removes stale temp files', category: 'AI proposals', shell: 'sh', body: 'find /tmp -mtime +7 -delete', params: [], variables: [], pending: true, proposed_by: 'mcp:claude' },
  // Deployed to many devices from the library, with a parameter and a variable.
  { id: 'script-bulk', name: 'Bulk installer', description: '', category: 'Tests', shell: 'sh', body: 'install --site "$OPENRMM_PARAM_SITE" --key "$customerKey"',
    params: [{ name: 'site', description: 'Which office', kind: 'enum', values: ['hq', 'branch'] }], variables: ['customerKey'] },
  // An assistant's script nobody reviews in these flows: never deployable.
  { id: 'script-unreviewed', name: 'Unreviewed deploy', description: '', category: 'AI proposals', shell: 'sh', body: 'echo unreviewed', params: [], variables: [], pending: true, proposed_by: 'mcp:claude' }];

// An assistant's policy, patch policy and device task, all switched off and
// awaiting review. The first activation of the policy is refused as stale (the
// assistant added a task while the review was open); the second goes through.
const aiPolicy = { id: 'pol-ai', name: 'AI workstations', description: 'Proposed over MCP', enabled: false, pending: true, proposed_by: 'mcp:claude', created_at: '2026-11-12T00:00:00Z' };
let policyReviews = 0;
const policyReviewTasks = [
  { id: 'pol-task-1', policy_id: 'pol-ai', name: 'Clean temp', shell: 'bash', script: 'find /tmp -mtime +7 -delete', schedule_kind: 'interval', interval_sec: 3600, run_at_minutes: [], timezone: 'UTC', enabled: false, proposed_by: 'mcp:claude' },
  { id: 'pol-task-2', policy_id: 'pol-ai', name: 'Second task', shell: 'bash', script: 'uptime', schedule_kind: 'interval', interval_sec: 7200, run_at_minutes: [], timezone: 'UTC', enabled: false, proposed_by: 'mcp:claude' },
];
const aiPatchPolicy = { id: 'patch-ai', name: 'AI weekend', enabled: false, pending: true, proposed_by: 'mcp:claude', auto_approve: ['security'], auto_approve_age_days: 3,
  exclusions: [], window_days: [0, 6], window_start_min: 120, window_end_min: 360, timezone: 'America/Sao_Paulo', reboot: 'always' };
const aiTask = { id: 'task-ai', agent_id: 'dev-1', name: 'AI cleanup task', shell: 'bash', script: 'find /var/tmp -mtime +30 -delete', schedule_kind: 'interval',
  interval_sec: 3600, run_at_minute: 0, days_of_week: 0, run_at_minutes: [], timezone: 'UTC', enabled: false, proposed_by: 'mcp:claude' };

// The fleet: who is logged in where (Devices ▸ Logged-in user), with a device
// no one is logged in to and one offline with its last report.
const fleet = [
  { id: 'dev-1', hostname: 'SMOKE-PC', description: 'Smoke device', os: 'Linux', arch: 'amd64', version: '1.0.0', status: 'online', tags: [], last_seen: '2026-01-01T00:00:00Z', logged_in_user: 'ACME\\zoe', logged_in_user_since: '2026-01-01T00:00:00Z', last_boot_at: '2026-01-01T00:00:00Z' },
  { id: 'dev-2', hostname: 'ALPHA-WS', description: '', os: 'windows', arch: 'amd64', version: '0.11.0', status: 'online', tags: [], last_seen: '2026-01-01T00:00:00Z', logged_in_user: 'AzureAD\\Ana', last_boot_at: '2025-12-01T00:00:00Z' },
  { id: 'dev-3', hostname: 'BETA-SRV', description: '', os: 'windows', arch: 'amd64', version: '0.11.0', status: 'offline', tags: [], last_seen: '2025-12-31T00:00:00Z', logged_in_user: '' },
];

// Devices ▸ a device ▸ Jobs: one finished with output, one waiting for approval.
const deviceJobs = [
  { id: 'djob-done', agent_id: 'dev-1', status: 'completed', description: 'Disk report', created_by: 'smoke-admin', created_at: '2026-01-01T00:00:00Z', exit_code: 0, stdout: 'olá from SMOKE-PC\n', stderr: '' },
  { id: 'djob-wait', agent_id: 'dev-1', status: 'awaiting_approval', description: 'Reboot', created_by: 'smoke-admin', created_at: '2026-01-01T00:00:00Z', exit_code: null, stdout: '', stderr: '' },
];

// Patches > Compliance: descriptions belong to devices, counts sort as
// numbers, and dates with offsets must be compared by their actual instant.
const complianceRows = [
  { agent_id: 'dev-1', hostname: 'SMOKE-PC', description: 'Room 10', os: 'linux', status: 'critical', missing_total: 10, missing_security: 10, approved: 2, failed: 0, reboot_pending: false, last_scan: '2026-01-01T00:00:00Z' },
  { agent_id: 'dev-2', hostname: 'ALPHA-WS', description: 'Room 2', os: 'windows', status: 'at_risk', missing_total: 2, missing_security: 0, approved: 1, failed: 0, reboot_pending: false, last_scan: '2026-01-01T01:00:00+02:00' },
  { agent_id: 'dev-3', hostname: 'BETA-SRV', description: '', os: 'windows', status: 'unknown', missing_total: 0, missing_security: 0, approved: 0, failed: 0, reboot_pending: false },
];

// Reports ▸ Hardware & System: the last logged-in user, kept after logoff; one
// is endpoint text that would be a formula in a spreadsheet.
const hardwareRows = [
  { hostname: 'SMOKE-PC', os: 'linux', arch: 'amd64', cpu_model: 'Test CPU', client: 'Acme', last_logged_in_user: 'ACME\\maria', last_logged_in_user_at: '2026-01-01T00:00:00Z' },
  { hostname: 'ALPHA-WS', os: 'windows', arch: 'amd64', cpu_model: 'Test CPU', client: 'Acme', last_logged_in_user: '@SUM(1+1)', last_logged_in_user_at: '2026-01-01T00:00:00Z' },
  { hostname: 'BETA-SRV', os: 'windows', arch: 'amd64', cpu_model: 'Test CPU', client: 'Acme', last_logged_in_user: '' },
];

// Policy targeting (RFC #258): the device's role, and the assistant policy's
// assignments and exceptions as the console changes them.
const smokeRole = { os_role: '', role_override: '' };
const smokeDevice = () => ({ ...fleet[0], os: 'linux', ...smokeRole, role: smokeRole.role_override || smokeRole.os_role });
const policyAssignments = [{ id: 'asg-1', policy_id: 'pol-ai', scope_type: 'agent', scope_id: 'dev-1', os: '', role: '' }];
const policyExclusions = [];
// The assistant's patch policy, as its searchable pickers change it.
const patchAssignments = [{ id: 'pasg-1', policy_id: 'patch-ai', scope_type: 'agent', scope_id: 'dev-1', os: '', role: '' }];
const patchExclusions = [];
// Alerts > Silences & maintenance: one window of each state the dialog shows.
const hourFromNow = (h) => new Date(Date.now() + h * 3600 * 1000).toISOString();
const suppressions = [
  { id: 'sup-active', kind: 'maintenance', scope_type: 'agent', scope_id: 'dev-1', starts_at: hourFromNow(-1), ends_at: hourFromNow(2), reason: 'Nightly patching', created_by: 'user-1', created_at: hourFromNow(-1) },
  { id: 'sup-canceled', kind: 'silence', scope_type: 'agent', scope_id: 'dev-1', starts_at: hourFromNow(-3), ends_at: hourFromNow(3), reason: 'Called off', created_by: 'user-1', created_at: hourFromNow(-3), canceled_at: hourFromNow(-2), canceled_by: 'user-1' },
  { id: 'sup-ended', kind: 'silence', scope_type: 'agent', scope_id: 'dev-1', starts_at: hourFromNow(-5), ends_at: hourFromNow(-4), reason: 'Old window', created_by: 'user-1', created_at: hourFromNow(-5) },
];
const suppressionQueries = [];

const remoteConfig = {
  ai: { provider: 'openai-compatible', base_url: '', model: '', api_key_set: true, configured: true },
  remote: { default: 'native', providers: ['native', 'meshcentral', 'rustdesk'], meshcentral_enabled: true, meshcentral_url: 'https://mesh.example.invalid', meshcentral_user: 'admin', meshcentral_token_set: true, meshcentral_shared_account: true, meshcentral_match_hostname: false, rustdesk_enabled: false, guest_access: false, e2e_policy: 'preferred' },
  jobs: { allow_unapproved: false }, server: { version: 'e2e', base_url: 'https://agents.example.invalid' },
};

function json(res, status, body) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    const pathname = url.pathname;

    if (pathname.startsWith('/api/')) {
      const token = (req.headers.authorization || '').replace(/^Bearer\s+/, '');
      let payload;
      if (req.method === 'POST' || req.method === 'PUT') {
        let raw = '';
        for await (const chunk of req) raw += chunk;
        if (raw) payload = JSON.parse(raw);
      }
      apiRequests.push({ method: req.method, pathname, token, payload });
      if (!token) return json(res, 401, { error: 'missing bearer token' });
      if (token === 'expire-token') return json(res, 401, { error: 'expired token' });
      if (pathname === '/api/me') return json(res, 200, operationsViewer ? { ...user, role: 'readonly', permissions: ['devices.view'] } : user);
      if (pathname === '/api/script-variables') {
        if (req.method === 'POST') {
          const item = { id: 'variable-e2e', ...payload, values: [] }; scriptVariables.push(item);
          return json(res, 201, item);
        }
        return json(res, 200, { variables: [...scriptVariables, ...extraVariables] });
      }
      if (pathname === '/api/script-variables/variable-e2e/values' && req.method === 'PUT') {
        const item = scriptVariables[0];
        item.values = [{ scope_type: payload.scope_type, scope_id: payload.scope_id, set: true, ...(item.secret ? {} : { value: payload.value }) }];
        return json(res, 200, { name: item.name, set: true });
      }
      if (pathname === '/api/scripts') return json(res, 200, { scripts: libraryScripts });
      if (pathname === '/api/scripts/script-proposal/approve' && req.method === 'POST') {
        Object.assign(libraryScripts[1], { pending: false, approved_by: user.id }); return json(res, 200, libraryScripts[1]);
      }
      if (pathname === '/api/script-variables/variable-extra-198/approve' && req.method === 'POST') {
        Object.assign(extraVariables[198], { pending: false, approved_by: user.id }); return json(res, 200, { id: 'variable-extra-198', name: extraVariables[198].name });
      }
      if (pathname === '/api/script-categories') return json(res, 200, { categories: [{ name: 'Tests', count: 1 }] });
      if (pathname === '/api/scripts/script-e2e' && req.method === 'PUT') {
        Object.assign(libraryScripts[0], payload); return json(res, 200, libraryScripts[0]);
      }
      if (pathname === '/api/devices/dev-1/operations-profile') {
        if (req.method === 'PUT') {
          if (operationsViewer) return json(res, 403, { error: 'read only' });
          if (operationsConflict) { operationsConflict = false; operationsProfile.purpose = 'Concurrent operator edit'; operationsProfile.version++; return json(res, 409, { error: 'conflict' }); }
          if (payload.expected_version !== operationsProfile.version) return json(res, 409, { error: 'conflict' });
          const { expected_version, ...fields } = payload;
          Object.assign(operationsProfile, fields, { version: expected_version + 1, updated_at: '2026-11-20T10:00:00Z', updated_by: 'smoke-admin' });
          await delay(150);
        }
        return json(res, 200, operationsProfile);
      }
      if (pathname === '/api/devices/dev-2/operations-profile') return json(res, 403, { error: 'outside scope' });
      if (pathname === '/api/devices/dev-2') return json(res, 200, fleet[1]);
      if (pathname === '/api/devices/dev-1' && req.method === 'GET') return json(res, 200, smokeDevice());
      if (pathname === '/api/devices/dev-1/role' && req.method === 'PUT') {
        if (!['', 'workstation', 'server'].includes(payload?.role)) return json(res, 400, { error: 'invalid role' });
        smokeRole.role_override = payload.role;
        return json(res, 200, smokeDevice());
      }
      if (pathname === '/api/policies/pol-ai/exclusions') {
        if (req.method === 'POST') policyExclusions.push({ agent_id: payload.agent_id, hostname: 'SMOKE-PC', created_by: user.id, created_at: '2026-11-12T00:00:00Z' });
        return json(res, req.method === 'POST' ? 201 : 200, { exclusions: policyExclusions });
      }
      if (pathname === '/api/policies/pol-ai/exclusions/dev-1' && req.method === 'DELETE') {
        policyExclusions.splice(policyExclusions.findIndex((e) => e.agent_id === 'dev-1'), 1);
        return json(res, 200, { exclusions: policyExclusions });
      }
      if (pathname === '/api/devices/dev-1/run-script' && req.method === 'POST') return json(res, 201, { id: 'variable-job-e2e', status: 'awaiting_approval', requires_approval: true });
      if (pathname === '/api/checks') {
        if (req.method === 'POST') {
          const check = { id: ruleChecks.length ? 'service-e2e-' + ruleChecks.length : 'rule-e2e', ...payload };
          ruleChecks.push(check);
          return json(res, 201, check);
        }
        const policy = url.searchParams.get('policy_id');
        const checks = [...ruleChecks, ...extraChecks, sharedCheck];
        return json(res, 200, { checks: policy ? checks.filter((c) => c.attachments?.some((a) => a.policy_id === policy)) : checks });
      }
      if (pathname === '/api/edit-proposals') {
        return json(res, 200, { proposals: url.searchParams.get('kind') === 'check' && editProposal.status === 'pending' ? [editProposal] : [] });
      }
      if (pathname === '/api/edit-proposals/edit-check-e2e') {
        return json(res, 200, { proposal: editProposal, current: { fields: { name: sharedCheck.name, interval_sec: 300 } },
          proposed: { fields: { name: 'Reviewed memory check', interval_sec: 300 } }, stale: false });
      }
      if (pathname === '/api/edit-proposals/edit-check-e2e/apply' && req.method === 'POST') {
        if (editProposal.status !== 'pending') return json(res, 409, { error: 'already decided' });
        editProposal.status = 'applied';
        sharedCheck.name = 'Reviewed memory check';
        return json(res, 200, { status: 'applied', steps: ['update'] });
      }
      if (pathname === '/api/checks/check-share-e2e/attachments' && req.method === 'POST') {
        if (payload?.policy_id !== sharedPolicy.id) return json(res, 400, { error: 'unexpected target' });
        if (sharedCheck.attachments.some((a) => a.policy_id === sharedPolicy.id)) return json(res, 409, { error: 'already attached' });
        sharedCheck.attachments.push({ policy_id: sharedPolicy.id, name: sharedPolicy.name });
        return json(res, 200, sharedCheck);
      }
      if (pathname === '/api/policies') return json(res, 200, { policies: [aiPolicy, sharedPolicy] });
      if (pathname === '/api/policies/pol-shared-e2e/assignments') return json(res, 200, { assignments: [] });
      if (pathname === '/api/policies/pol-shared-e2e/exclusions') return json(res, 200, { exclusions: [] });
      if (pathname.startsWith('/api/checks/') && req.method === 'PUT') { const check = ruleChecks.find((c) => c.id === pathname.split('/').pop()); Object.assign(check, payload); return json(res, 200, check); }
      if (pathname === '/api/policies/pol-ai/assignments') {
        if (req.method === 'POST') {
          const a = { id: 'asg-' + (policyAssignments.length + 1), policy_id: 'pol-ai', ...payload };
          policyAssignments.push(a);
          return json(res, 201, a);
        }
        return json(res, 200, { assignments: policyAssignments });
      }
      if (pathname === '/api/policies/pol-ai/review') {
        policyReviews++;
        return json(res, 200, { fingerprint: 'fp-' + policyReviews, policy: aiPolicy, assignments: [{ id: 'asg-1', policy_id: 'pol-ai', scope_type: 'agent', scope_id: 'dev-1' }],
          tasks: policyReviews === 1 ? policyReviewTasks.slice(0, 1) : policyReviewTasks, checks: ruleChecks.filter((c) => c.attachments?.some((a) => a.policy_id === 'pol-ai')), pending_tasks: policyReviews === 1 ? 1 : 2, pending_checks: 0 });
      }
      if (pathname === '/api/policies/pol-ai/activate' && req.method === 'POST') {
        if (payload?.fingerprint !== 'fp-' + policyReviews || policyReviews < 2) return json(res, 409, { error: 'conflict: it changed after this review was opened; review it again' });
        Object.assign(aiPolicy, { enabled: true, pending: false, approved_by: user.id });
        return json(res, 200, { policy: aiPolicy, approved_policy: true, approved_tasks: 2, approved_checks: 0 });
      }
      if (pathname === '/api/patch-policies') return json(res, 200, { policies: [aiPatchPolicy] });
      if (pathname === '/api/patch-policies/patch-ai/assignments') {
        if (req.method === 'POST') { const item = { id: 'pasg-' + (patchAssignments.length + 1), policy_id: 'patch-ai', ...payload }; patchAssignments.push(item); return json(res, 201, item); }
        return json(res, 200, { assignments: patchAssignments });
      }
      if (pathname === '/api/patch-policies/patch-ai/exclusions') {
        if (req.method === 'POST') { if (payload?.agent_id !== 'dev-1') return json(res, 404, { error: 'no such device' }); patchExclusions.push({ agent_id: 'dev-1', hostname: 'SMOKE-PC' }); }
        return json(res, req.method === 'POST' ? 201 : 200, { exclusions: patchExclusions });
      }
      if (pathname === '/api/patch-policies/patch-ai/exclusions/dev-1' && req.method === 'DELETE') {
        const i = patchExclusions.findIndex((e) => e.agent_id === 'dev-1');
        if (i < 0) return json(res, 404, { error: 'the device is not excluded from this policy' });
        patchExclusions.splice(i, 1);
        return json(res, 200, { exclusions: patchExclusions });
      }
      if (pathname.startsWith('/api/patch-policies/assignments/') && req.method === 'DELETE') {
        const i = patchAssignments.findIndex((a) => a.id === pathname.split('/').pop());
        if (i < 0) return json(res, 404, { error: 'not found' });
        patchAssignments.splice(i, 1);
        return json(res, 200, { ok: true });
      }
      if (pathname === '/api/patch-policies/patch-ai/review') return json(res, 200, { fingerprint: 'pfp', policy: aiPatchPolicy, assignments: [{ id: 'pasg-1', policy_id: 'patch-ai', scope_type: 'agent', scope_id: 'dev-1' }] });
      if (pathname === '/api/patch-policies/patch-ai/activate' && req.method === 'POST') {
        if (payload?.fingerprint !== 'pfp') return json(res, 409, { error: 'stale' });
        Object.assign(aiPatchPolicy, { enabled: true, pending: false, approved_by: user.id });
        return json(res, 200, aiPatchPolicy);
      }
      if (pathname === '/api/tasks') return json(res, 200, { tasks: url.searchParams.get('policy_id') ? [] : [aiTask] });
      if (pathname === '/api/tasks/task-ai' && req.method === 'PUT') {
        Object.assign(aiTask, payload, { approved_by: user.id }); return json(res, 200, aiTask);
      }
      if (pathname === '/api/alert-defaults') return json(res, 200, { defaults: ruleDefaults, can_manage: overrideManager });
      if (pathname.startsWith('/api/alert-defaults/') && req.method === 'PUT') {
        const type = pathname.split('/').at(-1);
        ruleDefaults[type] = { type, config: payload };
        return json(res, 200, ruleDefaults[type]);
      }
      if (pathname === '/api/me/notifications') {
        if (req.method === 'PUT') rulePrefs = payload.rules;
        return json(res, 200, { email: 'e2e@example.invalid', delivery_configured: true, subscriptions: [{ event_type: 'agent_offline', enabled: true }], rules: rulePrefs, available_rules: [...ruleChecks, ...extraChecks].map(({ id, name, type }) => ({ id, name, type })) });
      }
      if (pathname === '/api/config') {
        if (req.method === 'PUT') {
          if (payload?.remote?.default && !['native', 'meshcentral', 'rustdesk'].includes(payload.remote.default)) return json(res, 400, { error: 'invalid remote provider' });
          if (payload?.remote?.default === 'rustdesk' && !(payload.remote.rustdesk_enabled ?? remoteConfig.remote.rustdesk_enabled)) return json(res, 400, { error: 'RustDesk must be enabled to be the default remote provider' });
          const { meshcentral_token_key, ...remote } = payload.remote || {};
          Object.assign(remoteConfig.remote, remote);
          if (meshcentral_token_key?.trim()) remoteConfig.remote.meshcentral_token_set = true;
        }
        return json(res, 200, remoteConfig);
      }
      if (pathname === '/api/devices') return json(res, 200, { devices: fleet });
      if (pathname === '/api/reports/compliance') return json(res, 200, { rows: complianceRows });
      if (pathname === '/api/devices/dev-3/patches/scan' && req.method === 'POST') {
        return json(res, 503, { error: 'device is offline', code: 'device_offline' });
      }
      if (/^\/api\/devices\/dev-[123]\/patches\/scan$/.test(pathname) && req.method === 'POST') return json(res, 200, { ok: true });
      if (pathname === '/api/reports/hardware') return json(res, 200, { rows: hardwareRows });
      if (pathname === '/api/jobs/bulk' && req.method === 'POST') {
        return json(res, 201, { batches: [{ client_id: 'client-a', batch_id: 'batch-a', count: 1 }, { client_id: 'client-b', batch_id: 'batch-b', count: 1 }], companies: 2, count: 2 });
      }
      if (pathname === '/api/jobs') return json(res, 200, { jobs: url.searchParams.get('agent_id') === 'dev-1' ? deviceJobs : [] });
      if (pathname === '/api/audit') {
        const entries = Number(url.searchParams.get('limit')) >= 50 ? [
          { id: 'audit-snapshot', actor: user.id, action: 'alert.ack', target: 'alert-target-1', detail: { hostname: '=1+1' }, created_at: '2026-11-10T12:00:00Z' },
          { id: 'audit-legacy', actor: user.id, action: 'alert.resolve', target: 'alert-target-2', detail: { version: 2 }, target_label: 'ORDINARY-HOST', created_at: '2026-11-10T12:00:00Z' },
        ] : [];
        return json(res, 200, { entries, total: entries.length });
      }
      if (pathname === '/api/alert-suppressions' && req.method === 'GET') {
        suppressionQueries.push(url.search);
        const all = url.searchParams.get('active') === 'false';
        return json(res, 200, { suppressions: suppressions.filter((s) => all || (!s.canceled_at && new Date(s.ends_at) > new Date())) });
      }
      if (pathname.startsWith('/api/alert-suppressions/') && req.method === 'DELETE') {
        const row = suppressions.find((s) => s.id === pathname.split('/').pop() && !s.canceled_at);
        if (!row) return json(res, 404, { error: 'suppression not found' });
        Object.assign(row, { canceled_at: new Date().toISOString(), canceled_by: user.id });
        return json(res, 200, { status: 'canceled' });
      }
      if (pathname === '/api/alerts') return json(res, 200, { alerts: [] });
      if (pathname === '/api/sites') return json(res, 200, { sites: [] });
      if (pathname === '/api/clients') return json(res, 200, { clients: [] });
      if (pathname === '/api/chat/sessions') return json(res, 200, { sessions: [] });
      return json(res, 200, {});
    }

    if (pathname === '/auth-config.js') {
      const token = url.searchParams.get('expired') === '1' ? 'expire-token' : 'smoke-token';
      res.setHeader('Content-Type', 'text/javascript');
      res.end(`window.__API_BASE__=location.origin;addEventListener('error',(event)=>document.documentElement.dataset.smokeError=event.message);addEventListener('unhandledrejection',(event)=>document.documentElement.dataset.smokeError=String(event.reason));sessionStorage.setItem('rmm-ai-auth',${JSON.stringify(JSON.stringify({ token, user, role: 'admin', permissions }))});const route=new URLSearchParams(location.search).get('route');if(route){const timer=setInterval(()=>{const button=route==='Open navigation'?document.querySelector('[aria-label="Open navigation"]'):[...document.querySelectorAll('button')].find((el)=>el.textContent.trim()===route);if(button){button.click();clearInterval(timer)}},25)}setTimeout(()=>{document.documentElement.dataset.smokeWidth=innerWidth+':'+document.documentElement.scrollWidth},900);`);
      return;
    }

    if (pathname === '/authenticated/' || pathname === '/expired/') {
      const html = await readFile(path.join(root, 'index.html'), 'utf8');
      const expired = pathname === '/expired/' ? '?expired=1' : '';
      res.setHeader('Content-Type', 'text/html');
      res.end(html.replace('/config.js', '/auth-config.js' + expired));
      return;
    }

    const requested = pathname === '/' ? 'index.html' : pathname.slice(1);
    const file = path.resolve(root, requested);
    assert.ok(file.startsWith(root + path.sep));
    const info = await stat(file);
    if (!info.isFile()) throw new Error('not a file');
    res.setHeader('Content-Type', types[path.extname(file)] || 'application/octet-stream');
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.setHeader('Content-Type', 'text/plain');
    res.end('not found');
  }
});

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const { port } = server.address();
const baseURL = `http://127.0.0.1:${port}`;

// The console follows the browser's language, and headless Chrome takes that
// from the environment on Linux: on a pt_BR machine every assertion below read
// Portuguese and failed. The checks are written in English, so pin it.
const chromeEnv = { ...process.env, LANG: 'en_US.UTF-8', LANGUAGE: 'en_US', LC_ALL: 'en_US.UTF-8' };

function runChrome(chrome, url, width = 1280, height = 800) {
  return new Promise((resolve, reject) => {
    const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--lang=en-US', `--window-size=${width},${height}`, '--virtual-time-budget=1800', '--dump-dom', url], { stdio: ['ignore', 'pipe', 'pipe'], env: chromeEnv });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (data) => { stdout += data; });
    child.stderr.on('data', (data) => { stderr += data; });
    child.on('error', reject);
    child.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function freePort() {
  const probe = createServer();
  await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve));
  const { port } = probe.address();
  await new Promise((resolve) => probe.close(resolve));
  return port;
}

async function runMobileChrome(chrome, url, { width = 375, flow } = {}) {
  const port = await freePort();
  const profile = await mkdtemp(path.join(os.tmpdir(), 'openrmm-smoke-'));
  const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--lang=en-US', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'], env: chromeEnv });
  let chromeErrors = '';
  child.stderr.on('data', (data) => { chromeErrors += data; });
  let socket;
  try {
    let ready = false;
    for (let attempt = 0; attempt < 50; attempt++) {
      try { ready = (await fetch(`http://127.0.0.1:${port}/json/version`)).ok; } catch {}
      if (ready) break;
      await delay(50);
    }
    assert.ok(ready, `Chrome DevTools did not start: ${chromeErrors}`);
    const target = await (await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' })).json();
    socket = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { socket.once('open', resolve); socket.once('error', reject); });
    let commandId = 0;
    const pending = new Map();
    const exceptions = [];
    socket.on('message', (raw) => {
      const message = JSON.parse(raw.toString());
      if (message.id && pending.has(message.id)) {
        const { resolve, reject } = pending.get(message.id);
        pending.delete(message.id);
        if (message.error) reject(new Error(message.error.message)); else resolve(message.result);
      } else if (message.method === 'Runtime.exceptionThrown') {
        exceptions.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text);
      }
    });
    const command = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++commandId;
      pending.set(id, { resolve, reject });
      socket.send(JSON.stringify({ id, method, params }));
    });
    await command('Page.enable');
    await command('Runtime.enable');
    const metrics = (requested) => command('Emulation.setDeviceMetricsOverride', { width: requested, height: 812, deviceScaleFactor: 1, mobile: width === 375 });
    await metrics(width);
    await command('Page.navigate', { url });
    await delay(250);
    // Headless Chrome can report a width a couple of pixels off, and an early
    // sample is not the settled page. Correct until innerWidth matches, then
    // record that width ourselves: the page's own timer can run first.
    let requested = width;
    for (let attempt = 0; attempt < 3; attempt++) {
      const reading = await command('Runtime.evaluate', { expression: 'innerWidth', returnByValue: true });
      const actual = reading.result.value;
      if (actual === width) break;
      if (!Number.isFinite(actual)) break;
      requested += width - actual;
      await metrics(requested);
      await delay(100);
    }
    await delay(1750);
    await command('Runtime.evaluate', {
      expression: 'document.documentElement.dataset.smokeWidth=innerWidth+":"+document.documentElement.scrollWidth',
      returnByValue: true,
    });
    if (flow) {
      const result = await command('Runtime.evaluate', { expression: `(${flow.toString()})()`, returnByValue: true, awaitPromise: true });
      assert.equal(result.exceptionDetails, undefined, JSON.stringify(result.exceptionDetails));
      assert.equal(result.result.value, true, 'browser rule flow did not finish');
    }
    const evaluated = await command('Runtime.evaluate', { expression: 'document.documentElement.outerHTML', returnByValue: true });
    return { code: 0, stdout: evaluated.result.value, stderr: exceptions.join('\n') };
  } finally {
    if (socket?.readyState === WebSocket.OPEN) socket.close();
    child.kill('SIGTERM');
    await new Promise((resolve) => child.once('close', resolve));
    await rm(profile, { recursive: true, force: true });
  }
}

async function operationsProfileBrowserFlow() {
  const wait = async (fn) => { for (let i = 0; i < 150; i++) { const v = fn(); if (v) return v; await new Promise((r) => setTimeout(r, 40)); } throw new Error('Operations timed out: ' + fn); };
  const button = (text) => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const input = (label) => document.querySelector('[aria-label="' + label + '"]');
  const change = (el, value) => { Object.getOwnPropertyDescriptor(el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype, 'value').set.call(el, value); el.dispatchEvent(new Event('input', { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })); };
  (await wait(() => button('Devices'))).click();
  (await wait(() => [...document.querySelectorAll('tbody tr')].find((tr) => tr.textContent.includes('SMOKE-PC')))).click();
  (await wait(() => button('Operations'))).click();
  await wait(() => input('Purpose'));
  if (!document.body.textContent.includes('Backup not verified')) throw new Error('Default backup must be unverified');
  change(input('Purpose'), 'My unsaved edit');
  window.dataBus.emit('*');
  await new Promise((r) => setTimeout(r, 200));
  if (input('Purpose').value !== 'My unsaved edit') throw new Error('Refresh destroyed edits');
  (await wait(() => button('Save profile') && !button('Save profile').disabled && button('Save profile'))).click();
  await wait(() => document.body.textContent.includes('Someone else changed this profile'));
  if (input('Purpose').value !== 'My unsaved edit') throw new Error('Conflict destroyed edits');
  button('Reload saved profile').click();
  await wait(() => input('Purpose').value === 'Concurrent operator edit' && !button('Reload saved profile').disabled);
  change(input('Purpose'), 'Verified browser save');
  change(input('Backup verification date'), '2026-11-19');
  const save = await wait(() => button('Save profile') && !button('Save profile').disabled && button('Save profile'));
  save.click(); save.click();
  await wait(() => document.body.textContent.includes('Profile saved.'));
  if (input('Backup verification date').value !== '2026-11-19') throw new Error('Calendar date shifted');
  // Switch the selected device while its drawer is mounted.
  [...document.querySelectorAll('tbody tr')].find((tr) => tr.textContent.includes('ALPHA-WS')).click();
  await wait(() => document.querySelector('h2')?.textContent === 'ALPHA-WS');
  (await wait(() => button('Operations'))).click();
  await wait(() => document.body.textContent.includes('Unable to load this device’s profile.'));
  if (document.querySelector('[data-operations-device="dev-1"]') || input('Purpose')) throw new Error('Previous device profile leaked on scoped failure');
  document.documentElement.dataset.operationsProfileE2e = 'passed'; return true;
}

async function operationsViewerBrowserFlow() {
  const wait = async (fn) => { for (let i = 0; i < 150; i++) { const v = fn(); if (v) return v; await new Promise((r) => setTimeout(r, 40)); } throw new Error('Operations viewer timed out: ' + fn); };
  const button = (text) => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  (await wait(() => button('Devices'))).click();
  (await wait(() => [...document.querySelectorAll('tbody tr')].find((tr) => tr.textContent.includes('SMOKE-PC')))).click();
  (await wait(() => button('Operations'))).click();
  const form = await wait(() => document.querySelector('[data-operations-device="dev-1"]'));
  if (button('Save profile') || [...form.querySelectorAll('input,textarea,select')].some((el) => !el.disabled)) throw new Error('Viewer can edit operations');
  if (!form.textContent.includes('Read-only:') || form.querySelector('textarea').value !== 'Verified browser save') throw new Error('Viewer missing saved profile');
  document.documentElement.dataset.operationsViewerE2e = 'passed'; return true;
}

// Actual browser interactions against the bundled console and HTTP fixture.
// This script starts listeners and is run only by the CI smoke step.
async function parentRuleOverrideBrowserFlow() {
  const wait = async (fn) => { for (let i=0;i<150;i++) { const x=fn(); if(x) return x; await new Promise((r)=>setTimeout(r,50)); } throw new Error('Parent override timed out: '+fn); };
  const button = (text, root=document) => [...root.querySelectorAll('button')].find((el)=>el.textContent.trim()===text);
  const choose = (el,value) => { Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set.call(el,value); el.dispatchEvent(new Event('change',{bubbles:true})); };
  (await wait(()=>button('Checks'))).click();
  (await wait(()=>button('New check'))).click();
  choose(await wait(()=>[...document.querySelectorAll('select')].find((el)=>[...el.options].some((o)=>o.value==='connectivity'))),'connectivity');
  (await wait(()=>button('Add a policy or device…'))).click();
  (await wait(()=>button('AI workstations (policy)'))).click();
  const label = await wait(()=>[...document.querySelectorAll('label')].find((el)=>el.textContent.includes('Replace the parent rule for Client/Site')));
  label.querySelector('button').click();
  const enabled=[...document.querySelectorAll('label')].find((el)=>el.textContent.trim()==='Enabled'); enabled.querySelector('button').click();
  const name=document.querySelector('input[placeholder="Device connectivity"]');
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(name,'Site explicit exception');name.dispatchEvent(new Event('input',{bubbles:true}));
  (await wait(()=>button('Create check')&&!button('Create check').disabled&&button('Create check'))).click();
  await wait(()=>!button('Create check'));
  (await wait(()=>[...document.querySelectorAll('tr')].find((el)=>el.textContent.includes('Site explicit exception')))).click();
  (await wait(()=>button('Edit'))).click();
  (await wait(()=>button('Save check')&&!button('Save check').disabled&&button('Save check'))).click();
  await wait(()=>!button('Save check'));
  (await wait(()=>button('Policies'))).click();
  (await wait(()=>[...document.querySelectorAll('button')].find((el)=>el.textContent.includes('AI workstations')))).click();
  (await wait(()=>button('Activate'))).click();
  const review=await wait(()=>[...document.querySelectorAll('[role="dialog"]')].find((d)=>(d.getAttribute('aria-label')||'').startsWith('Review and activate')));
  await wait(()=>review.textContent.includes('Replace the parent rule for Client/Site') && review.textContent.includes('override_parent'));
  document.documentElement.dataset.parentOverrideE2e='passed'; return true;
}

async function scopedParentRuleBrowserFlow() {
  const wait=async(fn)=>{for(let i=0;i<150;i++){const x=fn();if(x)return x;await new Promise((r)=>setTimeout(r,50));}throw new Error('Scoped override timed out: '+fn);};
  (await wait(()=>[...document.querySelectorAll('button')].find((el)=>el.textContent.trim()==='Checks'))).click();
  const row=await wait(()=>[...document.querySelectorAll('tr')].find((el)=>el.textContent.includes('Site explicit exception')));
  if(!row.querySelector('button')?.disabled)throw new Error('Scoped rule toggle was not disabled');
  row.click();
  await wait(()=>document.body.textContent.includes('Explicit parent rule replacement'));
  if([...document.querySelectorAll('button')].some((el)=>el.textContent.trim()==='Edit'))throw new Error('Scoped rule edit was offered');
  document.documentElement.dataset.scopedOverrideE2e='passed';return true;
}

async function alertRulesBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) {
      const result = fn();
      if (result) return result;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error('Alert rule E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const choose = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Checks'))).click();
  (await wait(() => button('New check'))).click();
  const type = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'connectivity')));
  choose(type, 'connectivity');
  (await wait(() => button('Add a policy or device…'))).click();
  (await wait(() => button('SMOKE-PC'))).click();
  const mode = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'ignore')));
  choose(mode, 'ignore');
  (await wait(() => button('Create check') && !button('Create check').disabled && button('Create check'))).click();
  await wait(() => !button('Create check'));
  (await wait(() => button('Fleet defaults'))).click();
  const delayInput = await wait(() => document.querySelector('input[max="10080"]'));
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(delayInput, '30');
  delayInput.dispatchEvent(new Event('input', { bubbles: true }));
  delayInput.dispatchEvent(new Event('change', { bubbles: true }));
  (await wait(() => button('Save'))).click();
  await wait(() => !document.querySelector('input[max="10080"]'));
  (await wait(() => button('Users & Settings'))).click();
  const card = await wait(() => [...document.querySelectorAll('div')].find((el) => el.textContent.trim() === 'Email notifications')?.closest('.max-w-2xl'));
  const ruleMode = await wait(() => card.querySelector('select'));
  choose(ruleMode, 'selected');
  const labels = await wait(() => card.querySelectorAll('label').length > 1 && card.querySelectorAll('label'));
  const defaults = [...labels].find((el) => el.textContent.includes('Include built-in alerts'));
  defaults.querySelector('button').click();
  const rule = [...labels].find((el) => el.textContent.includes('Device connectivity'));
  if (!rule) throw new Error('Created rule missing from email preferences');
  rule.querySelector('button').click();
  const list = await wait(() => document.getElementById('notification-custom-rules'));
  const rows = list.querySelectorAll('label');
  if (rows.length < 60 || list.scrollHeight <= list.clientHeight) throw new Error('Large rule list must scroll');
  if (list.clientHeight > rows[0].getBoundingClientRect().height * 5 + 2) throw new Error('More than five rule rows visible');
  const search = card.querySelector('input[aria-controls="notification-custom-rules"]');
  const filter = (value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(search, value);
    search.dispatchEvent(new Event('input', { bubbles: true }));
    search.dispatchEvent(new Event('change', { bubbles: true }));
  };
  filter('MONITOR 60');
  await wait(() => list.querySelectorAll('label').length === 1);
  list.querySelector('label button').click();
  filter('no matching monitor');
  await wait(() => list.querySelectorAll('label').length === 0);
  filter('');
  await wait(() => list.querySelectorAll('label').length > 60);
  (await wait(() => button('Save', card))).click();
  await wait(() => document.body.textContent.includes('Notification settings saved'));
  document.documentElement.dataset.alertRulesE2e = 'passed';
  return true;
}

async function scriptVariablesBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Script variables E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const change = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Scripts'))).click();
  (await wait(() => document.getElementById('scripts-tab-variables'))).click();
  (await wait(() => button('New variable'))).click();
  const definition = await wait(() => document.querySelector('[role="dialog"][aria-label="Script variables"]'));
  change(definition.querySelector('input[aria-label="Name"]'), 'customerKey');
  definition.querySelector('input[type="checkbox"]').click();
  (await wait(() => button('Save', definition) && !button('Save', definition).disabled && button('Save', definition))).click();
  await wait(() => !document.contains(definition));
  // 200 variables: a bounded scroll area, a counter, search and the secret filter.
  const list = await wait(() => document.getElementById('script-variables-list'));
  await wait(() => list.querySelectorAll('button[aria-expanded]').length === 200);
  if (list.scrollHeight <= list.clientHeight) throw new Error('The variables list must scroll');
  if (!document.body.textContent.includes('200 of 200 variables')) throw new Error('Variable counter missing');
  change(document.querySelector('select[aria-label="Filter variables"]'), 'secret');
  await wait(() => document.getElementById('script-variables-list')?.querySelectorAll('button[aria-expanded]').length === 101);
  // The proposal from an assistant: the review filter shows it, Approve accepts it.
  change(document.querySelector('select[aria-label="Filter variables"]'), 'pending');
  await wait(() => document.getElementById('script-variables-list')?.querySelectorAll('button[aria-expanded]').length === 1);
  button('Approve', document.getElementById('script-variables-list')).click();
  // Approve opens the review; the proposal is approved from inside it.
  const variableReview = await wait(() => [...document.querySelectorAll('[role="dialog"]')].find((d) => (d.getAttribute('aria-label') || '').startsWith('Approve the variable')));
  if (!variableReview.textContent.includes('mcp:claude')) throw new Error('The variable review does not name its proposer');
  button('Approve', variableReview).click();
  await wait(() => !document.getElementById('script-variables-list'));
  change(document.querySelector('select[aria-label="Filter variables"]'), 'all');
  change(document.querySelector('input[aria-label="Search variables…"]'), 'CUSTOMERKEY');
  await wait(() => document.getElementById('script-variables-list')?.querySelectorAll('button[aria-expanded]').length === 1);
  // Values open on demand.
  const row = document.getElementById('script-variables-list').querySelector('button[aria-expanded]');
  if (row.getAttribute('aria-expanded') !== 'false') throw new Error('Values must start closed');
  row.click();
  (await wait(() => button('Set a value'))).click();
  const dialog = await wait(() => document.querySelector('[role="dialog"][aria-label="Value"]'));
  const input = dialog.querySelector('input[type="password"]');
  if (!input || input.value !== '') throw new Error('Secret must start blank in a password field');
  change(input, 'q7X');
  (await wait(() => button('Save', dialog) && !button('Save', dialog).disabled && button('Save', dialog))).click();
  await wait(() => !document.contains(dialog) && document.body.textContent.includes('Stored (hidden)'));
  if (document.body.textContent.includes('q7X')) throw new Error('Secret rendered after save');
  document.getElementById('scripts-tab-library').click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Proposed cleanup') && el.textContent.includes('Awaiting review')))).click();
  await wait(() => document.querySelector('[role="note"]')?.textContent.includes('mcp:claude'));
  if (button('Run on device')) throw new Error('A proposed script offered Run on device');
  (await wait(() => button('Approve'))).click();
  const scriptReview = await wait(() => [...document.querySelectorAll('[role="dialog"]')].find((d) => (d.getAttribute('aria-label') || '').startsWith('Review and approve the script')));
  if (!scriptReview.textContent.includes('find /tmp -mtime +7 -delete')) throw new Error('The script review does not show the script');
  button('Approve', scriptReview).click();
  await wait(() => !button('Approve') && !document.querySelector('[role="note"]'));
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Variable installer')))).click();
  // The editor's picker: bounded to five rows, searchable, selection shown as a chip.
  const picker = await wait(() => document.getElementById('script-variable-picker'));
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 200);
  if (picker.scrollHeight <= picker.clientHeight) throw new Error('The variable picker must scroll');
  if (picker.clientHeight > picker.querySelector('label').getBoundingClientRect().height * 5 + 2) throw new Error('More than five picker rows visible');
  change(document.querySelector('input[aria-controls="script-variable-picker"]'), 'customerkey');
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 1);
  const label = await wait(() => [...picker.querySelectorAll('label')].find((el) => el.textContent.includes('customerKey') && el.querySelector('input[type="checkbox"]')));
  label.querySelector('input').click();
  change(document.querySelector('input[aria-controls="script-variable-picker"]'), 'nothing like this');
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 0);
  await wait(() => [...document.querySelectorAll('button')].some((el) => el.getAttribute('aria-label') === 'Remove: customerKey'));
  (await wait(() => button('Save script'))).click();
  await wait(() => !button('Save script'));
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Variable installer') && el.textContent.includes('Script variables')))).click();
  (await wait(() => button('Run on device'))).click();
  const device = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'dev-1')));
  change(device, 'dev-1');
  (await wait(() => button('Create job') && !button('Create job').disabled && button('Create job'))).click();
  await wait(() => !button('Create job'));
  document.documentElement.dataset.scriptVariablesE2e = 'passed'; return true;
}

// The device page's Run script tab, filled from a saved script with script
// variables (the previous flow bound customerKey to it): unedited, the job runs
// the saved script by id, so the variables go with it; an edit turns it into a
// copy, which the form calls out, and Undo edits brings the run by id back. The
// smoke user may bind variables, so the edited copy keeps the script's names
// and goes as a one-off job that asks for them.
async function deviceLibraryRunBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Device library run E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const change = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const bound = () => document.querySelector('[data-run="library"]');
  const edited = () => document.querySelector('[data-run="edited"]');
  (await wait(() => button('Devices'))).click();
  (await wait(() => [...document.querySelectorAll('tbody tr')].find((tr) => tr.textContent.includes('SMOKE-PC')))).click();
  (await wait(() => button('Run script'))).click();
  // The saved script is picked from the searchable library picker.
  const pickLibraryScript = async () => {
    (await wait(() => button('Start from a saved script…'))).click();
    (await wait(() => button('Variable installer'))).click();
  };
  await pickLibraryScript();
  await wait(() => bound()?.textContent.includes('Variable installer'));
  const editor = await wait(() => [...document.querySelectorAll('textarea')].find((el) => el.value.includes('customerKey')));
  change(editor, editor.value + '\necho edited');
  await wait(() => edited() && !bound());
  if (!edited().textContent.includes('Variable installer')) throw new Error('The edited notice does not name the script');
  // The smoke user may bind variables: the edited copy keeps the script's names.
  if (!document.querySelector('[data-run="variables"]')?.textContent.includes('customerKey')) throw new Error('An edited copy lost its variables for a binder');
  (await wait(() => button('Undo edits'))).click();
  await wait(() => bound() && !edited());
  const create = 'Create approval-gated job';
  // First the edited copy, as a one-off job with the variable; then the saved script, by id.
  const again = await wait(() => [...document.querySelectorAll('textarea')].find((el) => el.value.includes('customerKey')));
  change(again, again.value + '\necho edited');
  await wait(() => edited());
  (await wait(() => button(create) && !button(create).disabled && button(create))).click();
  await wait(() => !edited());
  (await wait(() => button('Run script'))).click();
  await pickLibraryScript();
  await wait(() => bound());
  (await wait(() => button(create) && !button(create).disabled && button(create))).click();
  await wait(() => !bound());
  document.documentElement.dataset.deviceLibraryRunE2e = 'passed'; return true;
}

async function servicesAndRemoteSettingsBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Services/settings E2E timed out: ' + fn.toString());
  };
  const button = (text) => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const change = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Checks'))).click();
  for (const [kind, service] of [['linuxservice', 'sshd.service'], ['macservice', 'system/com.apple.notifyd']]) {
    (await wait(() => button('New check'))).click();
    const type = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === kind)));
    change(type, kind);
    const input = await wait(() => document.querySelector('input[placeholder="' + service + '"]'));
    change(input, service);
    if (kind === 'macservice') {
      const state = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'loaded')));
      change(state, 'loaded');
    }
    (await wait(() => button('Add a policy or device…'))).click();
    (await wait(() => button('SMOKE-PC'))).click();
    (await wait(() => button('Create check') && !button('Create check').disabled && button('Create check'))).click();
    await wait(() => !button('Create check'));
  }
  (await wait(() => button('Users & Settings'))).click();
  (await wait(() => button('Configuration'))).click();
  const tab = (key) => document.getElementById('remote-config-tab-' + key);
  const panel = () => document.querySelector('[role="tabpanel"]:not([hidden])');
  await wait(() => tab('native'));
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  assert(panel().id === 'remote-config-panel-default', 'The default provider is the first and initial tab');
  assert(document.querySelector('[role="tab"]') === tab('default'), 'The default provider tab comes first');
  tab('native').click();
  await wait(() => panel().id === 'remote-config-panel-native');
  assert(!panel().querySelector('input[type="password"]'), 'Native does not show MeshCentral credentials');
  tab('meshcentral').click();
  await wait(() => panel().id === 'remote-config-panel-meshcentral');
  change(panel().querySelector('input[placeholder="https://mesh.example.com"]'), 'https://updated-mesh.example.invalid');
  change(panel().querySelector('input[type="password"]'), 'e2e-token-placeholder');
  tab('rustdesk').click();
  await wait(() => panel().id === 'remote-config-panel-rustdesk');
  change(panel().querySelector('select'), 'on');
  tab('default').click();
  await wait(() => panel().id === 'remote-config-panel-default');
  change(panel().querySelector('select'), 'rustdesk');
  tab('native').click();
  await wait(() => panel().id === 'remote-config-panel-native');
  const selectContaining = (value) => panel().querySelector('option[value="' + value + '"]').parentElement;
  change(selectContaining('on'), 'on');
  change(selectContaining('required'), 'required');
  tab('meshcentral').click();
  await wait(() => panel().id === 'remote-config-panel-meshcentral');
  assert(panel().querySelector('input[type="password"]').value === 'e2e-token-placeholder', 'Unsaved token survives tab navigation');
  tab('meshcentral').focus();
  tab('meshcentral').dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }));
  await wait(() => panel().id === 'remote-config-panel-default');
  assert(document.activeElement === tab('default'), 'Keyboard moves focus to selected provider');
  (await wait(() => button('Save configuration'))).click();
  await wait(() => document.body.textContent.includes('Configuration saved'));
  document.documentElement.dataset.servicesSettingsE2e = 'passed';
  return true;
}

// Policy targeting: correct a device's role, assign a policy to every Windows
// server, and leave a device out of it and put it back.
async function policyTargetingBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Policy targeting E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const choose = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  // The device page: Linux has no role until one is set; set it, then follow detection again.
  (await wait(() => button('Devices'))).click();
  (await wait(() => [...document.querySelectorAll('tbody tr')].find((tr) => tr.textContent.includes('SMOKE-PC')))).click();
  // The list's role filter has the same label; the device page's selector is the one that can follow detection ('').
  const roleSelect = () => [...document.querySelectorAll('select[aria-label="Role"]')].find((el) => [...el.options].some((o) => o.value === ''));
  const role = await wait(roleSelect);
  if (role.options[0].textContent !== 'Detected (Not set)') throw new Error('Linux must show that no role is detected: ' + role.options[0].textContent);
  choose(role, 'server');
  await wait(() => roleSelect()?.value === 'server' && document.body.textContent.includes('Device role saved'));
  choose(roleSelect(), '');
  await wait(() => roleSelect()?.value === '' && document.body.textContent.includes('Device role saved'));
  // The policy drawer: an assignment to every Windows server.
  (await wait(() => button('Policies'))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('AI workstations') && el.textContent.includes('AI proposal')))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Assignments')))).click();
  (await wait(() => button('Add an assignment…'))).click();
  (await wait(() => button('Every device (narrow it with OS and role)'))).click();
  choose(await wait(() => document.querySelector('select[aria-label="Operating system"]')), 'windows');
  choose(await wait(() => document.querySelector('select[aria-label="Device role"]')), 'server');
  (await wait(() => button('Assign') && !button('Assign').disabled && button('Assign'))).click();
  await wait(() => [...document.querySelectorAll('span.flex-1')].some((name) => name.textContent === 'Every deviceWindowsServer'));
  // Exceptions: leave SMOKE-PC out, then put it back.
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Exceptions')))).click();
  await wait(() => document.body.textContent.includes('No device is left out.'));
  (await wait(() => button('Leave out a device…'))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'SMOKE-PC' && el.closest('.max-h-60')))).click();
  (await wait(() => button('Leave out') && !button('Leave out').disabled && button('Leave out'))).click();
  (await wait(() => document.querySelector('button[aria-label="Put back in the policy"]'))).click();
  await wait(() => document.body.textContent.includes('No device is left out.'));
  document.documentElement.dataset.policyTargetingE2e = 'passed';
  return true;
}

async function patchComplianceBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Patch compliance E2E timed out: ' + fn.toString());
  };
  const button = (label, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === label);
  const rows = () => [...document.querySelectorAll('tbody tr')];
  const order = () => rows().map((r) => r.cells[0].textContent).join(',');
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  (await wait(() => button('Patches'))).click();
  await wait(() => rows().length === 3 && button('Description', document.querySelector('thead')));
  assert(rows()[2].cells[1].title === 'Room 10', 'Description tooltip uses the device description');
  const sort = async (label, expected, direction) => {
    const header = button(label, document.querySelector('thead'));
    header.focus(); header.click();
    await wait(() => order() === expected && header.closest('th').getAttribute('aria-sort') === direction);
    assert(document.activeElement === header, 'Sort header retains keyboard focus');
  };
  await sort('Description', 'ALPHA-WS,SMOKE-PC,BETA-SRV', 'ascending');
  await sort('Description', 'SMOKE-PC,ALPHA-WS,BETA-SRV', 'descending');
  await sort('Missing', 'BETA-SRV,ALPHA-WS,SMOKE-PC', 'ascending');
  await sort('Missing', 'SMOKE-PC,ALPHA-WS,BETA-SRV', 'descending');
  await sort('Last scan', 'ALPHA-WS,SMOKE-PC,BETA-SRV', 'ascending');
  await sort('Last scan', 'SMOKE-PC,ALPHA-WS,BETA-SRV', 'descending');
  const input = document.querySelector('input[placeholder="Filter devices…"]');
  const filter = (text) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, text);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  };
  filter('ROOM 2');
  await wait(() => order() === 'ALPHA-WS');
  button('Scan', rows()[0]).click();
  await wait(() => document.body.textContent.includes('Scan requested'));
  filter('BETA-SRV');
  await wait(() => order() === 'BETA-SRV');
  button('Scan', rows()[0]).click();
  await wait(() => document.body.textContent.includes('Device offline'));
  document.documentElement.dataset.patchComplianceE2e = 'passed';
  return true;
}

async function patchPolicyPickersBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Patch policy pickers E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const type = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const options = () => [...document.querySelectorAll('.max-h-60 button')].map((el) => el.textContent.trim());
  // The open picker's own search box (the form's name field may still hold the focus).
  const pickerSearch = () => document.querySelector('.max-h-60')?.previousElementSibling?.querySelector('input');
  (await wait(() => button('Patches'))).click();
  (await wait(() => [...document.querySelectorAll('button')].filter((el) => el.textContent.trim() === 'Policies').length > 1
    && [...document.querySelectorAll('button')].filter((el) => el.textContent.trim() === 'Policies').at(-1))).click();
  (await wait(() => document.body.textContent.includes('AI weekend') && button('Edit'))).click();
  // A device assignment reads by name, not "agent: <id>".
  await wait(() => [...document.querySelectorAll('span.flex-1')].some((el) => el.textContent === 'SMOKE-PC'));
  if (document.body.textContent.includes('agent: dev-1')) throw new Error('A device assignment shows its id');
  // Assigned to: the search narrows the scopes; nothing matching says so.
  (await wait(() => button('Add assignment…'))).click();
  const search = await wait(pickerSearch);
  await wait(() => options().includes('Every device (narrow it with OS and role)') && options().includes('SMOKE-PC'));
  type(search, 'no-such-scope');
  await wait(() => options().length === 0 && document.body.textContent.includes('No matches'));
  type(search, 'every');
  await wait(() => options().join('|') === 'Every device (narrow it with OS and role)');
  (await wait(() => [...document.querySelectorAll('.max-h-60 button')].find((el) => el.textContent.trim().startsWith('Every device')))).click();
  (await wait(() => button('Add') && !button('Add').disabled && button('Add'))).click();
  await wait(() => [...document.querySelectorAll('span.flex-1')].some((el) => el.textContent === 'Every device'));
  // Exceptions: search the devices and leave one out.
  (await wait(() => button('Leave out a device…'))).click();
  type(await wait(pickerSearch), 'smoke');
  await wait(() => options().join('|') === 'SMOKE-PC');
  (await wait(() => [...document.querySelectorAll('.max-h-60 button')].find((el) => el.textContent.trim() === 'SMOKE-PC'))).click();
  (await wait(() => button('Leave out') && !button('Leave out').disabled && button('Leave out'))).click();
  // Put the device back, then remove the assignment just added; the device
  // assignment stays.
  (await wait(() => document.querySelector('button[aria-label="Put back in the policy"]'))).click();
  await wait(() => !document.querySelector('button[aria-label="Put back in the policy"]'));
  const everyDeviceRow = () => [...document.querySelectorAll('span.flex-1')].find((el) => el.textContent === 'Every device')?.parentElement;
  (await wait(() => everyDeviceRow()?.querySelector('button'))).click();
  await wait(() => !everyDeviceRow() && [...document.querySelectorAll('span.flex-1')].some((el) => el.textContent === 'SMOKE-PC'));
  document.documentElement.dataset.patchPickersE2e = 'passed';
  return true;
}

async function policyActivationBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Policy activation E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const dialog = (prefix) => [...document.querySelectorAll('[role="dialog"]')].find((d) => (d.getAttribute('aria-label') || '').startsWith(prefix));
  // A policy proposed by an assistant: switched off, reviewed whole, activated.
  (await wait(() => button('Policies'))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('AI workstations') && el.textContent.includes('AI proposal')))).click();
  await wait(() => document.querySelector('[role="note"]')?.textContent.includes('mcp:claude'));
  (await wait(() => button('Activate'))).click();
  let review = await wait(() => dialog('Review and activate'));
  await wait(() => review.textContent.includes('Clean temp') && review.textContent.includes('find /tmp -mtime +7 -delete'));
  button('Activate', review).click();
  // The assistant added a task meanwhile: refused, explained, reloaded.
  await wait(() => review.querySelector('[role="alert"]')?.textContent.includes('It changed after you opened this review') && review.textContent.includes('Second task'));
  button('Activate', review).click();
  await wait(() => !dialog('Review and activate'));
  await wait(() => !document.querySelector('[role="note"]') && button('Switch off'));
  // A patch policy proposed by an assistant.
  (await wait(() => button('Patches'))).click();
  // The Patches tab of the same name comes after the navigation entry.
  (await wait(() => [...document.querySelectorAll('button')].filter((el) => el.textContent.trim() === 'Policies').length > 1
    && [...document.querySelectorAll('button')].filter((el) => el.textContent.trim() === 'Policies').at(-1))).click();
  await wait(() => document.body.textContent.includes('AI weekend') && document.body.textContent.includes('Proposed by mcp:claude'));
  (await wait(() => button('Activate'))).click();
  review = await wait(() => dialog('Review and activate'));
  await wait(() => review.textContent.includes('America/Sao_Paulo') && review.textContent.includes('SMOKE-PC'));
  button('Activate', review).click();
  await wait(() => !dialog('Review and activate'));
  // A task proposed by an assistant: the toggle opens its review.
  (await wait(() => button('Scheduled tasks'))).click();
  const row = await wait(() => [...document.querySelectorAll('tr')].find((tr) => tr.textContent.includes('AI cleanup task')));
  // The enabled column holds the toggle, a button with no text.
  row.children[4].querySelector('button').click();
  review = await wait(() => dialog('Review and approve the task'));
  if (!review.textContent.includes('find /var/tmp -mtime +30 -delete')) throw new Error('The task review does not show its script');
  button('Approve and switch on', review).click();
  await wait(() => !dialog('Review and approve the task'));
  document.documentElement.dataset.policyActivationE2e = 'passed';
  return true;
}

async function deviceJobsAndBootBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Device jobs and last boot E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const rows = () => [...document.querySelectorAll('tbody tr')].filter((tr) => tr.querySelector('td') && !tr.querySelector('[colspan]'));
  const hosts = () => rows().map((tr) => ['SMOKE-PC', 'ALPHA-WS', 'BETA-SRV'].find((h) => tr.textContent.includes(h))).join(',');
  const lastBootHeader = () => [...document.querySelectorAll('th button')].find((el) => el.textContent.trim().startsWith('Last boot'));
  try { localStorage.removeItem('rmm-devices-hidden-columns'); localStorage.removeItem('rmm-devices-shown-columns'); } catch (e) {}
  (await wait(() => button('Devices'))).click();
  await wait(() => rows().length === 3);
  // Last boot starts hidden; the Columns menu shows it.
  if (lastBootHeader()) throw new Error('Last boot is shown before anyone asked for it');
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Columns')))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Last boot' && !el.closest('th')))).click();
  const header = await wait(lastBootHeader);
  const column = header.closest('th').cellIndex;
  const bootCell = (host) => rows().find((tr) => tr.textContent.includes(host))?.cells[column];
  if (bootCell('BETA-SRV')?.textContent.trim() !== '—') throw new Error('A device without a boot time must show a dash');
  if (!bootCell('SMOKE-PC')?.querySelector('[title]')?.title) throw new Error('The boot time is not on hover');
  header.click();
  await wait(() => hosts() === 'ALPHA-WS,SMOKE-PC,BETA-SRV'); // longest running first, no boot time last
  // A device's Jobs tab: a finished job opens in place with its output.
  rows().find((tr) => tr.textContent.includes('SMOKE-PC')).click();
  // The drawer's tab bar, next to its Inventory tab (the sidebar has a Jobs entry too).
  const inventoryTab = await wait(() => button('Inventory'));
  button('Jobs', inventoryTab.parentElement).click();
  const done = await wait(() => document.querySelector('[data-job="djob-done"]'));
  const toggle = done.querySelector('button[aria-expanded]');
  if (toggle.getAttribute('aria-expanded') !== 'false') throw new Error('A job starts collapsed');
  toggle.click();
  await wait(() => done.textContent.includes('olá from SMOKE-PC') && toggle.getAttribute('aria-expanded') === 'true');
  if (!button('Open in Jobs', done)) throw new Error('The expanded job offers no way to the full job');
  const waiting = document.querySelector('[data-job="djob-wait"]');
  waiting.querySelector('button[aria-expanded]').click();
  await wait(() => waiting.textContent.includes('Waiting for approval: nothing has run yet.'));
  if (done.textContent.includes('olá from SMOKE-PC')) throw new Error('Opening another job leaves the first one open');
  document.documentElement.dataset.deviceJobsE2e = 'passed';
  return true;
}

async function bulkLibraryDeployBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Bulk library deploy E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  (await wait(() => button('Devices'))).click();
  (await wait(() => button('Deploy to many'))).click();
  const modal = await wait(() => [...document.querySelectorAll('div')].find((el) => el.textContent.startsWith('Deploy to many devices') && el.querySelector('input[type="checkbox"]')));
  for (const host of ['SMOKE-PC', 'ALPHA-WS']) {
    (await wait(() => [...modal.querySelectorAll('label')].find((l) => l.textContent.includes(host)))).querySelector('input[type="checkbox"]').click();
  }
  (await wait(() => button('From the library', modal))).click();
  (await wait(() => button('Choose a saved script…', modal))).click();
  await wait(() => button('Bulk installer', modal));
  if (button('Unreviewed deploy', modal)) throw new Error('A script awaiting review is offered for deployment');
  button('Bulk installer', modal).click();
  const library = await wait(() => modal.querySelector('[data-deploy="library"]'));
  await wait(() => library.textContent.includes('customerKey'));
  const site = await wait(() => [...library.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'branch')));
  if (site.value !== 'hq') throw new Error('The parameter does not start on its first value');
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(site, 'branch');
  site.dispatchEvent(new Event('change', { bubbles: true }));
  (await wait(() => { const b = button('Create deployment (2)', modal); return b && !b.disabled && b; })).click();
  await wait(() => !document.body.contains(modal) || !button('Create deployment (2)'));
  document.documentElement.dataset.bulkLibraryE2e = 'passed';
  return true;
}

async function sharedMonitoringBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Shared monitoring E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  (await wait(() => button('Checks'))).click();
  const proposals = await wait(() => document.querySelector('[data-edit-proposals="check"]'));
  (await wait(() => button('Review', proposals))).click();
  const field = await wait(() => document.querySelector('[data-edit-review] [data-edit-field="name"]'));
  if (!field.textContent.includes('Existing memory check') || !field.textContent.includes('Reviewed memory check')) {
    throw new Error('The review must show the old and proposed values');
  }
  if (document.querySelector('[data-edit-review] [data-edit-field="interval_sec"]')) throw new Error('An unchanged field appeared in the review');
  (await wait(() => button('Apply edit') && !button('Apply edit').disabled && button('Apply edit'))).click();
  await wait(() => !document.querySelector('[data-edit-proposals="check"]'));
  await wait(() => [...document.querySelectorAll('tbody tr')].some((tr) => tr.textContent.includes('Reviewed memory check')));

  (await wait(() => button('Policies'))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Backup coverage')))).click();
  const checkName = 'Reviewed memory check';
  const picker = await wait(() => button('Add an existing check…'));
  const pickerArea = picker.parentElement;
  picker.click();
  (await wait(() => button(checkName, pickerArea))).click();
  await wait(() => [...document.querySelectorAll('span')]
    .some((span) => span.textContent.trim() === checkName && !pickerArea.contains(span)));

  picker.click();
  // The policy's rows and the picker refresh independently after the attach.
  // Require a known remaining option so a closed or empty picker cannot pass.
  await wait(() => button('Monitor 01', pickerArea) && !button(checkName, pickerArea));
  document.documentElement.dataset.sharedMonitoringE2e = 'passed';
  return true;
}

// Devices ▸ Logged-in user: the column, sorting by it (no user last either
// way), the user filter, search by user, and hiding the column (remembered).
async function devicesUserBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Devices user E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const choose = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const type = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const rows = () => [...document.querySelectorAll('tbody tr')].filter((tr) => tr.querySelector('td') && !tr.querySelector('[colspan]'));
  const hosts = () => rows().map((tr) => ['SMOKE-PC', 'ALPHA-WS', 'BETA-SRV'].find((h) => tr.textContent.includes(h))).join(',');
  try { localStorage.removeItem('rmm-devices-hidden-columns'); } catch (e) {}
  (await wait(() => button('Devices'))).click();
  await wait(() => rows().length === 3);
  const header = await wait(() => [...document.querySelectorAll('th button')].find((el) => el.textContent.trim().startsWith('Logged-in user')));
  const beta = rows().find((tr) => tr.textContent.includes('BETA-SRV'));
  if (!beta.textContent.includes('No user logged in')) throw new Error('A device without a session must say so');
  if (!rows().find((tr) => tr.textContent.includes('AzureAD\\Ana'))) throw new Error('The Entra ID user is shown as AzureAD\\user');
  header.click();
  await wait(() => hosts() === 'SMOKE-PC,ALPHA-WS,BETA-SRV'); // ACME\zoe < AzureAD\Ana, empty last
  if (header.closest('th').getAttribute('aria-sort') !== 'ascending') throw new Error('aria-sort is not set');
  header.click();
  await wait(() => hosts() === 'ALPHA-WS,SMOKE-PC,BETA-SRV'); // descending, empty still last
  const userFilter = await wait(() => document.querySelector('select[aria-label="Logged-in user"]'));
  choose(userFilter, 'none');
  await wait(() => hosts() === 'BETA-SRV');
  choose(userFilter, 'with');
  await wait(() => hosts() === 'ALPHA-WS,SMOKE-PC');
  choose(userFilter, 'all');
  const search = await wait(() => [...document.querySelectorAll('input')].find((el) => /search/i.test(el.placeholder || '')));
  type(search, 'azuread');
  await wait(() => hosts() === 'ALPHA-WS');
  type(search, '');
  await wait(() => rows().length === 3);
  // Hide the column; the choice is remembered.
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Columns')))).click();
  const toggle = await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Logged-in user' && !el.closest('th')));
  toggle.click();
  await wait(() => ![...document.querySelectorAll('th')].some((th) => th.textContent.trim().startsWith('Logged-in user')));
  if (!(localStorage.getItem('rmm-devices-hidden-columns') || '').includes('user')) throw new Error('The hidden column is not remembered');
  toggle.click();
  await wait(() => [...document.querySelectorAll('th')].some((th) => th.textContent.trim().startsWith('Logged-in user')));
  document.documentElement.dataset.devicesUserE2e = 'passed';
  return true;
}

async function hardwareLastUserBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Hardware last-user E2E timed out: ' + fn.toString());
  };
  const button = (text) => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const type = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const rows = () => [...document.querySelectorAll('tbody tr')].filter((tr) => tr.querySelector('td') && !tr.querySelector('[colspan]'));
  const headers = () => [...document.querySelectorAll('thead th')].map((th) => th.textContent.trim());
  try { localStorage.removeItem('rmm-report-hidden'); } catch (e) {}
  (await wait(() => button('Reports'))).click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Hardware & System')))).click();
  await wait(() => rows().length === 3 && rows().some((tr) => tr.textContent.includes('SMOKE-PC')));
  if (headers().at(-1) !== 'Last logged-in user') throw new Error('The last logged-in user column follows department: ' + headers().join('|'));
  const maria = await wait(() => [...document.querySelectorAll('tbody td span')].find((el) => el.textContent === 'ACME\\maria'));
  if (!/^Last seen /.test(maria.title || '')) throw new Error('The tooltip says when the user was last seen: ' + maria.title);
  const beta = rows().find((tr) => tr.textContent.includes('BETA-SRV'));
  if (beta.lastElementChild.textContent.trim() !== '—') throw new Error('A machine with no user ever seen shows a dash');
  // The row filter matches the user.
  const filter = await wait(() => document.querySelector('input[placeholder="Filter rows…"]'));
  type(filter, 'maria');
  await wait(() => rows().length === 1 && rows()[0].textContent.includes('SMOKE-PC'));
  type(filter, '');
  await wait(() => rows().length === 3);
  // The CSV carries it, guarded against spreadsheet formulas.
  const original = URL.createObjectURL;
  let download;
  URL.createObjectURL = (blob) => { download = blob.text(); return original.call(URL, blob); };
  try {
    (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Download CSV' && !el.disabled))).click();
    await wait(() => download);
    const csv = await download;
    const [head, ...lines] = csv.split('\r\n');
    if (!head.endsWith(',Last logged-in user')) throw new Error('CSV header: ' + head);
    if (!lines.some((l) => l.startsWith('SMOKE-PC,') && l.endsWith(',ACME\\maria'))) throw new Error('CSV misses the last user');
    if (!lines.some((l) => l.endsWith(`,"'@SUM(1+1)"`)) || lines.some((l) => l.endsWith(',@SUM(1+1)'))) throw new Error('The last user reached the CSV unguarded');
  } finally { URL.createObjectURL = original; }
  // Hide the column; the choice is remembered per report.
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim().startsWith('Columns')))).click();
  const toggle = await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Last logged-in user' && !el.closest('th')));
  toggle.click();
  await wait(() => !headers().includes('Last logged-in user'));
  if (!(localStorage.getItem('rmm-report-hidden') || '').includes('last_logged_in_user')) throw new Error('The hidden column is not remembered');
  toggle.click();
  await wait(() => headers().includes('Last logged-in user'));
  document.documentElement.dataset.hardwareUserE2e = 'passed';
  return true;
}

async function auditCsvBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const v = fn(); if (v) return v; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Audit CSV flow timed out');
  };
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Audit log'))).click();
  const exportButton = await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Export CSV') && !el.disabled));
  const original = URL.createObjectURL;
  let download;
  URL.createObjectURL = (blob) => { download = blob.text(); return original.call(URL, blob); };
  try {
    exportButton.click();
    await wait(() => download);
    const csv = await download;
    if (!csv.includes('"\'=1+1","alert-target-1"') || !csv.includes('"ORDINARY-HOST","alert-target-2"')) throw new Error('CSV guard or legitimate target missing');
    document.documentElement.dataset.auditCsvE2e = 'passed';
    return true;
  } finally { URL.createObjectURL = original; }
}

// Alerts > Silences & maintenance lists every window (active=false) with its
// state, and a manager cancels the active one from the list.
async function alertSilencesBrowserFlow() {
  const wait = async (fn, what) => {
    for (let i = 0; i < 150; i++) { const v = fn(); if (v) return v; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Silences flow timed out: ' + what);
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  (await wait(() => [...document.querySelectorAll('aside button, nav button')].find((el) => el.textContent.trim().startsWith('Alerts')), 'Alerts nav')).click();
  (await wait(() => button('Silences & maintenance'), 'dialog button')).click();
  const dialog = await wait(() => [...document.querySelectorAll('div')].find((el) => el.textContent.includes('Scheduled and previous') && el.textContent.includes('Nightly patching') && el.querySelector('button')), 'the list');
  const row = (reason) => [...dialog.querySelectorAll('div.rounded-lg')].find((el) => el.textContent.includes(reason));
  const active = await wait(() => row('Nightly patching'), 'active row');
  if (!active.textContent.includes('Maintenance window') || !button('Cancel', active)) throw new Error('the active window must show its kind and a Cancel button');
  if (!row('Called off')?.textContent.includes('Canceled') || button('Cancel', row('Called off'))) throw new Error('the canceled window must say so and offer no Cancel');
  if (!row('Old window')?.textContent.includes('Ended') || button('Cancel', row('Old window'))) throw new Error('the ended window must say so and offer no Cancel');
  if (document.body.textContent.includes('No suppression windows.')) throw new Error('a list with rows must not show the empty state');
  button('Cancel', active).click();
  await wait(() => document.body.textContent.includes('Suppression canceled'), 'cancel toast');
  document.documentElement.dataset.alertSilencesE2e = 'passed';
  return true;
}

// Tailwind 4 with the console's Tailwind 3 compatibility rules, checked in a
// real browser cascade: probes built from classes the console uses, measured
// with getComputedStyle. Each assertion would fail on a plain Tailwind 4 build.
async function tailwindCompatBrowserFlow() {
  for (let i = 0; i < 150 && !document.querySelector('aside'); i++) await new Promise((r) => setTimeout(r, 50));
  const host = document.createElement('div');
  host.style.cssText = 'position:fixed;left:0;top:0;width:400px;';
  host.innerHTML = `
    <div class="space-y-1"><div id="tw-a">a</div><code id="tw-b">b</code></div>
    <div class="space-y-2"><div class="hidden">x</div><div id="tw-c">y</div></div>
    <div class="space-y-2"><div>a</div><div id="tw-d" class="mt-4">b</div></div>
    <div class="divide-y divide-line"><div id="tw-e">a</div><div id="tw-f">b</div></div>
    <div id="tw-line" class="border border-line">ref</div>
    <div id="tw-g" class="border">bare border</div>
    <table><tbody><tr><td id="tw-h">cell</td></tr></tbody></table>
    <select id="tw-i" disabled><option>x</option></select>
    <input id="tw-j" class="focus:ring-2" placeholder="placeholder">
    <div id="tw-k" class="bg-elevated/40">dormant</div>
    <button id="tw-l">on</button><button id="tw-m" disabled>off</button>
    <div class="relative overflow-hidden w-40 h-4"><div id="tw-n" class="absolute inset-0 -translate-x-full animate-[shimmer_1.4s_infinite]"></div></div>`;
  document.body.appendChild(host);
  const css = (id, prop, pseudo) => getComputedStyle(document.getElementById(id), pseudo).getPropertyValue(prop);
  const fail = (what, got) => { throw new Error('Tailwind compat: ' + what + ' (got ' + JSON.stringify(got) + ')'); };
  try {
    // Stacks keep Tailwind 3's "every child after the first" rule.
    if (css('tw-a', 'margin-bottom') !== '0px' || css('tw-b', 'margin-top') !== '4px') fail('space-y spaces from the second child', [css('tw-a', 'margin-bottom'), css('tw-b', 'margin-top')]);
    if (css('tw-c', 'margin-top') !== '8px') fail('a child after a class-hidden one is spaced', css('tw-c', 'margin-top'));
    if (css('tw-d', 'margin-top') !== '8px') fail('the stack rule beats a child margin utility', css('tw-d', 'margin-top'));
    if (css('tw-e', 'border-bottom-width') !== '0px' || css('tw-f', 'border-top-width') !== '1px') fail('divide-y borders from the second child', [css('tw-e', 'border-bottom-width'), css('tw-f', 'border-top-width')]);
    if (css('tw-f', 'border-top-color') !== css('tw-line', 'border-top-color')) fail('divide-line colours the divider', css('tw-f', 'border-top-color'));
    // Preflight defaults the console relied on.
    if (css('tw-g', 'border-top-color') !== 'rgb(229, 231, 235)') fail('a bare border is gray-200', css('tw-g', 'border-top-color'));
    if (css('tw-h', 'padding-left') !== '1px') fail('table cells keep the browser padding', css('tw-h', 'padding-left'));
    if (!(parseFloat(css('tw-i', 'opacity')) < 1)) fail('a disabled select stays dimmed', css('tw-i', 'opacity'));
    if (css('tw-j', 'color', '::placeholder') !== 'rgb(156, 163, 175)') fail('placeholders are gray-400', css('tw-j', 'color', '::placeholder'));
    if (css('tw-l', 'cursor') !== 'pointer' || css('tw-m', 'cursor') !== 'default') fail('pointer on buttons, default when disabled', [css('tw-l', 'cursor'), css('tw-m', 'cursor')]);
    document.getElementById('tw-j').focus();
    if (!css('tw-j', 'box-shadow').includes('rgba(59, 130, 246, 0.5)')) fail('a ring with no colour is blue-500 at 50%', css('tw-j', 'box-shadow'));
    // A palette opacity Tailwind 3 never generated stays off.
    if (css('tw-k', 'background-color') !== 'rgba(0, 0, 0, 0)') fail('dormant palette opacity classes render nothing', css('tw-k', 'background-color'));
    // The skeleton sweep runs from -100% to +100% of its track.
    const sweep = document.getElementById('tw-n');
    const [animation] = sweep.getAnimations();
    if (!animation) fail('the shimmer animation runs', null);
    animation.pause();
    const offset = () => sweep.getBoundingClientRect().left - sweep.parentElement.getBoundingClientRect().left;
    const width = sweep.parentElement.getBoundingClientRect().width;
    animation.currentTime = 0;
    const start = offset();
    animation.currentTime = 1400 * 0.999;
    const end = offset();
    if (!(start < -0.95 * width && end > 0.95 * width)) fail('the shimmer sweeps the whole track', { start, end, width });
    // Hover is not wrapped in a hover-capable media query.
    const hoverMedia = [...document.styleSheets].flatMap((sheet) => { try { return [...sheet.cssRules]; } catch { return []; } })
      .some(function hasHover(rule) { return (rule.conditionText || '').includes('hover') || [...(rule.cssRules || [])].some(hasHover); });
    if (hoverMedia) fail('hover applies on every device', 'an @media (hover) rule');
  } finally {
    host.remove();
  }
  document.documentElement.dataset.tailwindCompatE2e = 'passed';
  return true;
}

function assertBrowser(result, pattern) {
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, pattern, result.stderr);
  assert.doesNotMatch(result.stderr, /Content Security Policy.*(?:refused|violat)|(?:refused|violat).*Content Security Policy/i);
}

function assertMobile(result, pattern) {
  assertBrowser(result, pattern);
  assert.match(result.stdout, /data-smoke-width="(\d+):(\d+)"/);
  const [, viewportWidth, scrollWidth] = result.stdout.match(/data-smoke-width="(\d+):(\d+)"/);
  assert.equal(Number(viewportWidth), 375, 'mobile smoke must use a 375px CSS viewport');
  assert.ok(Number(scrollWidth) <= Number(viewportWidth) + 1, `mobile page overflows horizontally: ${scrollWidth} > ${viewportWidth}`);
}

try {
  const response = await fetch(baseURL + '/');
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.doesNotMatch(html, /https?:\/\/(?:cdn|unpkg|fonts\.)/i);
  for (const asset of ['assets/app.js', 'assets/app.css', 'assets/styles.css', 'assets/logo_open_rmm_edit3.png']) {
    const assetResponse = await fetch(`${baseURL}/${asset}`);
    assert.equal(assetResponse.status, 200, `${asset} must be served`);
    assert.notEqual(assetResponse.headers.get('content-type'), 'text/html', `${asset} must not fall back to HTML`);
  }
  assert.equal((await fetch(baseURL + '/assets/missing.js')).status, 404, 'missing assets must return 404');

  const candidates = [process.env.CHROME_BIN, 'google-chrome', 'chromium', 'chromium-browser'].filter(Boolean);
  const chrome = candidates.find((candidate) => spawnSync(candidate, ['--version'], { stdio: 'ignore' }).status === 0);
  if (process.env.OPENRMM_REQUIRE_BROWSER === '1') assert.ok(chrome, 'CI browser E2E requires Chrome');
  if (chrome) {
    assertBrowser(await runChrome(chrome, baseURL + '/'), /Sign in to your instance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/'), /Fleet health at a glance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=Devices'), /3 agents · 2 online/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    const mobile = await runMobileChrome(chrome, baseURL + '/authenticated/?route=Open%20navigation');
    assertMobile(mobile, /smoke-admin/);
    assert.match(mobile.stdout, /translate-x-0/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=Devices'), /3 agents · 2 online/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    assertBrowser(await runChrome(chrome, baseURL + '/expired/'), /Sign in to your instance/);
    const operationsFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: operationsProfileBrowserFlow });
    assertBrowser(operationsFlow, /data-operations-profile-e2e="passed"/);
    const writes = apiRequests.filter((r) => r.method === 'PUT' && r.pathname.endsWith('/operations-profile'));
    assert.equal(writes.length, 2, 'conflict and one save; duplicate submit is suppressed');
    assert.equal(writes[1].payload.expected_version, 1);
    assert.equal(writes[1].payload.backup_verified_on, '2026-11-19');
    assert.equal('updated_by' in writes[1].payload, false);
    assert.equal('version' in writes[1].payload, false);
    operationsViewer = true;
    const operationsViewerFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: operationsViewerBrowserFlow });
    assertBrowser(operationsViewerFlow, /data-operations-viewer-e2e="passed"/);
    operationsViewer = false;
    const rules = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: alertRulesBrowserFlow });
    assertBrowser(rules, /data-alert-rules-e2e="passed"/);
    const created = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/checks');
    assert.equal(created?.payload.type, 'connectivity');
    assert.equal(created?.payload.config.mode, 'ignore');
    assert.equal(created?.payload.enabled, true);
    assert.equal(ruleDefaults.connectivity.config.after_min, 30);
    assert.deepEqual(rulePrefs, { mode: 'selected', include_defaults: false, check_ids: ['rule-e2e', 'sample-rule-60'] });
    const settingsFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: servicesAndRemoteSettingsBrowserFlow });
    assertBrowser(settingsFlow, /data-services-settings-e2e="passed"/);
    for (const [type, service, state] of [['linuxservice', 'sshd.service', undefined], ['macservice', 'system/com.apple.notifyd', 'loaded']]) {
      const body = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/checks' && r.payload?.type === type)?.payload;
      assert.ok(body, type + ' was submitted');
      assert.equal(body.config.service, service);
      assert.equal(body.config.state, state);
      assert.equal('warn_threshold' in body, false);
    }
    const saved = apiRequests.find((r) => r.method === 'PUT' && r.pathname === '/api/config')?.payload;
    assert.equal(saved?.remote.default, 'rustdesk');
    assert.equal(saved?.remote.meshcentral_url, 'https://updated-mesh.example.invalid');
    assert.equal(saved?.remote.meshcentral_token_key, 'e2e-token-placeholder');
    assert.equal(saved?.remote.rustdesk_enabled, true);
    assert.equal(saved?.remote.guest_access, true);
    assert.equal(saved?.remote.e2e_policy, 'required');
    assert.equal(saved?.jobs.allow_unapproved, false);
    assert.equal(remoteConfig.remote.default, 'rustdesk');
    assert.equal(remoteConfig.remote.rustdesk_enabled, true);
    assert.equal(remoteConfig.remote.e2e_policy, 'required');
    assert.equal('meshcentral_token_key' in remoteConfig.remote, false, 'GET config stays write-only');
    const variablesFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: scriptVariablesBrowserFlow });
    assertBrowser(variablesFlow, /data-script-variables-e2e="passed"/);
    assert.equal(scriptVariables[0]?.name, 'customerKey');
    assert.equal(scriptVariables[0]?.secret, true);
    const variableValue = apiRequests.find((r) => r.method === 'PUT' && r.pathname === '/api/script-variables/variable-e2e/values');
    assert.equal(variableValue?.payload.value, 'q7X', 'short secrets must be submitted without rewriting');
    assert.equal('value' in scriptVariables[0].values[0], false, 'secret read-back is write-only');
    assert.deepEqual(libraryScripts[0].variables, ['customerKey']);
    assert.ok(apiRequests.some((r) => r.method === 'POST' && r.pathname === '/api/script-variables/variable-extra-198/approve'), 'proposed variable approved');
    assert.ok(apiRequests.some((r) => r.method === 'POST' && r.pathname === '/api/scripts/script-proposal/approve'), 'proposed script approved');
    const variableJob = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/devices/dev-1/run-script');
    assert.deepEqual(variableJob?.payload, { script_id: 'script-e2e', params: {} });
    const libraryRunFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: deviceLibraryRunBrowserFlow });
    assertBrowser(libraryRunFlow, /data-device-library-run-e2e="passed"/);
    const libraryRuns = apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/devices/dev-1/run-script');
    assert.equal(libraryRuns.length, 2, 'the device page ran the saved script by id');
    assert.deepEqual(libraryRuns[1].payload, { script_id: 'script-e2e', params: {} });
    const copies = apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/jobs' && String(r.payload?.script || '').includes('customerKey'));
    assert.equal(copies.length, 1, 'only the edited copy went as a one-off job');
    assert.ok(copies[0].payload.script.includes('echo edited'));
    assert.deepEqual(copies[0].payload.variables, ['customerKey'], 'the edited copy asked for the script variable');
    const complianceFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: patchComplianceBrowserFlow });
    assertBrowser(complianceFlow, /data-patch-compliance-e2e="passed"/);
    assert.deepEqual(apiRequests.filter((r) => r.method === 'POST' && /\/patches\/scan$/.test(r.pathname)).map((r) => r.pathname), [
      '/api/devices/dev-2/patches/scan',
      '/api/devices/dev-3/patches/scan',
    ], 'each filtered row scans only its own device');
    const targetingFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: policyTargetingBrowserFlow });
    assertBrowser(targetingFlow, /data-policy-targeting-e2e="passed"/);
    assert.deepEqual(apiRequests.filter((r) => r.method === 'PUT' && r.pathname === '/api/devices/dev-1/role').map((r) => r.payload), [{ role: 'server' }, { role: '' }]);
    assert.deepEqual(apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/policies/pol-ai/assignments')?.payload, { scope_type: 'global', scope_id: '', os: 'windows', role: 'server' });
    assert.deepEqual(apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/policies/pol-ai/exclusions')?.payload, { agent_id: 'dev-1' });
    assert.ok(apiRequests.some((r) => r.method === 'DELETE' && r.pathname === '/api/policies/pol-ai/exclusions/dev-1'), 'the device was put back');
    assert.equal(policyExclusions.length, 0);
    const patchPickersFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: patchPolicyPickersBrowserFlow });
    assertBrowser(patchPickersFlow, /data-patch-pickers-e2e="passed"/);
    assert.deepEqual(apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/patch-policies/patch-ai/assignments')?.payload,
      { scope_type: 'global', scope_id: '', os: '', role: '' }, 'the scope found by search was assigned');
    assert.deepEqual(apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/patch-policies/patch-ai/exclusions')?.payload, { agent_id: 'dev-1' });
    assert.ok(apiRequests.some((r) => r.method === 'DELETE' && r.pathname === '/api/patch-policies/patch-ai/exclusions/dev-1'), 'the device was put back in the patch policy');
    assert.ok(apiRequests.some((r) => r.method === 'DELETE' && r.pathname === '/api/patch-policies/assignments/pasg-2'), 'the assignment was removed');
    assert.equal(patchExclusions.length, 0);
    assert.deepEqual(patchAssignments.map((a) => a.id), ['pasg-1'], 'only the removed assignment is gone');
    const activationFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: policyActivationBrowserFlow });
    assertBrowser(activationFlow, /data-policy-activation-e2e="passed"/);
    const activations = apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/policies/pol-ai/activate').map((r) => r.payload?.fingerprint);
    assert.deepEqual(activations, ['fp-1', 'fp-2'], 'the stale review is refused and the reloaded one is sent');
    assert.equal(aiPolicy.enabled, true);
    assert.deepEqual(apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/patch-policies/patch-ai/activate')?.payload, { fingerprint: 'pfp' });
    assert.equal(aiPatchPolicy.enabled, true);
    const approvedTask = apiRequests.find((r) => r.method === 'PUT' && r.pathname === '/api/tasks/task-ai')?.payload;
    assert.equal(approvedTask?.enabled, true, 'the AI task was switched on through its review');
    assert.equal(approvedTask?.script, 'find /var/tmp -mtime +30 -delete');
    const deviceJobsFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: deviceJobsAndBootBrowserFlow });
    assertBrowser(deviceJobsFlow, /data-device-jobs-e2e="passed"/);
    const bulkFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: bulkLibraryDeployBrowserFlow });
    assertBrowser(bulkFlow, /data-bulk-library-e2e="passed"/);
    assert.deepEqual(apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/jobs/bulk').map((r) => r.payload),
      [{ agent_ids: ['dev-1', 'dev-2'], script_id: 'script-bulk', params: { site: 'branch' } }], 'the saved script went by id, with the parameter chosen');
    const sharedFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: sharedMonitoringBrowserFlow });
    assertBrowser(sharedFlow, /data-shared-monitoring-e2e="passed"/);
    assert.equal(apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/edit-proposals/edit-check-e2e/apply').length, 1);
    assert.deepEqual(apiRequests.filter((r) => r.method === 'POST' && r.pathname === '/api/checks/check-share-e2e/attachments').map((r) => r.payload),
      [{ policy_id: 'pol-shared-e2e' }], 'the console attaches the reviewed check by id');
    assert.equal(sharedCheck.attachments.length, 2, 'sharing preserves the direct device attachment');
    const usersFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: devicesUserBrowserFlow });
    assertBrowser(usersFlow, /data-devices-user-e2e="passed"/);
    const hardwareFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: hardwareLastUserBrowserFlow });
    assertBrowser(hardwareFlow, /data-hardware-user-e2e="passed"/);
    const csvFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: auditCsvBrowserFlow });
    assertBrowser(csvFlow, /data-audit-csv-e2e="passed"/);
    const silencesFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: alertSilencesBrowserFlow });
    assertBrowser(silencesFlow, /data-alert-silences-e2e="passed"/);
    assert.ok(suppressionQueries.includes('?active=false'), 'the dialog lists every window, ended and canceled ones included');
    assert.ok(apiRequests.some((r) => r.method === 'DELETE' && r.pathname === '/api/alert-suppressions/sup-active'), 'Cancel reached the API for the active window');
    assert.ok(suppressions.find((s) => s.id === 'sup-active').canceled_at, 'the active window was canceled');
    const compatFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: tailwindCompatBrowserFlow });
    assertBrowser(compatFlow, /data-tailwind-compat-e2e="passed"/);
    policyAssignments.splice(0, policyAssignments.length, { id: 'override-site', policy_id: 'pol-ai', scope_type: 'site', scope_id: 'site-1' });
    aiPolicy.enabled = false;
    const overrideFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: parentRuleOverrideBrowserFlow });
    assertBrowser(overrideFlow, /data-parent-override-e2e="passed"/);
    const overrideRequest = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/checks' && r.payload?.name === 'Site explicit exception');
    assert.equal(overrideRequest?.payload.config.override_parent, true);
    assert.deepEqual(overrideRequest?.payload.attachments, [{policy_id:'pol-ai'}]);
    assert.equal(overrideRequest?.payload.enabled, false);
    assert.ok(apiRequests.some((r) => r.method === 'PUT' && r.pathname.startsWith('/api/checks/') && r.payload?.config.override_parent === true && r.payload?.enabled === false));
    overrideManager = false;
    const mutationCount = apiRequests.filter((r) => r.method !== 'GET').length;
    const scopedFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: scopedParentRuleBrowserFlow });
    assertBrowser(scopedFlow, /data-scoped-override-e2e="passed"/);
    assert.equal(apiRequests.filter((r) => r.method !== 'GET').length, mutationCount);
    assert.ok(apiRequests.some((request) => request.pathname === '/api/me' && request.token === 'smoke-token'), 'authenticated API requests must include the bearer token');
    assert.ok(apiRequests.some((request) => request.pathname === '/api/me' && request.token === 'expire-token'), '401 scenario must exercise an authenticated request');
  }
  console.log(`Smoke passed${chrome ? ` in ${chrome}` : ' (static checks; browser unavailable)'}.`);
} finally {
  await new Promise((resolve) => server.close(resolve));
}
