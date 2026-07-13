/**
 * apiClient.ts — cliente REAL (fetch) para o backend Go do RMM-AI.
 *
 * Espelha 1:1 a superfície de `window.api` definida em src/mockapi.jsx.
 * Para conectar o frontend ao backend:
 *   - Num projeto Vite/TS: importe `api` daqui e use no lugar do mock.
 *   - No protótipo atual (Babel/HTML): exponha como `window.api = api` no fim
 *     do arquivo e remova o conteúdo simulado de src/mockapi.jsx.
 *
 * Auth: o token de POST /api/auth/login é salvo em localStorage e enviado como
 * `Authorization: Bearer <token>` em toda chamada /api/*. Em 401, limpa a sessão.
 */

// ----------------------------- tipos (do brief) -----------------------------
export type Role = string;
export type PermissionKey = string;
export type User = { id: string; username: string; role: Role; permissions?: PermissionKey[]; created_at: string };
export type UserScope = { scope_type: "client" | "site" | "group" | "agent"; scope_id: string };
export type Permission = { key: string; label: string; description: string; group: string };
export type RoleDefinition = {
  key: string; name: string; description: string; system: boolean;
  permissions: PermissionKey[]; created_at?: string; updated_at?: string;
};

export type Agent = {
  id: string; hostname: string; os: string; arch: string; version: string;
  status: "online" | "offline"; group_id?: string; tags: string[];
  mesh_id?: string; last_seen?: string; created_at: string;
  site_id?: string; description: string;
};

export type Job = {
  id: string; agent_id: string; type: "run_script"; description: string;
  shell: "bash" | "sh" | "powershell" | "cmd"; script: string;
  status: "awaiting_approval" | "pending" | "sent" | "running" | "completed" | "failed" | "rejected";
  requires_approval: boolean; created_by: string; approved_by?: string;
  exit_code?: number; stdout: string; stderr: string; created_at: string; updated_at: string;
  task_id?: string; batch_id?: string;
};

export type RemoteSession = {
  id: string; agent_id: string; provider: "native" | "terminal" | "meshcentral" | "rustdesk";
  status: string; connect_url: string; expires_at: string;
};

export type AuditEntry = {
  id: string; actor: string; action: string; target: string;
  detail: Record<string, unknown>; created_at: string;
};

export type ChatSession = { id: string; title: string; created_at: string };
export type ChatMessage = {
  id: string; session_id: string;
  role: "user" | "assistant" | "tool" | "system"; content: string;
  tool_calls?: unknown; tool_call_id?: string; name?: string; created_at: string;
};

export type Client = { id: string; name: string; created_at: string };
export type Site = { id: string; client_id: string; name: string; created_at: string };

export type Script = {
  id: string; name: string; description: string; category: string;
  shell: "bash" | "sh" | "powershell" | "cmd"; body: string;
  default_args: string; created_at: string; updated_at: string;
};

export type CheckType = "diskspace" | "memory" | "cpuload" | "ping" | "winservice" | "script";
export type Check = {
  id: string; agent_id?: string; policy_id?: string; type: CheckType; name: string;
  config: Record<string, unknown>; warn_threshold?: number; err_threshold?: number;
  interval_sec: number; enabled: boolean; created_at: string;
};
export type CheckResult = {
  id: string; check_id: string; agent_id: string;
  status: "passing" | "warning" | "failing"; value?: number; output: string; ran_at: string;
};

export type Task = {
  id: string; agent_id?: string; policy_id?: string; name: string;
  script_id?: string; shell: string; script: string;
  schedule_kind: "interval" | "daily"; interval_sec: number; run_at_minute: number;
  enabled: boolean; created_at: string;
};

export type Policy = { id: string; name: string; description: string; created_at: string };
export type PolicyAssignment = {
  id: string; policy_id: string; scope_type: "client" | "site" | "agent";
  scope_id: string; created_at: string;
};

export type Alert = {
  id: string; agent_id?: string; source_type: "check" | "task" | "agent"; source_id?: string;
  severity: "info" | "warning" | "error"; message: string;
  status: "new" | "acked" | "resolved"; created_at: string; resolved_at?: string;
};

// --------------------------------- base ------------------------------------
const API_BASE =
  (typeof import.meta !== "undefined" && (import.meta as any).env?.VITE_API_BASE) ||
  (globalThis as any).__API_BASE__ ||
  "http://localhost:8080";

const TOKEN_KEY = "rmm-ai-auth";
const DEFAULT_ROLE_PERMS: Record<string, PermissionKey[]> = {
  readonly: ["devices.view", "devices.remote", "clients.view"],
  tech: ["ai_chat.use", "mcp.use", "alerts.view", "alerts.manage", "jobs.view", "jobs.create", "jobs.manage", "checks.view", "tasks.view", "policies.view", "scripts.view", "patches.view", "patches.manage", "devices.view", "devices.manage", "devices.remote", "devices.files", "devices.terminal", "clients.view", "reports.view"],
  admin: ["ai_chat.use", "mcp.use", "alerts.view", "alerts.manage", "jobs.view", "jobs.create", "jobs.manage", "checks.view", "checks.manage", "tasks.view", "tasks.manage", "policies.view", "policies.manage", "scripts.view", "scripts.manage", "patches.view", "patches.manage", "patches.policies.manage", "devices.view", "devices.manage", "devices.delete", "devices.remote", "devices.files", "devices.terminal", "clients.view", "clients.manage", "reports.view", "audit.view", "settings.manage", "enrollment.manage", "api_tokens.manage", "users.manage", "roles.manage"],
};
const PERM_ALIASES: Record<string, PermissionKey> = { admin: "settings.manage", tech: "jobs.manage", readonly: "devices.view" };

type Session = { token: string; user: User; role: Role; permissions: PermissionKey[] };

let session: Session | null = (() => {
  try { return JSON.parse(localStorage.getItem(TOKEN_KEY) || "null"); } catch { return null; }
})();

function setSession(s: typeof session) {
  session = s;
  if (s) localStorage.setItem(TOKEN_KEY, JSON.stringify(s));
  else localStorage.removeItem(TOKEN_KEY);
}

function normalizeSession(token: string, r: Partial<User> & { user?: User; role?: Role; permissions?: PermissionKey[] }): Session {
  const user = (r.user || r) as User;
  const role = r.role || user.role;
  const permissions = r.permissions || user.permissions || DEFAULT_ROLE_PERMS[role] || [];
  return { token, user: { ...user, permissions }, role, permissions };
}

function sessionPerms() {
  return new Set(session?.permissions || session?.user?.permissions || DEFAULT_ROLE_PERMS[session?.role || ""] || []);
}

function canPermission(key: string) {
  if (!session) return false;
  return sessionPerms().has(PERM_ALIASES[key] || key);
}

class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

async function request<T>(method: string, path: string, body?: unknown, attempt = 0): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (session?.token) headers.Authorization = `Bearer ${session.token}`;

  const res = await fetch(`${API_BASE}${path}`, {
    method, headers, body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  // 429: per-IP API limiter pushed back. Retry with backoff (honoring
  // Retry-After) before surfacing — the SPA fires bursts on navigation.
  if (res.status === 429 && attempt < 4) {
    const ra = parseFloat(res.headers.get("Retry-After") || "");
    const wait = Number.isFinite(ra) && ra > 0 ? ra * 1000 : 350 * (attempt + 1) + Math.random() * 250;
    await sleep(Math.min(wait, 4000));
    return request<T>(method, path, body, attempt + 1);
  }
  if (res.status === 401) { setSession(null); throw new ApiError(401, "unauthorized"); }
  if (!res.ok) {
    let msg = res.statusText;
    try { const j = await res.json(); msg = (j as any).error || (j as any).message || msg; } catch {}
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

const GET = <T>(p: string) => request<T>("GET", p);
const POST = <T>(p: string, b?: unknown) => request<T>("POST", p, b);
const PUT = <T>(p: string, b?: unknown) => request<T>("PUT", p, b);
const PATCH = <T>(p: string, b?: unknown) => request<T>("PATCH", p, b);
const DELETE = <T>(p: string) => request<T>("DELETE", p);
const qs = (o: Record<string, unknown>) => {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v != null && v !== "") u.set(k, String(v));
  const s = u.toString(); return s ? `?${s}` : "";
};

function adaptPermissions(r: { permissions?: Permission[] }) {
  const permissions = (r.permissions || []).map((p) => (
    p.key === "ai_chat.use" ? { ...p, label: p.label === "Use AI Chat" ? "AI Chat" : p.label, group: "AI" }
      : p.key === "jobs.manage" ? { ...p, label: "Manage jobs", description: "Approve, reject, and cancel jobs.", group: "Jobs" }
        : p
  ));
  if (!permissions.some((p) => p.key === "mcp.use")) {
    permissions.push({ key: "mcp.use", label: "MCP", description: "Connect external MCP clients to AI tools.", group: "AI" });
  }
  if (!permissions.some((p) => p.key === "jobs.create")) {
    const i = permissions.findIndex((p) => p.key === "jobs.manage");
    const create = { key: "jobs.create", label: "Create jobs", description: "Create approval-gated jobs and bulk deployments.", group: "Jobs" };
    if (i >= 0) permissions.splice(i, 0, create); else permissions.push(create);
  }
  return { permissions };
}

// --------------------------------- api -------------------------------------
export const api = {
  API_BASE,

  // ---- auth & sessão ----
  async login(username: string, password: string) {
    const r = await POST<{ token: string; role: Role; user: User; permissions?: PermissionKey[] }>("/api/auth/login", { username, password });
    setSession(normalizeSession(r.token, r));
    return r;
  },
  /** Primeiro acesso quando a instância está vazia (sem usuários). */
  async bootstrap(username: string, password: string) {
    const r = await POST<{ token: string; role: Role; user: User; permissions?: PermissionKey[] }>("/api/auth/bootstrap", { username, password });
    setSession(normalizeSession(r.token, r));
    return r;
  },
  /** Login por token colado (MVP). Resolve o principal via GET /api/me. */
  async loginWithToken(token: string) {
    setSession({ token, user: { id: "", username: "", role: "readonly", permissions: [], created_at: "" }, role: "readonly", permissions: [] });
    const user = await GET<User>("/api/me");
    const next = normalizeSession(token, user);
    setSession(next);
    return next;
  },
  async me() { return GET<User>("/api/me"); },
  async logout() { try { await POST("/api/auth/logout"); } finally { setSession(null); } },
  currentRole(): Role | null { return session?.role ?? null; },
  permissions(): PermissionKey[] { return Array.from(sessionPerms()); },
  can(key: string) { return canPermission(key); },

  // ---- devices ----
  listDevices: () => GET<{ devices: Agent[] }>("/api/devices"),
  getDevice: (id: string) => GET<Agent>(`/api/devices/${id}`),
  getInventory: (id: string) => GET<{ agent_id: string; data: any; collected_at: string }>(`/api/devices/${id}/inventory`),
  startRemote: (id: string, provider: RemoteSession["provider"]) =>
    POST<RemoteSession>(`/api/devices/${id}/remote`, { provider }),
  setDeviceSite: (id: string, site_id: string) =>
    PATCH<Agent>(`/api/devices/${id}/site`, { site_id }),
  deleteDevice: (id: string) => DELETE<{ status: string }>(`/api/devices/${id}`),
  setDeviceGroup: (id: string, group_id: string | null) =>
    PATCH<{ status: string }>(`/api/devices/${id}/group`, { group_id }),
  // departments = device groups, each under a site (Company > Site > Department)
  listGroups: () => GET<{ groups: { id: string; name: string; site_id?: string; created_at: string }[] }>("/api/groups"),
  createGroup: (body: { name: string; site_id: string }) => POST<{ id: string; name: string; site_id?: string }>("/api/groups", body),
  deleteGroup: (id: string) => DELETE<{ status: string }>(`/api/groups/${id}`),

  // ---- jobs ----
  listJobs: (params: { agent_id?: string; limit?: number } = {}) =>
    GET<{ jobs: Job[] }>(`/api/jobs${qs(params)}`),
  getJob: (id: string) => GET<Job>(`/api/jobs/${id}`),
  createJob: (body: { agent_id: string; shell: Job["shell"]; script: string; description: string; require_approval?: boolean }) =>
    POST<Job>("/api/jobs", body),
  // A deployment never spans companies: the server splits the targets into one
  // batch per company (a multi-company request fans out into several batches).
  createBulkJob: (body: { agent_ids?: string[]; group_id?: string; site_id?: string; shell: Job["shell"]; script: string; description?: string }) =>
    POST<{ batches: { client_id?: string; batch_id: string; count: number }[]; count: number; companies: number }>("/api/jobs/bulk", body),
  getBatch: (batchId: string) => GET<{ jobs: Job[] }>(`/api/jobs/batch/${batchId}`),
  approveBatch: (batchId: string) => POST<{ approved: number }>(`/api/jobs/batch/${batchId}/approve`),
  rejectBatch: (batchId: string) => POST<{ rejected: number }>(`/api/jobs/batch/${batchId}/reject`),
  approveJob: (id: string) => POST<Job>(`/api/jobs/${id}/approve`),
  rejectJob: (id: string) => POST<Job>(`/api/jobs/${id}/reject`),

  // ---- audit ----
  listAudit: (limit = 100) => GET<{ entries: AuditEntry[] }>(`/api/audit${qs({ limit })}`),

  // ---- enroll ----
  createEnrollToken: (body: { description: string; uses: number; ttl_hours: number; site_id?: string }) =>
    POST<{ token: string; [k: string]: unknown }>("/api/enroll-tokens", body),

  // ---- API tokens (MCP connectors & integrations) ----
  listAPITokens: () =>
    GET<{ tokens: { id: string; name: string; role: string; created_at: string; expires_at?: string | null }[] }>("/api/api-tokens"),
  createAPIToken: (body: { name: string; role?: string; user_id?: string; expires_in_days?: number }) =>
    POST<{ id: string; name: string; role: string; token: string; note: string; expires_at?: string | null }>("/api/api-tokens", body),
  deleteAPIToken: (id: string) => DELETE<{ status: string }>("/api/api-tokens/" + id),

  // ---- chat ----
  listSessions: () => GET<{ sessions: ChatSession[] }>("/api/chat/sessions"),
  createSession: (title: string) => POST<ChatSession>("/api/chat/sessions", { title }),
  deleteSession: (id: string) => DELETE<{ status: string }>(`/api/chat/sessions/${id}`),
  getMessages: (id: string) => GET<{ messages: ChatMessage[] }>(`/api/chat/sessions/${id}/messages`),
  /** Roda o loop do agente no backend; retorna a resposta + jobs propostos. */
  sendMessage: (id: string, content: string) =>
    POST<{ content: string; created_job_ids: string[] }>(`/api/chat/sessions/${id}/messages`, { content }),

  // ---- users ----
  listUsers: () => GET<{ users: User[] }>("/api/users"),
  createUser: (body: { username: string; role: Role; password?: string; scopes?: UserScope[] }) => POST<User>("/api/users", body),
  updateUserRole: (id: string, role: Role) => PATCH<User>(`/api/users/${id}`, { role }),
  deleteUser: (id: string) => DELETE<void>(`/api/users/${id}`),
  getUserScopes: (id: string) => GET<{ scopes: UserScope[] }>(`/api/users/${id}/scopes`),
  setUserScopes: (id: string, scopes: UserScope[]) => PUT<{ scopes: UserScope[] }>(`/api/users/${id}/scopes`, { scopes }),

  // ---- roles & permissions ----
  listRoles: () => GET<{ roles: RoleDefinition[] }>("/api/roles"),
  listPermissions: async () => adaptPermissions(await GET<{ permissions: Permission[] }>("/api/permissions")),
  createRole: (body: { key: string; name: string; description?: string; permissions: PermissionKey[] }) => POST<RoleDefinition>("/api/roles", body),
  updateRole: (key: string, body: { name?: string; description?: string; permissions?: PermissionKey[] }) => PUT<RoleDefinition>(`/api/roles/${encodeURIComponent(key)}`, body),
  deleteRole: (key: string) => DELETE<void>(`/api/roles/${encodeURIComponent(key)}`),

  // ---- org (clients & sites) ----
  listClients: () => GET<{ clients: Client[] }>("/api/clients"),
  createClient: (name: string) => POST<Client>("/api/clients", { name }),
  updateClient: (id: string, name: string) => PATCH<{ status: string }>(`/api/clients/${id}`, { name }),
  deleteClient: (id: string) => DELETE<void>(`/api/clients/${id}`),
  listSites: (client_id?: string) => GET<{ sites: Site[] }>(`/api/sites${qs({ client_id })}`),
  createSite: (client_id: string, name: string) => POST<Site>("/api/sites", { client_id, name }),
  updateSite: (id: string, name: string) => PATCH<{ status: string }>(`/api/sites/${id}`, { name }),
  deleteSite: (id: string) => DELETE<void>(`/api/sites/${id}`),

  // ---- scripts ----
  listScripts: () => GET<{ scripts: Script[] }>("/api/scripts"),
  createScript: (body: Partial<Script>) => POST<Script>("/api/scripts", body),
  updateScript: (id: string, body: Partial<Script>) => PUT<Script>(`/api/scripts/${id}`, body),
  deleteScript: (id: string) => DELETE<void>(`/api/scripts/${id}`),
  /** Cria um job aprovável a partir de um script. */
  runScript: (agentId: string, body: { shell: string; script: string; description?: string; name?: string }) =>
    POST<Job>(`/api/devices/${agentId}/run-script`, body),

  // ---- checks ----
  listChecks: (params: { agent_id?: string; policy_id?: string } = {}) =>
    GET<{ checks: Check[] }>(`/api/checks${qs(params)}`),
  createCheck: (body: Partial<Check>) => POST<Check>("/api/checks", body),
  updateCheck: (id: string, body: Partial<Check>) => PUT<Check>(`/api/checks/${id}`, body),
  deleteCheck: (id: string) => DELETE<void>(`/api/checks/${id}`),
  getCheckResults: (id: string, agent_id?: string) =>
    GET<{ results: CheckResult[] }>(`/api/checks/${id}/results${qs({ agent_id })}`),

  // ---- tasks ----
  listTasks: (params: { agent_id?: string; policy_id?: string } = {}) =>
    GET<{ tasks: Task[] }>(`/api/tasks${qs(params)}`),
  createTask: (body: Partial<Task>) => POST<Task>("/api/tasks", body),
  updateTask: (id: string, body: Partial<Task>) => PUT<Task>(`/api/tasks/${id}`, body),
  deleteTask: (id: string) => DELETE<void>(`/api/tasks/${id}`),

  // ---- policies ----
  listPolicies: () => GET<{ policies: Policy[] }>("/api/policies"),
  createPolicy: (body: { name: string; description?: string }) => POST<Policy>("/api/policies", body),
  deletePolicy: (id: string) => DELETE<void>(`/api/policies/${id}`),
  listAssignments: (policyId: string) => GET<{ assignments: PolicyAssignment[] }>(`/api/policies/${policyId}/assignments`),
  createAssignment: (policyId: string, body: { scope_type: PolicyAssignment["scope_type"]; scope_id: string }) =>
    POST<PolicyAssignment>(`/api/policies/${policyId}/assignments`, body),
  deleteAssignment: (aid: string) => DELETE<void>(`/api/policies/assignments/${aid}`),

  // ---- alerts ----
  listAlerts: (status?: Alert["status"]) => GET<{ alerts: Alert[] }>(`/api/alerts${qs({ status })}`),
  ackAlert: (id: string) => POST<Alert>(`/api/alerts/${id}/ack`),
  resolveAlert: (id: string) => POST<Alert>(`/api/alerts/${id}/resolve`),

  // ---- config (ajuste para o seu endpoint real, ex.: GET /api/config) ----
  getConfig: () => GET<any>("/api/config"),
};

// Protótipo (Babel/HTML) — descomente para usar este cliente no lugar do mock:
// (globalThis as any).api = api;
// (globalThis as any).ROLES = ROLES;

export default api;
