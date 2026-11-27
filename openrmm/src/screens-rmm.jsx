/* screens-rmm.jsx — Part 2: Alerts, Checks, Scheduled tasks */

// Weekday indexes match Go's time.Weekday (0=Sunday), so bit i of a task's
// days_of_week bitmask is weekdayShort(i) on both ends.
const WEEKDAY_BITS = [0, 1, 2, 3, 4, 5, 6];

// Colour and icon per severity; the label is t('sev.<name>').
const SEV = {
  error: { c: 'bad', icon: 'alert' },
  warning: { c: 'warn', icon: 'alert' },
  info: { c: 'run', icon: 'info' },
};
// Labels are t('check.type.<type>') and t('check.cfg.<type>'), filled in by
// checkType() so every reader gets them in the console's language.
const CHECK_TYPES = {
  diskspace: { icon: 'disk', unit: '%', dir: 'low', cfg: 'path', cfgPh: '/' },
  memory: { icon: 'mem', unit: '%', dir: 'low', cfg: null },
  cpuload: { icon: 'cpu', unit: '%', dir: 'high', cfg: null },
  ping: { icon: 'network', unit: '', dir: 'state', cfg: 'host', cfgPh: '10.0.0.1' },
  winservice: { icon: 'settings', unit: '', dir: 'state', cfg: 'service', cfgPh: 'Spooler' }, // i18n-ignore: a Windows service name
  linuxservice: { icon: 'settings', unit: '', dir: 'state', cfg: 'service', cfgPh: 'sshd.service' },
  macservice: { icon: 'settings', unit: '', dir: 'state', cfg: 'service', cfgPh: 'system/com.apple.notifyd' },
  dockercontainer: { icon: 'devices', unit: '', dir: 'state', cfg: 'container', cfgPh: 'web' },
  script: { icon: 'script', unit: '', dir: 'script', cfg: 'script', cfgPh: 'exit 0' },
  connectivity: { icon: 'network', unit: '', dir: 'server', server: true },
  job_failure: { icon: 'script', unit: '', dir: 'server', server: true },
  patch_failure: { icon: 'settings', unit: '', dir: 'server', server: true },
  reboot_pending: { icon: 'refresh', unit: '', dir: 'server', server: true },
  agent_outdated: { icon: 'info', unit: '', dir: 'server', server: true },
};

const SERVER_RULE_TYPES = ['connectivity', 'job_failure', 'patch_failure', 'reboot_pending', 'agent_outdated'];

function serverRuleConfig(type, input = {}) {
  const cfg = { mode: input.mode || 'alert', severity: input.severity || (type === 'connectivity' ? 'error' : 'warning') };
  if (input.override_parent === true) cfg.override_parent = true;
  if (type === 'connectivity') cfg.after_min = input.after_min ?? 3;
  if (type === 'job_failure') cfg.scope = input.scope || 'tasks';
  if (type === 'reboot_pending') cfg.after_days = input.after_days ?? 3;
  if (type === 'agent_outdated') cfg.behind = input.behind || 'minor';
  return cfg;
}

function ServerRuleFields({ type, value, onChange, disabled = false }) {
  const cfg = serverRuleConfig(type, value);
  const set = (key, next) => onChange({ ...cfg, [key]: next });
  return <div className="space-y-3">
    <Field label={t('rules.mode')}><Select value={cfg.mode} disabled={disabled} onChange={(e) => set('mode', e.target.value)}><option value="alert">{t('rules.alert')}</option><option value="ignore">{t('rules.ignore')}</option></Select></Field>
    {cfg.mode === 'alert' && <>
      <Field label={t('alerts.col.severity')}><Select value={cfg.severity} disabled={disabled} onChange={(e) => set('severity', e.target.value)}>{['info', 'warning', 'error'].map((level) => <option key={level} value={level}>{t('sev.' + level)}</option>)}</Select></Field>
      {type === 'connectivity' && <Field label={t('rules.afterMin')}><Input type="number" min="3" max="10080" value={cfg.after_min} disabled={disabled} onChange={(e) => set('after_min', Number(e.target.value))} /></Field>}
      {type === 'reboot_pending' && <Field label={t('rules.afterDays')}><Input type="number" min="0" max="90" value={cfg.after_days} disabled={disabled} onChange={(e) => set('after_days', Number(e.target.value))} /></Field>}
      {type === 'job_failure' && <Field label={t('rules.jobScope')}><Select value={cfg.scope} disabled={disabled} onChange={(e) => set('scope', e.target.value)}><option value="tasks">{t('rules.tasks')}</option><option value="all">{t('rules.allJobs')}</option></Select></Field>}
      {type === 'agent_outdated' && <Field label={t('rules.behind')}><Select value={cfg.behind} disabled={disabled} onChange={(e) => set('behind', e.target.value)}>{['major', 'minor', 'patch'].map((level) => <option key={level} value={level}>{t('rules.' + level)}</option>)}</Select></Field>}
    </>}
    <p className="text-[12px] text-muted">{t('rules.serverHelp')}</p>
    {cfg.override_parent && <p className="text-[12px] font-medium text-brandhi">{t('rules.overrideActive')}</p>}
    <p className="text-[11px] text-faint">{t('rules.overrideHelp')}</p>
    <p className="text-[11px] text-faint">{t('rules.priority')}</p>
  </div>;
}

function AlertDefaultsEditor({ onClose, canEdit }) {
  const toast = useToast();
  const q = useQuery(['alert-defaults-editor'], () => api.getAlertDefaults());
  const [type, setType] = useState('connectivity');
  const [drafts, setDrafts] = useState({});
  const [busy, setBusy] = useState(false);
  const value = drafts[type] || q.data?.defaults?.[type]?.config;
  const save = async () => {
    if (!canEdit || !q.data) return;
    setBusy(true);
    try { await api.updateAlertDefault(type, serverRuleConfig(type, value)); toast.success(t('rules.defaultSaved')); onClose(); }
    catch (e) { toast.error(t('common.failed'), e.message); }
    finally { setBusy(false); }
  };
  return <Modal open onClose={onClose} width="max-w-lg">
    <div className="px-5 py-4 border-b border-line text-[15px] font-semibold">{t('rules.defaults')}</div>
    <div className="p-5 space-y-4"><p className="text-[12px] text-muted">{t('rules.defaultsHint')}</p>
      <Select value={type} onChange={(e) => setType(e.target.value)}>{SERVER_RULE_TYPES.map((kind) => <option key={kind} value={kind}>{checkType(kind).label}</option>)}</Select>
      {q.data ? <ServerRuleFields type={type} value={value} disabled={!canEdit} onChange={(cfg) => setDrafts((prev) => ({ ...prev, [type]: cfg }))} /> : <Skeleton className="h-40" />}
    </div>
    <div className="px-5 py-3 border-t border-line flex justify-end gap-2"><Button variant="ghost" onClick={onClose}>{t('action.close')}</Button>{canEdit && <Button variant="primary" loading={busy} disabled={!q.data} onClick={save}>{t('action.save')}</Button>}</div>
  </Modal>;
}

// A check whose type this build does not recognise.
//
// CHECK_TYPES was read directly — CHECK_TYPES[c.type].icon — in five places,
// which meant one unrecognised row did not degrade a cell: it threw during
// render and React unmounted the console to a white page. The server is the
// authority on what types exist, and it can legitimately know one this bundle
// does not: an agent shipped ahead of the console, a migration in progress, a
// row edited by hand.
//
// So the lookup falls back, and the fallback shows the raw type rather than
// dressing it up as some other kind of check. An operator seeing "kernel_taint"
// under an unfamiliar icon can act on it; one seeing "Script check" has been
// told something untrue about a thing that runs on their machines.
//
// SEV, PATCH_CLASS_STYLE and NOTIFY_LABELS already guarded their lookups. This
// was the one that did not.
function checkType(type) {
  const known = CHECK_TYPES[type];
  if (!known) return { label: type || t('check.unknownType'), icon: 'settings', unit: '', dir: 'state', cfg: null };
  return { ...known, label: t('check.type.' + type), cfgLabel: known.cfg ? t('check.cfg.' + type) : undefined };
}

// A task's schedule in words: "Every 1 h", "Daily at 03:00", "Mon Wed at 08:00".
function taskSchedule(task) {
  const zone = ' ' + scheduleZone(task.timezone);
  if (task.schedule_kind === 'daily') return t('tasks.dailyAt', { time: minuteToHHMM(task.run_at_minute) + zone });
  if (task.schedule_kind === 'weekly') {
    const days = WEEKDAY_BITS.filter((i) => (task.days_of_week & (1 << i)) !== 0).map(weekdayShort).join(' ');
    const at = (task.run_at_minutes || []).map(minuteToHHMM).join(', ');
    return t('tasks.weeklyAt', { days: days || '—', times: at ? at + zone : '—' });
  }
  return t('tasks.every', { interval: fmtInterval(task.interval_sec) });
}

function checkAggregateStatus(c) {
  if (!c.enabled) return 'disabled';
  const x = c.summary || {};
  if (x.failing > 0) return 'failing';
  if (x.warning > 0) return 'warning';
  if (x.unknown > 0 || !x.total) return 'unknown';
  return 'passing';
}

/* ================================ ALERTS ================================== */
function AlertsScreen({ navigate }) {
  const { can } = useAuth();
  const toast = useToast();
  const devMap = useDeviceMap();
  const [statusF, setStatusF] = useState('all');
  const [sevF, setSevF] = useState('all');
  const [sourceF, setSourceF] = useState('all');
  const [search, setSearch] = useState('');
  const [cursors, setCursors] = useState(['']);
  const [selected, setSelected] = useState([]);
  const [detailID, setDetailID] = useState(null);
  const [showSuppressions, setShowSuppressions] = useState(false);
  const cursor = cursors[cursors.length - 1];
  const params = { limit: 50, cursor, status: statusF === 'all' ? '' : statusF, severity: sevF === 'all' ? '' : sevF, source_type: sourceF === 'all' ? '' : sourceF, q: search.trim() };
  const alertsQ = useQuery(['alerts', statusF, sevF, sourceF, search, cursor], () => api.listAlerts(params), { invalidateOn: ['alerts'], poll: 5000 });

  const alerts = alertsQ.data?.alerts || [];
  const counts = alertsQ.data?.counts || { new: 0, acked: 0, suppressed: 0, resolved: 0, total: 0 };
  const resetPage = (setter, value) => { setter(value); setCursors(['']); setSelected([]); };
  const checked = (id) => selected.includes(id);
  const toggleSelected = (id) => setSelected((xs) => xs.includes(id) ? xs.filter((x) => x !== id) : [...xs, id]);
  const visibleSelected = selected.filter((id) => alerts.some((a) => a.id === id));
  const allSelected = alerts.length > 0 && alerts.every((a) => checked(a.id));

  const actionBody = (a, extra) => ({ expected_version: a.version, ...(extra || {}) });
  // Resolving and silencing ask for a note and a duration in a console dialog
  // (dialog: { action, alert } for one alert, { action, bulk: true } for the
  // selection), not in the browser's prompt over the tab.
  const [dialog, setDialog] = useState(null);
  const silenceUntil = (minutes) => new Date(Date.now() + Number(minutes) * 60000).toISOString();
  const validMinutes = (v) => (Number.isFinite(Number(v)) && Number(v) > 0 ? '' : t('alerts.positiveMinutes'));
  const act = async (action, a) => {
    if (action === 'resolve' || action === 'suppress') { setDialog({ action, alert: a }); return; }
    try {
      if (action === 'ack') await api.ackAlert(a.id, actionBody(a));
      toast.success(t('alerts.acknowledged'));
      setDetailID(null);
    } catch (e) { toast.error(t('alerts.actionFailed'), e.message); }
  };
  const actOne = async (action, a, values) => {
    try {
      if (action === 'resolve') await api.resolveAlert(a.id, actionBody(a, { note: values.note || '' }));
      if (action === 'suppress') await api.suppressAlert(a.id, actionBody(a, { note: values.note || '', silence_until: silenceUntil(values.minutes) }));
      toast.success(action === 'resolve' ? t('alerts.resolved') : t('alerts.suppressed'));
      setDialog(null); setDetailID(null);
    } catch (e) { toast.error(t('alerts.actionFailed'), e.message); }
  };
  const bulk = async (action) => {
    if (!visibleSelected.length) return;
    if (action === 'resolve' || action === 'suppress') { setDialog({ action, bulk: true }); return; }
    await runBulk(action, {});
  };
  const runBulk = async (action, values) => {
    const body = { ids: visibleSelected, action, expected_versions: {} };
    alerts.filter((a) => visibleSelected.includes(a.id)).forEach((a) => body.expected_versions[a.id] = a.version);
    if (action === 'resolve') body.note = values.note || '';
    if (action === 'suppress') { body.silence_until = silenceUntil(values.minutes); body.note = values.note || ''; }
    try {
      const out = await api.bulkAlertAction(body);
      setSelected([]); setDialog(null);
      if ((out.errors || []).length) toast.info(t('alerts.bulkPartial'), t('alerts.bulkPartialMsg', { updated: out.alerts.length, conflicts: out.errors.length }));
      else toast.success(t('alerts.bulkUpdated', { n: out.alerts.length }));
    } catch (e) { toast.error(t('alerts.bulkFailed'), e.message); }
  };
  const dialogProps = (() => {
    if (!dialog) return null;
    const many = !!dialog.bulk;
    if (dialog.action === 'resolve') {
      return {
        title: t('alerts.resolve'), submitLabel: t('alerts.resolve'),
        fields: [{ name: 'note', type: 'textarea', label: many ? t('alerts.bulkResolveNote') : t('alerts.resolveNote') }],
        onSubmit: (v) => (many ? runBulk('resolve', v) : actOne('resolve', dialog.alert, v)),
      };
    }
    return {
      title: t('alerts.suppress'), submitLabel: t('alerts.suppress'),
      fields: [
        { name: 'minutes', type: 'number', min: 1, initial: '60', label: many ? t('alerts.bulkSuppressMinutes') : t('alerts.suppressMinutes') },
        { name: 'note', type: 'text', label: t('alerts.suppressReason') },
      ],
      validate: (v) => validMinutes(v.minutes),
      onSubmit: (v) => (many ? runBulk('suppress', v) : actOne('suppress', dialog.alert, v)),
    };
  })();

  return (
    <Page wide>
      <PageHeader title={t('title.alerts')} subtitle={t('alerts.subtitle')}
        actions={can('alerts.manage') && <Button variant="secondary" icon="clock" onClick={() => setShowSuppressions(true)}>{t('alerts.silences')}</Button>} />

      <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 mb-4">
        <StatTile label={t('status.new')} value={counts.new} icon="alert" tone={counts.new ? 'bad' : 'default'} highlight={counts.new > 0} onClick={() => resetPage(setStatusF, 'new')} />
        <StatTile label={t('status.acked')} value={counts.acked} icon="eye" tone="warn" onClick={() => resetPage(setStatusF, 'acked')} />
        <StatTile label={t('status.suppressed')} value={counts.suppressed} icon="clock" tone="run" onClick={() => resetPage(setStatusF, 'suppressed')} />
        <StatTile label={t('status.resolved')} value={counts.resolved} icon="approve" tone="ok" onClick={() => resetPage(setStatusF, 'resolved')} />
        <StatTile label={t('alerts.total')} value={counts.total} icon="list" onClick={() => resetPage(setStatusF, 'all')} />
      </div>

      <Card className="mb-4"><div className="flex flex-wrap items-center gap-2 p-2.5">
        <Input value={search} onChange={(e) => resetPage(setSearch, e.target.value)} placeholder={t('alerts.search')} className="min-w-[240px] flex-1" />
        <Select value={statusF} onChange={(e) => resetPage(setStatusF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">{t('alerts.anyStatus')}</option><option value="open">{t('alerts.open')}</option><option value="new">{t('status.new')}</option><option value="acked">{t('status.acked')}</option><option value="suppressed">{t('status.suppressed')}</option><option value="resolved">{t('status.resolved')}</option></Select>
        <Select value={sevF} onChange={(e) => resetPage(setSevF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">{t('alerts.anySeverity')}</option><option value="error">{t('sev.error')}</option><option value="warning">{t('sev.warning')}</option><option value="info">{t('sev.info')}</option></Select>
        <Select value={sourceF} onChange={(e) => resetPage(setSourceF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">{t('alerts.anySource')}</option><option value="check">{t('alerts.source.check')}</option><option value="agent">{t('alerts.source.agent')}</option><option value="task">{t('alerts.source.task')}</option><option value="system">{t('alerts.source.system')}</option><option value="rule">{t('alerts.source.rule')}</option></Select>
      </div></Card>

      {visibleSelected.length > 0 && can('alerts.manage') && <div className="mb-3 flex items-center gap-2 rounded-xl border border-brand/25 bg-brand/5 px-3 py-2">
        <span className="text-[12.5px] text-ink mr-auto">{t('alerts.selected', { n: visibleSelected.length })}</span>
        <Button size="sm" variant="subtle" onClick={() => bulk('ack')}>{t('alerts.acknowledge')}</Button>
        <Button size="sm" variant="subtle" onClick={() => bulk('suppress')}>{t('alerts.suppress')}</Button>
        <Button size="sm" variant="secondary" icon="approve" onClick={() => bulk('resolve')}>{t('alerts.resolve')}</Button>
      </div>}

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            {can('alerts.manage') && <th className="px-4 py-2.5 w-8"><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? [] : alerts.map((a) => a.id))} /></th>}
            <th className="font-semibold px-3 py-2.5">{t('alerts.col.severity')}</th><th className="font-semibold px-3 py-2.5">{t('alerts.col.message')}</th>
            <th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('alerts.col.device')}</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">{t('alerts.col.source')}</th>
            <th className="font-semibold px-3 py-2.5">{t('devices.col.status')}</th><th className="font-semibold px-3 py-2.5 text-right">{t('devices.col.lastSeen')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('alerts.col.actions')}</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {alerts.map((a) => {
              const s = SEV[a.severity] || SEV.warning; const dev = devMap[a.agent_snapshot_id || a.agent_id];
              return (
                <tr key={a.id} onClick={() => setDetailID(a.id)} className={cx('cursor-pointer hover:bg-elevated/40 transition align-middle', a.status === 'resolved' && 'opacity-60')}>
                  {can('alerts.manage') && <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}><input type="checkbox" checked={checked(a.id)} onChange={() => toggleSelected(a.id)} /></td>}
                  <td className="px-3 py-3"><span className={cx('inline-flex items-center gap-1.5 text-[12px] font-medium', `text-${s.c}`)}><Icon name={s.icon} size={14} />{t('sev.' + (SEV[a.severity] ? a.severity : 'warning'))}</span></td>
                  <td className="px-3 py-3 text-[13px] text-ink max-w-[380px]"><div className="truncate">{a.message}</div>{a.occurrence_count > 1 && <div className="text-[10.5px] text-faint mt-0.5">{t('alerts.observations', { n: a.occurrence_count })}</div>}</td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : a.agent_hostname || '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{a.source_type}{a.source_id ? '·' + a.source_id.slice(0, 10) : ''}</td>
                  <td className="px-3 py-3"><StatusPill status={a.status} size="sm" /></td>
                  <td className="px-3 py-3 text-right text-[12px] text-muted tabular whitespace-nowrap">{timeAgo(a.last_seen_at || a.created_at)}</td>
                  <td className="px-4 py-3 text-right" onClick={(e) => e.stopPropagation()}>
                    {a.status !== 'resolved' && can('alerts.manage') ? (
                      <div className="flex items-center justify-end gap-1.5">
                        {a.status === 'new' && <Button size="sm" variant="subtle" onClick={() => act('ack', a)}>{t('alerts.ack')}</Button>}
                        {a.status !== 'suppressed' && <Button size="sm" variant="subtle" onClick={() => act('suppress', a)}>{t('alerts.mute')}</Button>}
                        <Button size="sm" variant="secondary" icon="approve" onClick={() => act('resolve', a)}>{t('alerts.resolve')}</Button>
                      </div>
                    ) : a.status === 'resolved' ? <span className="text-[11.5px] text-faint">{timeAgo(a.resolved_at)}</span> : <span className="text-[11px] text-faint">{t('alerts.viewOnly')}</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {alertsQ.loading && <div className="p-4 space-y-2">{Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>}
        {!alertsQ.loading && alerts.length === 0 && <EmptyState icon="approve" title={t('alerts.none')} msg={t('alerts.noneMsg')} />}
        {(cursors.length > 1 || alertsQ.data?.next_cursor) && <div className="border-t border-line px-4 py-3 flex justify-end gap-2">
          <Button size="sm" variant="ghost" disabled={cursors.length === 1} onClick={() => { setCursors((xs) => xs.slice(0, -1)); setSelected([]); }}>{t('audit.previous')}</Button>
          <Button size="sm" variant="secondary" disabled={!alertsQ.data?.next_cursor} onClick={() => { setCursors((xs) => [...xs, alertsQ.data.next_cursor]); setSelected([]); }}>{t('audit.next')}</Button>
        </div>}
      </Card>
      <AlertDetail navigate={navigate} alertID={detailID} onClose={() => setDetailID(null)} canManage={can('alerts.manage')} onAction={act} />
      <FormDialog open={!!dialog} onClose={() => setDialog(null)} {...(dialogProps || {})} />
      {showSuppressions && <AlertSuppressionsModal onClose={() => setShowSuppressions(false)} devMap={devMap} />}
    </Page>
  );
}

function AlertDetail({ alertID, onClose, canManage, onAction, navigate }) {
  const { can } = useAuth();
  const q = useQuery(['alert-detail', alertID], () => alertID ? api.getAlert(alertID) : Promise.resolve(null), { invalidateOn: ['alerts'], poll: alertID ? 5000 : 0 });
  const a = q.data?.alert;
  const events = q.data?.events || [];
  return <Drawer open={!!alertID} onClose={onClose} width="max-w-[680px]">
    <div className="px-5 py-4 border-b border-line flex items-start gap-3">
      <div className="flex-1 min-w-0"><div className="flex items-center gap-2"><h2 className="text-[16px] font-semibold text-ink">{t('alerts.detail')}</h2>{a && <StatusPill status={a.status} size="sm" />}</div><div className="text-[12px] text-muted mt-1 truncate">{a?.agent_hostname || t('common.loading')}</div></div>
      <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
    </div>
    <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
      {q.loading && <Skeleton className="h-40 w-full" />}
      {a && <>
        <Card className="p-4 space-y-3">
          <div className="text-[14px] text-ink font-medium">{a.message}</div>
          <div className="grid grid-cols-2 gap-3 text-[12px]"><div><span className="text-faint">{t('alerts.firstSeen')}</span><div className="text-muted mt-0.5">{fmtTime(a.first_seen_at)}</div></div><div><span className="text-faint">{t('alerts.lastSeen')}</span><div className="text-muted mt-0.5">{fmtTime(a.last_seen_at)}</div></div><div><span className="text-faint">{t('alerts.occurrences')}</span><div className="text-muted mt-0.5">{a.occurrence_count}</div></div><div><span className="text-faint">{t('alerts.col.source')}</span><div className="text-muted mt-0.5">{tEnum('alerts.source', a.source_type)}</div></div></div>
          {a.rule_id && can('checks.view') && navigate && <Button size="sm" variant="ghost" icon="check" onClick={() => { onClose(); navigate('checks', { focus: a.rule_id }); }}>{t('rules.ruleLink')}</Button>}
          {(a.observed_value != null || a.threshold_value != null) && <div className="text-[12px] mono text-muted">{t('alerts.observed', { value: a.observed_value ?? '—', threshold: a.threshold_value ?? '—' })}</div>}
          {a.output && <pre className="rounded-lg border border-line bg-elevated/50 p-3 text-[11.5px] text-muted whitespace-pre-wrap break-words max-h-48 overflow-y-auto">{a.output}</pre>}
          {a.status === 'suppressed' && <div className="rounded-lg bg-runsoft border border-run/20 px-3 py-2 text-[12px] text-run">{t('alerts.suppressedUntil', { when: fmtTime(a.silenced_until) })}{a.silence_reason ? ` — ${a.silence_reason}` : ''}</div>}
          {a.status === 'resolved' && a.resolution_note && <div className="rounded-lg bg-oksoft border border-ok/20 px-3 py-2 text-[12px] text-ok">{a.resolution_note}</div>}
        </Card>
        <div><div className="text-[12px] font-semibold text-muted mb-2">{t('alerts.timeline')}</div><div className="space-y-2">
          {events.slice().reverse().map((e) => <div key={e.id} className="flex gap-3 rounded-lg border border-line bg-card px-3 py-2.5"><div className="mt-1 h-2 w-2 rounded-full bg-brand shrink-0" /><div className="min-w-0 flex-1"><div className="text-[12.5px] text-ink">{tEnum('alerts.event', e.event_type)}</div>{e.message && <div className="text-[11.5px] text-muted mt-0.5 break-words">{e.message}</div>}<div className="text-[10.5px] text-faint mt-1">{e.actor} · {fmtTime(e.created_at)}</div></div></div>)}
        </div></div>
      </>}
    </div>
    {a && a.status !== 'resolved' && canManage && <div className="px-5 py-3.5 border-t border-line flex justify-end gap-2">{a.status === 'new' && <Button size="sm" variant="subtle" onClick={() => onAction('ack', a)}>{t('alerts.acknowledge')}</Button>}{a.status !== 'suppressed' && <Button size="sm" variant="subtle" onClick={() => onAction('suppress', a)}>{t('alerts.suppress')}</Button>}<Button size="sm" variant="primary" icon="approve" onClick={() => onAction('resolve', a)}>{t('alerts.resolve')}</Button></div>}
  </Drawer>;
}

function AlertSuppressionsModal({ onClose, devMap }) {
  const toast = useToast();
  const suppressionsQ = useQuery(['alert-suppressions'], () => api.listAlertSuppressions({ active: false }), { invalidateOn: ['alerts'] });
  const checksQ = useQuery(['suppression-checks'], () => api.listChecks(), { invalidateOn: ['checks'] });
  const policiesQ = useQuery(['suppression-policies'], () => api.listPolicies(), { invalidateOn: ['policies'] });
  const clientsQ = useQuery(['suppression-clients'], () => api.listClients(), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['suppression-sites'], () => api.listSites(), { invalidateOn: ['org'] });
  const groupsQ = useQuery(['suppression-groups'], () => api.listGroups(), { invalidateOn: ['groups'] });
  const asLocalInput = (date) => new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  const [kind, setKind] = useState('maintenance');
  const [scopeType, setScopeType] = useState('agent');
  const [scopeID, setScopeID] = useState('');
  const [startsAt, setStartsAt] = useState('');
  const [endsAt, setEndsAt] = useState(asLocalInput(new Date(Date.now() + 60 * 60000)));
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const optionsByType = {
    agent: Object.values(devMap).map((x) => ({ value: x.id, label: x.hostname, group: t('sup.group.agent') })),
    check: (checksQ.data?.checks || []).map((x) => ({ value: x.id, label: x.name, group: t('sup.group.check') })),
    policy: (policiesQ.data?.policies || []).map((x) => ({ value: x.id, label: x.name, group: t('sup.group.policy') })),
    client: (clientsQ.data?.clients || []).map((x) => ({ value: x.id, label: x.name, group: t('sup.group.client') })),
    site: (sitesQ.data?.sites || []).map((x) => ({ value: x.id, label: x.name, group: t('sup.group.site') })),
    group: (groupsQ.data?.groups || []).map((x) => ({ value: x.id, label: x.name, group: t('sup.group.group') })),
  };
  const create = async () => {
    if (!scopeID || !endsAt) return;
    setBusy(true);
    try {
      await api.createAlertSuppression({ kind, scope_type: scopeType, scope_id: scopeID, starts_at: startsAt ? new Date(startsAt).toISOString() : undefined, ends_at: new Date(endsAt).toISOString(), reason });
      toast.success(kind === 'maintenance' ? t('sup.maintenanceScheduled') : t('sup.silenceCreated'));
      setReason(''); setStartsAt(''); setEndsAt(asLocalInput(new Date(Date.now() + 60 * 60000)));
    } catch (e) { toast.error(t('sup.createFailed'), e.message); } finally { setBusy(false); }
  };
  const cancel = async (row) => {
    try { await api.cancelAlertSuppression(row.id); toast.info(t('sup.canceled')); }
    catch (e) { toast.error(t('sup.cancelFailed'), e.message); }
  };
  const rows = suppressionsQ.data?.suppressions || [];
  return <Modal open onClose={onClose} width="max-w-3xl">
    <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div><div className="text-[15px] font-semibold text-ink">{t('alerts.silences')}</div><div className="text-[11.5px] text-muted mt-0.5">{t('sup.subtitle')}</div></div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
    <div className="p-5 max-h-[72vh] overflow-y-auto scroll-area space-y-5">
      <Card className="p-4 space-y-3">
        <div className="grid sm:grid-cols-2 gap-3"><Field label={t('sup.type')}><Select value={kind} onChange={(e) => setKind(e.target.value)}><option value="maintenance">{t('sup.kind.maintenance')}</option><option value="silence">{t('sup.kind.silence')}</option></Select></Field><Field label={t('sup.scope')}><Select value={scopeType} onChange={(e) => { setScopeType(e.target.value); setScopeID(''); }}>{['agent', 'check', 'policy', 'group', 'site', 'client'].map((s) => <option key={s} value={s}>{t('sup.scope.' + s)}</option>)}</Select></Field></div>
        <Field label={t('sup.target')}><Combobox value={scopeID} onChange={setScopeID} options={optionsByType[scopeType] || []} placeholder={t('sup.selectTarget')} /></Field>
        <div className="grid sm:grid-cols-2 gap-3"><Field label={t('sup.starts')} hint={t('sup.startsHint')}><Input type="datetime-local" value={startsAt} onChange={(e) => setStartsAt(e.target.value)} /></Field><Field label={t('sup.ends')}><Input type="datetime-local" value={endsAt} onChange={(e) => setEndsAt(e.target.value)} /></Field></div>
        <Field label={t('sup.reason')}><Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t('sup.reasonPlaceholder')} /></Field>
        <div className="flex justify-end"><Button variant="primary" loading={busy} disabled={!scopeID || !endsAt} onClick={create}>{t('action.create')}</Button></div>
      </Card>
      <div><div className="text-[12px] font-semibold text-muted mb-2">{t('sup.history')}</div><div className="space-y-2">
        {rows.map((row) => { const active = !row.canceled_at && new Date(row.ends_at) > new Date(); return <div key={row.id} className="rounded-lg border border-line bg-card px-3 py-2.5 flex items-center gap-3"><StatusPill status={active ? 'running' : row.canceled_at ? 'rejected' : 'resolved'} label={active ? tEnum('sup.kind', row.kind) : row.canceled_at ? t('sup.canceledBadge') : t('sup.ended')} size="sm" /><div className="min-w-0 flex-1"><div className="text-[12.5px] text-ink">{tEnum('sup.scope', row.scope_type)} · <span className="mono">{row.scope_id.slice(0, 10)}</span></div><div className="text-[11px] text-muted truncate">{fmtTime(row.starts_at)} → {fmtTime(row.ends_at)}{row.reason ? ` · ${row.reason}` : ''}</div></div>{active && <Button size="sm" variant="ghost" onClick={() => cancel(row)}>{t('action.cancel')}</Button>}</div>; })}
        {!suppressionsQ.loading && rows.length === 0 && <div className="text-[12px] text-faint py-4 text-center">{t('sup.none')}</div>}
      </div></div>
    </div>
  </Modal>;
}

/* ================================ CHECKS ================================== */
function ChecksScreen({ route } = {}) {
  const { can } = useAuth();
  const toast = useToast();
  const checksQ = useQuery(['checks'], () => api.listChecks(), { invalidateOn: ['checks'], poll: 8000 });
  const devMap = useDeviceMap();
  const policiesQ = useQuery(['pols-c'], () => can('policies.view') ? api.listPolicies() : Promise.resolve({ policies: [] }), { invalidateOn: ['policies'] });
  const [editing, setEditing] = useState(null); // check obj or 'new'
  const [detail, setDetail] = useState(null);
  const [confirmToggle, setConfirmToggle] = useState(null);
  // An AI proposal is switched on through its review, never a bare toggle.
  const [reviewCheck, setReviewCheck] = useState(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [showDefaults, setShowDefaults] = useState(false);
  const [focusOpened, setFocusOpened] = useState('');
  const defaultsQ = useQuery(['alert-defaults'], () => api.getAlertDefaults());

  useEffect(() => {
    if (!route?.focus || focusOpened === route.focus) return;
    const row = (checksQ.data?.checks || []).find((item) => item.id === route.focus);
    if (row) { setDetail(row); setFocusOpened(route.focus); }
  }, [route?.focus, checksQ.data, focusOpened]);

  const checks = checksQ.data?.checks || [];
  const polMap = {}; (policiesQ.data?.policies || []).forEach((p) => polMap[p.id] = p);
  const summary = { passing: 0, warning: 0, failing: 0, unknown: 0 };
  checks.forEach((c) => { if (c.enabled && !checkType(c.type).server) { const x = c.summary || {}; summary.passing += x.passing || 0; summary.warning += x.warning || 0; summary.failing += x.failing || 0; summary.unknown += x.unknown || 0; } });
  const toggle = async (c) => {
    try {
      // Where a check applies changes through its attachments, not this PUT.
      const { attachments, agent_id, policy_id, ...fields } = c;
      await api.updateCheck(c.id, { ...fields, enabled: !c.enabled });
      toast.success(c.enabled ? t('checks.disabled') : t('checks.enabled'));
    } catch (e) {
      toast.error(t('checks.updateFailed'), e.message);
    }
  };

  return (
      <Page wide>
        <PageHeader title={t('title.checks')} subtitle={t('checks.subtitle')}
        actions={<div className="flex gap-2"><Button variant="secondary" icon="settings" onClick={() => setShowDefaults(true)}>{t('rules.defaults')}</Button>{can('checks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>{t('checks.new')}</Button>}</div>} />
      <EditProposalsPanel kind="check" />

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4 max-w-3xl">
        <div className="rounded-xl border border-ok/25 bg-oksoft px-3.5 py-3"><div className="text-[11px] text-muted">{t('status.passing')}</div><div className="text-[22px] font-semibold text-ok tabular">{summary.passing}</div></div>
        <div className="rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3"><div className="text-[11px] text-muted">{t('status.warning')}</div><div className="text-[22px] font-semibold text-warn tabular">{summary.warning}</div></div>
        <div className="rounded-xl border border-bad/25 bg-badsoft px-3.5 py-3"><div className="text-[11px] text-muted">{t('status.failing')}</div><div className="text-[22px] font-semibold text-bad tabular">{summary.failing}</div></div>
        <div className="rounded-xl border border-line bg-elevated px-3.5 py-3"><div className="text-[11px] text-muted">{t('status.unknown')}</div><div className="text-[22px] font-semibold text-faint tabular">{summary.unknown}</div></div>
      </div>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">{t('devices.col.status')}</th><th className="font-semibold px-3 py-2.5">{t('checks.col.check')}</th>
            <th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('checks.col.target')}</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">{t('checks.col.thresholds')}</th>
            <th className="font-semibold px-3 py-2.5">{t('checks.col.interval')}</th><th className="font-semibold px-3 py-2.5">{t('checks.col.enabled')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('checks.col.latest')}</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {checks.map((c) => {
              const ct = checkType(c.type);
              return (
                <tr key={c.id} onClick={() => setDetail(c)} className="cursor-pointer hover:bg-elevated/40 transition">
                  <td className="px-4 py-3">{ct.server ? <span className="text-[11px] text-muted">{t('rules.server')}</span> : <StatusPill status={checkAggregateStatus(c)} size="sm" />}</td>
                  <td className="px-3 py-3"><div className="flex items-center gap-2"><Icon name={ct.icon} size={15} className="text-muted" /><div><div className="flex items-center gap-2"><span className="text-[13px] text-ink font-medium">{c.name}</span>{awaitingReview(c) && <ProposalBadge />}</div><div className="text-[11px] text-faint">{ct.label}{!c.enabled && ' ' + t('checks.disabledSuffix')}</div></div></div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted"><AppliesTo item={c} devMap={devMap} policies={policiesQ.data?.policies || []} /></td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{c.warn_threshold != null ? t('checks.thresholds', { warn: c.warn_threshold, err: c.err_threshold }) : '—'}</td>
                  <td className="px-3 py-3 text-[12px] text-muted">{ct.server ? t(c.config?.mode === 'ignore' ? 'rules.ignore' : 'rules.alert') : fmtInterval(c.interval_sec)}</td>
                  <td className="px-3 py-3" onClick={(e) => e.stopPropagation()}><Toggle checked={c.enabled} onChange={() => (!c.enabled && awaitingReview(c) ? setReviewCheck(c) : setConfirmToggle(c))} disabled={!can('checks.manage') || (c.config?.override_parent && !defaultsQ.data?.can_manage)} /></td>
                  <td className="px-4 py-3 text-right text-[12px] text-muted">{c.summary?.total ? t('checks.latest', { ok: c.summary.passing || 0, warn: c.summary.warning || 0, fail: c.summary.failing || 0 }) + (c.summary.unknown ? ' ' + t('checks.latestUnknown', { n: c.summary.unknown }) : '') : t('checks.noState')}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {checksQ.loading && <div className="p-4 space-y-2">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-8 w-full" />)}</div>}
        {!checksQ.loading && checks.length === 0 && <EmptyState icon="check" title={t('checks.none')} msg={t('checks.noneMsg')} />}
      </Card>

      <CheckDetail check={detail} onClose={() => setDetail(null)} devMap={devMap} canViewOverrides={!!defaultsQ.data?.can_manage} onEdit={(c) => { setDetail(null); setEditing(c); }} />
      {showDefaults && <AlertDefaultsEditor canEdit={!!defaultsQ.data?.can_manage} onClose={() => { setShowDefaults(false); defaultsQ.refetch(); }} />}
      {editing && <CheckEditor check={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} />}
      <ReviewModal open={!!reviewCheck} onClose={() => setReviewCheck(null)} busy={reviewBusy}
        title={reviewCheck && t('review.checkTitle', { name: reviewCheck.name })} intro={t('review.checkIntro')}
        confirmLabel={t('review.approve')}
        onConfirm={async () => { setReviewBusy(true); try { await toggle(reviewCheck); setReviewCheck(null); } finally { setReviewBusy(false); } }}>
        {reviewCheck && <CheckReview check={reviewCheck} target={devMap[reviewCheck.agent_id]?.hostname || polMap[reviewCheck.policy_id]?.name} />}
      </ReviewModal>
      <ConfirmDialog open={!!confirmToggle} onClose={() => setConfirmToggle(null)} onConfirm={() => toggle(confirmToggle)}
        title={confirmToggle?.enabled ? t('checks.disableTitle') : t('checks.enableTitle')}
        confirmLabel={confirmToggle?.enabled ? t('action.disable') : t('action.enable')} danger={!!confirmToggle?.enabled}
        body={confirmToggle && (confirmToggle.enabled
          ? t('checks.disableBody', { name: confirmToggle.name })
          : t('checks.enableBody', { name: confirmToggle.name }))} />
    </Page>
  );
}

function CheckDetail({ check, onClose, devMap, onEdit, canViewOverrides }) {
  const { can } = useAuth();
  const resultsQ = useQuery([check?.id, 'results'], () => check && !checkType(check.type).server ? api.getCheckResults(check.id) : Promise.resolve({ results: [] }), { invalidateOn: ['checks'] });
  const overridesQ = useQuery([check?.id, 'overrides'], () => check && checkType(check.type).server && canViewOverrides ? api.getCheckOverrides(check.id) : Promise.resolve(null), { invalidateOn: ['checks'] });
  if (!check) return null;
  const ct = checkType(check.type); const dev = devMap[check.agent_id];
  const results = resultsQ.data?.results || [];
  return (
    <Drawer open={!!check} onClose={onClose} width="max-w-[620px]">
      <div className="px-5 py-4 border-b border-line flex items-start gap-3">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted shrink-0"><Icon name={ct.icon} size={19} /></div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{check.name}</h2><span className="shrink-0"><StatusPill status={checkAggregateStatus(check)} size="sm" /></span></div>
          <div className="text-[12px] text-muted mt-0.5 truncate">{ct.label} · {dev?.hostname || t('checks.policyTarget')} · {ct.server ? t('rules.server') : t('checks.every', { interval: fmtInterval(check.interval_sec) })}</div>
        </div>
        {can('checks.manage') && (!check.config?.override_parent || canViewOverrides) && <Button size="sm" variant="secondary" icon="edit" onClick={() => onEdit(check)}>{t('action.edit')}</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        {ct.server ? <Card className="p-4 space-y-3">
          <ServerRuleFields type={check.type} value={check.config} disabled onChange={() => {}} />
          {overridesQ.data && <div className="space-y-2 text-[12px] text-muted"><p>{t('rules.coverage', { n: overridesQ.data.covered })}</p>{(overridesQ.data.overrides || []).map((item) => <p key={item.check_id || 'fleet'}>{t('rules.overridden', { n: item.devices, name: item.name || t('rules.defaults') })}</p>)}{!(overridesQ.data.overrides || []).length && <p>{t('rules.noOverrides')}</p>}</div>}
        </Card> : <>
        <Card className="overflow-hidden"><div className="px-4 py-3 border-b border-line text-[12.5px] font-semibold text-ink">{t('checks.instances')}</div><div className="divide-y divide-line">
          {(check.instances || []).map((x) => <div key={x.agent_id} className="px-4 py-2.5 flex items-center gap-3"><StatusPill status={x.status || 'unknown'} size="sm" /><span className="text-[12.5px] text-ink flex-1 truncate">{x.hostname}</span><span className="text-[11.5px] text-muted mono truncate max-w-[240px]">{x.value != null ? `${x.value}${ct.unit}` : x.output || t('checks.noResult')}</span><span className="text-[10.5px] text-faint">{timeAgo(x.last_result_at)}</span></div>)}
          {!(check.instances || []).length && <div className="px-4 py-5 text-center text-[12px] text-faint">{t('checks.noTargets')}</div>}
        </div></Card>
        <Card className="p-4">
          <div className="flex items-center justify-between mb-3"><span className="text-[12.5px] font-semibold text-ink">{t('checks.history', { n: results.length })}</span>
            {check.warn_threshold != null && <span className="text-[11.5px] text-faint mono">{t('checks.thresholds', { warn: check.warn_threshold, err: check.err_threshold })} {ct.unit}</span>}
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
          <div className="text-[12px] font-semibold text-muted mb-2">{t('checks.recent')}</div>
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
        </>}
      </div>
    </Drawer>
  );
}

function CheckEditor({ check, onClose, devMap, policies, defaultPolicyId }) {
  const toast = useToast();
  const { can } = useAuth();
  const [type, setType] = useState(check?.type || 'diskspace');
  const [name, setName] = useState(check?.name || '');
  // Where it applies: kept as the editor opened, so saving sends only the change.
  const [initialAtts] = useState(() => (check ? attachmentTargets(check.attachments) : defaultPolicyId ? [{ policy_id: defaultPolicyId }] : []));
  const [atts, setAtts] = useState(initialAtts);
  const overrideAccessQ = useQuery(['rule-override-access'], () => api.getAlertDefaults());
  const overridePolicyIds = atts.map((a) => a.policy_id).filter(Boolean).sort();
  const overridePolicyKey = overridePolicyIds.join(',');
  const overrideTargetsQ = useQuery([overridePolicyKey, 'rule-override-targets'], async () => {
    const results = await Promise.all(overridePolicyIds.map((id) => api.listAssignments(id)));
    return { policy_key: overridePolicyKey, assignments: results.flatMap((result) => result.assignments || []) };
  }, { invalidateOn: ['policies'] });
  const overrideAssignments = overrideTargetsQ.data?.assignments;
  const canManageOverrides = !!overrideAccessQ.data?.can_manage;
  const savedOverride = !!check?.config?.override_parent;
  const targetsChanged = initialAtts.length !== atts.length ||
    initialAtts.some((initial) => !atts.some((draft) => attachmentKey(initial) === attachmentKey(draft)));
  const overrideTargetsLoaded = overrideTargetsQ.data?.policy_key === overridePolicyKey && Array.isArray(overrideAssignments);
  const eligibleOverrideTargets = atts.length > 0 && atts.every((a) => !!a.policy_id) &&
    overrideTargetsLoaded && overrideAssignments.every((a) => a.scope_type === 'client' || a.scope_type === 'site');
  // Existing checks save the rule before writing its attachment changes.
  const saveTargetsFirst = !!check && !savedOverride && targetsChanged && eligibleOverrideTargets;
  const canOverride = canManageOverrides && eligibleOverrideTargets && !saveTargetsFirst;
  const overrideLocked = savedOverride && !canManageOverrides;
  const initialType = check?.type || 'diskspace';
  const [ruleCfg, setRuleCfg] = useState(() => serverRuleConfig(initialType, check?.config));
  const [cfgVal, setCfgVal] = useState(check?.config?.[checkType(initialType).cfg] || '');
  const [serviceState, setServiceState] = useState(check?.config?.state || 'running');
  const [dockerMode, setDockerMode] = useState(check?.config?.mode === 'healthy' ? 'healthy' : 'running');
  const [shell, setShell] = useState(check?.config?.shell || 'powershell');
  const [warn, setWarn] = useState(check?.warn_threshold ?? 20);
  const [err, setErr] = useState(check?.err_threshold ?? 10);
  const [interval, setInterval] = useState(check?.interval_sec ?? 300);
  const [failureCount, setFailureCount] = useState(check?.failure_count ?? 2);
  const [recoveryCount, setRecoveryCount] = useState(check?.recovery_count ?? 2);
  const [hysteresis, setHysteresis] = useState(check?.hysteresis ?? 2);
  const [cooldown, setCooldown] = useState(check?.cooldown_sec ?? 300);
  const [timeout, setTimeoutSec] = useState(check?.timeout_sec ?? 180);
  const [timeoutPolicy, setTimeoutPolicy] = useState(check?.timeout_policy || 'count');
  const [enabled, setEnabled] = useState(check?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const ct = checkType(type);
  const overrideSaveBlocked = ct.server && !!ruleCfg.override_parent && !canOverride;

  const save = async () => {
    if (overrideLocked || overrideSaveBlocked) return;
    setBusy(true);
    const config = ct.server ? serverRuleConfig(type, ruleCfg) : {}; if (ct.cfg) config[ct.cfg] = cfgVal; if (type === 'script') config.shell = shell; if (type === 'macservice') config.state = serviceState; if (type === 'dockercontainer') config.mode = dockerMode;
    const body = { type, name: name || ct.label, config, interval_sec: Number(interval), enabled,
      failure_count: Number(failureCount), recovery_count: Number(recoveryCount), hysteresis: Number(hysteresis), cooldown_sec: Number(cooldown), timeout_sec: Number(timeout), timeout_policy: timeoutPolicy };
    if (!ct.server && ct.dir !== 'state' && ct.dir !== 'script') { body.warn_threshold = Number(warn); body.err_threshold = Number(err); }
    try {
      if (check) { await api.updateCheck(check.id, body); await syncAttachments('check', check.id, initialAtts, atts); }
      else await api.createCheck({ ...body, attachments: atts });
      toast.success(check ? t('checks.updated') : t('checks.created')); onClose();
    }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  // Asked in a console dialog, not the browser's confirm over the tab.
  const [confirmDel, setConfirmDel] = useState(false);
  const del = () => setConfirmDel(true);
  const doDelete = async () => { try { await api.deleteCheck(check.id); toast.info(t('checks.deleted')); setConfirmDel(false); onClose(); } catch (e) { toast.error(t('common.failed'), e.message); } };

  return (
    <>
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{check ? t('checks.editTitle') : t('checks.new')}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <fieldset disabled={overrideLocked} className="p-5 space-y-4 max-h-[70vh] overflow-y-auto scroll-area">
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('checks.type')}><Select value={type} onChange={(e) => { const next = e.target.value; setType(next); setDockerMode(check?.type === next && check?.config?.mode === 'healthy' ? 'healthy' : 'running'); setServiceState(check?.type === next ? check?.config?.state || 'running' : 'running'); setRuleCfg(serverRuleConfig(next, check?.type === next ? check.config : undefined)); setCfgVal(check?.type === next ? check?.config?.[checkType(next).cfg] || '' : ''); if (next === 'cpuload' && Number(err) <= Number(warn)) { setWarn(80); setErr(90); } if ((next === 'diskspace' || next === 'memory') && Number(err) >= Number(warn)) { setWarn(20); setErr(10); } }}>{Object.keys(CHECK_TYPES).map((k) => <option key={k} value={k}>{checkType(k).label}</option>)}</Select></Field>
          <Field label={t('checks.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={ct.label} /></Field>
        </div>
        <PolicyAttachmentActions kind="check" item={check} policyId={defaultPolicyId} onDone={onClose} />
        <Field label={t('checks.applyTo')}><AttachmentsField value={atts} onChange={setAtts} devMap={devMap} policies={policies} /></Field>
        {ct.server && <ServerRuleFields type={type} value={ruleCfg} onChange={setRuleCfg} disabled={overrideLocked} />}
        {ct.server && canManageOverrides && (canOverride || (overrideTargetsLoaded && ruleCfg.override_parent)) &&
          <label className="flex items-start gap-2.5"><Toggle checked={!!ruleCfg.override_parent} onChange={(next) => setRuleCfg((prev) => ({ ...prev, override_parent: next }))} /><span className="text-[13px] text-ink">{t('rules.overrideParent')}</span></label>}
        {ct.server && canManageOverrides && saveTargetsFirst && <p className="text-[12px] text-muted">{t('rules.overrideSaveTargetsFirst')}</p>}
        {overrideLocked && <p className="text-[12px] text-muted">{t('rules.overrideRestricted')}</p>}
        {type === 'script' && <Field label={t('deploy.shell')}><Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>}
        {ct.cfg && <Field label={ct.cfgLabel}>{ct.cfg === 'script' ? <CodeEditor value={cfgVal} onChange={setCfgVal} shell={shell} minHeight={120} /> : <Input value={cfgVal} onChange={(e) => setCfgVal(e.target.value)} placeholder={ct.cfgPh} className="mono" />}</Field>}
        {type === 'dockercontainer' && <Field label={t('docker.checkMode')}><Select value={dockerMode} onChange={(e) => setDockerMode(e.target.value)}><option value="running">{t('docker.checkRunning')}</option><option value="healthy">{t('docker.checkHealthy')}</option></Select></Field>}
        {type === 'macservice' && <Field label={t('check.service.state')}><Select value={serviceState} onChange={(e) => setServiceState(e.target.value)}><option value="running">{t('check.service.running')}</option><option value="loaded">{t('check.service.loaded')}</option></Select></Field>}
        {!ct.server && ct.dir !== 'state' && ct.dir !== 'script' && (
          <div className="grid grid-cols-3 gap-3">
            <Field label={t('checks.warn', { unit: ct.unit })}><Input type="number" value={warn} onChange={(e) => setWarn(e.target.value)} /></Field>
            <Field label={t('checks.error', { unit: ct.unit })}><Input type="number" value={err} onChange={(e) => setErr(e.target.value)} /></Field>
            <Field label={t('checks.intervalSec')}><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>
          </div>
        )}
        {(ct.dir === 'state' || ct.dir === 'script') && <Field label={t('checks.intervalSec')} className="w-40"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>}
        {!ct.server && <div className="rounded-xl border border-line bg-elevated/30 p-3 space-y-3"><div className="text-[12px] font-semibold text-ink">{t('checks.reliability')}</div>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3"><Field label={t('checks.failuresToAlert')}><Input type="number" min="1" max="100" value={failureCount} onChange={(e) => setFailureCount(e.target.value)} /></Field><Field label={t('checks.successesToRecover')}><Input type="number" min="1" max="100" value={recoveryCount} onChange={(e) => setRecoveryCount(e.target.value)} /></Field><Field label={t('checks.timeout')}><Input type="number" min="5" value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} /></Field><Field label={t('checks.timeouts')}><Select value={timeoutPolicy} onChange={(e) => setTimeoutPolicy(e.target.value)}><option value="count">{t('checks.timeoutCount')}</option><option value="ignore">{t('checks.timeoutIgnore')}</option></Select></Field><Field label={t('checks.cooldown')}><Input type="number" min="0" value={cooldown} onChange={(e) => setCooldown(e.target.value)} /></Field>{(ct.dir === 'low' || ct.dir === 'high') && <Field label={t('checks.hysteresis')}><Input type="number" min="0" max="100" step="0.1" value={hysteresis} onChange={(e) => setHysteresis(e.target.value)} /></Field>}</div>
        </div>}
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={(next) => { setEnabled(next); toast.info(next ? t('checks.willEnable') : t('checks.willDisable')); }} /><span className="text-[13px] text-ink">{t('checks.enabledLabel')}</span></label>
        <div className="flex items-start gap-2 rounded-lg border border-line bg-elevated/40 px-3 py-2 text-[11.5px] text-muted"><Icon name="info" size={14} className="mt-0.5 shrink-0 text-faint" />{ct.server ? t('rules.serverHelp') : type === 'dockercontainer' ? t('docker.checkHint') : type === 'linuxservice' ? t('check.service.linuxHint') : type === 'macservice' ? t('check.service.macHint') : ct.dir === 'low' ? t('checks.help.low') : ct.dir === 'high' ? t('checks.help.high') : type === 'ping' ? t('checks.help.ping') : ct.dir === 'state' ? t('checks.help.state') : t('checks.help.script')}</div>
      </fieldset>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{check && can('checks.manage') && !overrideLocked && <Button variant="danger" icon="trash" onClick={del}>{t('action.delete')}</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} onClick={save} disabled={overrideLocked || overrideSaveBlocked}>{check ? t('checks.save') : t('checks.create')}</Button></div>
    </Modal>
    <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={doDelete} danger title={t('checks.deletePrompt')} body="" confirmLabel={t('action.delete')} />
    </>
  );
}

// ---- where a check or task applies: any number of policies and devices ----

// attachmentKey identifies an attachment's target.
function attachmentKey(a) { return a.policy_id ? 'pol:' + a.policy_id : 'dev:' + a.agent_id; }

// attachmentTargets keeps only the target ids of an item's attachments, the
// shape the API takes.
function attachmentTargets(atts) { return (atts || []).map((a) => (a.policy_id ? { policy_id: a.policy_id } : { agent_id: a.agent_id })); }

// attachmentLabel names an attachment: the policy's name or the device's
// hostname, as the server sent it, else as this console knows it.
function attachmentLabel(a, devMap, policies) {
  if (a.policy_id) return a.name || policies.find((p) => p.id === a.policy_id)?.name || a.policy_id;
  return a.name || devMap[a.agent_id]?.hostname || a.agent_id;
}

// syncAttachments applies the difference between where an item applied when
// the editor opened and where it should apply now, one attachment at a time.
async function syncAttachments(kind, id, before, after) {
  const was = new Set(before.map(attachmentKey)), now = new Set(after.map(attachmentKey));
  for (const a of before) {
    if (now.has(attachmentKey(a))) continue;
    const target = a.policy_id ? ['policy', a.policy_id] : ['device', a.agent_id];
    await (kind === 'check' ? api.detachCheck(id, ...target) : api.detachTask(id, ...target));
  }
  for (const a of after) {
    if (was.has(attachmentKey(a))) continue;
    await (kind === 'check' ? api.attachCheck(id, a) : api.attachTask(id, a));
  }
}

// AttachmentsField edits where a check or task applies: each policy or device
// is a removable chip, and the searchable picker adds more.
function AttachmentsField({ value, onChange, devMap, policies, disabled }) {
  const keys = new Set(value.map(attachmentKey));
  const options = [
    ...Object.values(devMap).filter((d) => !keys.has('dev:' + d.id)).map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: t('sup.group.agent') })),
    ...policies.filter((p) => !keys.has('pol:' + p.id)).map((p) => ({ value: 'pol:' + p.id, label: t('checks.policySuffix', { name: p.name }), group: t('sup.group.policy') })),
  ];
  const add = (v) => { if (v) onChange([...value, v.startsWith('pol:') ? { policy_id: v.slice(4) } : { agent_id: v.slice(4) }]); };
  return (
    <div className="space-y-2" data-attachments="">
      {value.length > 0 && <div className="flex flex-wrap gap-1.5">{value.map((a) => { const label = attachmentLabel(a, devMap, policies); return (
        <span key={attachmentKey(a)} className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-elevated px-2 py-1 text-[12px] text-ink">
          <Icon name={a.policy_id ? 'policy' : 'devices'} size={12} className={a.policy_id ? 'text-brandhi' : 'text-faint'} />
          <span className="truncate max-w-[200px]">{label}</span>
          {!disabled && <button type="button" aria-label={t('action.remove') + ': ' + label} onClick={() => onChange(value.filter((x) => attachmentKey(x) !== attachmentKey(a)))} className="text-faint hover:text-ink"><Icon name="close" size={12} /></button>}
        </span>); })}</div>}
      {!disabled && <Combobox value="" onChange={add} placeholder={t('attach.add')} options={options} />}
      {value.length === 0 && <p className="text-[11.5px] text-faint">{t('attach.noneHint')}</p>}
      {value.length > 1 && <p className="text-[11.5px] text-muted flex items-center gap-1.5"><Icon name="info" size={13} className="text-faint" />{t('attach.sharedNote', { n: value.length })}</p>}
    </div>
  );
}

// AppliesTo is the list cell: the one place an item applies, the first of
// several with how many more, or Not in use.
function AppliesTo({ item, devMap, policies }) {
  const atts = item.attachments || [];
  if (!atts.length) return <span className="text-faint">{t('attach.notInUse')}</span>;
  const first = atts[0];
  const dev = first.agent_id && devMap[first.agent_id];
  const label = attachmentLabel(first, devMap, policies);
  const all = atts.map((a) => attachmentLabel(a, devMap, policies)).join(', ');
  return (
    <span className="flex items-center gap-1.5" title={atts.length > 1 ? all : undefined}>
      {first.policy_id ? <Icon name="policy" size={13} className="text-brandhi" /> : dev ? <OsIcon os={dev.os} size={13} /> : <Icon name="devices" size={13} className="text-faint" />}
      <span className={cx('truncate max-w-[180px]', first.policy_id && 'text-brandhi')}>{label}</span>
      {atts.length > 1 && <span className="rounded-md border border-line bg-elevated px-1.5 text-[10.5px] text-muted">{t('attach.more', { n: atts.length - 1 })}</span>}
    </span>
  );
}

// PolicyAttachmentActions are what an editor opened from inside a policy
// offers for an item that policy shares with other places: take it out of this
// policy only, or give this policy a copy of its own to change.
function PolicyAttachmentActions({ kind, item, policyId, onDone }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const atts = item?.attachments || [];
  if (!policyId || atts.length < 2 || !atts.some((a) => a.policy_id === policyId)) return null;
  const run = async (fn, message) => {
    setBusy(true);
    try { await fn(); toast.success(message); onDone(); } catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-xl border border-line bg-elevated/40 px-3 py-2.5" data-policy-actions="">
      <span className="text-[12px] text-muted flex-1 min-w-[180px]">{t('attach.policyShared', { n: atts.length - 1 })}</span>
      <Button size="sm" variant="secondary" icon="copy" disabled={busy}
        onClick={() => run(() => (kind === 'check' ? api.copyCheck(item.id, { policy_id: policyId }) : api.copyTask(item.id, { policy_id: policyId })), t('attach.copied'))}>{t('attach.copyForPolicy')}</Button>
      <Button size="sm" variant="ghost" icon="close" disabled={busy}
        onClick={() => run(() => (kind === 'check' ? api.detachCheck(item.id, 'policy', policyId) : api.detachTask(item.id, 'policy', policyId)), t('attach.removed'))}>{t('attach.removeFromPolicy')}</Button>
    </div>
  );
}

// ---- edits the AI proposed ----

// EDIT_MANAGE is the permission deciding an edit of each kind takes, the one
// an edit by hand takes; the server checks it again.
const EDIT_MANAGE = { script: 'scripts.manage', check: 'checks.manage', task: 'tasks.manage', policy: 'policies.manage', patch_policy: 'patches.policies.manage' };
// EDIT_REFRESH is what to reload once an edit of each kind is applied.
const EDIT_REFRESH = { script: 'scripts', check: 'checks', task: 'tasks', policy: 'policies', patch_policy: 'patch-policies' };

// EditProposalsPanel lists the edits the assistant proposed to this screen's
// kind of object that wait for review, and opens each one's review. Nothing in
// them has happened.
function EditProposalsPanel({ kind }) {
  const q = useQuery(['edit-proposals', kind], () => api.listEditProposals({ kind, status: 'pending' }), { invalidateOn: ['edit-proposals', EDIT_REFRESH[kind]] });
  const [open, setOpen] = useState(null);
  const list = q.data?.proposals || [];
  if (!list.length) return null;
  return (
    <div data-edit-proposals={kind} className="mb-4 rounded-xl border border-brand/25 bg-brandsoft px-3.5 py-3 space-y-2">
      <div className="flex items-center gap-2 text-[12.5px] text-ink font-medium"><Icon name="sparkle" size={14} className="text-brandhi" />{t('edits.pending', { n: list.length })}</div>
      {list.map((p) => (
        <div key={p.id} className="flex items-center gap-3 rounded-lg bg-card border border-line px-3 py-2">
          <div className="flex-1 min-w-0"><div className="text-[13px] text-ink truncate">{p.target_name || p.target_id}</div><div className="text-[11.5px] text-faint truncate">{p.reason}</div></div>
          <Button size="sm" variant="secondary" onClick={() => setOpen(p.id)}>{t('edits.review')}</Button>
        </div>
      ))}
      {open && <EditProposalReview id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// editValue shows one field's value: text as it is, anything else as JSON.
function editValue(v) {
  if (v === null || v === undefined) return '—';
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2);
}

// editDiff lists what a proposal changes between the object now and after:
// the fields that differ, and the attachments, assignments and exclusions it
// adds or removes.
function editDiff(current, proposed) {
  const fields = [];
  const keys = new Set([...Object.keys(current?.fields || {}), ...Object.keys(proposed?.fields || {})]);
  keys.forEach((k) => {
    const before = current?.fields?.[k], after = proposed?.fields?.[k];
    if (JSON.stringify(before) !== JSON.stringify(after)) fields.push({ key: k, before, after });
  });
  const listDiff = (a, b, key) => {
    const was = new Set((a || []).map(key)), now = new Set((b || []).map(key));
    return { added: (b || []).filter((x) => !was.has(key(x))), removed: (a || []).filter((x) => !now.has(key(x))) };
  };
  const asg = (x) => [x.scope_type, x.scope_id || '', x.os || '', x.role || ''].join('|');
  return {
    fields,
    attachments: listDiff(current?.attachments, proposed?.attachments, (x) => x),
    assignments: listDiff(current?.assignments, proposed?.assignments, asg),
    exclusions: listDiff(current?.exclusions, proposed?.exclusions, (x) => x),
  };
}

// EditProposalReview shows one proposal: why, who, the object now against
// what the proposal would leave, and Apply or Reject for whoever could make the
// edit by hand. A proposal whose object changed since cannot be applied.
function EditProposalReview({ id, onClose }) {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery(['edit-proposal', id], () => api.getEditProposal(id), {});
  const [busy, setBusy] = useState(false);
  const d = q.data;
  const p = d?.proposal;
  const names = d?.names || {};
  const label = (key) => names[key] || key.slice(key.indexOf(':') + 1);
  const asgLabel = (a) => (a.scope_type === 'global' ? t('pol.scope.global') : t('sup.group.' + (a.scope_type === 'group' ? 'group' : a.scope_type)) + ': ' + label(a.scope_type + ':' + a.scope_id))
    + (a.os ? ' · ' + a.os : '') + (a.role ? ' · ' + a.role : '');
  const diff = d && !d.gone ? editDiff(d.current, d.proposed) : null;
  const canDecide = p && can(EDIT_MANAGE[p.kind]) && p.status === 'pending';
  const decide = async (apply) => {
    setBusy(true);
    try {
      if (apply) await api.applyEditProposal(id); else await api.rejectEditProposal(id);
      toast.success(apply ? t('edits.applied') : t('edits.rejected'));
      window.dataBus.emit('edit-proposals'); window.dataBus.emit(EDIT_REFRESH[p.kind]);
      onClose();
    } catch (e) { toast.error(t('common.failed'), e.message); window.dataBus.emit('edit-proposals'); }
    finally { setBusy(false); }
  };
  const changeList = (title, items, render) => (items.length > 0 && (
    <div className="space-y-1"><div className="text-[11px] uppercase tracking-wide text-faint">{title}</div>
      <div className="flex flex-wrap gap-1.5">{items.map((x, i) => <span key={i} className="rounded-md border border-line bg-elevated px-2 py-0.5 text-[12px] text-ink">{render(x)}</span>)}</div></div>));
  return (
    <Modal open onClose={onClose} width="max-w-3xl">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between">
        <div className="text-[15px] font-semibold text-ink">{t('edits.title', { name: p?.target_name || p?.target_id || '' })}</div>
        <button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button>
      </div>
      <div className="p-5 space-y-4 max-h-[70vh] overflow-y-auto scroll-area" data-edit-review="">
        {p && <div className="rounded-xl border border-line bg-elevated/40 px-3.5 py-3 text-[12.5px] text-ink space-y-1">
          <div>{p.reason}</div>
          <div className="text-[11.5px] text-faint">{t('edits.by', { who: p.proposed_by, when: timeAgo(p.created_at) })}</div>
        </div>}
        {d?.gone && <div className="text-[12.5px] text-bad">{t('edits.gone')}</div>}
        {d?.stale && <div className="flex items-start gap-2 rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3 text-[12.5px] text-ink"><Icon name="info" size={15} className="text-warn mt-0.5 shrink-0" />{t('edits.stale')}</div>}
        {d?.pending_target && <div className="flex items-start gap-2 rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3 text-[12.5px] text-ink" data-edit-blocked="pending"><Icon name="info" size={15} className="text-warn mt-0.5 shrink-0" />{t('edits.pendingTarget')}</div>}
        {diff && diff.fields.map((f) => (
          <div key={f.key} className="space-y-1.5" data-edit-field={f.key}>
            <div className="text-[12px] font-semibold text-ink mono">{f.key}</div>
            <div className="grid md:grid-cols-2 gap-2">
              <div><div className="text-[10.5px] uppercase tracking-wide text-faint mb-1">{t('edits.before')}</div><CodeBlock code={editValue(f.before)} shell="text" maxHeight={220} showCopy={false} wrap /></div>
              <div><div className="text-[10.5px] uppercase tracking-wide text-faint mb-1">{t('edits.after')}</div><CodeBlock code={editValue(f.after)} shell="text" maxHeight={220} showCopy={false} wrap /></div>
            </div>
          </div>
        ))}
        {diff && <>
          {changeList(t('edits.attachAdded'), diff.attachments.added, label)}
          {changeList(t('edits.attachRemoved'), diff.attachments.removed, label)}
          {changeList(t('edits.assignAdded'), diff.assignments.added, asgLabel)}
          {changeList(t('edits.assignRemoved'), diff.assignments.removed, asgLabel)}
          {changeList(t('edits.excluded'), diff.exclusions.added, (x) => label('agent:' + x))}
          {changeList(t('edits.included'), diff.exclusions.removed, (x) => label('agent:' + x))}
        </>}
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">
        <div className="flex-1 text-[11.5px] text-faint">{t('edits.applyHint')}</div>
        <Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button>
        {canDecide && <Button variant="ghost" icon="close" disabled={busy} onClick={() => decide(false)}>{t('edits.reject')}</Button>}
        {canDecide && <Button variant="primary" icon="approve" loading={busy} disabled={!!d?.stale || !!d?.gone || !!d?.pending_target} onClick={() => decide(true)}>{t('edits.apply')}</Button>}
      </div>
    </Modal>
  );
}

/* ============================ SCHEDULED TASKS ============================= */
// awaitingReview reports an AI proposal nobody has released yet. Such a row is
// inert — the scheduler selects WHERE enabled — and enabling it is the review.
function awaitingReview(row) {
  return !!row?.proposed_by && !row?.approved_by;
}

function ProposalBadge() {
  return (
    <span title={t('proposal.hint')}
      className="inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-wide bg-brand/15 text-brandhi">
      <Icon name="sparkle" size={11} />{t('proposal.badge')}
    </span>
  );
}

// ReviewModal is the confirmation an AI proposal goes through before it takes
// effect: what it does is on screen, and nothing changes until the person
// confirms. Policies, patch policies, tasks, checks, library scripts and script
// variables all use it, so every review reads the same way. error shows why the
// confirmation was refused (for a policy, that it changed under the review).
function ReviewModal({ open, title, intro, children, confirmLabel, onConfirm, onClose, busy, error }) {
  if (!open) return null;
  return (
    <Modal open onClose={busy ? () => {} : onClose} width="max-w-2xl">
      <div role="dialog" aria-label={title} className="p-5 flex flex-col gap-4 max-h-[85vh]">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-xl flex items-center justify-center shrink-0 bg-brandsoft text-brandhi"><Icon name="shield" size={18} /></div>
          <div className="flex-1 min-w-0">
            <div className="text-[15px] font-semibold text-ink break-words">{title}</div>
            {intro && <div className="text-[13px] text-muted mt-1 leading-relaxed">{intro}</div>}
          </div>
        </div>
        <div className="overflow-y-auto scroll-area space-y-4 min-h-0 pr-1">{children}</div>
        {error && <div role="alert" className="rounded-xl border border-bad/30 bg-badsoft p-3 text-[12.5px] text-bad">{error}</div>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={busy}>{t('action.cancel')}</Button>
          <Button variant="primary" icon="check" loading={busy} onClick={onConfirm}>{confirmLabel}</Button>
        </div>
      </div>
    </Modal>
  );
}

function ReviewSection({ title, children }) {
  return (
    <section>
      <div className="text-[11px] uppercase tracking-wide text-faint font-semibold mb-1.5">{title}</div>
      {children}
    </section>
  );
}

function ReviewRow({ label, children }) {
  return (
    <div className="flex gap-3 text-[12.5px] py-1">
      <div className="w-36 shrink-0 text-faint">{label}</div>
      <div className="flex-1 min-w-0 text-ink break-words">{children}</div>
    </div>
  );
}

// Policy targeting (RFC #258): an assignment can be narrowed to one OS and/or
// one role of its scope; empty is any. TargetSelects edits the pair,
// TargetChips shows it next to an assignment.
const TARGET_OSES = ['windows', 'macos', 'linux'];
const DEVICE_ROLES = ['workstation', 'server'];

function TargetSelects({ os, role, onOs, onRole, disabled }) {
  return (
    <>
      <Select aria-label={t('target.osLabel')} value={os} onChange={(e) => onOs(e.target.value)} disabled={disabled} className="w-auto">
        <option value="">{t('target.anyOS')}</option>
        {TARGET_OSES.map((o) => <option key={o} value={o}>{t('target.os.' + o)}</option>)}
      </Select>
      <Select aria-label={t('target.roleLabel')} value={role} onChange={(e) => onRole(e.target.value)} disabled={disabled} className="w-auto">
        <option value="">{t('target.anyRole')}</option>
        {DEVICE_ROLES.map((r) => <option key={r} value={r}>{t('target.role.' + r)}</option>)}
      </Select>
    </>
  );
}

function TargetChips({ os, role }) {
  if (!os && !role) return null;
  return (
    <span className="inline-flex flex-wrap gap-1">
      {os && <span className="rounded-md border border-line2 px-1.5 py-0.5 text-[10.5px] text-muted">{t('target.os.' + os)}</span>}
      {role && <span className="rounded-md border border-line2 px-1.5 py-0.5 text-[10.5px] text-muted">{t('target.role.' + role)}</span>}
    </span>
  );
}

// TaskReview shows what approving a task switches on: where and when it runs,
// and the script itself, in full.
function TaskReview({ task, target, scriptName }) {
  return (
    <div className="space-y-2">
      <ReviewRow label={t('review.target')}>{target || '—'}</ReviewRow>
      <ReviewRow label={t('review.schedule')}>{taskSchedule(task)}</ReviewRow>
      {task.proposed_by && <ReviewRow label={t('review.proposedByLabel')}>{task.proposed_by}</ReviewRow>}
      {task.script_id
        ? <ReviewRow label={t('review.script')}>{t('review.libraryScript', { name: scriptName || task.script_id })}</ReviewRow>
        : <CodeBlock code={task.script || ''} shell={task.shell} maxHeight={260} label={task.shell} />}
    </div>
  );
}

// CheckReview shows what approving a check or server rule switches on.
function CheckReview({ check, target }) {
  const ct = checkType(check.type);
  const config = check.config && Object.keys(check.config).length ? JSON.stringify(check.config, null, 2) : '';
  return (
    <div className="space-y-2">
      <ReviewRow label={t('review.type')}>{ct.label}</ReviewRow>
      <ReviewRow label={t('review.target')}>{target || '—'}</ReviewRow>
      {check.proposed_by && <ReviewRow label={t('review.proposedByLabel')}>{check.proposed_by}</ReviewRow>}
      {check.warn_threshold != null && <ReviewRow label={t('review.thresholds')}>{t('checks.thresholds', { warn: check.warn_threshold, err: check.err_threshold })}</ReviewRow>}
      {!ct.server && <ReviewRow label={t('review.interval')}>{fmtInterval(check.interval_sec)}</ReviewRow>}
      {ct.server && check.config?.override_parent && <ReviewRow label={t('rules.overrideParent')}>{t('rules.overrideHelp')}</ReviewRow>}
      {!ct.server && <ReviewRow label={t('checks.timeouts')}>{t(check.timeout_policy === 'ignore' ? 'checks.timeoutIgnore' : 'checks.timeoutCount')}</ReviewRow>}
      {config && <CodeBlock code={config} shell="json" maxHeight={200} label={t('review.config')} />}
    </div>
  );
}

function TasksScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const tasksQ = useQuery(['tasks'], () => api.listTasks(), { invalidateOn: ['tasks'] });
  const devMap = useDeviceMap();
  const policiesQ = useQuery(['pols-t'], () => can('policies.view') ? api.listPolicies() : Promise.resolve({ policies: [] }), { invalidateOn: ['policies'] });
  const scriptsQ = useQuery(['scripts-t'], () => can('scripts.view') ? api.listScripts() : Promise.resolve({ scripts: [] }), { invalidateOn: ['scripts'] });
  const [editing, setEditing] = useState(null);
  const [confirmToggleTask, setConfirmToggleTask] = useState(null);
  // An AI proposal is switched on through its review, never a bare toggle.
  const [reviewTask, setReviewTask] = useState(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const tasks = tasksQ.data?.tasks || [];
  const polMap = {}; (policiesQ.data?.policies || []).forEach((p) => polMap[p.id] = p);
  const scriptMap = {}; (scriptsQ.data?.scripts || []).forEach((s) => scriptMap[s.id] = s);

  const toggle = async (task) => {
    // Task updates use PUT, so the server validates the entire schedule. Keep
    // the current task fields when changing just the enabled state. Where it
    // applies changes through its attachments, so those stay out.
    try {
      const { attachments, agent_id, policy_id, ...fields } = task;
      await api.updateTask(task.id, { ...fields, enabled: !task.enabled });
      toast.success(task.enabled ? t('tasks.disabled') : t('tasks.enabled'));
    } catch (e) {
      toast.error(t('tasks.updateFailed'), e.message);
    }
  };
  const [runningTask, setRunningTask] = useState(null); // { task, count } pending confirm
  const askRun = async (task) => {
    try { const { count } = await api.taskTargets(task.id); setRunningTask({ task, count }); }
    catch (e) { toast.error(t('tasks.targetsFailed'), e.message); }
  };
  const doRun = async (task) => {
    try { const { queued } = await api.runTask(task.id); toast.success(t('tasks.queued'), t('tasks.runningOn', { agents: tn('tasks.agents', queued) })); }
    catch (e) { toast.error(t('tasks.runFailed'), e.message); }
  };

  return (
      <Page wide>
        <PageHeader title={t('title.tasks')} subtitle={t('tasks.subtitle')}
        actions={can('tasks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>{t('tasks.new')}</Button>} />
      <EditProposalsPanel kind="task" />
      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">{t('tasks.col.task')}</th><th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('checks.col.target')}</th>
            <th className="font-semibold px-3 py-2.5">{t('tasks.col.schedule')}</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">{t('tasks.col.script')}</th>
            <th className="font-semibold px-3 py-2.5">{t('checks.col.enabled')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('alerts.col.actions')}</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {tasks.map((task) => {
              const sc = scriptMap[task.script_id];
              return (
                <tr key={task.id} className="hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><div className="flex items-center gap-2"><Icon name="tasks" size={15} className="text-muted" /><span className="text-[13px] text-ink font-medium">{task.name}</span>{awaitingReview(task) && <ProposalBadge />}</div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted"><AppliesTo item={task} devMap={devMap} policies={policiesQ.data?.policies || []} /></td>
                  <td className="px-3 py-3 text-[12.5px] text-muted flex items-center gap-1.5"><Icon name="clock" size={13} className="text-faint" />{taskSchedule(task)}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[12px] text-faint">{sc?.name || t('tasks.inline')}</td>
                  <td className="px-3 py-3"><Toggle checked={task.enabled} onChange={() => (!task.enabled && awaitingReview(task) ? setReviewTask(task) : setConfirmToggleTask(task))} disabled={!can('tasks.manage')} /></td>
                  <td className="px-4 py-3 text-right">{can('tasks.manage') && <div className="flex items-center justify-end gap-1">{!awaitingReview(task) && <Button size="sm" variant="ghost" icon="play" onClick={() => askRun(task)}>{t('action.runNow')}</Button>}<Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing(task)}>{t('action.edit')}</Button></div>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!tasksQ.loading && tasks.length === 0 && <EmptyState icon="tasks" title={t('tasks.none')} msg={t('tasks.noneMsg')} />}
      </Card>
      {editing && <TaskEditor task={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} scripts={scriptsQ.data?.scripts || []} />}
      <ReviewModal open={!!reviewTask} onClose={() => setReviewTask(null)} busy={reviewBusy}
        title={reviewTask && t('review.taskTitle', { name: reviewTask.name })} intro={t('review.taskIntro')}
        confirmLabel={t('review.approve')}
        onConfirm={async () => { setReviewBusy(true); try { await toggle(reviewTask); setReviewTask(null); } finally { setReviewBusy(false); } }}>
        {reviewTask && <TaskReview task={reviewTask} target={devMap[reviewTask.agent_id]?.hostname || polMap[reviewTask.policy_id]?.name} scriptName={scriptMap[reviewTask.script_id]?.name} />}
      </ReviewModal>
      <ConfirmDialog open={!!confirmToggleTask} onClose={() => setConfirmToggleTask(null)} onConfirm={() => toggle(confirmToggleTask)}
        title={confirmToggleTask?.enabled ? t('tasks.disableTitle') : t('tasks.enableTitle')}
        confirmLabel={confirmToggleTask?.enabled ? t('action.disable') : t('action.enable')} danger={!!confirmToggleTask?.enabled}
        body={confirmToggleTask && (confirmToggleTask.enabled
          ? t('tasks.disableBody', { name: confirmToggleTask.name })
          : t('tasks.enableBody', { name: confirmToggleTask.name }))} />
      <ConfirmDialog open={!!runningTask} onClose={() => setRunningTask(null)} onConfirm={() => doRun(runningTask.task)}
        title={t('tasks.runTitle')} confirmLabel={t('action.runNow')}
        body={runningTask && (runningTask.count === 0
          ? t('tasks.noTargets')
          : t('tasks.runBody', { name: runningTask.task.name, agents: tn('tasks.agents', runningTask.count) }))} />
    </Page>
  );
}

// defaultPolicyId pre-selects the target when the editor is opened from inside a
// policy, where "which policy" is not a question the operator should be re-asked.
function TaskEditor({ task, onClose, devMap, policies, scripts, defaultPolicyId }) {
  const toast = useToast();
  const { can } = useAuth();
  const [name, setName] = useState(task?.name || '');
  const [initialAtts] = useState(() => (task ? attachmentTargets(task.attachments) : defaultPolicyId ? [{ policy_id: defaultPolicyId }] : []));
  const [atts, setAtts] = useState(initialAtts);
  const [scriptId, setScriptId] = useState(task?.script_id || '');
  const [kind, setKind] = useState(task?.schedule_kind || 'interval');
  const [intervalSec, setIntervalSec] = useState(task?.interval_sec || 3600);
  const [minute, setMinute] = useState(task?.run_at_minute || 180);
  const [daysOfWeek, setDaysOfWeek] = useState(task?.days_of_week || 0);
  const [times, setTimes] = useState(task?.run_at_minutes?.length ? task.run_at_minutes : [180]);
  // A new task starts in this browser's zone; an existing one keeps its own,
  // and one from before zones existed shows as the UTC it runs in.
  const [timezone, setTimezone] = useState(task ? scheduleZone(task.timezone) : browserTimezone());
  const [enabled, setEnabled] = useState(task?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (kind === 'weekly' && (daysOfWeek === 0 || times.length === 0)) { toast.error(t('tasks.pickWeekday')); return; }
    setBusy(true);
    const sc = scripts.find((s) => s.id === scriptId);
    const body = { name: name || t('tasks.defaultName'), script_id: scriptId || undefined, shell: sc?.shell || 'bash', schedule_kind: kind, interval_sec: Number(intervalSec), run_at_minute: Number(minute), days_of_week: kind === 'weekly' ? daysOfWeek : 0, run_at_minutes: kind === 'weekly' ? times : [], timezone, enabled };
    try {
      if (task) { await api.updateTask(task.id, body); await syncAttachments('task', task.id, initialAtts, atts); }
      else await api.createTask({ ...body, attachments: atts });
      toast.success(task ? t('tasks.updated') : t('tasks.created')); onClose();
    }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  // Asked in a console dialog, not the browser's confirm over the tab.
  const [confirmDel, setConfirmDel] = useState(false);
  const del = () => setConfirmDel(true);
  const doDelete = async () => { try { await api.deleteTask(task.id); toast.info(t('tasks.deleted')); setConfirmDel(false); onClose(); } catch (e) { toast.error(t('common.failed'), e.message); } };

  return (
    <>
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{task ? t('tasks.editTitle') : t('tasks.newTitle')}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <div className="p-5 space-y-4">
        <Field label={t('checks.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tasks.namePlaceholder')} /></Field>
        <PolicyAttachmentActions kind="task" item={task} policyId={defaultPolicyId} onDone={onClose} />
        <Field label={t('checks.applyTo')}><AttachmentsField value={atts} onChange={setAtts} devMap={devMap} policies={policies} /></Field>
        <Field label={t('tasks.col.script')}><Combobox value={scriptId} onChange={setScriptId} placeholder={t('tasks.chooseScript')}
          options={scripts.map((s) => ({ value: s.id, label: s.name }))} /></Field>
        <Field label={t('tasks.schedule')}>
          <div className="flex gap-1 p-0.5 bg-app rounded-lg border border-line2 w-fit mb-2">
            {['interval', 'daily', 'weekly'].map((v) => [v, t('tasks.kind.' + v)]).map(([v, l]) => <button key={v} onClick={() => setKind(v)} className={cx('h-7 px-3 rounded-md text-[12px] font-medium transition', kind === v ? 'bg-elevated text-ink' : 'text-faint hover:text-muted')}>{l}</button>)}
          </div>
          {kind === 'interval' && (
            <div className="flex items-center gap-2"><span className="text-[12.5px] text-muted">{t('tasks.everyLabel')}</span><Input type="number" value={Math.round(intervalSec / 60)} onChange={(e) => setIntervalSec(Number(e.target.value) * 60)} className="w-24" /><span className="text-[12.5px] text-muted">{t('tasks.minutes')}</span></div>
          )}
          {kind === 'daily' && (
            <div className="flex items-center gap-2"><span className="text-[12.5px] text-muted">{t('tasks.at')}</span><Input type="time" value={minuteToHHMM(minute)} onChange={(e) => { const [h, m] = e.target.value.split(':').map(Number); setMinute(h * 60 + m); }} className="w-32" /></div>
          )}
          {kind === 'weekly' && (
            <div className="space-y-3">
              <div className="flex gap-1">
                {WEEKDAY_BITS.map((i) => {
                  const on = (daysOfWeek & (1 << i)) !== 0;
                  return <button key={i} type="button" onClick={() => setDaysOfWeek(daysOfWeek ^ (1 << i))}
                    className={cx('h-8 min-w-10 px-1.5 rounded-md text-[11.5px] font-medium transition', on ? 'bg-brand text-white' : 'bg-app border border-line2 text-muted hover:text-ink')}>{weekdayShort(i)}</button>;
                })}
              </div>
              <div className="space-y-2">
                {times.map((m, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <span className="text-[12.5px] text-muted">{t('tasks.at')}</span>
                    <Input type="time" value={minuteToHHMM(m)} onChange={(e) => { const [h, mm] = e.target.value.split(':').map(Number); const next = times.slice(); next[idx] = h * 60 + mm; setTimes(next); }} className="w-32" />
                    {times.length > 1 && <button type="button" onClick={() => setTimes(times.filter((_, i) => i !== idx))} className="text-faint hover:text-bad p-1"><Icon name="trash" size={15} /></button>}
                  </div>
                ))}
                <Button variant="ghost" size="sm" icon="plus" onClick={() => setTimes([...times, 540])}>{t('tasks.addTime')}</Button>
              </div>
            </div>
          )}
        </Field>
        {kind !== 'interval' && <Field label={t('schedule.timezone')} hint={t('schedule.timezoneHint')}><TimezoneSelect value={timezone} onChange={setTimezone} /></Field>}
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={setEnabled} /><span className="text-[13px] text-ink">{t('checks.enabledLabel')}</span></label>
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{task && can('tasks.manage') && <Button variant="danger" icon="trash" onClick={del}>{t('action.delete')}</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} onClick={save}>{task ? t('tasks.save') : t('tasks.create')}</Button></div>
    </Modal>
    <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={doDelete} danger title={t('tasks.deletePrompt')} body="" confirmLabel={t('action.delete')} />
    </>
  );
}

window.AlertsScreen = AlertsScreen;
window.ChecksScreen = ChecksScreen;
window.TasksScreen = TasksScreen;
// Shared with the policies screen, which lives in another file (each source file
// is wrapped in its own block scope by the build).
window.awaitingReview = awaitingReview;
window.ProposalBadge = ProposalBadge;
window.ReviewModal = ReviewModal;
window.ReviewSection = ReviewSection;
window.ReviewRow = ReviewRow;
window.TaskReview = TaskReview;
window.TargetSelects = TargetSelects;
window.TargetChips = TargetChips;
window.DEVICE_ROLES = DEVICE_ROLES;
window.CheckReview = CheckReview;
window.CHECK_TYPES = CHECK_TYPES;
window.checkType = checkType;
window.SEV = SEV;
// The build gives each source file its own block scope, so the policy drawer in
// screens-rmm2.jsx cannot reach these without going through window. It edits the
// checks and tasks a policy owns with the same editors this screen uses, rather
// than a second, drifting copy of the same forms.
window.CheckEditor = CheckEditor;
window.EditProposalsPanel = EditProposalsPanel;
window.AppliesTo = AppliesTo;
// The device drawer (screens-devices.jsx) describes checks and tasks the same way.
window.checkType = checkType;
window.taskSchedule = taskSchedule;
window.TaskEditor = TaskEditor;
