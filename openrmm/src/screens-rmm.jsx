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
  script: { icon: 'script', unit: '', dir: 'script', cfg: 'script', cfgPh: 'exit 0' },
};

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
  if (task.schedule_kind === 'daily') return t('tasks.dailyAt', { time: minuteToHHMM(task.run_at_minute) });
  if (task.schedule_kind === 'weekly') {
    const days = WEEKDAY_BITS.filter((i) => (task.days_of_week & (1 << i)) !== 0).map(weekdayShort).join(' ');
    const at = (task.run_at_minutes || []).map(minuteToHHMM).join(', ');
    return t('tasks.weeklyAt', { days: days || '—', times: at || '—' });
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
  const act = async (action, a) => {
    try {
      if (action === 'ack') await api.ackAlert(a.id, actionBody(a));
      if (action === 'resolve') {
        const note = window.prompt(t('alerts.resolveNote'), '') ?? null;
        if (note === null) return;
        await api.resolveAlert(a.id, actionBody(a, { note }));
      }
      if (action === 'suppress') {
        const raw = window.prompt(t('alerts.suppressMinutes'), '60');
        if (raw === null) return;
        const minutes = Number(raw);
        if (!Number.isFinite(minutes) || minutes <= 0) throw new Error(t('alerts.positiveMinutes'));
        const note = window.prompt(t('alerts.suppressReason'), '') ?? '';
        await api.suppressAlert(a.id, actionBody(a, { note, silence_until: new Date(Date.now() + minutes * 60000).toISOString() }));
      }
      toast.success(action === 'ack' ? t('alerts.acknowledged') : action === 'resolve' ? t('alerts.resolved') : t('alerts.suppressed'));
      setDetailID(null);
    } catch (e) { toast.error(t('alerts.actionFailed'), e.message); }
  };
  const bulk = async (action) => {
    if (!visibleSelected.length) return;
    const body = { ids: visibleSelected, action, expected_versions: {} };
    alerts.filter((a) => visibleSelected.includes(a.id)).forEach((a) => body.expected_versions[a.id] = a.version);
    if (action === 'resolve') body.note = window.prompt(t('alerts.bulkResolveNote'), '') ?? '';
    if (action === 'suppress') {
      const raw = window.prompt(t('alerts.bulkSuppressMinutes'), '60');
      if (raw === null) return;
      const minutes = Number(raw);
      if (!Number.isFinite(minutes) || minutes <= 0) return toast.error(t('alerts.invalidDuration'));
      body.silence_until = new Date(Date.now() + minutes * 60000).toISOString();
      body.note = window.prompt(t('alerts.suppressReason'), '') ?? '';
    }
    try {
      const out = await api.bulkAlertAction(body);
      setSelected([]);
      if ((out.errors || []).length) toast.info(t('alerts.bulkPartial'), t('alerts.bulkPartialMsg', { updated: out.alerts.length, conflicts: out.errors.length }));
      else toast.success(t('alerts.bulkUpdated', { n: out.alerts.length }));
    } catch (e) { toast.error(t('alerts.bulkFailed'), e.message); }
  };

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
        <Select value={sourceF} onChange={(e) => resetPage(setSourceF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">{t('alerts.anySource')}</option><option value="check">{t('alerts.source.check')}</option><option value="agent">{t('alerts.source.agent')}</option><option value="task">{t('alerts.source.task')}</option><option value="system">{t('alerts.source.system')}</option></Select>
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
      <AlertDetail alertID={detailID} onClose={() => setDetailID(null)} canManage={can('alerts.manage')} onAction={act} />
      {showSuppressions && <AlertSuppressionsModal onClose={() => setShowSuppressions(false)} devMap={devMap} />}
    </Page>
  );
}

function AlertDetail({ alertID, onClose, canManage, onAction }) {
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
  const summary = { passing: 0, warning: 0, failing: 0, unknown: 0 };
  checks.forEach((c) => { if (c.enabled) { const x = c.summary || {}; summary.passing += x.passing || 0; summary.warning += x.warning || 0; summary.failing += x.failing || 0; summary.unknown += x.unknown || 0; } });
  const toggle = async (c) => {
    try {
      await api.updateCheck(c.id, { ...c, enabled: !c.enabled });
      toast.success(c.enabled ? t('checks.disabled') : t('checks.enabled'));
    } catch (e) {
      toast.error(t('checks.updateFailed'), e.message);
    }
  };

  return (
      <Page wide>
        <PageHeader title={t('title.checks')} subtitle={t('checks.subtitle')}
        actions={can('checks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>{t('checks.new')}</Button>} />

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
              const ct = checkType(c.type); const dev = devMap[c.agent_id]; const pol = polMap[c.policy_id];
              return (
                <tr key={c.id} onClick={() => setDetail(c)} className="cursor-pointer hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><StatusPill status={checkAggregateStatus(c)} size="sm" /></td>
                  <td className="px-3 py-3"><div className="flex items-center gap-2"><Icon name={ct.icon} size={15} className="text-muted" /><div><div className="flex items-center gap-2"><span className="text-[13px] text-ink font-medium">{c.name}</span>{awaitingReview(c) && <ProposalBadge />}</div><div className="text-[11px] text-faint">{ct.label}{!c.enabled && ' ' + t('checks.disabledSuffix')}</div></div></div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : pol ? <span className="flex items-center gap-1.5 text-brandhi"><Icon name="policy" size={13} />{pol.name}</span> : '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{c.warn_threshold != null ? t('checks.thresholds', { warn: c.warn_threshold, err: c.err_threshold }) : '—'}</td>
                  <td className="px-3 py-3 text-[12px] text-muted">{fmtInterval(c.interval_sec)}</td>
                  <td className="px-3 py-3" onClick={(e) => e.stopPropagation()}><Toggle checked={c.enabled} onChange={() => setConfirmToggle(c)} disabled={!can('checks.manage')} /></td>
                  <td className="px-4 py-3 text-right text-[12px] text-muted">{c.summary?.total ? t('checks.latest', { ok: c.summary.passing || 0, warn: c.summary.warning || 0, fail: c.summary.failing || 0 }) + (c.summary.unknown ? ' ' + t('checks.latestUnknown', { n: c.summary.unknown }) : '') : t('checks.noState')}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {checksQ.loading && <div className="p-4 space-y-2">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-8 w-full" />)}</div>}
        {!checksQ.loading && checks.length === 0 && <EmptyState icon="check" title={t('checks.none')} msg={t('checks.noneMsg')} />}
      </Card>

      <CheckDetail check={detail} onClose={() => setDetail(null)} devMap={devMap} onEdit={(c) => { setDetail(null); setEditing(c); }} />
      {editing && <CheckEditor check={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} />}
      <ConfirmDialog open={!!confirmToggle} onClose={() => setConfirmToggle(null)} onConfirm={() => toggle(confirmToggle)}
        title={confirmToggle?.enabled ? t('checks.disableTitle') : t('checks.enableTitle')}
        confirmLabel={confirmToggle?.enabled ? t('action.disable') : t('action.enable')} danger={!!confirmToggle?.enabled}
        body={confirmToggle && (confirmToggle.enabled
          ? t('checks.disableBody', { name: confirmToggle.name })
          : t('checks.enableBody', { name: confirmToggle.name }))} />
    </Page>
  );
}

function CheckDetail({ check, onClose, devMap, onEdit }) {
  const { can } = useAuth();
  const resultsQ = useQuery([check?.id, 'results'], () => check ? api.getCheckResults(check.id) : Promise.resolve({ results: [] }), { invalidateOn: ['checks'] });
  if (!check) return null;
  const ct = checkType(check.type); const dev = devMap[check.agent_id];
  const results = resultsQ.data?.results || [];
  return (
    <Drawer open={!!check} onClose={onClose} width="max-w-[620px]">
      <div className="px-5 py-4 border-b border-line flex items-start gap-3">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted shrink-0"><Icon name={ct.icon} size={19} /></div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{check.name}</h2><span className="shrink-0"><StatusPill status={checkAggregateStatus(check)} size="sm" /></span></div>
          <div className="text-[12px] text-muted mt-0.5 truncate">{ct.label} · {dev?.hostname || t('checks.policyTarget')} · {t('checks.every', { interval: fmtInterval(check.interval_sec) })}</div>
        </div>
        {can('checks.manage') && <Button size="sm" variant="secondary" icon="edit" onClick={() => onEdit(check)}>{t('action.edit')}</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
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
      </div>
    </Drawer>
  );
}

function CheckEditor({ check, onClose, devMap, policies, defaultPolicyId }) {
  const toast = useToast();
  const { can } = useAuth();
  const [type, setType] = useState(check?.type || 'diskspace');
  const [name, setName] = useState(check?.name || '');
  const [target, setTarget] = useState(check?.policy_id ? 'pol:' + check.policy_id : check?.agent_id ? 'dev:' + check.agent_id : defaultPolicyId ? 'pol:' + defaultPolicyId : '');
  const initialType = check?.type || 'diskspace';
  const [cfgVal, setCfgVal] = useState(check?.config?.[checkType(initialType).cfg] || '');
  const [shell, setShell] = useState(check?.config?.shell || 'powershell');
  const [warn, setWarn] = useState(check?.warn_threshold ?? 20);
  const [err, setErr] = useState(check?.err_threshold ?? 10);
  const [interval, setInterval] = useState(check?.interval_sec ?? 300);
  const [failureCount, setFailureCount] = useState(check?.failure_count ?? 2);
  const [recoveryCount, setRecoveryCount] = useState(check?.recovery_count ?? 2);
  const [hysteresis, setHysteresis] = useState(check?.hysteresis ?? 2);
  const [cooldown, setCooldown] = useState(check?.cooldown_sec ?? 300);
  const [timeout, setTimeoutSec] = useState(check?.timeout_sec ?? 180);
  const [enabled, setEnabled] = useState(check?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const ct = checkType(type);
  const devices = Object.values(devMap);

  const save = async () => {
    setBusy(true);
    const config = {}; if (ct.cfg) config[ct.cfg] = cfgVal; if (type === 'script') config.shell = shell;
    const body = { type, name: name || ct.label, config, interval_sec: Number(interval), enabled,
      failure_count: Number(failureCount), recovery_count: Number(recoveryCount), hysteresis: Number(hysteresis), cooldown_sec: Number(cooldown), timeout_sec: Number(timeout) };
    if (ct.dir !== 'state' && ct.dir !== 'script') { body.warn_threshold = Number(warn); body.err_threshold = Number(err); }
    if (target.startsWith('dev:')) body.agent_id = target.slice(4); else if (target.startsWith('pol:')) body.policy_id = target.slice(4);
    try { if (check) await api.updateCheck(check.id, body); else await api.createCheck(body); toast.success(check ? t('checks.updated') : t('checks.created')); onClose(); }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  const del = async () => { if (!window.confirm(t('checks.deletePrompt'))) return; try { await api.deleteCheck(check.id); toast.info(t('checks.deleted')); onClose(); } catch (e) { toast.error(t('common.failed'), e.message); } };

  return (
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{check ? t('checks.editTitle') : t('checks.new')}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <div className="p-5 space-y-4 max-h-[70vh] overflow-y-auto scroll-area">
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('checks.type')}><Select value={type} onChange={(e) => { const next = e.target.value; setType(next); setCfgVal(check?.type === next ? check?.config?.[checkType(next).cfg] || '' : ''); if (next === 'cpuload' && Number(err) <= Number(warn)) { setWarn(80); setErr(90); } if ((next === 'diskspace' || next === 'memory') && Number(err) >= Number(warn)) { setWarn(20); setErr(10); } }}>{Object.keys(CHECK_TYPES).map((k) => <option key={k} value={k}>{checkType(k).label}</option>)}</Select></Field>
          <Field label={t('checks.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={ct.label} /></Field>
        </div>
        <Field label={t('checks.applyTo')}><Combobox value={target} onChange={setTarget} placeholder={t('checks.selectTarget')}
          options={[
            ...devices.map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: t('sup.group.agent') })),
            ...policies.map((p) => ({ value: 'pol:' + p.id, label: t('checks.policySuffix', { name: p.name }), group: t('sup.group.policy') })),
          ]} /></Field>
        {type === 'script' && <Field label={t('deploy.shell')}><Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>}
        {ct.cfg && <Field label={ct.cfgLabel}>{ct.cfg === 'script' ? <CodeEditor value={cfgVal} onChange={setCfgVal} shell={shell} minHeight={120} /> : <Input value={cfgVal} onChange={(e) => setCfgVal(e.target.value)} placeholder={ct.cfgPh} className="mono" />}</Field>}
        {ct.dir !== 'state' && ct.dir !== 'script' && (
          <div className="grid grid-cols-3 gap-3">
            <Field label={t('checks.warn', { unit: ct.unit })}><Input type="number" value={warn} onChange={(e) => setWarn(e.target.value)} /></Field>
            <Field label={t('checks.error', { unit: ct.unit })}><Input type="number" value={err} onChange={(e) => setErr(e.target.value)} /></Field>
            <Field label={t('checks.intervalSec')}><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>
          </div>
        )}
        {(ct.dir === 'state' || ct.dir === 'script') && <Field label={t('checks.intervalSec')} className="w-40"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>}
        <div className="rounded-xl border border-line bg-elevated/30 p-3 space-y-3"><div className="text-[12px] font-semibold text-ink">{t('checks.reliability')}</div>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3"><Field label={t('checks.failuresToAlert')}><Input type="number" min="1" max="100" value={failureCount} onChange={(e) => setFailureCount(e.target.value)} /></Field><Field label={t('checks.successesToRecover')}><Input type="number" min="1" max="100" value={recoveryCount} onChange={(e) => setRecoveryCount(e.target.value)} /></Field><Field label={t('checks.timeout')}><Input type="number" min="5" value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} /></Field><Field label={t('checks.cooldown')}><Input type="number" min="0" value={cooldown} onChange={(e) => setCooldown(e.target.value)} /></Field>{(ct.dir === 'low' || ct.dir === 'high') && <Field label={t('checks.hysteresis')}><Input type="number" min="0" max="100" step="0.1" value={hysteresis} onChange={(e) => setHysteresis(e.target.value)} /></Field>}</div>
        </div>
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={(next) => { setEnabled(next); toast.info(next ? t('checks.willEnable') : t('checks.willDisable')); }} /><span className="text-[13px] text-ink">{t('checks.enabledLabel')}</span></label>
        <div className="flex items-start gap-2 rounded-lg border border-line bg-elevated/40 px-3 py-2 text-[11.5px] text-muted"><Icon name="info" size={14} className="mt-0.5 shrink-0 text-faint" />{ct.dir === 'low' ? t('checks.help.low') : ct.dir === 'high' ? t('checks.help.high') : type === 'ping' ? t('checks.help.ping') : ct.dir === 'state' ? t('checks.help.state') : t('checks.help.script')}</div>
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{check && can('checks.manage') && <Button variant="danger" icon="trash" onClick={del}>{t('action.delete')}</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} onClick={save} disabled={!target}>{check ? t('checks.save') : t('checks.create')}</Button></div>
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
      className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-wide bg-brand/15 text-brandhi">
      <Icon name="sparkle" size={11} />{t('proposal.badge')}
    </span>
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
  const tasks = tasksQ.data?.tasks || [];
  const polMap = {}; (policiesQ.data?.policies || []).forEach((p) => polMap[p.id] = p);
  const scriptMap = {}; (scriptsQ.data?.scripts || []).forEach((s) => scriptMap[s.id] = s);

  const toggle = async (task) => {
    // Task updates use PUT, so the server validates the entire schedule. Keep
    // the current task fields when changing just the enabled state.
    try {
      await api.updateTask(task.id, { ...task, enabled: !task.enabled });
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
      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">{t('tasks.col.task')}</th><th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('checks.col.target')}</th>
            <th className="font-semibold px-3 py-2.5">{t('tasks.col.schedule')}</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">{t('tasks.col.script')}</th>
            <th className="font-semibold px-3 py-2.5">{t('checks.col.enabled')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('alerts.col.actions')}</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {tasks.map((task) => {
              const dev = devMap[task.agent_id]; const pol = polMap[task.policy_id]; const sc = scriptMap[task.script_id];
              return (
                <tr key={task.id} className="hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><div className="flex items-center gap-2"><Icon name="tasks" size={15} className="text-muted" /><span className="text-[13px] text-ink font-medium">{task.name}</span>{awaitingReview(task) && <ProposalBadge />}</div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : pol ? <span className="flex items-center gap-1.5 text-brandhi"><Icon name="policy" size={13} />{pol.name}</span> : '—'}</td>
                  <td className="px-3 py-3 text-[12.5px] text-muted flex items-center gap-1.5"><Icon name="clock" size={13} className="text-faint" />{taskSchedule(task)}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[12px] text-faint">{sc?.name || t('tasks.inline')}</td>
                  <td className="px-3 py-3"><Toggle checked={task.enabled} onChange={() => setConfirmToggleTask(task)} disabled={!can('tasks.manage')} /></td>
                  <td className="px-4 py-3 text-right">{can('tasks.manage') && <div className="flex items-center justify-end gap-1"><Button size="sm" variant="ghost" icon="play" onClick={() => askRun(task)}>{t('action.runNow')}</Button><Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing(task)}>{t('action.edit')}</Button></div>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!tasksQ.loading && tasks.length === 0 && <EmptyState icon="tasks" title={t('tasks.none')} msg={t('tasks.noneMsg')} />}
      </Card>
      {editing && <TaskEditor task={editing === 'new' ? null : editing} onClose={() => setEditing(null)} devMap={devMap} policies={policiesQ.data?.policies || []} scripts={scriptsQ.data?.scripts || []} />}
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
  const [target, setTarget] = useState(task?.policy_id ? 'pol:' + task.policy_id : task?.agent_id ? 'dev:' + task.agent_id : defaultPolicyId ? 'pol:' + defaultPolicyId : '');
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
    if (kind === 'weekly' && (daysOfWeek === 0 || times.length === 0)) { toast.error(t('tasks.pickWeekday')); return; }
    setBusy(true);
    const sc = scripts.find((s) => s.id === scriptId);
    const body = { name: name || t('tasks.defaultName'), script_id: scriptId || undefined, shell: sc?.shell || 'bash', schedule_kind: kind, interval_sec: Number(intervalSec), run_at_minute: Number(minute), days_of_week: kind === 'weekly' ? daysOfWeek : 0, run_at_minutes: kind === 'weekly' ? times : [], enabled };
    if (target.startsWith('dev:')) body.agent_id = target.slice(4); else if (target.startsWith('pol:')) body.policy_id = target.slice(4);
    try { if (task) await api.updateTask(task.id, body); else await api.createTask(body); toast.success(task ? t('tasks.updated') : t('tasks.created')); onClose(); }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  const del = async () => { if (!window.confirm(t('tasks.deletePrompt'))) return; try { await api.deleteTask(task.id); toast.info(t('tasks.deleted')); onClose(); } catch (e) { toast.error(t('common.failed'), e.message); } };

  return (
    <Modal open onClose={onClose} width="max-w-lg">
      <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div className="text-[15px] font-semibold text-ink">{task ? t('tasks.editTitle') : t('tasks.newTitle')}</div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
      <div className="p-5 space-y-4">
        <Field label={t('checks.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tasks.namePlaceholder')} /></Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('checks.col.target')}><Combobox value={target} onChange={setTarget} placeholder={t('tasks.select')}
            options={[
              ...devices.map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: t('sup.group.agent') })),
              ...policies.map((p) => ({ value: 'pol:' + p.id, label: p.name, group: t('sup.group.policy') })),
            ]} /></Field>
          <Field label={t('tasks.col.script')}><Combobox value={scriptId} onChange={setScriptId} placeholder={t('tasks.chooseScript')}
            options={scripts.map((s) => ({ value: s.id, label: s.name }))} /></Field>
        </div>
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
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={setEnabled} /><span className="text-[13px] text-ink">{t('checks.enabledLabel')}</span></label>
      </div>
      <div className="px-5 py-3.5 border-t border-line flex items-center gap-2">{task && can('tasks.manage') && <Button variant="danger" icon="trash" onClick={del}>{t('action.delete')}</Button>}<div className="flex-1" /><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} onClick={save} disabled={!target}>{task ? t('tasks.save') : t('tasks.create')}</Button></div>
    </Modal>
  );
}

window.AlertsScreen = AlertsScreen;
window.ChecksScreen = ChecksScreen;
window.TasksScreen = TasksScreen;
// Shared with the policies screen, which lives in another file (each source file
// is wrapped in its own block scope by the build).
window.awaitingReview = awaitingReview;
window.ProposalBadge = ProposalBadge;
window.CHECK_TYPES = CHECK_TYPES;
window.checkType = checkType;
window.SEV = SEV;
// The build gives each source file its own block scope, so the policy drawer in
// screens-rmm2.jsx cannot reach these without going through window. It edits the
// checks and tasks a policy owns with the same editors this screen uses, rather
// than a second, drifting copy of the same forms.
window.CheckEditor = CheckEditor;
// The device drawer (screens-devices.jsx) describes checks and tasks the same way.
window.checkType = checkType;
window.taskSchedule = taskSchedule;
window.TaskEditor = TaskEditor;
