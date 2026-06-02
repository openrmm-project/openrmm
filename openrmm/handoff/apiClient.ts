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
export type Role = "admin" | "tech" | "readonly";
export type User = { id: string; username: string; role: Role; created_at: string };

export type Agent = {
  id: string; hostname: string; os: string; arch: string; version: string;
  status: "online" | "offline"; group_id?: string; tags: string[];
  rustdesk_id?: string; mesh_id?: string; last_seen?: string; created_at: string;
  site_id?: string; description: string;
};

export type Job = {
  id: string; agent_id: string; type: "run_script"; description: string;
  shell: "bash" | "sh" | "powershell" | "cmd"; script: string;
  status: "awaiting_approval" | "pending" | "sent" | "running" | "completed" | "failed" | "rejected";
  requires_approval: boolean; created_by: string; approved_by?: string;
  exit_code?: number; stdout: string; stderr: string; created_at: string; updated_at: string;
  task_id?: string;
};

export type RemoteSession = {
  id: string; agent_id: string; provider: "native" | "meshcentral" | "rustdesk";
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
const ROLES: Record<Role, number> = { admin: 3, tech: 2, readonly: 1 };

let session: { token: string; user: User; role: Role } | null = (() => {
  try { return JSON.parse(localStorage.getItem(TOKEN_KEY) || "null"); } catch { return null; }
})();

function setSession(s: typeof session) {
  session = s;
  if (s) localStorage.setItem(TOKEN_KEY, JSON.stringify(s));
  else localStorage.removeItem(TOKEN_KEY);
}

class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (session?.token) headers.Authorization = `Bearer ${session.token}`;

  const res = await fetch(`${API_BASE}${path}`, {
    method, headers, body: body !== undefined ? JSON.stringify(body) : undefined,
  });

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

// --------------------------------- api -------------------------------------
export const api = {
  API_BASE,

  // ---- auth & sessão ----
  async login(username: string, password: string) {
    const r = await POST<{ token: string; role: Role; user: User }>("/api/auth/login", { username, password });
    setSession({ token: r.token, user: r.user, role: r.role });
    return r;
  },
  /** Primeiro acesso quando a instância está vazia (sem usuários). */
  async bootstrap(username: string, password: string) {
    const r = await POST<{ token: string; role: Role; user: User }>("/api/auth/bootstrap", { username, password });
    setSession({ token: r.token, user: r.user, role: r.role });
    return r;
  },
  /** Login por token colado (MVP). Resolve o principal via GET /api/me. */
  async loginWithToken(token: string) {
    setSession({ token, user: { id: "", username: "", role: "readonly", created_at: "" }, role: "readonly" });
    const user = await GET<User>("/api/me");
    setSession({ token, user, role: user.role });
    return { token, role: user.role, user };
  },
  async me() { return GET<User>("/api/me"); },
  async logout() { try { await POST("/api/auth/logout"); } finally { setSession(null); } },
  currentRole(): Role | null { return session?.role ?? null; },
  can(min: Role) { return !!session && ROLES[session.role] >= ROLES[min]; },

  // ---- devices ----
  listDevices: () => GET<{ devices: Agent[] }>("/api/devices"),
  getDevice: (id: string) => GET<Agent>(`/api/devices/${id}`),
  getInventory: (id: string) => GET<{ agent_id: string; data: any; collected_at: string }>(`/api/devices/${id}/inventory`),
  startRemote: (id: string, provider: RemoteSession["provider"]) =>
    POST<RemoteSession>(`/api/devices/${id}/remote`, { provider }),
  setDeviceSite: (id: string, site_id: string) =>
    PATCH<Agent>(`/api/devices/${id}/site`, { site_id }),

  // ---- jobs ----
  listJobs: (params: { agent_id?: string; limit?: number } = {}) =>
    GET<{ jobs: Job[] }>(`/api/jobs${qs(params)}`),
  getJob: (id: string) => GET<Job>(`/api/jobs/${id}`),
  createJob: (body: { agent_id: string; shell: Job["shell"]; script: string; description: string; require_approval?: boolean }) =>
    POST<Job>("/api/jobs", body),
  approveJob: (id: string) => POST<Job>(`/api/jobs/${id}/approve`),
  rejectJob: (id: string) => POST<Job>(`/api/jobs/${id}/reject`),

  // ---- audit ----
  listAudit: (limit = 100) => GET<{ entries: AuditEntry[] }>(`/api/audit${qs({ limit })}`),

  // ---- enroll ----
  createEnrollToken: (body: { description: string; uses: number; ttl_hours: number; site_id?: string }) =>
    POST<{ token: string; [k: string]: unknown }>("/api/enroll-tokens", body),

  // ---- chat ----
  listSessions: () => GET<{ sessions: ChatSession[] }>("/api/chat/sessions"),
  createSession: (title: string) => POST<ChatSession>("/api/chat/sessions", { title }),
  getMessages: (id: string) => GET<{ messages: ChatMessage[] }>(`/api/chat/sessions/${id}/messages`),
  /** Roda o loop do agente no backend; retorna a resposta + jobs propostos. */
  sendMessage: (id: string, content: string) =>
    POST<{ content: string; created_job_ids: string[] }>(`/api/chat/sessions/${id}/messages`, { content }),

  // ---- users ----
  listUsers: () => GET<{ users: User[] }>("/api/users"),
  createUser: (body: { username: string; role: Role; password?: string }) => POST<User>("/api/users", body),
  deleteUser: (id: string) => DELETE<void>(`/api/users/${id}`),

  // ---- org (clients & sites) ----
  listClients: () => GET<{ clients: Client[] }>("/api/clients"),
  createClient: (name: string) => POST<Client>("/api/clients", { name }),
  deleteClient: (id: string) => DELETE<void>(`/api/clients/${id}`),
  listSites: (client_id?: string) => GET<{ sites: Site[] }>(`/api/sites${qs({ client_id })}`),
  createSite: (client_id: string, name: string) => POST<Site>("/api/sites", { client_id, name }),
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
