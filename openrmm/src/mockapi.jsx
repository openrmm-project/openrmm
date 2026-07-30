/* mockapi.jsx — REAL fetch client for the OpenRMM Go backend.
   (Filename kept for index.html; this is the live client, not a mock.)

   Exposes window.api with the exact surface the UI expects. Bearer token from
   POST /api/auth/login is sent as Authorization on every /api/* call.
   API base: window.__API_BASE__ (default http://localhost:8080). */

const API_BASE = new URL(window.__API_BASE__ || location.origin, location.href).href.replace(/\/+$/, '');
const ROLES = { admin: 3, tech: 2, readonly: 1 };
const DEFAULT_ROLE_PERMS = {
  readonly: ['devices.view', 'devices.remote', 'clients.view'],
  tech: ['ai_chat.use', 'mcp.use', 'alerts.view', 'alerts.manage', 'jobs.view', 'jobs.create', 'jobs.manage', 'checks.view', 'tasks.view', 'policies.view', 'scripts.view', 'patches.view', 'patches.manage', 'devices.view', 'devices.manage', 'devices.remote', 'devices.files', 'devices.terminal', 'clients.view', 'reports.view'],
  admin: ['ai_chat.use', 'mcp.use', 'alerts.view', 'alerts.manage', 'jobs.view', 'jobs.create', 'jobs.manage', 'checks.view', 'checks.manage', 'tasks.view', 'tasks.manage', 'policies.view', 'policies.manage', 'scripts.view', 'scripts.manage', 'patches.view', 'patches.manage', 'patches.policies.manage', 'devices.view', 'devices.manage', 'devices.delete', 'devices.remote', 'devices.files', 'devices.terminal', 'clients.view', 'clients.manage', 'reports.view', 'audit.view', 'settings.manage', 'enrollment.manage', 'api_tokens.manage', 'users.manage', 'roles.manage'],
};
const PERM_ALIASES = { admin: 'settings.manage', tech: 'jobs.manage', readonly: 'devices.view' };
const AUTH_KEY = 'rmm-ai-auth';

/* event bus kept for compatibility (mutations can emit; queries may listen) */
const bus = (() => {
  const subs = new Set();
  return { sub(fn) { subs.add(fn); return () => subs.delete(fn); }, emit(key) { subs.forEach((f) => f(key)); } };
})();
window.dataBus = bus;

/* ------------------------------- session --------------------------------- */
let session = (() => {
  try { return JSON.parse(sessionStorage.getItem(AUTH_KEY) || 'null'); } catch (e) { return null; }
})();
function setSession(s) {
  session = s;
  try { s ? sessionStorage.setItem(AUTH_KEY, JSON.stringify(s)) : sessionStorage.removeItem(AUTH_KEY); } catch (e) {}
}

function normalizeSession(token, r) {
  const user = r.user || r;
  const role = r.role || user.role;
  const permissions = r.permissions || user.permissions || DEFAULT_ROLE_PERMS[role] || [];
  return { token, user: { ...user, permissions }, role, permissions };
}
function sessionPerms() {
  if (!session) return new Set();
  return new Set(session.permissions || session.user?.permissions || DEFAULT_ROLE_PERMS[session.role] || []);
}
function canPermission(key) {
  if (!session) return false;
  return sessionPerms().has(PERM_ALIASES[key] || key);
}

function apiError(status, message) { const e = new Error(message); e.status = status; return e; }

/* ------------------------------- transport ------------------------------- */
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function request(method, path, body, attempt = 0) {
  const hadSession = !!session?.token;
  const headers = { 'Content-Type': 'application/json' };
  if (session && session.token) headers.Authorization = 'Bearer ' + session.token;
  let res;
  try {
    res = await fetch(API_BASE + path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined });
  } catch (e) {
    throw apiError(0, 'cannot reach backend at ' + API_BASE);
  }
  // 429: the per-IP API limiter pushed back. The chatty SPA fires bursts on
  // navigation, so retry a few times with backoff (honoring Retry-After) before
  // surfacing the error — this is what kept tabs occasionally rendering empty.
  if (res.status === 429 && attempt < 4) {
    const ra = parseFloat(res.headers.get('Retry-After'));
    const wait = Number.isFinite(ra) && ra > 0 ? ra * 1000 : 350 * (attempt + 1) + Math.random() * 250;
    await sleep(Math.min(wait, 4000));
    return request(method, path, body, attempt + 1);
  }
  if (res.status === 401) {
    setSession(null);
    if (hadSession && !path.startsWith('/api/auth/')) window.dispatchEvent(new Event('rmm-auth-expired'));
    let j = {}; try { j = await res.json(); } catch (e) {}
    const err = apiError(401, j.error || 'unauthorized');
    if (j.totp_required) err.totpRequired = true;
    throw err;
  }
  if (!res.ok) {
    let msg = res.statusText;
    try { const j = await res.json(); msg = j.error || j.message || msg; } catch (e) {}
    throw apiError(res.status, msg);
  }
  let out;
  if (res.status === 204) {
    out = undefined;
  } else {
    const text = await res.text();
    out = text ? JSON.parse(text) : undefined;
  }
  // Any mutation invalidates all live queries so the UI refreshes immediately.
  if (method !== 'GET') bus.emit('*');
  return out;
}
const GET = (p) => request('GET', p);
const POST = (p, b) => request('POST', p, b);
const PUT = (p, b) => request('PUT', p, b);
const PATCH = (p, b) => request('PATCH', p, b);
const DELETE = (p) => request('DELETE', p);
const websocketURL = (path) => {
  const url = new URL(API_BASE + path);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.toString();
};
const qs = (o) => {
  const u = new URLSearchParams();
  Object.entries(o || {}).forEach(([k, v]) => { if (v != null && v !== '') u.set(k, String(v)); });
  const s = u.toString(); return s ? '?' + s : '';
};

/* ---------- adapter: real (flat) inventory -> UI's structured shape ---------- */
function adaptInventory(inv) {
  const d = (inv && inv.data) || {};
  const rawHardware = d.hardware || {};
  const asArray = (v) => Array.isArray(v) ? v : (v ? [v] : []);
  const first = (...vals) => vals.find((v) => v != null && String(v).trim() !== '');
  const hardware = {
    service_tag: first(rawHardware.service_tag, rawHardware.serial_number, d.service_tag, d.serial_number),
    serial_number: first(rawHardware.serial_number, d.serial_number),
    processor: first(rawHardware.processor, d.cpu_model),
    memory_total_bytes: d.mem_total_bytes,
    system_model: first(rawHardware.system_model, rawHardware.model, d.system_model),
    manufacturer: first(rawHardware.manufacturer, d.system_vendor),
    bios: first(rawHardware.bios, rawHardware.bios_version, d.bios_version),
    motherboard: rawHardware.motherboard || d.motherboard,
    storage_devices: asArray(rawHardware.storage_devices || d.storage_devices),
    gpus: asArray(rawHardware.gpus || d.gpus),
  };
  const disks = (d.disks || []).map((x) => ({
    mount: x.mount, fs: x.fstype,
    total_gb: x.total_bytes ? Math.round(x.total_bytes / 1e9) : undefined,
    free_gb: x.total_bytes != null && x.used_percent != null ? Math.round((x.total_bytes * (100 - x.used_percent) / 100) / 1e9) : undefined,
  }));
  return {
    agent_id: inv && inv.agent_id,
    collected_at: inv && inv.collected_at,
    data: {
      system: { hostname: d.hostname, os: d.platform ? d.platform + ' ' + (d.platform_version || '') : d.os, arch: d.arch, uptime_hours: d.uptime_sec ? Math.round(d.uptime_sec / 3600) : undefined, kernel: d.kernel_version },
      cpu: { model: d.cpu_model, cores: d.cpu_cores || d.num_cpu },
      memory: { total_gb: d.mem_total_bytes ? Math.round(d.mem_total_bytes / 1e9) : undefined, used_pct: d.mem_used_percent != null ? Math.round(d.mem_used_percent) : undefined },
      disks,
      hardware,
      network: { interfaces: (d.network_interfaces || []).map((n) => ({ name: n.name, ipv4: n.ipv4, mac: n.mac })) },
      software: (d.software || []).map((s) => ({ name: s.name, version: s.version })),
      agent: { version: d.agent_version, last_check_in: inv && inv.collected_at },
      raw: d,
    },
  };
}

function adaptPermissions(r) {
  const permissions = ((r && r.permissions) || []).map((p) => (
    p.key === 'ai_chat.use' ? { ...p, label: p.label === 'Use AI Chat' ? 'AI Chat' : p.label, group: 'AI' }
      : p.key === 'jobs.manage' ? { ...p, label: 'Manage jobs', description: 'Approve, reject, and cancel jobs.', group: 'Jobs' }
        : p
  ));
  if (!permissions.some((p) => p.key === 'mcp.use')) {
    permissions.push({ key: 'mcp.use', label: 'MCP', description: 'Connect external MCP clients to AI tools.', group: 'AI' });
  }
  if (!permissions.some((p) => p.key === 'jobs.create')) {
    const i = permissions.findIndex((p) => p.key === 'jobs.manage');
    const create = { key: 'jobs.create', label: 'Create jobs', description: 'Create approval-gated jobs and bulk deployments.', group: 'Jobs' };
    if (i >= 0) permissions.splice(i, 0, create); else permissions.push(create);
  }
  return { permissions };
}

/* ---------------------------------- api ---------------------------------- */
const api = {
  API_BASE,
  websocketURL,

  /* ---- auth ---- */
  async login(username, password, code) {
    const r = await POST('/api/auth/login', { username, password, code });
    setSession(normalizeSession(r.token, r));
    return r;
  },
  setup2FA(currentPassword) { return POST('/api/me/2fa/setup', { current_password: currentPassword }); },
  enable2FA(code) { return POST('/api/me/2fa/enable', { code }); },
  disable2FA(currentPassword, code) { return POST('/api/me/2fa/disable', { current_password: currentPassword, code }); },
  regenRecoveryCodes(code) { return POST('/api/me/2fa/recovery-codes', { code }); },
  reset2FA(id) { return POST('/api/users/' + id + '/2fa/reset'); },
  changePassword(currentPassword, newPassword) { return POST('/api/me/password', { current_password: currentPassword, new_password: newPassword }); },
  async bootstrap(username, password) {
    const r = await POST('/api/auth/bootstrap', { username, password });
    setSession(normalizeSession(r.token, r));
    return r;
  },
  async loginWithToken(token) {
    setSession({ token, user: { id: '', username: '', role: 'readonly', created_at: '', permissions: [] }, role: 'readonly', permissions: [] });
    try {
      const user = await GET('/api/me');
      const next = normalizeSession(token, user);
      setSession(next);
      return next;
    } catch (e) {
      setSession(null);
      throw e;
    }
  },
  async me() { return GET('/api/me'); },
  async logout() { try { await POST('/api/auth/logout'); } catch (e) {} finally { setSession(null); } },
  currentRole() { return session ? session.role : null; },
  permissions() { return Array.from(sessionPerms()); },
  can(key) { return canPermission(key); },
  hasUsers() { return true; },

  /* ---- devices ---- */
  listDevices(params) { return GET('/api/devices' + qs(params || {})); },
  getDevice(id) { return GET('/api/devices/' + id); },
  async getInventory(id) { return adaptInventory(await GET('/api/devices/' + id + '/inventory')); },
  startRemote(id, provider) { return POST('/api/devices/' + id + '/remote', { provider }); },
  getRemoteCapabilities(id) { return GET('/api/devices/' + id + '/remote-capabilities'); },
  setDeviceSite(id, site_id) { return PATCH('/api/devices/' + id + '/site', { site_id }); },
  deleteDevice(id) { return DELETE('/api/devices/' + id); },
  updateDevice(id, body) { return PATCH('/api/devices/' + id, body); },
  setRemoteIds(id, body) { return PATCH('/api/devices/' + id + '/remote-ids', body); },
  setDeviceGroup(id, group_id) { return PATCH('/api/devices/' + id + '/group', { group_id }); },

  /* ---- departments (device groups, under a site) ---- */
  listGroups() { return GET('/api/groups'); },
  createGroup(body) { return POST('/api/groups', body); },
  deleteGroup(id) { return DELETE('/api/groups/' + id); },

  /* ---- jobs ---- */
  listJobs(params) { return GET('/api/jobs' + qs(params)); },
  getJob(id) { return GET('/api/jobs/' + id); },
  createJob(body) { return POST('/api/jobs', body); },
  createBulkJob(body) { return POST('/api/jobs/bulk', body); },
  getBatch(batchId) { return GET('/api/jobs/batch/' + batchId); },
  approveBatch(batchId) { return POST('/api/jobs/batch/' + batchId + '/approve'); },
  rejectBatch(batchId) { return POST('/api/jobs/batch/' + batchId + '/reject'); },
  approveJob(id) { return POST('/api/jobs/' + id + '/approve'); },
  rejectJob(id) { return POST('/api/jobs/' + id + '/reject'); },
  cancelJob(id) { return POST('/api/jobs/' + id + '/cancel'); },
  completeJob(id, success = true) { return POST('/api/jobs/' + id + '/complete', { success }); },

  /* ---- audit ---- */
  listAudit(limit = 100) { return GET('/api/audit' + qs({ limit })); },

  /* ---- enroll ---- */
  createEnrollToken(body) { return POST('/api/enroll-tokens', body); },

  /* ---- API tokens (MCP connectors & integrations) ---- */
  listAPITokens() { return GET('/api/api-tokens'); },
  createAPIToken(body) { return POST('/api/api-tokens', body); },
  deleteAPIToken(id) { return DELETE('/api/api-tokens/' + id); },

  /* ---- chat ---- */
  listSessions() { return GET('/api/chat/sessions'); },
  createSession(title) { return POST('/api/chat/sessions', { title }); },
  deleteSession(id) { return DELETE('/api/chat/sessions/' + id); },
  getMessages(id) { return GET('/api/chat/sessions/' + id + '/messages'); },
  sendMessage(id, content) { return POST('/api/chat/sessions/' + id + '/messages', { content }); },

  /* ---- users ---- */
  listUsers() { return GET('/api/users'); },
  createUser(body) { return POST('/api/users', body); },
  updateUserRole(id, role) { return PATCH('/api/users/' + id, { role }); },
  deleteUser(id) { return DELETE('/api/users/' + id); },
  getUserScopes(id) { return GET('/api/users/' + id + '/scopes'); },
  setUserScopes(id, scopes) { return PUT('/api/users/' + id + '/scopes', { scopes }); },
  setUserEmail(id, email) { return PUT('/api/users/' + id + '/email', { email }); },

  /* ---- password reset (unauthenticated) ---- */
  // The request endpoint always resolves, whatever the address: the server
  // deliberately refuses to reveal whether an account exists.
  requestPasswordReset(email) { return POST('/api/auth/password-reset', { email }); },
  confirmPasswordReset(token, password) { return POST('/api/auth/password-reset/confirm', { token, password }); },

  /* ---- email notifications ---- */
  // Preferences are self-service (any signed-in user); the SMTP transport below
  // is instance-wide and needs settings.manage.
  getMyNotifications() { return GET('/api/me/notifications'); },
  updateMyNotifications(body) { return PUT('/api/me/notifications', body); },
  getSMTPConfig() { return GET('/api/config/smtp'); },
  updateSMTPConfig(body) { return PUT('/api/config/smtp', body); },
  testSMTP(to) { return POST('/api/config/smtp/test', { to }); },

  /* ---- roles & permissions ---- */
  listRoles() { return GET('/api/roles'); },
  async listPermissions() { return adaptPermissions(await GET('/api/permissions')); },
  createRole(body) { return POST('/api/roles', body); },
  updateRole(key, body) { return PUT('/api/roles/' + encodeURIComponent(key), body); },
  deleteRole(key) { return DELETE('/api/roles/' + encodeURIComponent(key)); },

  /* ---- org ---- */
  listClients() { return GET('/api/clients'); },
  createClient(name) { return POST('/api/clients', { name }); },
  updateClient(id, name) { return PATCH('/api/clients/' + id, { name }); },
  deleteClient(id) { return DELETE('/api/clients/' + id); },
  listSites(client_id) { return GET('/api/sites' + qs({ client_id })); },
  createSite(client_id, name) { return POST('/api/sites', { client_id, name }); },
  updateSite(id, name) { return PATCH('/api/sites/' + id, { name }); },
  deleteSite(id) { return DELETE('/api/sites/' + id); },

  /* ---- scripts ---- */
  listScripts() { return GET('/api/scripts'); },
  createScript(body) { return POST('/api/scripts', body); },
  updateScript(id, body) { return PUT('/api/scripts/' + id, body); },
  deleteScript(id) { return DELETE('/api/scripts/' + id); },
  // Run an ad-hoc script on a device -> approval-gated job (POST /api/jobs).
  runScript(agentId, body) {
    return POST('/api/jobs', {
      agent_id: agentId, shell: body.shell, script: body.script,
      description: body.description || ('Run script: ' + (body.name || 'manual')),
    });
  },

  /* ---- checks ---- */
  async listChecks(params) {
    const r = await GET('/api/checks' + qs(params));
    const checks = (r && r.checks) || [];
    // Attach the latest result for the status pill (best-effort, parallel).
    await Promise.all(checks.map(async (c) => {
      try {
        const rr = await GET('/api/checks/' + c.id + '/results' + qs({ agent_id: c.agent_id, limit: 1 }));
        c.latest = (rr.results || [])[0];
      } catch (e) {}
    }));
    return { checks };
  },
  createCheck(body) { return POST('/api/checks', body); },
  updateCheck(id, body) { return PUT('/api/checks/' + id, body); },
  deleteCheck(id) { return DELETE('/api/checks/' + id); },
  getCheckResults(id, agent_id) { return GET('/api/checks/' + id + '/results' + qs({ agent_id })); },

  /* ---- tasks ---- */
  listTasks(params) { return GET('/api/tasks' + qs(params)); },
  createTask(body) { return POST('/api/tasks', body); },
  updateTask(id, body) { return PUT('/api/tasks/' + id, body); },
  deleteTask(id) { return DELETE('/api/tasks/' + id); },
  taskTargets(id) { return GET('/api/tasks/' + id + '/targets'); },
  runTask(id) { return POST('/api/tasks/' + id + '/run', {}); },

  /* ---- policies ---- */
  listPolicies() { return GET('/api/policies'); },
  createPolicy(body) { return POST('/api/policies', body); },
  deletePolicy(id) { return DELETE('/api/policies/' + id); },
  listAssignments(policyId) { return GET('/api/policies/' + policyId + '/assignments'); },
  createAssignment(policyId, body) { return POST('/api/policies/' + policyId + '/assignments', body); },
  deleteAssignment(aid) { return DELETE('/api/policies/assignments/' + aid); },

  /* ---- alerts ---- */
  listAlerts(status) { return GET('/api/alerts' + qs({ status })); },
  ackAlert(id) { return POST('/api/alerts/' + id + '/ack'); },
  resolveAlert(id) { return POST('/api/alerts/' + id + '/resolve'); },

  /* ---- reports (fleet inventory: software / hardware / machines) ---- */
  reportSoftware(params) { return GET('/api/reports/software' + qs(params || {})); },
  reportHardware(params) { return GET('/api/reports/hardware' + qs(params || {})); },
  reportMachines(params) { return GET('/api/reports/machines' + qs(params || {})); },

  /* ---- patch management ---- */
  devicePatches(id) { return GET('/api/devices/' + id + '/patches'); },
  scanDevicePatches(id) { return POST('/api/devices/' + id + '/patches/scan'); },
  installDevicePatches(id, identifiers) { return POST('/api/devices/' + id + '/patches/install', { identifiers: identifiers || [] }); },
  rebootDevice(id) { return POST('/api/devices/' + id + '/reboot'); },
  patchCatalog() { return GET('/api/patches'); },
  approvePatch(id, body) { return POST('/api/patches/' + id + '/approve', body); },
  compliance() { return GET('/api/reports/compliance'); },
  listPatchPolicies() { return GET('/api/patch-policies'); },
  createPatchPolicy(b) { return POST('/api/patch-policies', b); },
  updatePatchPolicy(id, b) { return PUT('/api/patch-policies/' + id, b); },
  deletePatchPolicy(id) { return DELETE('/api/patch-policies/' + id); },
  listPatchAssignments(id) { return GET('/api/patch-policies/' + id + '/assignments'); },
  addPatchAssignment(id, b) { return POST('/api/patch-policies/' + id + '/assignments', b); },
  deletePatchAssignment(aid) { return DELETE('/api/patch-policies/assignments/' + aid); },

  /* ---- config ---- */
  getConfig() { return GET('/api/config'); },
  updateConfig(body) { return PUT('/api/config', body); },
};

window.api = api;
window.ROLES = ROLES;
window.DEFAULT_ROLE_PERMS = DEFAULT_ROLE_PERMS;
window.PERM_ALIASES = PERM_ALIASES;
