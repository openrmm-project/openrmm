/* app.jsx — shell: auth, sidebar, router, topbar. Mounts the app. */
const { useState: uS, useEffect: uE, useContext: uC, createContext: cCtx } = React;

// Apply the persisted theme at module load so every entry point matches the
// user's choice — including the standalone remote tab (?dev=…), which renders
// before the console's theme effect runs and would otherwise fall back to dark.
(function applyStoredTheme() {
  const stored = localStorage.getItem('rmm-theme') || 'dark';
  document.documentElement.classList.toggle('dark', stored === 'dark');
  document.documentElement.classList.toggle('light', stored !== 'dark');
})();

const AUTH_KEY = 'rmm-ai-auth';
const AuthCtx = cCtx(null);
function useAuth() { return uC(AuthCtx); }

/* -------------------------------- Login ----------------------------------- */
// People sign in as themselves: a password (with its second factor) or SSO.
// There is no "paste a token" mode. It used to take RMM_ADMIN_TOKEN or any API
// token, which skipped the second factor and the SSO policy, logged the work as
// "admin" rather than as a person, and turned a leaked automation token into a
// console. Tokens are for the API and automation.
function LoginScreen({ onAuthed, onForgot }) {
  // Two steps: who, then how. The console recognises an account that signs in
  // through single sign-on — a managed service's owner, or any linked account —
  // and sends it to the provider; everyone else gets the password field. Nobody
  // has to know which button is theirs, and the owner of a managed console is
  // never offered a password path the provider's second factor does not guard.
  const [step, setStep] = useState('who'); // 'who' | 'password' | 'sso' (on its way to the provider)
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [need2fa, setNeed2fa] = useState(false);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  // Whether an identity provider is configured is the server's answer, not a
  // build flag: one bundle serves an install with no provider and one behind
  // Entra. Until it replies there is no button, which is the right default —
  // an SSO button that fails is worse than none.
  const [sso, setSso] = useState({ enabled: false, label: '', button: false });

  uE(() => {
    let live = true;
    api.ssoStatus().then((r) => { if (live) setSso(r || { enabled: false }); });
    return () => { live = false; };
  }, []);

  // The callback sends the browser back with the outcome in the fragment.
  // Fragments are never sent to a server, so a token in one stays out of proxy
  // logs, access logs and Referer headers.
  uE(() => {
    const hash = new URLSearchParams((location.hash || '').replace(/^#/, ''));
    const reason = hash.get('sso_error');
    if (!reason) return;
    history.replaceState(null, '', location.pathname + location.search);
    setErr(t({
      sso_denied: 'auth.err.ssoDenied',
      sso_expired: 'auth.err.ssoExpired',
      sso_no_account: 'auth.err.ssoNoAccount',
    }[reason] || 'auth.err.sso'));
  }, []);

  const identify = async () => {
    if (busy || !username.trim()) return;
    setBusy(true); setErr(null);
    try {
      const method = sso.enabled ? await api.authMethod(username.trim()) : 'password';
      if (method === 'sso') { setStep('sso'); api.ssoStart(location.pathname + location.search); return; }
      setStep('password');
    } finally { setBusy(false); }
  };
  const anotherAccount = () => { setStep('who'); setPassword(''); setCode(''); setNeed2fa(false); setErr(null); };

  const doLogin = async () => {
    if (busy || !username.trim() || !password) return;
    setBusy(true); setErr(null);
    try {
      const res = await api.login(username.trim(), password, code.trim() || undefined);
      sessionStorage.setItem(AUTH_KEY, JSON.stringify({ token: res.token, user: res.user, role: res.role, permissions: res.permissions || res.user?.permissions || [] }));
      onAuthed(res);
    } catch (e) {
      if (e.totpRequired) { setNeed2fa(true); setErr(t(code ? 'auth.err.badCode' : 'auth.err.needCode')); }
      else if (e.code === 'sso_required') setErr(t('auth.err.ssoRequired'));
      else setErr(e.message || t('auth.err.failed'));
    } finally { setBusy(false); }
  };

  return (
    <div className="h-full w-full overflow-y-auto scroll-area grain flex items-center justify-center p-6">
      <div className="w-full max-w-[400px] my-auto animate-fade-in">
        <div className="flex items-center gap-2.5 mb-7">
          <BrandMark size={34} />
          <div>
            <div className="text-[17px] font-semibold text-ink tracking-tight">OpenRMM</div>
            <div className="text-[12px] text-faint -mt-0.5">{t('auth.brand.subtitle')}</div>
          </div>
        </div>

        <Card className="p-6 shadow-2xl shadow-black/30">
          <div className="text-[15px] font-semibold text-ink">{t('auth.title')}</div>
          <div className="text-[12.5px] text-muted mt-1">{t('auth.selfHosted', { base: api.API_BASE })}</div>

          {step === 'password' ? (
          <form className="mt-5 space-y-3" onSubmit={(e) => { e.preventDefault(); doLogin(); }}>
            <div className="flex items-center justify-between gap-2 text-[12.5px]">
              <span className="text-ink truncate">{username.trim()}</span>
              <button type="button" onClick={anotherAccount} className="text-faint hover:text-muted transition shrink-0">{t('auth.changeUser')}</button>
            </div>
            <Field label={t('auth.password')}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></Field>
            {need2fa && <Field label={t('auth.code')} hint={t('auth.code.hint')}><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder={t('auth.code.placeholder')} autoFocus className="mono tracking-widest" /></Field>}
            {err && <div className="text-[12.5px] text-bad flex items-center gap-1.5"><Icon name="alert" size={13} />{err}</div>}
            <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={busy || !username.trim() || !password}>{t('auth.signIn')}</Button>
          </form>
          ) : (
          <form className="mt-5 space-y-3" onSubmit={(e) => { e.preventDefault(); identify(); }}>
            <Field label={t('auth.username')}><Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus /></Field>
            {step === 'sso' && <div className="text-[12.5px] text-muted">{t('auth.ssoRedirect')}</div>}
            {err && <div className="text-[12.5px] text-bad flex items-center gap-1.5"><Icon name="alert" size={13} />{err}</div>}
            <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy || step === 'sso'} disabled={busy || step === 'sso' || !username.trim()}>{t('auth.continue')}</Button>
          </form>
          )}
          {sso.enabled && sso.button && (
            <>
              <div className="flex items-center gap-3 my-4">
                <div className="h-px flex-1 bg-line" />
                <div className="text-[11px] text-faint uppercase tracking-wider">{t('auth.or')}</div>
                <div className="h-px flex-1 bg-line" />
              </div>
              <Button type="button" variant="ghost" size="lg" className="w-full"
                      onClick={() => api.ssoStart(location.pathname + location.search)}>
                <Icon name="shield" size={14} />{sso.label || t('auth.sso')}
              </Button>
            </>
          )}
          {step === 'password' && <button type="button" onClick={onForgot} className="mt-3 w-full text-center text-[12px] text-faint hover:text-muted transition">{t('auth.forgot')}</button>}
        </Card>
      </div>
    </div>
  );
}

// ForgotPasswordScreen requests a reset link.
//
// It always shows the same confirmation, because the API always answers the
// same: telling the user "no such account" here would turn the login page into
// an account-existence oracle for anyone with an address list.
function ForgotPasswordScreen({ onBack }) {
  const [email, setEmail] = useState('');
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (busy || !email.trim()) return;
    setBusy(true);
    try { await api.requestPasswordReset(email.trim()); } catch (e) {}
    finally { setBusy(false); setSent(true); }
  };

  return (
    <AuthShell title={t('auth.reset.title')} subtitle={t('auth.reset.subtitle')}>
      {sent ? (
        <div className="mt-4 space-y-4">
          <div className="flex items-start gap-2 rounded-lg border border-ok/25 bg-oksoft px-3 py-2.5">
            <Icon name="check" size={14} className="text-ok mt-0.5 shrink-0" />
            <div className="text-[12.5px] text-muted">{(() => {
              const address = email.trim();
              const sentence = t('auth.reset.sent', { email: address });
              const parts = sentence.split(address);
              return parts.length === 2
                ? <>{parts[0]}<b className="text-ink">{address}</b>{parts[1]}</>
                : sentence;
            })()}</div>
          </div>
          <div className="text-[11.5px] text-faint">{t('auth.reset.spam')}</div>
          <Button variant="secondary" size="lg" className="w-full" onClick={onBack}>{t('auth.back')}</Button>
        </div>
      ) : (
        <form className="mt-4 space-y-3" onSubmit={(e) => { e.preventDefault(); submit(); }}>
          <Field label={t('auth.reset.address')} hint={t('auth.reset.address.hint')}>
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" autoFocus placeholder="you@example.com" />
          </Field>
          <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={busy || !email.trim()}>{t('auth.reset.send')}</Button>
          <button type="button" onClick={onBack} className="w-full text-center text-[12px] text-faint hover:text-muted transition">{t('auth.back')}</button>
        </form>
      )}
    </AuthShell>
  );
}

// ResetPasswordScreen consumes the token from ?reset=<token>.
function ResetPasswordScreen({ token, onDone }) {
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const tooShort = password.length > 0 && password.length < 12;
  const mismatch = confirm.length > 0 && password !== confirm;
  const canSubmit = password.length >= 12 && password === confirm && !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true); setErr(null);
    try { await api.confirmPasswordReset(token, password); setDone(true); }
    catch (e) { setErr(e.code === 'sso_required' ? t('auth.err.ssoRequired') : (e.message || t('auth.new.err'))); }
    finally { setBusy(false); }
  };

  return (
    <AuthShell title={t('auth.new.title')} subtitle={done ? t('auth.new.done') : t('auth.new.subtitle')}>
      {done ? (
        <div className="mt-4 space-y-4">
          <div className="flex items-start gap-2 rounded-lg border border-ok/25 bg-oksoft px-3 py-2.5">
            <Icon name="check" size={14} className="text-ok mt-0.5 shrink-0" />
            <div className="text-[12.5px] text-muted">{t('auth.new.changed')}</div>
          </div>
          <Button variant="primary" size="lg" className="w-full" onClick={onDone}>{t('auth.signIn')}</Button>
        </div>
      ) : (
        <form className="mt-4 space-y-3" onSubmit={(e) => { e.preventDefault(); submit(); }}>
          <Field label={t('auth.new.password')} hint={tooShort ? t('auth.new.tooShort') : undefined}>
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" autoFocus className={tooShort ? 'border-bad' : ''} />
          </Field>
          <Field label={t('auth.new.confirm')} hint={mismatch ? t('auth.new.mismatch') : undefined}>
            <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" className={mismatch ? 'border-bad' : ''} />
          </Field>
          {err && <div className="text-[12.5px] text-bad flex items-center gap-1.5"><Icon name="alert" size={13} />{err}</div>}
          <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={!canSubmit}>{t('auth.new.submit')}</Button>
          <button type="button" onClick={onDone} className="w-full text-center text-[12px] text-faint hover:text-muted transition">{t('auth.back')}</button>
        </form>
      )}
    </AuthShell>
  );
}

// VerifyEmailScreen redeems ?verify-email=<token>: someone confirming that
// their address may receive alerts through the shared alert service. No
// session is needed — holding the link is the proof — and the answer is shown
// before anything else, whoever is signed in.
function VerifyEmailScreen({ token, onDone }) {
  const [state, setState] = useState('working'); // working | done | failed
  const [err, setErr] = useState('');
  // A link works once: redeem it once, even if the effect runs twice.
  const sent = React.useRef(false);
  uE(() => {
    if (sent.current) return;
    sent.current = true;
    api.confirmEmailVerification(token)
      .then(() => setState('done'))
      .catch((e) => { setErr(e.message || ''); setState('failed'); });
  }, [token]);
  return (
    <AuthShell title={t('verify.title')} subtitle={state === 'working' ? t('verify.working') : state === 'done' ? t('verify.done') : t('verify.failed')}>
      <div className="mt-4 space-y-4">
        {state === 'working' && <div className="flex justify-center py-2"><Spinner size={20} /></div>}
        {state === 'done' && (
          <div className="flex items-start gap-2 rounded-lg border border-ok/25 bg-oksoft px-3 py-2.5">
            <Icon name="check" size={14} className="text-ok mt-0.5 shrink-0" />
            <div className="text-[12.5px] text-muted">{t('verify.doneBody')}</div>
          </div>
        )}
        {state === 'failed' && (
          <div className="flex items-start gap-2 rounded-lg border border-bad/25 bg-badsoft px-3 py-2.5">
            <Icon name="alert" size={14} className="text-bad mt-0.5 shrink-0" />
            <div className="text-[12.5px] text-muted">{t('verify.failedBody')}{err ? <div className="text-faint mt-1">{err}</div> : null}</div>
          </div>
        )}
        {state !== 'working' && <Button variant="primary" size="lg" className="w-full" onClick={onDone}>{t('verify.continue')}</Button>}
      </div>
    </AuthShell>
  );
}

// AuthShell is the signed-out page frame shared by the reset screens, so they
// match the login card instead of drifting from it.
function AuthShell({ title, subtitle, children }) {
  return (
    <div className="h-full w-full overflow-y-auto scroll-area grain flex items-center justify-center p-6">
      <div className="w-full max-w-[400px] my-auto animate-fade-in">
        <div className="flex items-center gap-2.5 mb-7">
          <BrandMark size={34} />
          <div>
            <div className="text-[17px] font-semibold text-ink tracking-tight">OpenRMM</div>
            <div className="text-[12px] text-faint -mt-0.5">{t('auth.brand.subtitle')}</div>
          </div>
        </div>
        <Card className="p-6 shadow-2xl shadow-black/30">
          <div className="text-[15px] font-semibold text-ink">{title}</div>
          <div className="text-[12.5px] text-muted mt-1">{subtitle}</div>
          {children}
        </Card>
      </div>
    </div>
  );
}

function BrandMark({ size = 32 }) {
  return (
    <img src={(window.__BASE_PATH__ || '/') + 'assets/logo_open_rmm_edit3.png'} alt="OpenRMM" width={size} height={size}
      className={cx('shrink-0 object-contain', `brand-mark-${size}`)} />
  );
}

/* ------------------------------- Sidebar ---------------------------------- */
// Labels are translation KEYS, not text. Resolving them here would run t() once
// at module load and freeze the language for the session; the sidebar calls
// t(item.label) at render, so switching the picker updates it.
const NAV = [
  { section: null, items: [
    { id: 'dashboard', label: 'nav.dashboard', icon: 'dashboard' },
    { id: 'chat', label: 'nav.chat', icon: 'chat', accent: true, min: 'ai_chat.use' },
    { id: 'alerts', label: 'nav.alerts', icon: 'alert', badge: 'alerts', min: 'alerts.view' },
  ] },
  { section: 'nav.section.fleet', items: [
    { id: 'devices', label: 'nav.devices', icon: 'devices', min: 'devices.view' },
    { id: 'jobs', label: 'nav.jobs', icon: 'flash', badge: 'jobs', min: 'jobs.view' },
    { id: 'checks', label: 'nav.checks', icon: 'check', min: 'checks.view' },
    { id: 'tasks', label: 'nav.tasks', icon: 'tasks', min: 'tasks.view' },
    { id: 'policies', label: 'nav.policies', icon: 'policy', min: 'policies.view' },
    { id: 'scripts', label: 'nav.scripts', icon: 'script', min: 'scripts.view' },
    { id: 'patches', label: 'nav.patches', icon: 'shield', min: 'patches.view' },
    { id: 'reports', label: 'nav.reports', icon: 'report', min: 'reports.view' },
  ] },
  { section: 'nav.section.organization', items: [
    { id: 'clients', label: 'nav.clients', icon: 'building', min: 'clients.view' },
    { id: 'audit', label: 'nav.audit', icon: 'audit', min: 'audit.view' },
    { id: 'settings', label: 'nav.settings', icon: 'settings' },
  ] },
];

function Sidebar({ route, navigate, mobileOpen, onClose }) {
  const { user, role, logout, can } = useAuth();
  const jobsQ = useQuery(['nav-jobs', role], () => can('jobs.view') ? api.listJobs() : Promise.resolve({ jobs: [] }), { invalidateOn: ['jobs'], poll: can('jobs.view') ? 5000 : 0 });
  const alertsQ = useQuery(['nav-alerts', role], () => can('alerts.view') ? api.listAlerts() : Promise.resolve({ alerts: [] }), { invalidateOn: ['alerts'], poll: can('alerts.view') ? 8000 : 0 });
  const awaiting = (jobsQ.data?.jobs || []).filter((j) => j.status === 'awaiting_approval').length;
  const openAlerts = alertsQ.data?.counts?.new ?? (alertsQ.data?.alerts || []).filter((a) => a.status === 'new').length;
  const badges = { jobs: awaiting, alerts: openAlerts };

  return (
    <>
    <button aria-label={t('nav.close')} onClick={onClose} className={cx('fixed inset-0 z-[89] bg-black/60 md:hidden', mobileOpen ? 'block' : 'hidden')} />
    <aside className={cx('fixed inset-y-0 left-0 z-[90] w-[232px] shrink-0 h-full bg-panel border-r border-line flex flex-col transition-transform md:static md:z-auto md:translate-x-0', mobileOpen ? 'translate-x-0' : '-translate-x-full')}>
      <div className="h-14 flex items-center gap-2.5 px-4 border-b border-line">
        <BrandMark size={28} />
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-ink tracking-tight leading-none">OpenRMM</div>
          <div className="text-[10.5px] text-faint mt-0.5">{t('app.console')}</div>
        </div>
        <button aria-label={t('nav.close')} onClick={onClose} className="ml-auto p-1.5 text-faint hover:text-ink md:hidden"><Icon name="close" size={18} /></button>
      </div>

      <nav className="flex-1 overflow-y-auto overflow-x-hidden scroll-area px-2.5 py-3">
        {NAV.map((grp, gi) => {
          const items = grp.items.filter((it) => !it.min || can(it.min));
          if (!items.length) return null;
          return (
          <div key={gi} className={gi > 0 ? 'mt-5' : ''}>
            {grp.section && <div className="px-2.5 mb-1.5 text-[10.5px] uppercase tracking-wider text-faint font-semibold">{t(grp.section)}</div>}
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
                    <span className="flex-1 text-left truncate">{t(it.label)}</span>
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
          <Tip label={t('nav.signOut')}><button onClick={logout} className="text-faint hover:text-bad transition p-1"><Icon name="logout" size={16} /></button></Tip>
        </div>
      </div>
    </aside>
    </>
  );
}

/* -------------------------------- Topbar ---------------------------------- */
function Topbar({ route, navigate, onToggleTheme, theme, onOpenNav }) {
  const { can } = useAuth();
  // Resolved at render, not frozen into a module-level constant: t() called
  // once at load would keep whatever language the session started in. Separate
  // keys from nav.*, because the sidebar says "Jobs" where this heading says
  // "Jobs & Approvals" — collapsing them would reword the product, not
  // translate it.
  const titles = {
    dashboard: t('title.dashboard'), devices: t('title.devices'), jobs: t('title.jobs'),
    alerts: t('title.alerts'), checks: t('title.checks'), tasks: t('title.tasks'),
    policies: t('title.policies'), scripts: t('title.scripts'), chat: t('title.chat'),
    clients: t('title.clients'), audit: t('title.audit'), settings: t('title.settings'),
    reports: t('title.reports'), patches: t('title.patches'),
  };
  return (
    <header className="h-14 shrink-0 border-b border-line bg-panel/70 backdrop-blur flex items-center gap-3 px-3 sm:px-5">
      <button aria-label={t('app.openNav')} onClick={onOpenNav} className="h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-line2 text-muted hover:text-ink md:hidden flex"><Icon name="menu" size={18} /></button>
      <div className="text-[14.5px] font-semibold text-ink tracking-tight whitespace-nowrap">{titles[route.name] || ''}</div>
      <div className="flex-1" />
      {can('ai_chat.use') && <button onClick={() => navigate('chat')} className="hidden md:flex items-center gap-2 h-9 px-3 rounded-lg border border-line2 bg-elevated/60 hover:bg-elevated text-[12.5px] text-muted hover:text-ink transition group">
        <Icon name="sparkle" size={14} className="text-brandhi" />
        <span>{t('app.askAi')}</span>
        <Kbd>⌘K</Kbd>
      </button>}
      <Tip label={theme === 'dark' ? t('app.lightMode') : t('app.darkMode')}>
        <button onClick={onToggleTheme} className="h-9 w-9 flex items-center justify-center rounded-lg border border-line2 bg-elevated/60 hover:bg-elevated text-muted hover:text-ink transition">
          <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} />
        </button>
      </Tip>
    </header>
  );
}

/* -------------------------------- Router ---------------------------------- */
function Shell() {
  const { can } = useAuth();
  const [route, setRoute] = useState({ name: 'dashboard', params: {} });
  const [theme, setTheme] = useState(() => localStorage.getItem('rmm-theme') || 'dark');
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const navigate = (name, params = {}) => { setRoute({ name, params }); setMobileNavOpen(false); };

  uE(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    document.documentElement.classList.toggle('light', theme === 'light');
    localStorage.setItem('rmm-theme', theme);
  }, [theme]);

  uE(() => {
    const h = (e) => { if (can('ai_chat.use') && (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); navigate('chat'); } };
    window.addEventListener('keydown', h); return () => window.removeEventListener('keydown', h);
  }, []);

  const screens = {
    dashboard: window.DashboardScreen, devices: window.DevicesScreen, jobs: window.JobsScreen,
    alerts: window.AlertsScreen, checks: window.ChecksScreen, tasks: window.TasksScreen,
    policies: window.PoliciesScreen, scripts: window.ScriptsScreen, chat: window.ChatScreen,
    clients: window.ClientsScreen, audit: window.AuditScreen, settings: window.SettingsScreen,
    reports: window.ReportsScreen, patches: window.PatchesScreen,
  };
  const screenMin = { devices: 'devices.view', clients: 'clients.view', chat: 'ai_chat.use', alerts: 'alerts.view', jobs: 'jobs.view', checks: 'checks.view', tasks: 'tasks.view', policies: 'policies.view', scripts: 'scripts.view', patches: 'patches.view', reports: 'reports.view', audit: 'audit.view' };
  const denied = screenMin[route.name] && !can(screenMin[route.name]);
  const Screen = denied
    ? (() => <Page><EmptyState icon="shield" title={t('app.denied')} msg={t('app.deniedMsg')} /></Page>)
    : (screens[route.name] || (() => <EmptyState title={t('app.soon')} msg={t('app.soonMsg')} />));
  const fullBleed = route.name === 'chat';

  return (
    <div className="h-full w-full flex bg-app">
      <Sidebar route={route} navigate={navigate} mobileOpen={mobileNavOpen} onClose={() => setMobileNavOpen(false)} />
      <div className="flex-1 min-w-0 flex flex-col">
        <Topbar route={route} navigate={navigate} theme={theme} onOpenNav={() => setMobileNavOpen(true)} onToggleTheme={() => setTheme(theme === 'dark' ? 'light' : 'dark')} />
        <main className={cx('flex-1 min-h-0', fullBleed ? 'overflow-hidden' : 'overflow-y-auto scroll-area')}>
          <ErrorBoundary key={route.name}><Screen route={route} navigate={navigate} /></ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

/* --------------------------------- Root ----------------------------------- */
// Restore a tab's own session, or redeem a handoff bound by the server to
// this browser. A token received from a URL is never a login credential.
async function restoreConsoleAuth(transport, browser, authKey) {
  const { location, history, sessionStorage } = browser;
  const hash = new URLSearchParams((location.hash || '').replace(/^#/, ''));
  const code = hash.get('sso_code');
  if (hash.has('sso_code') || hash.has('sso_token')) {
    history.replaceState(null, '', location.pathname + location.search);
  }
  if (code) {
    try {
      const res = await transport.exchangeSSO(code);
      sessionStorage.setItem(authKey, JSON.stringify(res));
      return res;
    } catch (e) { /* Preserve this tab's session after an unsolicited handoff. */ }
  }
  try {
    const a = JSON.parse(sessionStorage.getItem(authKey) || 'null');
    return a?.token ? await transport.loginWithToken(a.token) : null;
  } catch (e) { return null; }
}

function App() {
  // Subscribing here re-renders the whole tree when the language changes.
  // Without it the picker would only take effect on reload, which reads as a
  // broken control rather than a deliberate one.
  useLocale();
  // Standalone native-remote tab: ?dev=<id>&host=<name>&os=<os> renders just the
  // viewer, which brokers its own session and auto-reconnects (auth via sessionStorage).
  const params = new URLSearchParams(location.search);
  // An invitation link (/join#<token>) is opened by someone with no account:
  // no sign-in and no console, only the guest page (screens-devices.jsx).
  if (/\/join\/?$/.test(location.pathname)) {
    const GJ = window.GuestJoinScreen;
    const guestCtx = { user: null, role: null, permissions: [], logout: () => {}, can: () => false };
    return <AuthCtx.Provider value={guestCtx}><ToastProvider><GJ /></ToastProvider></AuthCtx.Provider>;
  }
  const remoteDev = params.get('dev');
  if (remoteDev) {
    const RV = window.RemoteViewer;
    let stored = null;
    try { stored = JSON.parse(sessionStorage.getItem(AUTH_KEY) || 'null'); } catch (e) {}
    const permissions = stored?.permissions || stored?.user?.permissions || DEFAULT_ROLE_PERMS[stored?.role] || [];
    const permissionSet = new Set(permissions);
    const ctx = { user: stored?.user || null, role: stored?.role || null, permissions, logout: () => {}, can: (key) => permissionSet.has(PERM_ALIASES[key] || key) };
    return <AuthCtx.Provider value={ctx}><ToastProvider><RV deviceId={remoteDev} hostname={params.get('host') || 'remote'} os={params.get('os') || ''} onClose={() => window.close()} /></ToastProvider></AuthCtx.Provider>;
  }

  const [auth, setAuth] = useState(undefined); // undefined=loading, null=logged out
  // A reset link is opened cold, in whatever browser the mail client picked, so
  // the token arrives in the URL with no session behind it.
  const [resetToken, setResetToken] = useState(() => params.get('reset') || '');
  const [verifyToken, setVerifyToken] = useState(() => params.get('verify-email') || '');
  const [forgot, setForgot] = useState(false);

  // Drop the token from the address bar once it is in state: it should not stay
  // in history, or be re-sent as a Referer when the page loads anything.
  uE(() => {
    if (!resetToken && !verifyToken) return;
    const url = new URL(window.location.href);
    url.searchParams.delete('reset');
    url.searchParams.delete('verify-email');
    window.history.replaceState({}, '', url);
  }, []);

  uE(() => {
    const expired = () => setAuth(null);
    window.addEventListener('rmm-auth-expired', expired);
    return () => window.removeEventListener('rmm-auth-expired', expired);
  }, []);

  uE(() => {
    restoreConsoleAuth(api, window, AUTH_KEY).then(setAuth);
  }, []);

  const logout = async () => { setAuth(undefined); await api.logout(); setAuth(null); };

  // A reset link wins over a live session: the person following it is proving
  // control of the mailbox precisely because they cannot get in the normal way,
  // and an already-signed-in tab must not swallow the link.
  if (resetToken) {
    return <ToastProvider><ResetPasswordScreen token={resetToken} onDone={() => { setResetToken(''); setAuth(null); }} /></ToastProvider>;
  }
  if (verifyToken) {
    return <ToastProvider><VerifyEmailScreen token={verifyToken} onDone={() => setVerifyToken('')} /></ToastProvider>;
  }
  if (auth === undefined) return <div className="h-full flex items-center justify-center text-faint"><Spinner size={22} /></div>;
  if (auth === null) {
    return <ToastProvider>{forgot
      ? <ForgotPasswordScreen onBack={() => setForgot(false)} />
      : <LoginScreen onAuthed={setAuth} onForgot={() => setForgot(true)} />}</ToastProvider>;
  }

  const permissions = auth.permissions || auth.user?.permissions || DEFAULT_ROLE_PERMS[auth.role] || [];
  const permissionSet = new Set(permissions);
  const ctx = { user: auth.user, role: auth.role, permissions, logout, can: (key) => permissionSet.has(PERM_ALIASES[key] || key) };
  return (
    <AuthCtx.Provider value={ctx}>
      <ToastProvider><Shell /></ToastProvider>
    </AuthCtx.Provider>
  );
}

window.useAuth = useAuth;
window.BrandMark = BrandMark;
ReactDOM.createRoot(document.getElementById('root')).render(<App />);
