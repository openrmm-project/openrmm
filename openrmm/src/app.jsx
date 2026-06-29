/* app.jsx — shell: auth, sidebar, router, topbar. Mounts the app. */
const { useState: uS, useEffect: uE, useContext: uC, createContext: cCtx } = React;

const AUTH_KEY = 'rmm-ai-auth';
const AuthCtx = cCtx(null);
function useAuth() { return uC(AuthCtx); }

/* -------------------------------- Login ----------------------------------- */
const DEMO_USERS = [
  { username: 'admin', role: 'admin', label: 'Full access — manage everything' },
  { username: 'tech@northwind', role: 'tech', label: 'Run, approve & acknowledge' },
  { username: 'auditor', role: 'readonly', label: 'View only' },
];

function LoginScreen({ onAuthed }) {
  const [mode, setMode] = useState('password'); // password | token
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('demo');
  const [token, setToken] = useState('');
  const [code, setCode] = useState('');
  const [need2fa, setNeed2fa] = useState(false);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  const doLogin = async () => {
    setBusy(true); setErr(null);
    try {
      const res = mode === 'token' ? await api.loginWithToken(token.trim()) : await api.login(username.trim(), password, code.trim() || undefined);
      localStorage.setItem(AUTH_KEY, JSON.stringify({ token: res.token, user: res.user, role: res.role }));
      onAuthed(res);
    } catch (e) {
      if (e.totpRequired) { setNeed2fa(true); setErr(code ? 'Invalid code — try again' : 'Enter your authenticator code'); }
      else setErr(e.message || 'Login failed');
    } finally { setBusy(false); }
  };

  return (
    <div className="h-full w-full overflow-y-auto scroll-area grain flex items-center justify-center p-6">
      <div className="w-full max-w-[400px] my-auto animate-fade-in">
        <div className="flex items-center gap-2.5 mb-7">
          <BrandMark size={34} />
          <div>
            <div className="text-[17px] font-semibold text-ink tracking-tight">OpenRMM</div>
            <div className="text-[12px] text-faint -mt-0.5">Remote Monitoring & Management</div>
          </div>
        </div>

        <Card className="p-6 shadow-2xl shadow-black/30">
          <div className="text-[15px] font-semibold text-ink">Sign in to your instance</div>
          <div className="text-[12.5px] text-muted mt-1">Self-hosted · {api.API_BASE}</div>

          <div className="flex gap-1 mt-5 p-0.5 bg-app rounded-lg border border-line">
            {[['password', 'Username'], ['token', 'API token']].map(([v, l]) => (
              <button key={v} onClick={() => setMode(v)} className={cx('flex-1 h-8 rounded-md text-[12.5px] font-medium transition', mode === v ? 'bg-elevated text-ink shadow-sm' : 'text-faint hover:text-muted')}>{l}</button>
            ))}
          </div>

          <div className="mt-4 space-y-3" onKeyDown={(e) => e.key === 'Enter' && doLogin()}>
            {mode === 'password' ? (
              <>
                <Field label="Username"><Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="admin" autoFocus /></Field>
                <Field label="Password"><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" /></Field>
                {need2fa && <Field label="Authenticator code" hint="6-digit app code, or a recovery code if you lost your device."><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456 or xxxxx-xxxxx" autoFocus className="mono tracking-widest" /></Field>}
              </>
            ) : (
              <Field label="Bearer token" hint="Paste an issued API token or the admin token.">
                <Input value={token} onChange={(e) => setToken(e.target.value)} placeholder="tok_…" autoFocus className="mono" />
              </Field>
            )}
            {err && <div className="text-[12.5px] text-bad flex items-center gap-1.5"><Icon name="alert" size={13} />{err}</div>}
            <Button variant="primary" size="lg" className="w-full" loading={busy} onClick={doLogin} disabled={mode === 'token' && !token.trim()}>Sign in</Button>
          </div>

          <div className="mt-5 pt-4 border-t border-line">
            <div className="text-[11px] uppercase tracking-wide text-faint font-semibold mb-2">Demo — sign in as a role</div>
            <div className="space-y-1.5">
              {DEMO_USERS.map((u) => (
                <button key={u.username} onClick={() => { setMode('password'); setUsername(u.username); setPassword('demo'); }}
                  className="w-full flex items-center gap-2.5 rounded-lg border border-line bg-app hover:border-line2 hover:bg-elevated px-2.5 py-2 text-left transition">
                  <Avatar name={u.username} size={26} />
                  <div className="min-w-0 flex-1">
                    <div className="text-[12.5px] text-ink font-medium flex items-center gap-1.5">{u.username} <RoleBadge role={u.role} /></div>
                    <div className="text-[11px] text-faint truncate">{u.label}</div>
                  </div>
                  <Icon name="chevright" size={14} className="text-faint" />
                </button>
              ))}
            </div>
          </div>
        </Card>
        <div className="text-center text-[11.5px] text-faint mt-4">Password is anything non-empty in this demo. Token mode accepts any value.</div>
      </div>
    </div>
  );
}

function BrandMark({ size = 32 }) {
  return (
    <img src="assets/icone_openrmm.png" alt="OpenRMM" width={size} height={size}
      className="rounded-xl shrink-0 shadow-lg shadow-brand/20 object-cover"
      style={{ width: size, height: size }} />
  );
}

/* ------------------------------- Sidebar ---------------------------------- */
const NAV = [
  { section: null, items: [
    { id: 'dashboard', label: 'Dashboard', icon: 'dashboard' },
    { id: 'chat', label: 'AI Chat', icon: 'chat', accent: true },
    { id: 'alerts', label: 'Alerts', icon: 'alert', badge: 'alerts' },
  ] },
  { section: 'Fleet', items: [
    { id: 'devices', label: 'Devices', icon: 'devices' },
    { id: 'jobs', label: 'Jobs', icon: 'flash', badge: 'jobs' },
    { id: 'checks', label: 'Checks', icon: 'check' },
    { id: 'tasks', label: 'Scheduled tasks', icon: 'tasks' },
    { id: 'policies', label: 'Policies', icon: 'policy', min: 'admin' },
    { id: 'scripts', label: 'Scripts', icon: 'script', min: 'tech' },
    { id: 'reports', label: 'Reports', icon: 'report' },
  ] },
  { section: 'Organization', items: [
    { id: 'clients', label: 'Clients & Sites', icon: 'building' },
    { id: 'audit', label: 'Audit log', icon: 'audit', min: 'admin' },
    { id: 'settings', label: 'Users & Settings', icon: 'settings' },
  ] },
];

function Sidebar({ route, navigate }) {
  const { user, role, logout, can } = useAuth();
  const jobsQ = useQuery(['nav-jobs'], () => api.listJobs(), { invalidateOn: ['jobs'], poll: 5000 });
  const alertsQ = useQuery(['nav-alerts'], () => api.listAlerts(), { invalidateOn: ['alerts'], poll: 8000 });
  const awaiting = (jobsQ.data?.jobs || []).filter((j) => j.status === 'awaiting_approval').length;
  const openAlerts = (alertsQ.data?.alerts || []).filter((a) => a.status === 'new').length;
  const badges = { jobs: awaiting, alerts: openAlerts };

  return (
    <aside className="w-[232px] shrink-0 h-full bg-panel border-r border-line flex flex-col">
      <div className="h-14 flex items-center gap-2.5 px-4 border-b border-line">
        <BrandMark size={28} />
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-ink tracking-tight leading-none">OpenRMM</div>
          <div className="text-[10.5px] text-faint mt-0.5">console</div>
        </div>
      </div>

      <nav className="flex-1 overflow-y-auto overflow-x-hidden scroll-area px-2.5 py-3">
        {NAV.map((grp, gi) => {
          const items = grp.items.filter((it) => !it.min || can(it.min));
          if (!items.length) return null;
          return (
          <div key={gi} className={gi > 0 ? 'mt-5' : ''}>
            {grp.section && <div className="px-2.5 mb-1.5 text-[10.5px] uppercase tracking-wider text-faint font-semibold">{grp.section}</div>}
            <div className="space-y-0.5">
              {items.map((it) => {
                const active = route.name === it.id;
                const badge = badges[it.badge];
                return (
                  <button key={it.id} onClick={() => navigate(it.id)}
                    className={cx('w-full group flex items-center gap-2.5 h-[34px] px-2.5 rounded-lg text-[13px] font-medium transition-all relative',
                      active ? 'bg-elevated text-ink' : 'text-muted hover:text-ink hover:bg-elevated/50')}>
                    {active && <span className="absolute left-0 top-1/2 -translate-y-1/2 h-4 w-[3px] rounded-full bg-brand" />}
                    <Icon name={it.icon} size={16} className={cx(active ? (it.accent ? 'text-brandhi' : 'text-ink') : 'text-faint group-hover:text-muted', it.accent && !active && 'text-brandhi/70')} />
                    <span className="flex-1 text-left truncate">{it.label}</span>
                    {badge > 0 && (
                      <span className={cx('inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full text-[10.5px] font-bold tabular',
                        it.badge === 'alerts' ? 'bg-bad/20 text-bad' : 'bg-warn/20 text-warn', it.badge === 'jobs' && awaiting && 'animate-pulse')}>{badge}</span>
                    )}
                  </button>
                );
              })}
            </div>
          </div>
          );
        })}
      </nav>

      <div className="p-2.5 border-t border-line">
        <div className="flex items-center gap-2.5 px-2 py-2 rounded-lg hover:bg-elevated/60 transition">
          <Avatar name={user.username} size={30} />
          <div className="min-w-0 flex-1">
            <div className="text-[12.5px] text-ink font-medium truncate">{user.username}</div>
            <div className="mt-0.5"><RoleBadge role={role} /></div>
          </div>
          <Tip label="Sign out"><button onClick={logout} className="text-faint hover:text-bad transition p-1"><Icon name="logout" size={16} /></button></Tip>
        </div>
      </div>
    </aside>
  );
}

/* -------------------------------- Topbar ---------------------------------- */
function Topbar({ route, navigate, onToggleTheme, theme }) {
  const titles = { dashboard: 'Dashboard', devices: 'Devices', jobs: 'Jobs & Approvals', alerts: 'Alerts', checks: 'Checks', tasks: 'Scheduled tasks', policies: 'Policies', scripts: 'Script library', chat: 'AI Chat', clients: 'Clients & Sites', audit: 'Audit log', settings: 'Users & Settings', reports: 'Reports' };
  return (
    <header className="h-14 shrink-0 border-b border-line bg-panel/70 backdrop-blur flex items-center gap-3 px-5">
      <div className="text-[14.5px] font-semibold text-ink tracking-tight whitespace-nowrap">{titles[route.name] || ''}</div>
      <div className="flex-1" />
      <button onClick={() => navigate('chat')} className="hidden md:flex items-center gap-2 h-9 px-3 rounded-lg border border-line2 bg-elevated/60 hover:bg-elevated text-[12.5px] text-muted hover:text-ink transition group">
        <Icon name="sparkle" size={14} className="text-brandhi" />
        <span>Ask the AI…</span>
        <Kbd>⌘K</Kbd>
      </button>
      <Tip label={theme === 'dark' ? 'Light mode' : 'Dark mode'}>
        <button onClick={onToggleTheme} className="h-9 w-9 flex items-center justify-center rounded-lg border border-line2 bg-elevated/60 hover:bg-elevated text-muted hover:text-ink transition">
          <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} />
        </button>
      </Tip>
    </header>
  );
}

/* -------------------------------- Router ---------------------------------- */
function Shell() {
  const [route, setRoute] = useState({ name: 'dashboard', params: {} });
  const [theme, setTheme] = useState(() => localStorage.getItem('rmm-theme') || 'dark');
  const navigate = (name, params = {}) => setRoute({ name, params });

  uE(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    document.documentElement.classList.toggle('light', theme === 'light');
    localStorage.setItem('rmm-theme', theme);
  }, [theme]);

  uE(() => {
    const h = (e) => { if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); navigate('chat'); } };
    window.addEventListener('keydown', h); return () => window.removeEventListener('keydown', h);
  }, []);

  const screens = {
    dashboard: window.DashboardScreen, devices: window.DevicesScreen, jobs: window.JobsScreen,
    alerts: window.AlertsScreen, checks: window.ChecksScreen, tasks: window.TasksScreen,
    policies: window.PoliciesScreen, scripts: window.ScriptsScreen, chat: window.ChatScreen,
    clients: window.ClientsScreen, audit: window.AuditScreen, settings: window.SettingsScreen,
    reports: window.ReportsScreen,
  };
  const Screen = screens[route.name] || (() => <EmptyState title="Coming soon" msg="This screen is part of the build." />);
  const fullBleed = route.name === 'chat';

  return (
    <div className="h-full w-full flex bg-app">
      <Sidebar route={route} navigate={navigate} />
      <div className="flex-1 min-w-0 flex flex-col">
        <Topbar route={route} navigate={navigate} theme={theme} onToggleTheme={() => setTheme(theme === 'dark' ? 'light' : 'dark')} />
        <main className={cx('flex-1 min-h-0', fullBleed ? 'overflow-hidden' : 'overflow-y-auto scroll-area')}>
          <Screen route={route} navigate={navigate} key={route.name} />
        </main>
      </div>
    </div>
  );
}

/* --------------------------------- Root ----------------------------------- */
function App() {
  // Standalone native-remote tab: ?dev=<id>&host=<name>&os=<os> renders just the
  // viewer, which brokers its own session and auto-reconnects (auth via localStorage).
  const params = new URLSearchParams(location.search);
  const remoteDev = params.get('dev');
  if (remoteDev) {
    const RV = window.RemoteViewer;
    return <ToastProvider><RV deviceId={remoteDev} hostname={params.get('host') || 'remote'} os={params.get('os') || ''} onClose={() => window.close()} /></ToastProvider>;
  }

  const [auth, setAuth] = useState(undefined); // undefined=loading, null=logged out

  uE(() => {
    const raw = localStorage.getItem(AUTH_KEY);
    if (!raw) { setAuth(null); return; }
    try {
      const a = JSON.parse(raw);
      api.loginWithToken(a.token).then((res) => setAuth({ token: res.token, user: res.user, role: res.role })).catch(() => setAuth(null));
    } catch (e) { setAuth(null); }
  }, []);

  const logout = () => { api.logout(); localStorage.removeItem(AUTH_KEY); setAuth(null); };

  if (auth === undefined) return <div className="h-full flex items-center justify-center text-faint"><Spinner size={22} /></div>;
  if (auth === null) return <ToastProvider><LoginScreen onAuthed={setAuth} /></ToastProvider>;

  const ctx = { user: auth.user, role: auth.role, logout, can: (min) => ROLES[auth.role] >= ROLES[min] };
  return (
    <AuthCtx.Provider value={ctx}>
      <ToastProvider><Shell /></ToastProvider>
    </AuthCtx.Provider>
  );
}

window.useAuth = useAuth;
window.BrandMark = BrandMark;
ReactDOM.createRoot(document.getElementById('root')).render(<App />);
