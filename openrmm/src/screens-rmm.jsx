/* screens-rmm.jsx — Part 2: Alerts, Checks, Scheduled tasks */

// Weekday labels indexed to match Go's time.Weekday (0=Sunday), so bit i of a
// task's days_of_week bitmask lines up with WEEKDAYS[i] on both ends.
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

const SEV = {
  error: { c: 'bad', icon: 'alert', label: 'Error' },
  warning: { c: 'warn', icon: 'alert', label: 'Warning' },
  info: { c: 'run', icon: 'info', label: 'Info' },
};
const CHECK_TYPES = {
  diskspace: { label: 'Disk space', icon: 'disk', unit: '%', dir: 'low', cfg: 'path', cfgLabel: 'Mount path', cfgPh: '/' },
  memory: { label: 'Memory', icon: 'mem', unit: '%', dir: 'low', cfg: null },
  cpuload: { label: 'CPU load', icon: 'cpu', unit: '%', dir: 'high', cfg: null },
  ping: { label: 'Ping', icon: 'network', unit: 'ms', dir: 'high', cfg: 'host', cfgLabel: 'Host / IP', cfgPh: '10.0.0.1' },
  winservice: { label: 'Windows service', icon: 'settings', unit: '', dir: 'state', cfg: 'service', cfgLabel: 'Service name', cfgPh: 'Spooler' },
  script: { label: 'Script check', icon: 'script', unit: '', dir: 'script', cfg: 'script', cfgLabel: 'Inline script', cfgPh: 'exit 0' },
};

/* ================================ ALERTS ================================== */
function AlertsScreen({ navigate }) {
  const { can } = useAuth();
  const toast = useToast();
  const alertsQ = useQuery(['alerts'], () => api.listAlerts(), { invalidateOn: ['alerts'], poll: 5000 });
  const devMap = useDeviceMap();
  const [statusF, setStatusF] = useState('all');
  const [sevF, setSevF] = useState('all');

  const alerts = alertsQ.data?.alerts || [];
  const counts = { new: alerts.filter((a) => a.status === 'new').length, acked: alerts.filter((a) => a.status === 'acked').length, resolved: alerts.filter((a) => a.status === 'resolved').length };
  const filtered = alerts.filter((a) => (statusF === 'all' || a.status === statusF) && (sevF === 'all' || a.severity === sevF));

  const act = async (fn, id, label) => { try { await fn(id); toast.success(label); } catch (e) { toast.error('Failed', e.message); } };

  return (
    <Page wide>
      <PageHeader title="Alerts" subtitle="Conditions that need attention. Alerts auto-resolve when the condition clears." />

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
        <StatTile label="New" value={counts.new} icon="alert" tone={counts.new ? 'bad' : 'default'} highlight={counts.new > 0} onClick={() => setStatusF('new')} />
        <StatTile label="Acknowledged" value={counts.acked} icon="eye" tone="warn" onClick={() => setStatusF('acked')} />
        <StatTile label="Resolved" value={counts.resolved} icon="approve" tone="ok" onClick={() => setStatusF('resolved')} />
        <StatTile label="Total" value={alerts.length} icon="list" onClick={() => setStatusF('all')} />
      </div>

      <Card className="mb-4"><div className="flex items-center gap-2 p-2.5">
        <Select value={statusF} onChange={(e) => setStatusF(e.target.value)} className="w-auto min-w-[140px]"><option value="all">Any status</option><option value="new">New</option><option value="acked">Acknowledged</option><option value="resolved">Resolved</option></Select>
        <Select value={sevF} onChange={(e) => setSevF(e.target.value)} className="w-auto min-w-[140px]"><option value="all">Any severity</option><option value="error">Error</option><option value="warning">Warning</option><option value="info">Info</option></Select>
      </div></Card>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">Severity</th><th className="font-semibold px-3 py-2.5">Message</th>
            <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Device</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Source</th>
            <th className="font-semibold px-3 py-2.5">Status</th><th className="font-semibold px-3 py-2.5 text-right">When</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {filtered.map((a) => {
              const s = SEV[a.severity]; const dev = devMap[a.agent_id];
              return (
                <tr key={a.id} className={cx('hover:bg-elevated/40 transition align-middle', a.status === 'resolved' && 'opacity-55')}>
                  <td className="px-4 py-3"><span className={cx('inline-flex items-center gap-1.5 text-[12px] font-medium', `text-${s.c}`)}><Icon name={s.icon} size={14} />{s.label}</span></td>
                  <td className="px-3 py-3 text-[13px] text-ink max-w-[340px]"><div className="truncate">{a.message}</div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{a.source_type}{a.source_id ? '·' + a.source_id.slice(0, 10) : ''}</td>
                  <td className="px-3 py-3"><StatusPill status={a.status} size="sm" /></td>
                  <td className="px-3 py-3 text-right text-[12px] text-muted tabular whitespace-nowrap">{timeAgo(a.created_at)}</td>
                  <td className="px-4 py-3 text-right">
                    {a.status !== 'resolved' && can('alerts.manage') ? (
                      <div className="flex items-center justify-end gap-1.5">
                        {a.status === 'new' && <Button size="sm" variant="subtle" onClick={() => act(api.ackAlert, a.id, 'Acknowledged')}>Ack</Button>}
                        <Button size="sm" variant="secondary" icon="approve" onClick={() => act(api.resolveAlert, a.id, 'Resolved')}>Resolve</Button>
                      </div>
                    ) : a.status === 'resolved' ? <span className="text-[11.5px] text-faint">{timeAgo(a.resolved_at)}</span> : <span className="text-[11px] text-faint">view only</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {filtered.length === 0 && <EmptyState icon="approve" title="No alerts" msg="Nothing matches your filters — the fleet looks healthy." />}
      </Card>
    </Page>
  );
}

/* ================================ CHECKS ================================== */
function ChecksScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const checksQ = useQuery(['checks'], () => api.listChecks(), { invalidateOn: ['checks'], poll: 8000 });
  const devMap = useDeviceMap();
  const policiesQ = useQuery(['pols-c'], () => can('policies.view') ? api.listPolicies() : Promise.resolve({ policies: [] }), { invalidateOn: ['policies'] });
  const [editing, setEditing] = useState(null); // check obj or 'new'
  const [detail, setDetail] = useState(null);
  const [confirmToggle, setConfirmToggle] = useState(null);

  const checks = checksQ.data?.checks || [];
  const polMap = {}; (policiesQ.data?.policies || []).forEach((p) => polMap[p.id] = p);
  const summary = { passing: 0, warning: 0, failing: 0 };
  checks.forEach((c) => { if (c.enabled) { const st = c.latest?.status || 'passing'; summary[st]++; } });
  const toggle = async (c) => {
    try {
      await api.updateCheck(c.id, { ...c, enabled: !c.enabled });
      toast.success(c.enabled ? 'Check disabled' : 'Check enabled');
    } catch (e) {
      toast.error('Failed to update check', e.message);
    }
  };

  return (
      <Page wide>
        <PageHeader title="Checks" subtitle="Continuous monitoring across devices and policies."
        actions={can('checks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>New check</Button>} />

      <div className="grid grid-cols-3 gap-3 mb-4 max-w-xl">
        <div className="rounded-xl border border-ok/25 bg-oksoft px-3.5 py-3"><div className="text-[11px] text-muted">Passing</div><div className="text-[22px] font-semibold text-ok tabular">{summary.passing}</div></div>
        <div className="rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3"><div className="text-[11px] text-muted">Warning</div><div className="text-[22px] font-semibold text-warn tabular">{summary.warning}</div></div>
        <div className="rounded-xl border border-bad/25 bg-badsoft px-3.5 py-3"><div className="text-[11px] text-muted">Failing</div><div className="text-[22px] font-semibold text-bad tabular">{summary.failing}</div></div>
      </div>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">Status</th><th className="font-semibold px-3 py-2.5">Check</th>
            <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Target</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Thresholds</th>
            <th className="font-semibold px-3 py-2.5">Interval</th><th className="font-semibold px-3 py-2.5">Enabled</th><th className="font-semibold px-4 py-2.5 text-right">Latest</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {checks.map((c) => {
              const t = CHECK_TYPES[c.type]; const dev = devMap[c.agent_id]; const pol = polMap[c.policy_id];
              return (
                <tr key={c.id} onClick={() => setDetail(c)} className="cursor-pointer hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><StatusPill status={c.enabled ? (c.latest?.status || 'passing') : 'disabled'} size="sm" /></td>
                  <td className="px-3 py-3"><div className="flex items-center gap-2"><Icon name={t.icon} size={15} className="text-muted" /><div><div className="text-[13px] text-ink font-medium">{c.name}</div><div className="text-[11px] text-faint">{t.label}{!c.enabled && ' · disabled'}</div></div></div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : pol ? <span className="flex items-center gap-1.5 text-brandhi"><Icon name="policy" size={13} />{pol.name}</span> : '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{c.warn_threshold != null ? `warn ${c.warn_threshold} · err ${c.err_threshold}` : '—'}</td>
                  <td className="px-3 py-3 text-[12px] text-muted">{c.interval_sec < 60 ? c.interval_sec + 's' : Math.round(c.interval_sec / 60) + 'm'}</td>
                  <td className="px-3 py-3" onClick={(e) => e.stopPropagation()}><Toggle checked={c.enabled} onChange={() => setConfirmToggle(c)} disabled={!can('checks.manage')} /></td>
                  <td className="px-4 py-3 text-right text-[12.5px] mono text-ink">{c.latest?.value != null ? c.latest.value + (t.unit === 'ms' ? ' ms' : t.unit) : c.latest?.output || '—'}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {checksQ.loading && <div className="p-4 space-y-2">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-8 w-full" />)}</div>}
        {!checksQ.loading && checks.length === 0 && <EmptyState icon="check" title="No checks yet" msg="Create a check to start monitoring." />}
      </Card>

      <CheckDetail check={detail} onClose={() => setDetail(null)} devMap={devMap} onEdit={(c) => { setDetail(null); setEditing(c); }} />
      {editing && <CheckEditor check={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} />}
      <ConfirmDialog open={!!confirmToggle} onClose={() => setConfirmToggle(null)} onConfirm={() => toggle(confirmToggle)}
        title={confirmToggle?.enabled ? 'Disable check?' : 'Enable check?'}
        confirmLabel={confirmToggle?.enabled ? 'Disable' : 'Enable'} danger={!!confirmToggle?.enabled}
        body={confirmToggle && (confirmToggle.enabled
          ? <>Disable <b>{confirmToggle.name}</b>? It will stop running until it is enabled again.</>
          : <>Enable <b>{confirmToggle.name}</b>? It will resume running on its configured interval.</>)} />
    </Page>
  );
}

function CheckDetail({ check, onClose, devMap, onEdit }) {
  const { can } = useAuth();
  const resultsQ = useQuery([check?.id, 'results'], () => check ? api.getCheckResults(check.id) : Promise.resolve({ results: [] }), { invalidateOn: ['checks'] });
  if (!check) return null;
  const t = CHECK_TYPES[check.type]; const dev = devMap[check.agent_id];
  const results = resultsQ.data?.results || [];
  return (
    <Drawer open={!!check} onClose={onClose} width="max-w-[620px]">
      <div className="px-5 py-4 border-b border-line flex items-start gap-3">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted shrink-0"><Icon name={t.icon} size={19} /></div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{check.name}</h2><span className="shrink-0"><StatusPill status={check.enabled ? (check.latest?.status || 'passing') : 'disabled'} size="sm" /></span></div>
          <div className="text-[12px] text-muted mt-0.5 truncate">{t.label} · {dev?.hostname || 'policy'} · every {check.interval_sec < 60 ? check.interval_sec + 's' : Math.round(check.interval_sec / 60) + 'm'}</div>
        </div>
        {can('checks.manage') && <Button size="sm" variant="secondary" icon="edit" onClick={() => onEdit(check)}>Edit</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <Card className="p-4">
          <div className="flex items-center justify-between mb-3"><span className="text-[12.5px] font-semibold text-ink">History (last {results.length})</span>
            {check.warn_threshold != null && <span className="text-[11.5px] text-faint mono">warn {check.warn_threshold} · err {check.err_threshold} {t.unit}</span>}
          </div>
          <div className="flex items-end gap-[3px] h-24">
            {results.map((r, i) => {
              const max = Math.max(...results.map((x) => x.value || 0), 1);
              const h = Math.max(6, ((r.value || 0) / max) * 100);
              const c = r.status === 'failing' ? 'bg-bad' : r.status === 'warning' ? 'bg-warn' : 'bg-ok';
              return <svg key={i} viewBox="0 0 10 100" preserveAspectRatio="none" className="flex-1 h-full overflow-visible" aria-label={`${r.value ?? r.output} · ${timeAgo(r.ran_at)}`}><rect x="0" y={100 - h} width="10" height={h} rx="1" className={c.replace('bg-', 'fill-')} /></svg>;
            })}
          </div>
        </Card>
        <div>
          <div className="text-[12px] font-semibold text-muted mb-2">Recent results</div>
          <div className="space-y-1">
            {results.slice().reverse().slice(0, 12).map((r) => (
              <div key={r.id} className="flex items-center gap-3 rounded-lg border border-line bg-card px-3 py-2">
                <StatusPill status={r.status} size="sm" />
                <span className="text-[12.5px] text-ink mono flex-1">{r.output}</span>
                <span className="text-[11.5px] text-faint tabular">{fmtTime(r.ran_at)}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </Drawer>
  );
}

function CheckEditor({ check, onClose, devMap, policies }) {
  const toast = useToast();
  const { can } = useAuth();
  const [type, setType] = useState(check?.type || 'diskspace');
  const [name, setName] = useState(check?.name || '');
  const [target, setTarget] = useState(check?.policy_id ? 'pol:' + check.policy_id : check?.agent_id ? 'dev:' + check.agent_id : '');
  const [cfgVal, setCfgVal] = useState(check ? (Object.values(check.config || {})[0] || '') : '');
  const [warn, setWarn] = useState(check?.warn_threshold ?? 20);
  const [err, setErr] = useState(check?.err_threshold ?? 10);
  const [interval, setInterval] = useState(check?.interval_sec ?? 300);
  const [enabled, setEnabled] = useState(check?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const t = CHECK_TYPES[type];
  const devices = Object.values(devMap);

  const save = async () => {
    setBusy(true);
    const config = {}; if (t.cfg) config[t.cfg] = cfgVal;
    const body = { type, name: name || t.label, config, interval_sec: Number(interval), enabled };
    if (t.dir !== 'state' && t.dir !== 'script') { body.warn_threshold = Number(warn); body.err_threshold = Number(err); }
    if (target.startsWith('dev:')) body.agent_id = target.slice(4); else if (target.startsWith('pol:')) body.policy_id = target.slice(4);
    try { if (check) await api.updateCheck(check.id, body); else await api.createCheck(body); toast.success(check ? 'Check updated' : 'Check created'); onClose(); }
    catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };
  const del = async () => { if (!window.confirm('Delete this check?')) return; try { await api.deleteCheck(check.id); toast.info('Check deleted'); onClose(); } catch (e) { toast.error('Failed', e.message); } };

  return (
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{check ? 'Edit check' : 'New check'}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <div className="p-5 space-y-4 max-h-[70vh] overflow-y-auto scroll-area">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Type"><Select value={type} onChange={(e) => setType(e.target.value)}>{Object.entries(CHECK_TYPES).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}</Select></Field>
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t.label} /></Field>
        </div>
        <Field label="Apply to"><Combobox value={target} onChange={setTarget} placeholder="Select target…"
          options={[
            ...devices.map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: 'Devices' })),
            ...policies.map((p) => ({ value: 'pol:' + p.id, label: p.name + ' (policy)', group: 'Policies' })),
          ]} /></Field>
        {t.cfg && <Field label={t.cfgLabel}>{t.cfg === 'script' ? <CodeEditor value={cfgVal} onChange={setCfgVal} shell="bash" minHeight={120} /> : <Input value={cfgVal} onChange={(e) => setCfgVal(e.target.value)} placeholder={t.cfgPh} className="mono" />}</Field>}
        {t.dir !== 'state' && t.dir !== 'script' && (
          <div className="grid grid-cols-3 gap-3">
            <Field label={`Warn (${t.unit})`}><Input type="number" value={warn} onChange={(e) => setWarn(e.target.value)} /></Field>
            <Field label={`Error (${t.unit})`}><Input type="number" value={err} onChange={(e) => setErr(e.target.value)} /></Field>
            <Field label="Interval (s)"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>
          </div>
        )}
        {(t.dir === 'state' || t.dir === 'script') && <Field label="Interval (s)" className="w-40"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>}
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={(next) => { setEnabled(next); toast.info(next ? 'Check will be enabled when saved' : 'Check will be disabled when saved'); }} /><span className="text-[13px] text-ink">Enabled</span></label>
        <div className="flex items-start gap-2 rounded-lg border border-line bg-elevated/40 px-3 py-2 text-[11.5px] text-muted"><Icon name="info" size={14} className="mt-0.5 shrink-0 text-faint" />{t.dir === 'low' ? 'Alerts when the value drops below the threshold (e.g. low disk/memory).' : t.dir === 'high' ? 'Alerts when the value rises above the threshold (e.g. high CPU/latency).' : t.dir === 'state' ? 'Alerts when the service is not running.' : 'Alerts on a non-zero exit code.'}</div>
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{check && can('checks.manage') && <Button variant="danger" icon="trash" onClick={del}>Delete</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} onClick={save} disabled={!target}>{check ? 'Save check' : 'Create check'}</Button></div>
    </Modal>
  );
}

/* ============================ SCHEDULED TASKS ============================= */
function TasksScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const tasksQ = useQuery(['tasks'], () => api.listTasks(), { invalidateOn: ['tasks'] });
  const devMap = useDeviceMap();
  const policiesQ = useQuery(['pols-t'], () => can('policies.view') ? api.listPolicies() : Promise.resolve({ policies: [] }), { invalidateOn: ['policies'] });
  const scriptsQ = useQuery(['scripts-t'], () => can('scripts.view') ? api.listScripts() : Promise.resolve({ scripts: [] }), { invalidateOn: ['scripts'] });
  const [editing, setEditing] = useState(null);
  const [confirmToggleTask, setConfirmToggleTask] = useState(null);
  const tasks = tasksQ.data?.tasks || [];
  const polMap = {}; (policiesQ.data?.policies || []).forEach((p) => polMap[p.id] = p);
  const scriptMap = {}; (scriptsQ.data?.scripts || []).forEach((s) => scriptMap[s.id] = s);

  const schedule = (t) => {
    if (t.schedule_kind === 'daily') return 'Daily at ' + minuteToHHMM(t.run_at_minute);
    if (t.schedule_kind === 'weekly') {
      const days = WEEKDAYS.filter((_, i) => (t.days_of_week & (1 << i)) !== 0).join(' ');
      const at = (t.run_at_minutes || []).map(minuteToHHMM).join(', ');
      return (days || '—') + ' at ' + (at || '—');
    }
    return 'Every ' + (t.interval_sec < 3600 ? Math.round(t.interval_sec / 60) + 'm' : Math.round(t.interval_sec / 3600) + 'h');
  };
  const toggle = async (t) => {
    // Task updates use PUT, so the server validates the entire schedule. Keep
    // the current task fields when changing just the enabled state.
    try {
      await api.updateTask(t.id, { ...t, enabled: !t.enabled });
      toast.success(t.enabled ? 'Task disabled' : 'Task enabled');
    } catch (e) {
      toast.error('Failed to update task', e.message);
    }
  };
  const [runningTask, setRunningTask] = useState(null); // { task, count } pending confirm
  const askRun = async (t) => {
    try { const { count } = await api.taskTargets(t.id); setRunningTask({ task: t, count }); }
    catch (e) { toast.error('Failed to resolve targets', e.message); }
  };
  const doRun = async (t) => {
    try { const { queued } = await api.runTask(t.id); toast.success('Task queued', 'Running on ' + queued + ' agent' + (queued === 1 ? '' : 's') + '.'); }
    catch (e) { toast.error('Run failed', e.message); }
  };

  return (
      <Page wide>
        <PageHeader title="Scheduled tasks" subtitle="Run scripts on a schedule, per device or per policy."
        actions={can('tasks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>New task</Button>} />
      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">Task</th><th className="font-semibold px-3 py-2.5 hidden md:table-cell">Target</th>
            <th className="font-semibold px-3 py-2.5">Schedule</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Script</th>
            <th className="font-semibold px-3 py-2.5">Enabled</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {tasks.map((t) => {
              const dev = devMap[t.agent_id]; const pol = polMap[t.policy_id]; const sc = scriptMap[t.script_id];
              return (
                <tr key={t.id} className="hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><div className="flex items-center gap-2"><Icon name="tasks" size={15} className="text-muted" /><span className="text-[13px] text-ink font-medium">{t.name}</span></div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : pol ? <span className="flex items-center gap-1.5 text-brandhi"><Icon name="policy" size={13} />{pol.name}</span> : '—'}</td>
                  <td className="px-3 py-3 text-[12.5px] text-muted flex items-center gap-1.5"><Icon name="clock" size={13} className="text-faint" />{schedule(t)}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[12px] text-faint">{sc?.name || 'inline'}</td>
                  <td className="px-3 py-3"><Toggle checked={t.enabled} onChange={() => setConfirmToggleTask(t)} disabled={!can('tasks.manage')} /></td>
                  <td className="px-4 py-3 text-right">{can('tasks.manage') && <div className="flex items-center justify-end gap-1"><Button size="sm" variant="ghost" icon="play" onClick={() => askRun(t)}>Run now</Button><Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing(t)}>Edit</Button></div>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!tasksQ.loading && tasks.length === 0 && <EmptyState icon="tasks" title="No scheduled tasks" msg="Create a task to automate routine scripts." />}
      </Card>
      {editing && <TaskEditor task={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} scripts={scriptsQ.data?.scripts || []} />}
      <ConfirmDialog open={!!confirmToggleTask} onClose={() => setConfirmToggleTask(null)} onConfirm={() => toggle(confirmToggleTask)}
        title={confirmToggleTask?.enabled ? 'Disable task?' : 'Enable task?'}
        confirmLabel={confirmToggleTask?.enabled ? 'Disable' : 'Enable'} danger={!!confirmToggleTask?.enabled}
        body={confirmToggleTask && (confirmToggleTask.enabled
          ? <>Disable <b>{confirmToggleTask.name}</b>? It will stop running until it is enabled again.</>
          : <>Enable <b>{confirmToggleTask.name}</b>? It will resume running on its configured schedule.</>)} />
      <ConfirmDialog open={!!runningTask} onClose={() => setRunningTask(null)} onConfirm={() => doRun(runningTask.task)}
        title="Run task now?" confirmLabel="Run now"
        body={runningTask && (runningTask.count === 0
          ? <>This task has <b>no target agents</b> in scope right now — nothing will run.</>
          : <>Run <b>{runningTask.task.name}</b> immediately on <b>{runningTask.count}</b> agent{runningTask.count === 1 ? '' : 's'}? This runs the script outside the schedule; offline agents get it on reconnect.</>)} />
    </Page>
  );
}

function TaskEditor({ task, onClose, devMap, policies, scripts }) {
  const toast = useToast();
  const { can } = useAuth();
  const [name, setName] = useState(task?.name || '');
  const [target, setTarget] = useState(task?.policy_id ? 'pol:' + task.policy_id : task?.agent_id ? 'dev:' + task.agent_id : '');
  const [scriptId, setScriptId] = useState(task?.script_id || '');
  const [kind, setKind] = useState(task?.schedule_kind || 'interval');
  const [intervalSec, setIntervalSec] = useState(task?.interval_sec || 3600);
  const [minute, setMinute] = useState(task?.run_at_minute || 180);
  const [daysOfWeek, setDaysOfWeek] = useState(task?.days_of_week || 0);
  const [times, setTimes] = useState(task?.run_at_minutes?.length ? task.run_at_minutes : [180]);
  const [enabled, setEnabled] = useState(task?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const devices = Object.values(devMap);

  const save = async () => {
    if (kind === 'weekly' && (daysOfWeek === 0 || times.length === 0)) { toast.error('Pick at least one weekday and one time'); return; }
    setBusy(true);
    const sc = scripts.find((s) => s.id === scriptId);
    const body = { name: name || 'Task', script_id: scriptId || undefined, shell: sc?.shell || 'bash', schedule_kind: kind, interval_sec: Number(intervalSec), run_at_minute: Number(minute), days_of_week: kind === 'weekly' ? daysOfWeek : 0, run_at_minutes: kind === 'weekly' ? times : [], enabled };
    if (target.startsWith('dev:')) body.agent_id = target.slice(4); else if (target.startsWith('pol:')) body.policy_id = target.slice(4);
    try { if (task) await api.updateTask(task.id, body); else await api.createTask(body); toast.success(task ? 'Task updated' : 'Task created'); onClose(); }
    catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };
  const del = async () => { if (!window.confirm('Delete this scheduled task?')) return; try { await api.deleteTask(task.id); toast.info('Task deleted'); onClose(); } catch (e) { toast.error('Failed', e.message); } };

  return (
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{task ? 'Edit task' : 'New scheduled task'}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <div className="p-5 space-y-4">
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Nightly cleanup" /></Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Target"><Combobox value={target} onChange={setTarget} placeholder="Select…"
            options={[
              ...devices.map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: 'Devices' })),
              ...policies.map((p) => ({ value: 'pol:' + p.id, label: p.name, group: 'Policies' })),
            ]} /></Field>
          <Field label="Script"><Combobox value={scriptId} onChange={setScriptId} placeholder="Choose script…"
            options={scripts.map((s) => ({ value: s.id, label: s.name }))} /></Field>
        </div>
        <Field label="Schedule">
          <div className="flex gap-1 p-0.5 bg-app rounded-lg border border-line2 w-fit mb-2">
            {[['interval', 'Interval'], ['daily', 'Daily at time'], ['weekly', 'Weekly']].map(([v, l]) => <button key={v} onClick={() => setKind(v)} className={cx('h-7 px-3 rounded-md text-[12px] font-medium transition', kind === v ? 'bg-elevated text-ink' : 'text-faint hover:text-muted')}>{l}</button>)}
          </div>
          {kind === 'interval' && (
            <div className="flex items-center gap-2"><span className="text-[12.5px] text-muted">Every</span><Input type="number" value={Math.round(intervalSec / 60)} onChange={(e) => setIntervalSec(Number(e.target.value) * 60)} className="w-24" /><span className="text-[12.5px] text-muted">minutes</span></div>
          )}
          {kind === 'daily' && (
            <div className="flex items-center gap-2"><span className="text-[12.5px] text-muted">At</span><Input type="time" value={minuteToHHMM(minute)} onChange={(e) => { const [h, m] = e.target.value.split(':').map(Number); setMinute(h * 60 + m); }} className="w-32" /></div>
          )}
          {kind === 'weekly' && (
            <div className="space-y-3">
              <div className="flex gap-1">
                {WEEKDAYS.map((d, i) => {
                  const on = (daysOfWeek & (1 << i)) !== 0;
                  return <button key={i} type="button" onClick={() => setDaysOfWeek(daysOfWeek ^ (1 << i))}
                    className={cx('h-8 w-10 rounded-md text-[11.5px] font-medium transition', on ? 'bg-brand text-white' : 'bg-app border border-line2 text-muted hover:text-ink')}>{d}</button>;
                })}
              </div>
              <div className="space-y-2">
                {times.map((m, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <span className="text-[12.5px] text-muted">At</span>
                    <Input type="time" value={minuteToHHMM(m)} onChange={(e) => { const [h, mm] = e.target.value.split(':').map(Number); const next = times.slice(); next[idx] = h * 60 + mm; setTimes(next); }} className="w-32" />
                    {times.length > 1 && <button type="button" onClick={() => setTimes(times.filter((_, i) => i !== idx))} className="text-faint hover:text-bad p-1"><Icon name="trash" size={15} /></button>}
                  </div>
                ))}
                <Button variant="ghost" size="sm" icon="plus" onClick={() => setTimes([...times, 540])}>Add time</Button>
              </div>
            </div>
          )}
        </Field>
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={setEnabled} /><span className="text-[13px] text-ink">Enabled</span></label>
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{task && can('tasks.manage') && <Button variant="danger" icon="trash" onClick={del}>Delete</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} onClick={save} disabled={!target}>{task ? 'Save task' : 'Create task'}</Button></div>
    </Modal>
  );
}

window.AlertsScreen = AlertsScreen;
window.ChecksScreen = ChecksScreen;
window.TasksScreen = TasksScreen;
window.CHECK_TYPES = CHECK_TYPES;
window.SEV = SEV;
