/* screens-core.jsx — shared layout helpers, Dashboard, Devices, Device detail */

/* ------------------------------ layout bits ------------------------------- */
function Page({ children, className = '', wide }) {
  return <div className={cx('mx-auto px-3 py-4 sm:px-6 sm:py-6', wide ? 'max-w-[1500px]' : 'max-w-[1240px]', className)}>{children}</div>;
}
function PageHeader({ title, subtitle, actions, children }) {
  return (
    <div className="flex items-start gap-3 sm:gap-4 mb-5 flex-wrap">
      <div className="min-w-0">
        <h1 className="text-[20px] font-semibold text-ink tracking-tight">{title}</h1>
        {subtitle && <p className="text-[13px] text-muted mt-0.5">{subtitle}</p>}
      </div>
      <div className="flex-1" />
      {actions}
      {children}
    </div>
  );
}

/* dropdown menu
   Positioned fixed against the trigger rather than absolutely inside it: these
   menus open from rows that sit in cards with `overflow-hidden` (the users and
   devices tables, the site list), and an absolutely positioned menu is clipped
   by that — the last row's menu was cut off entirely. Fixed positioning escapes
   every ancestor's overflow.
   The direction is chosen from the space available, so a row near the bottom of
   the window opens upward instead of off-screen. GAP is the breathing room the
   old `mt-1.5` gave. */
const DROPDOWN_GAP = 6;
const DROPDOWN_MARGIN = 16;   // never touch the window edge
const DROPDOWN_TYPICAL = 240; // open upward only when less than this fits below

function Dropdown({ trigger, children, align = 'right', width = 'w-52' }) {
  const [open, setOpen] = useState(false);
  const [place, setPlace] = useState(null);
  const ref = useRef(null);

  const locate = () => {
    const r = ref.current?.getBoundingClientRect();
    if (!r) return;
    const below = window.innerHeight - r.bottom;
    const up = below < DROPDOWN_TYPICAL && r.top > below;
    setPlace({
      up,
      ...(up ? { bottom: window.innerHeight - r.top + DROPDOWN_GAP } : { top: r.bottom + DROPDOWN_GAP }),
      ...(align === 'right' ? { right: Math.max(DROPDOWN_MARGIN, window.innerWidth - r.right) } : { left: Math.max(DROPDOWN_MARGIN, r.left) }),
      maxHeight: (up ? r.top : below) - DROPDOWN_GAP - DROPDOWN_MARGIN,
    });
  };

  useEffect(() => {
    if (!open) return;
    const away = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    // A fixed menu does not travel with its trigger, so anything that moves the
    // trigger closes it rather than leaving it stranded mid-page.
    const dismiss = () => setOpen(false);
    window.addEventListener('mousedown', away);
    window.addEventListener('resize', dismiss);
    window.addEventListener('scroll', dismiss, true);
    return () => {
      window.removeEventListener('mousedown', away);
      window.removeEventListener('resize', dismiss);
      window.removeEventListener('scroll', dismiss, true);
    };
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <div onClick={() => { if (!open) locate(); setOpen((o) => !o); }}>{trigger}</div>
      {open && place && (
        <div style={{ position: 'fixed', top: place.top, bottom: place.bottom, left: place.left, right: place.right, maxHeight: place.maxHeight }}
          className={cx('z-50 overflow-y-auto scroll-area rounded-xl border border-line2 bg-elevated shadow-2xl shadow-black/40 py-1 animate-scale-in', width)}
          onClick={() => setOpen(false)}>
          {children}
        </div>
      )}
    </div>
  );
}
function MenuItem({ icon, children, onClick, danger, disabled, hint }) {
  return (
    <button disabled={disabled} onClick={onClick} className={cx('w-full flex items-center gap-2.5 px-3 py-2 text-[13px] text-left transition disabled:opacity-40 disabled:pointer-events-none', danger ? 'text-bad hover:bg-bad/10' : 'text-muted hover:text-ink hover:bg-card')}>
      {icon && <Icon name={icon} size={15} className="shrink-0" />}<span className="flex-1">{children}</span>
      {hint && <span className="text-[11px] text-faint">{hint}</span>}
    </button>
  );
}

/* stat tile */
function StatTile({ label, value, icon, tone = 'default', sub, onClick, highlight }) {
  const tones = {
    default: 'text-muted', ok: 'text-ok', warn: 'text-warn', run: 'text-run', bad: 'text-bad', brand: 'text-brandhi',
  };
  return (
    <button onClick={onClick} disabled={!onClick} className={cx('text-left rounded-2xl border bg-card p-4 transition-all', onClick && 'hover:border-line2 hover:bg-elevated cursor-pointer', highlight ? 'border-warn/35 bg-warn/[0.04]' : 'border-line')}>
      <div className="flex items-center justify-between">
        <span className="text-[12px] font-medium text-muted">{label}</span>
        <span className={cx('opacity-80', tones[tone])}><Icon name={icon} size={16} /></span>
      </div>
      <div className={cx('mt-2 text-[26px] font-semibold tracking-tight tabular', tone === 'default' ? 'text-ink' : tones[tone])}>{value}</div>
      {sub && <div className="text-[11.5px] text-faint mt-0.5">{sub}</div>}
    </button>
  );
}

/* role-gated wrapper: hides children for insufficient role, optionally shows disabled */
function Gate({ min, children, fallback = null }) {
  const { can } = useAuth();
  return can(min) ? children : fallback;
}

/* =============================== DASHBOARD ================================= */
// Dashboard sections the user can show/hide via "Customize". Each also has a
// permission gate; a widget only appears when permitted AND not hidden. The
// hidden set is persisted per browser in localStorage.
const DASH_WIDGETS = [
  { id: 'ai', label: 'AI assistant', perm: 'ai_chat.use' },
  { id: 'stats', label: 'Stat tiles', perm: null },
  { id: 'awaiting', label: 'Awaiting approval', perm: 'jobs.view' },
  { id: 'activity', label: 'Recent activity', perm: 'audit.view' },
  { id: 'alerts', label: 'Open alerts', perm: 'alerts.view' },
];
const DASH_HIDDEN_KEY = 'rmm-dash-hidden';
function loadDashHidden() {
  try { return JSON.parse(localStorage.getItem(DASH_HIDDEN_KEY)) || []; } catch { return []; }
}

function DashboardScreen({ navigate }) {
  const { can, role } = useAuth();
  const devicesQ = useQuery(['dash-dev', role], () => can('devices.view') ? api.listDevices() : Promise.resolve({ devices: [] }), { invalidateOn: ['devices'], poll: can('devices.view') ? 10000 : 0 });
  const jobsQ = useQuery(['dash-jobs', role], () => can('jobs.view') ? api.listJobs() : Promise.resolve({ jobs: [] }), { invalidateOn: ['jobs'], poll: can('jobs.view') ? 4000 : 0 });
  const auditQ = useQuery(['dash-audit', role], () => can('audit.view') ? api.listAudit(8) : Promise.resolve({ entries: [] }), { invalidateOn: ['audit'], poll: can('audit.view') ? 8000 : 0 });
  const alertsQ = useQuery(['dash-alerts', role], () => can('alerts.view') ? api.listAlerts() : Promise.resolve({ alerts: [] }), { invalidateOn: ['alerts'], poll: can('alerts.view') ? 8000 : 0 });
  const [ask, setAsk] = useState('');
  const [hidden, setHidden] = useState(loadDashHidden);
  const show = (id) => !hidden.includes(id);
  const toggleWidget = (id) => setHidden((h) => {
    const next = h.includes(id) ? h.filter((x) => x !== id) : [...h, id];
    try { localStorage.setItem(DASH_HIDDEN_KEY, JSON.stringify(next)); } catch {}
    return next;
  });
  const customizable = DASH_WIDGETS.filter((w) => !w.perm || can(w.perm));

  const devices = devicesQ.data?.devices || [];
  const jobs = jobsQ.data?.jobs || [];
  const alerts = alertsQ.data?.alerts || [];
  const online = devices.filter((d) => d.status === 'online').length;
  const awaiting = jobs.filter((j) => j.status === 'awaiting_approval');
  const failures = jobs.filter((j) => j.status === 'failed');
  const openAlerts = alerts.filter((a) => a.status !== 'resolved');

  const submitAsk = () => { if (ask.trim()) { sessionStorage.setItem('rmm-ask', ask.trim()); navigate('chat', { prompt: ask.trim() }); } };

  return (
    <Page wide>
      <PageHeader title="Dashboard" subtitle="Fleet health at a glance" actions={
        <Dropdown align="right" width="w-56" trigger={<Button variant="ghost" icon="settings">Customize</Button>}>
          <div className="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wide text-faint">Show panels</div>
          {customizable.map((w) => (
            <button key={w.id} onClick={(e) => { e.stopPropagation(); toggleWidget(w.id); }}
              className="w-full flex items-center gap-2.5 px-3 py-2 text-[13px] text-left text-muted hover:text-ink hover:bg-card transition">
              <span className={cx('h-4 w-4 rounded border flex items-center justify-center shrink-0', show(w.id) ? 'bg-brand border-brand text-white' : 'border-line2')}>
                {show(w.id) && <Icon name="check" size={12} />}
              </span>
              <span className="flex-1">{w.label}</span>
            </button>
          ))}
        </Dropdown>
      } />

      {show('ai') && can('ai_chat.use') && (
        <Card className="p-4 mb-5 relative overflow-hidden grain">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/30 flex items-center justify-center text-brandhi shrink-0"><Icon name="sparkle" size={18} /></div>
            <input value={ask} onChange={(e) => setAsk(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && submitAsk()}
              placeholder="Ask the AI to inspect or act on your fleet — e.g. “install 7-zip on the finance group”"
              className="flex-1 bg-transparent text-[14px] text-ink placeholder:text-faint outline-none min-w-0" />
            <Button variant="primary" icon="arrowright" onClick={submitAsk} disabled={!ask.trim()}>Ask</Button>
          </div>
        </Card>
      )}

      {show('stats') && (
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-3 mb-5">
        {can('devices.view') && <StatTile label="Total devices" value={devices.length} icon="devices" onClick={() => navigate('devices')} />}
        {can('devices.view') && <StatTile label="Online" value={online} icon="dot" tone="ok" sub={`${devices.length - online} offline`} onClick={() => navigate('devices')} />}
        {can('jobs.view') && <StatTile label="Awaiting approval" value={awaiting.length} icon="flash" tone="warn" highlight={awaiting.length > 0} onClick={() => navigate('jobs')} sub="needs a human" />}
        {can('alerts.view') && <StatTile label="Open alerts" value={openAlerts.length} icon="alert" tone={openAlerts.some((a) => a.severity === 'error') ? 'bad' : 'warn'} onClick={() => navigate('alerts')} />}
        {can('jobs.view') && <StatTile label="Recent failures" value={failures.length} icon="reject" tone="bad" onClick={() => navigate('jobs')} />}
      </div>
      )}

      {(() => {
        const showAwaiting = can('jobs.view') && show('awaiting');
        const showActivity = can('audit.view') && show('activity');
        const showAlerts = can('alerts.view') && show('alerts');
        const leftHasContent = showAwaiting || showActivity;
        if (!leftHasContent && !showAlerts) {
          // Only surface the "everything hidden" hint when the stats/AI rows are
          // also gone — otherwise the grid just collapses silently.
          const nothingElse = !show('stats') && !(show('ai') && can('ai_chat.use'));
          return nothingElse
            ? <Card className="p-10"><EmptyState icon="dashboard" title="All panels hidden" msg="Use Customize (top-right) to bring dashboard panels back." /></Card>
            : null;
        }
        return (
      <div className="grid lg:grid-cols-3 gap-5">
        {/* Approval queue preview */}
        {leftHasContent && <div className={cx(showAlerts ? 'lg:col-span-2' : 'lg:col-span-3', 'space-y-5')}>
          {showAwaiting && <Card>
            <div className="flex items-center justify-between px-4 h-12 border-b border-line">
              <div className="flex items-center gap-2 text-[13.5px] font-semibold text-ink"><Icon name="flash" size={15} className="text-warn" />Awaiting approval</div>
              <button onClick={() => navigate('jobs')} className="text-[12px] text-muted hover:text-ink flex items-center gap-1">View all <Icon name="chevright" size={13} /></button>
            </div>
            <div className="divide-y divide-line">
              {awaiting.length === 0 && <div className="px-4 py-8 text-center text-[13px] text-faint">Nothing waiting. The queue is clear.</div>}
              {awaiting.slice(0, 4).map((j) => {
                const dev = devices.find((d) => d.id === j.agent_id);
                const ai = j.created_by.startsWith('ai:');
                return (
                  <button key={j.id} onClick={() => navigate('jobs', { focus: j.id })} className="w-full text-left px-4 py-3 hover:bg-elevated/50 transition flex items-center gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="text-[13px] text-ink font-medium truncate">{j.description}</span>
                        {ai && <span className="inline-flex items-center gap-1 rounded-md bg-brandsoft border border-brand/25 text-brandhi px-1.5 py-0.5 text-[10px] font-semibold"><Icon name="sparkle" size={10} />AI</span>}
                      </div>
                      <div className="text-[11.5px] text-faint mt-0.5 flex items-center gap-1.5"><OsIcon os={dev?.os} size={12} />{dev?.hostname || j.agent_id} · {timeAgo(j.created_at)}</div>
                    </div>
                    <StatusPill status="awaiting_approval" size="sm" />
                  </button>
                );
              })}
            </div>
          </Card>}

          {showActivity && (
            <Card>
              <div className="px-4 h-12 flex items-center justify-between border-b border-line">
                <div className="flex items-center gap-2 text-[13.5px] font-semibold text-ink"><Icon name="history" size={15} className="text-muted" />Recent activity</div>
                <button onClick={() => navigate('audit')} className="text-[12px] text-muted hover:text-ink flex items-center gap-1">Audit log <Icon name="chevright" size={13} /></button>
              </div>
              <div className="p-2">
                {(auditQ.data?.entries || []).map((e) => <ActivityRow key={e.id} entry={e} />)}
              </div>
            </Card>
          )}
        </div>}

        {showAlerts && <div className="space-y-5">
          <Card>
            <div className="px-4 h-12 flex items-center justify-between border-b border-line">
              <div className="flex items-center gap-2 text-[13.5px] font-semibold text-ink"><Icon name="alert" size={15} className="text-bad" />Open alerts</div>
              <button onClick={() => navigate('alerts')} className="text-[12px] text-muted hover:text-ink flex items-center gap-1">All <Icon name="chevright" size={13} /></button>
            </div>
            <div className="divide-y divide-line">
              {openAlerts.length === 0 && <div className="px-4 py-8 text-center text-[13px] text-faint">No open alerts.</div>}
              {openAlerts.slice(0, 5).map((a) => {
                const dev = devices.find((d) => d.id === a.agent_id);
                return (
                  <button key={a.id} onClick={() => navigate('alerts')} className="w-full text-left px-4 py-2.5 hover:bg-elevated/50 transition flex items-start gap-2.5">
                    <span className={cx('mt-1 h-1.5 w-1.5 rounded-full shrink-0', a.severity === 'error' ? 'bg-bad' : a.severity === 'warning' ? 'bg-warn' : 'bg-run')} />
                    <div className="min-w-0 flex-1">
                      <div className="text-[12.5px] text-ink leading-snug">{a.message}</div>
                      <div className="text-[11px] text-faint mt-0.5">{dev?.hostname || 'fleet'} · {timeAgo(a.created_at)}</div>
                    </div>
                  </button>
                );
              })}
            </div>
          </Card>
        </div>}
      </div>
        );
      })()}
    </Page>
  );
}

const ACTION_META = {
  'job.approve': { icon: 'approve', c: 'text-ok' }, 'job.create': { icon: 'plus', c: 'text-brandhi' },
  'job.reject': { icon: 'reject', c: 'text-bad' }, 'remote.start': { icon: 'remote', c: 'text-run' },
  'alert.ack': { icon: 'eye', c: 'text-warn' }, 'alert.resolve': { icon: 'approve', c: 'text-ok' },
  'alert.raise': { icon: 'alert', c: 'text-bad' }, 'user.create': { icon: 'users', c: 'text-muted' },
  'auth.login': { icon: 'user', c: 'text-muted' }, 'enroll.create': { icon: 'key', c: 'text-muted' },
  'device.site': { icon: 'pin', c: 'text-muted' },
};
function ActivityRow({ entry }) {
  const m = ACTION_META[entry.action] || { icon: 'dot', c: 'text-faint' };
  const ai = entry.actor.startsWith('ai:');
  const detail = entry.detail || {};
  const desc = detail.description || detail.message || detail.reason || detail.check || detail.host || detail.hostname || detail.name || '';
  return (
    <div className="flex items-start gap-3 px-2.5 py-2 rounded-lg hover:bg-elevated/40 transition">
      <div className={cx('mt-0.5 h-6 w-6 rounded-lg bg-elevated border border-line flex items-center justify-center shrink-0', m.c)}><Icon name={m.icon} size={13} /></div>
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px] text-ink">
          <span className={cx('font-medium', ai && 'text-brandhi')}>{ai ? 'AI assistant' : entry.actor}</span>
          <span className="text-muted"> {entry.action.replace('.', ' ')}</span>
          {desc && <span className="text-faint"> · {desc}</span>}
        </div>
      </div>
      <span className="text-[11px] text-faint shrink-0 mt-0.5">{timeAgo(entry.created_at)}</span>
    </div>
  );
}

window.Page = Page; window.PageHeader = PageHeader; window.Dropdown = Dropdown; window.MenuItem = MenuItem;
window.StatTile = StatTile; window.Gate = Gate; window.DashboardScreen = DashboardScreen; window.ActivityRow = ActivityRow;
// ACTION_META is shared across files (Audit log); the build isolates each file in
// its own block scope, so cross-file references must resolve through the global.
window.ACTION_META = ACTION_META;
