/* mockapi.jsx — REAL fetch client for the RMM-AI Go backend.
   (Filename kept for index.html; this is the live client, not a mock.)

   Exposes window.api with the exact surface the UI expects. Bearer token from
   POST /api/auth/login is sent as Authorization on every /api/* call.
   API base: window.__API_BASE__ (default http://localhost:8080). */

const API_BASE = (window.__API_BASE__) || 'http://localhost:8080';
const ROLES = { admin: 3, tech: 2, readonly: 1 };
const AUTH_KEY = 'rmm-ai-auth';

/* event bus kept for compatibility (mutations can emit; queries may listen) */
const bus = (() => {
  const subs = new Set();
  return { sub(fn) { subs.add(fn); return () => subs.delete(fn); }, emit(key) { subs.forEach((f) => f(key)); } };
})();
window.dataBus = bus;

/* ------------------------------- session --------------------------------- */
let session = (() => {
  try { return JSON.parse(localStorage.getItem(AUTH_KEY) || 'null'); } catch (e) { return null; }
})();
function setSession(s) {
  session = s;
  try { s ? localStorage.setItem(AUTH_KEY, JSON.stringify(s)) : localStorage.removeItem(AUTH_KEY); } catch (e) {}
}

function apiError(status, message) { const e = new Error(message); e.status = status; return e; }

/* ------------------------------- transport ------------------------------- */
async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (session && session.token) headers.Authorization = 'Bearer ' + session.token;
  let res;
  try {
    res = await fetch(API_BASE + path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined });
  } catch (e) {
    throw apiError(0, 'cannot reach backend at ' + API_BASE);
  }
  if (res.status === 401) {
    setSession(null);
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
const qs = (o) => {
  const u = new URLSearchParams();
  Object.entries(o || {}).forEach(([k, v]) => { if (v != null && v !== '') u.set(k, String(v)); });
  const s = u.toString(); return s ? '?' + s : '';
};

/* ---------- adapter: real (flat) inventory -> UI's structured shape ---------- */
function adaptInventory(inv) {
  const d = (inv && inv.data) || {};
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
      network: { interfaces: (d.network_interfaces || []).map((n) => ({ name: n.name, ipv4: n.ipv4, mac: n.mac })) },
      software: (d.software || []).map((s) => ({ name: s.name, version: s.version })),
      agent: { version: d.agent_version, last_check_in: inv && inv.collected_at },
      raw: d,
    },
  };
}

/* ---------------------------------- api ---------------------------------- */
const api = {
  API_BASE,

  /* ---- auth ---- */
  async login(username, password, code) {
    const r = await POST('/api/auth/login', { username, password, code });
    setSession({ token: r.token, user: r.user, role: r.role });
    return r;
  },
  setup2FA() { return POST('/api/me/2fa/setup'); },
  enable2FA(code) { return POST('/api/me/2fa/enable', { code }); },
  disable2FA(code) { return POST('/api/me/2fa/disable', { code }); },
  regenRecoveryCodes(code) { return POST('/api/me/2fa/recovery-codes', { code }); },
  reset2FA(id) { return POST('/api/users/' + id + '/2fa/reset'); },
  changePassword(currentPassword, newPassword) { return POST('/api/me/password', { current_password: currentPassword, new_password: newPassword }); },
  async bootstrap(username, password) {
    const r = await POST('/api/auth/bootstrap', { username, password });
    setSession({ token: r.token, user: r.user, role: r.role });
    return r;
  },
  async loginWithToken(token) {
    setSession({ token, user: { id: '', username: '', role: 'readonly', created_at: '' }, role: 'readonly' });
    const user = await GET('/api/me');
    setSession({ token, user, role: user.role });
    return { token, user, role: user.role };
  },
  async me() { return GET('/api/me'); },
  async logout() { try { await POST('/api/auth/logout'); } catch (e) {} finally { setSession(null); } },
  currentRole() { return session ? session.role : null; },
  can(min) { return !!session && ROLES[session.role] >= ROLES[min]; },
  hasUsers() { return true; },

  /* ---- devices ---- */
  listDevices(params) { return GET('/api/devices' + qs(params || {})); },
  getDevice(id) { return GET('/api/devices/' + id); },
  async getInventory(id) { return adaptInventory(await GET('/api/devices/' + id + '/inventory')); },
  startRemote(id, provider) { return POST('/api/devices/' + id + '/remote', { provider }); },
  setDeviceSite(id, site_id) { return PATCH('/api/devices/' + id + '/site', { site_id }); },
  updateDevice(id, body) { return PATCH('/api/devices/' + id, body); },
  setRemoteIds(id, body) { return PATCH('/api/devices/' + id + '/remote-ids', body); },

  /* ---- jobs ---- */
  listJobs(params) { return GET('/api/jobs' + qs(params)); },
  getJob(id) { return GET('/api/jobs/' + id); },
  createJob(body) { return POST('/api/jobs', body); },
  approveJob(id) { return POST('/api/jobs/' + id + '/approve'); },
  rejectJob(id) { return POST('/api/jobs/' + id + '/reject'); },
  cancelJob(id) { return POST('/api/jobs/' + id + '/cancel'); },

  /* ---- audit ---- */
  listAudit(limit = 100) { return GET('/api/audit' + qs({ limit })); },

  /* ---- enroll ---- */
  createEnrollToken(body) { return POST('/api/enroll-tokens', body); },

  /* ---- chat ---- */
  listSessions() { return GET('/api/chat/sessions'); },
  createSession(title) { return POST('/api/chat/sessions', { title }); },
  getMessages(id) { return GET('/api/chat/sessions/' + id + '/messages'); },
  sendMessage(id, content) { return POST('/api/chat/sessions/' + id + '/messages', { content }); },

  /* ---- users ---- */
  listUsers() { return GET('/api/users'); },
  createUser(body) { return POST('/api/users', body); },
  deleteUser(id) { return DELETE('/api/users/' + id); },
  getUserScopes(id) { return GET('/api/users/' + id + '/scopes'); },
  setUserScopes(id, scopes) { return PUT('/api/users/' + id + '/scopes', { scopes }); },

  /* ---- org ---- */
  listClients() { return GET('/api/clients'); },
  createClient(name) { return POST('/api/clients', { name }); },
  deleteClient(id) { return DELETE('/api/clients/' + id); },
  listSites(client_id) { return GET('/api/sites' + qs({ client_id })); },
  createSite(client_id, name) { return POST('/api/sites', { client_id, name }); },
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

  /* ---- config ---- */
  getConfig() { return GET('/api/config'); },
  updateConfig(body) { return PUT('/api/config', body); },
};

window.api = api;
window.ROLES = ROLES;
