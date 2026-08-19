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
  ping: { label: 'Ping', icon: 'network', unit: '', dir: 'state', cfg: 'host', cfgLabel: 'Host / IP', cfgPh: '10.0.0.1' },
  winservice: { label: 'Windows service', icon: 'settings', unit: '', dir: 'state', cfg: 'service', cfgLabel: 'Service name', cfgPh: 'Spooler' },
  script: { label: 'Script check', icon: 'script', unit: '', dir: 'script', cfg: 'script', cfgLabel: 'Inline script', cfgPh: 'exit 0' },
};

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
        const note = window.prompt('Resolution note (optional):', '') ?? null;
        if (note === null) return;
        await api.resolveAlert(a.id, actionBody(a, { note }));
      }
      if (action === 'suppress') {
        const raw = window.prompt('Suppress for how many minutes?', '60');
        if (raw === null) return;
        const minutes = Number(raw);
        if (!Number.isFinite(minutes) || minutes <= 0) throw new Error('Enter a positive number of minutes.');
        const note = window.prompt('Reason for suppression:', '') ?? '';
        await api.suppressAlert(a.id, actionBody(a, { note, silence_until: new Date(Date.now() + minutes * 60000).toISOString() }));
      }
      toast.success(action === 'ack' ? 'Alert acknowledged' : action === 'resolve' ? 'Alert resolved' : 'Alert suppressed');
      setDetailID(null);
    } catch (e) { toast.error('Alert action failed', e.message); }
  };
  const bulk = async (action) => {
    if (!visibleSelected.length) return;
    const body = { ids: visibleSelected, action, expected_versions: {} };
    alerts.filter((a) => visibleSelected.includes(a.id)).forEach((a) => body.expected_versions[a.id] = a.version);
    if (action === 'resolve') body.note = window.prompt('Resolution note for selected alerts (optional):', '') ?? '';
    if (action === 'suppress') {
      const raw = window.prompt('Suppress selected alerts for how many minutes?', '60');
      if (raw === null) return;
      const minutes = Number(raw);
      if (!Number.isFinite(minutes) || minutes <= 0) return toast.error('Invalid duration');
      body.silence_until = new Date(Date.now() + minutes * 60000).toISOString();
      body.note = window.prompt('Reason for suppression:', '') ?? '';
    }
    try {
      const out = await api.bulkAlertAction(body);
      setSelected([]);
      if ((out.errors || []).length) toast.info('Bulk action partially completed', `${out.alerts.length} updated; ${out.errors.length} changed concurrently.`);
      else toast.success(`${out.alerts.length} alerts updated`);
    } catch (e) { toast.error('Bulk action failed', e.message); }
  };

  return (
    <Page wide>
      <PageHeader title="Alerts" subtitle="Incident state, occurrences and recovery history across the fleet."
        actions={can('alerts.manage') && <Button variant="secondary" icon="clock" onClick={() => setShowSuppressions(true)}>Silences & maintenance</Button>} />

      <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 mb-4">
        <StatTile label="New" value={counts.new} icon="alert" tone={counts.new ? 'bad' : 'default'} highlight={counts.new > 0} onClick={() => resetPage(setStatusF, 'new')} />
        <StatTile label="Acknowledged" value={counts.acked} icon="eye" tone="warn" onClick={() => resetPage(setStatusF, 'acked')} />
        <StatTile label="Suppressed" value={counts.suppressed} icon="clock" tone="run" onClick={() => resetPage(setStatusF, 'suppressed')} />
        <StatTile label="Resolved" value={counts.resolved} icon="approve" tone="ok" onClick={() => resetPage(setStatusF, 'resolved')} />
        <StatTile label="Total" value={counts.total} icon="list" onClick={() => resetPage(setStatusF, 'all')} />
      </div>

      <Card className="mb-4"><div className="flex flex-wrap items-center gap-2 p-2.5">
        <Input value={search} onChange={(e) => resetPage(setSearch, e.target.value)} placeholder="Search message, device or output…" className="min-w-[240px] flex-1" />
        <Select value={statusF} onChange={(e) => resetPage(setStatusF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">Any status</option><option value="open">Open</option><option value="new">New</option><option value="acked">Acknowledged</option><option value="suppressed">Suppressed</option><option value="resolved">Resolved</option></Select>
        <Select value={sevF} onChange={(e) => resetPage(setSevF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">Any severity</option><option value="error">Error</option><option value="warning">Warning</option><option value="info">Info</option></Select>
        <Select value={sourceF} onChange={(e) => resetPage(setSourceF, e.target.value)} className="w-auto min-w-[140px]"><option value="all">Any source</option><option value="check">Check</option><option value="agent">Connectivity</option><option value="task">Task</option><option value="system">System</option></Select>
      </div></Card>

      {visibleSelected.length > 0 && can('alerts.manage') && <div className="mb-3 flex items-center gap-2 rounded-xl border border-brand/25 bg-brand/5 px-3 py-2">
        <span className="text-[12.5px] text-ink mr-auto">{visibleSelected.length} selected</span>
        <Button size="sm" variant="subtle" onClick={() => bulk('ack')}>Acknowledge</Button>
        <Button size="sm" variant="subtle" onClick={() => bulk('suppress')}>Suppress</Button>
        <Button size="sm" variant="secondary" icon="approve" onClick={() => bulk('resolve')}>Resolve</Button>
      </div>}

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            {can('alerts.manage') && <th className="px-4 py-2.5 w-8"><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? [] : alerts.map((a) => a.id))} /></th>}
            <th className="font-semibold px-3 py-2.5">Severity</th><th className="font-semibold px-3 py-2.5">Message</th>
            <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Device</th><th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Source</th>
            <th className="font-semibold px-3 py-2.5">Status</th><th className="font-semibold px-3 py-2.5 text-right">Last seen</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {alerts.map((a) => {
              const s = SEV[a.severity] || SEV.warning; const dev = devMap[a.agent_snapshot_id || a.agent_id];
              return (
                <tr key={a.id} onClick={() => setDetailID(a.id)} className={cx('cursor-pointer hover:bg-elevated/40 transition align-middle', a.status === 'resolved' && 'opacity-60')}>
                  {can('alerts.manage') && <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}><input type="checkbox" checked={checked(a.id)} onChange={() => toggleSelected(a.id)} /></td>}
                  <td className="px-3 py-3"><span className={cx('inline-flex items-center gap-1.5 text-[12px] font-medium', `text-${s.c}`)}><Icon name={s.icon} size={14} />{s.label}</span></td>
                  <td className="px-3 py-3 text-[13px] text-ink max-w-[380px]"><div className="truncate">{a.message}</div>{a.occurrence_count > 1 && <div className="text-[10.5px] text-faint mt-0.5">{a.occurrence_count} observations</div>}</td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : a.agent_hostname || '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{a.source_type}{a.source_id ? '·' + a.source_id.slice(0, 10) : ''}</td>
                  <td className="px-3 py-3"><StatusPill status={a.status} size="sm" /></td>
                  <td className="px-3 py-3 text-right text-[12px] text-muted tabular whitespace-nowrap">{timeAgo(a.last_seen_at || a.created_at)}</td>
                  <td className="px-4 py-3 text-right" onClick={(e) => e.stopPropagation()}>
                    {a.status !== 'resolved' && can('alerts.manage') ? (
                      <div className="flex items-center justify-end gap-1.5">
                        {a.status === 'new' && <Button size="sm" variant="subtle" onClick={() => act('ack', a)}>Ack</Button>}
                        {a.status !== 'suppressed' && <Button size="sm" variant="subtle" onClick={() => act('suppress', a)}>Mute</Button>}
                        <Button size="sm" variant="secondary" icon="approve" onClick={() => act('resolve', a)}>Resolve</Button>
                      </div>
                    ) : a.status === 'resolved' ? <span className="text-[11.5px] text-faint">{timeAgo(a.resolved_at)}</span> : <span className="text-[11px] text-faint">view only</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {alertsQ.loading && <div className="p-4 space-y-2">{Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>}
        {!alertsQ.loading && alerts.length === 0 && <EmptyState icon="approve" title="No alerts" msg="Nothing matches these server-side filters." />}
        {(cursors.length > 1 || alertsQ.data?.next_cursor) && <div className="border-t border-line px-4 py-3 flex justify-end gap-2">
          <Button size="sm" variant="ghost" disabled={cursors.length === 1} onClick={() => { setCursors((xs) => xs.slice(0, -1)); setSelected([]); }}>Previous</Button>
          <Button size="sm" variant="secondary" disabled={!alertsQ.data?.next_cursor} onClick={() => { setCursors((xs) => [...xs, alertsQ.data.next_cursor]); setSelected([]); }}>Next</Button>
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
      <div className="flex-1 min-w-0"><div className="flex items-center gap-2"><h2 className="text-[16px] font-semibold text-ink">Alert detail</h2>{a && <StatusPill status={a.status} size="sm" />}</div><div className="text-[12px] text-muted mt-1 truncate">{a?.agent_hostname || 'Loading…'}</div></div>
      <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
    </div>
    <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
      {q.loading && <Skeleton className="h-40 w-full" />}
      {a && <>
        <Card className="p-4 space-y-3">
          <div className="text-[14px] text-ink font-medium">{a.message}</div>
          <div className="grid grid-cols-2 gap-3 text-[12px]"><div><span className="text-faint">First seen</span><div className="text-muted mt-0.5">{fmtTime(a.first_seen_at)}</div></div><div><span className="text-faint">Last seen</span><div className="text-muted mt-0.5">{fmtTime(a.last_seen_at)}</div></div><div><span className="text-faint">Occurrences</span><div className="text-muted mt-0.5">{a.occurrence_count}</div></div><div><span className="text-faint">Source</span><div className="text-muted mono mt-0.5">{a.source_type}</div></div></div>
          {(a.observed_value != null || a.threshold_value != null) && <div className="text-[12px] mono text-muted">Observed {a.observed_value ?? '—'} · threshold {a.threshold_value ?? '—'}</div>}
          {a.output && <pre className="rounded-lg border border-line bg-elevated/50 p-3 text-[11.5px] text-muted whitespace-pre-wrap break-words max-h-48 overflow-y-auto">{a.output}</pre>}
          {a.status === 'suppressed' && <div className="rounded-lg bg-runsoft border border-run/20 px-3 py-2 text-[12px] text-run">Suppressed until {fmtTime(a.silenced_until)}{a.silence_reason ? ` — ${a.silence_reason}` : ''}</div>}
          {a.status === 'resolved' && a.resolution_note && <div className="rounded-lg bg-oksoft border border-ok/20 px-3 py-2 text-[12px] text-ok">{a.resolution_note}</div>}
        </Card>
        <div><div className="text-[12px] font-semibold text-muted mb-2">Timeline</div><div className="space-y-2">
          {events.slice().reverse().map((e) => <div key={e.id} className="flex gap-3 rounded-lg border border-line bg-card px-3 py-2.5"><div className="mt-1 h-2 w-2 rounded-full bg-brand shrink-0" /><div className="min-w-0 flex-1"><div className="text-[12.5px] text-ink capitalize">{e.event_type.replaceAll('_', ' ')}</div>{e.message && <div className="text-[11.5px] text-muted mt-0.5 break-words">{e.message}</div>}<div className="text-[10.5px] text-faint mt-1">{e.actor} · {fmtTime(e.created_at)}</div></div></div>)}
        </div></div>
      </>}
    </div>
    {a && a.status !== 'resolved' && canManage && <div className="px-5 py-3.5 border-t border-line flex justify-end gap-2">{a.status === 'new' && <Button size="sm" variant="subtle" onClick={() => onAction('ack', a)}>Acknowledge</Button>}{a.status !== 'suppressed' && <Button size="sm" variant="subtle" onClick={() => onAction('suppress', a)}>Suppress</Button>}<Button size="sm" variant="primary" icon="approve" onClick={() => onAction('resolve', a)}>Resolve</Button></div>}
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
    agent: Object.values(devMap).map((x) => ({ value: x.id, label: x.hostname, group: 'Devices' })),
    check: (checksQ.data?.checks || []).map((x) => ({ value: x.id, label: x.name, group: 'Checks' })),
    policy: (policiesQ.data?.policies || []).map((x) => ({ value: x.id, label: x.name, group: 'Policies' })),
    client: (clientsQ.data?.clients || []).map((x) => ({ value: x.id, label: x.name, group: 'Clients' })),
    site: (sitesQ.data?.sites || []).map((x) => ({ value: x.id, label: x.name, group: 'Sites' })),
    group: (groupsQ.data?.groups || []).map((x) => ({ value: x.id, label: x.name, group: 'Departments' })),
  };
  const create = async () => {
    if (!scopeID || !endsAt) return;
    setBusy(true);
    try {
      await api.createAlertSuppression({ kind, scope_type: scopeType, scope_id: scopeID, starts_at: startsAt ? new Date(startsAt).toISOString() : undefined, ends_at: new Date(endsAt).toISOString(), reason });
      toast.success(kind === 'maintenance' ? 'Maintenance window scheduled' : 'Silence created');
      setReason(''); setStartsAt(''); setEndsAt(asLocalInput(new Date(Date.now() + 60 * 60000)));
    } catch (e) { toast.error('Could not create suppression', e.message); } finally { setBusy(false); }
  };
  const cancel = async (row) => {
    try { await api.cancelAlertSuppression(row.id); toast.info('Suppression canceled'); }
    catch (e) { toast.error('Could not cancel suppression', e.message); }
  };
  const rows = suppressionsQ.data?.suppressions || [];
  return <Modal open onClose={onClose} width="max-w-3xl">
    <div className="px-5 py-4 border-b border-line flex items-center justify-between"><div><div className="text-[15px] font-semibold text-ink">Silences & maintenance</div><div className="text-[11.5px] text-muted mt-0.5">Suppress matching incidents without changing check execution.</div></div><button onClick={onClose} className="text-faint hover:text-ink"><Icon name="close" size={18} /></button></div>
    <div className="p-5 max-h-[72vh] overflow-y-auto scroll-area space-y-5">
      <Card className="p-4 space-y-3">
        <div className="grid sm:grid-cols-2 gap-3"><Field label="Type"><Select value={kind} onChange={(e) => setKind(e.target.value)}><option value="maintenance">Maintenance window</option><option value="silence">Silence</option></Select></Field><Field label="Scope"><Select value={scopeType} onChange={(e) => { setScopeType(e.target.value); setScopeID(''); }}><option value="agent">Device</option><option value="check">Check</option><option value="policy">Policy</option><option value="group">Department</option><option value="site">Site</option><option value="client">Client</option></Select></Field></div>
        <Field label="Target"><Combobox value={scopeID} onChange={setScopeID} options={optionsByType[scopeType] || []} placeholder="Select a target…" /></Field>
        <div className="grid sm:grid-cols-2 gap-3"><Field label="Starts" hint="Blank means now"><Input type="datetime-local" value={startsAt} onChange={(e) => setStartsAt(e.target.value)} /></Field><Field label="Ends"><Input type="datetime-local" value={endsAt} onChange={(e) => setEndsAt(e.target.value)} /></Field></div>
        <Field label="Reason"><Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Planned operating-system maintenance" /></Field>
        <div className="flex justify-end"><Button variant="primary" loading={busy} disabled={!scopeID || !endsAt} onClick={create}>Create</Button></div>
      </Card>
      <div><div className="text-[12px] font-semibold text-muted mb-2">Scheduled and previous</div><div className="space-y-2">
        {rows.map((row) => { const active = !row.canceled_at && new Date(row.ends_at) > new Date(); return <div key={row.id} className="rounded-lg border border-line bg-card px-3 py-2.5 flex items-center gap-3"><StatusPill status={active ? 'running' : row.canceled_at ? 'rejected' : 'resolved'} label={active ? row.kind : row.canceled_at ? 'Canceled' : 'Ended'} size="sm" /><div className="min-w-0 flex-1"><div className="text-[12.5px] text-ink capitalize">{row.scope_type} · <span className="mono">{row.scope_id.slice(0, 10)}</span></div><div className="text-[11px] text-muted truncate">{fmtTime(row.starts_at)} → {fmtTime(row.ends_at)}{row.reason ? ` · ${row.reason}` : ''}</div></div>{active && <Button size="sm" variant="ghost" onClick={() => cancel(row)}>Cancel</Button>}</div>; })}
        {!suppressionsQ.loading && rows.length === 0 && <div className="text-[12px] text-faint py-4 text-center">No suppression windows.</div>}
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
      toast.success(c.enabled ? 'Check disabled' : 'Check enabled');
    } catch (e) {
      toast.error('Failed to update check', e.message);
    }
  };

  return (
      <Page wide>
        <PageHeader title="Checks" subtitle="Continuous monitoring across devices and policies."
        actions={can('checks.manage') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>New check</Button>} />

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4 max-w-3xl">
        <div className="rounded-xl border border-ok/25 bg-oksoft px-3.5 py-3"><div className="text-[11px] text-muted">Passing</div><div className="text-[22px] font-semibold text-ok tabular">{summary.passing}</div></div>
        <div className="rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3"><div className="text-[11px] text-muted">Warning</div><div className="text-[22px] font-semibold text-warn tabular">{summary.warning}</div></div>
        <div className="rounded-xl border border-bad/25 bg-badsoft px-3.5 py-3"><div className="text-[11px] text-muted">Failing</div><div className="text-[22px] font-semibold text-bad tabular">{summary.failing}</div></div>
        <div className="rounded-xl border border-line bg-elevated px-3.5 py-3"><div className="text-[11px] text-muted">Unknown</div><div className="text-[22px] font-semibold text-faint tabular">{summary.unknown}</div></div>
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
                  <td className="px-4 py-3"><StatusPill status={checkAggregateStatus(c)} size="sm" /></td>
                  <td className="px-3 py-3"><div className="flex items-center gap-2"><Icon name={t.icon} size={15} className="text-muted" /><div><div className="text-[13px] text-ink font-medium">{c.name}</div><div className="text-[11px] text-faint">{t.label}{!c.enabled && ' · disabled'}</div></div></div></td>
                  <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{dev ? <span className="flex items-center gap-1.5"><OsIcon os={dev.os} size={13} />{dev.hostname}</span> : pol ? <span className="flex items-center gap-1.5 text-brandhi"><Icon name="policy" size={13} />{pol.name}</span> : '—'}</td>
                  <td className="px-3 py-3 hidden lg:table-cell text-[11.5px] text-faint mono">{c.warn_threshold != null ? `warn ${c.warn_threshold} · err ${c.err_threshold}` : '—'}</td>
                  <td className="px-3 py-3 text-[12px] text-muted">{c.interval_sec < 60 ? c.interval_sec + 's' : Math.round(c.interval_sec / 60) + 'm'}</td>
                  <td className="px-3 py-3" onClick={(e) => e.stopPropagation()}><Toggle checked={c.enabled} onChange={() => setConfirmToggle(c)} disabled={!can('checks.manage')} /></td>
                  <td className="px-4 py-3 text-right text-[12px] text-muted">{c.summary?.total ? `${c.summary.passing || 0} ok · ${c.summary.warning || 0} warn · ${c.summary.failing || 0} fail${c.summary.unknown ? ` · ${c.summary.unknown} unknown` : ''}` : 'No target state'}</td>
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
          <div className="flex items-center gap-2 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{check.name}</h2><span className="shrink-0"><StatusPill status={checkAggregateStatus(check)} size="sm" /></span></div>
          <div className="text-[12px] text-muted mt-0.5 truncate">{t.label} · {dev?.hostname || 'policy'} · every {check.interval_sec < 60 ? check.interval_sec + 's' : Math.round(check.interval_sec / 60) + 'm'}</div>
        </div>
        {can('checks.manage') && <Button size="sm" variant="secondary" icon="edit" onClick={() => onEdit(check)}>Edit</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <Card className="overflow-hidden"><div className="px-4 py-3 border-b border-line text-[12.5px] font-semibold text-ink">Target instances</div><div className="divide-y divide-line">
          {(check.instances || []).map((x) => <div key={x.agent_id} className="px-4 py-2.5 flex items-center gap-3"><StatusPill status={x.status || 'unknown'} size="sm" /><span className="text-[12.5px] text-ink flex-1 truncate">{x.hostname}</span><span className="text-[11.5px] text-muted mono truncate max-w-[240px]">{x.value != null ? `${x.value}${t.unit}` : x.output || 'No result'}</span><span className="text-[10.5px] text-faint">{timeAgo(x.last_result_at)}</span></div>)}
          {!(check.instances || []).length && <div className="px-4 py-5 text-center text-[12px] text-faint">No effective targets.</div>}
        </div></Card>
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

function CheckEditor({ check, onClose, devMap, policies, defaultPolicyId }) {
  const toast = useToast();
  const { can } = useAuth();
  const [type, setType] = useState(check?.type || 'diskspace');
  const [name, setName] = useState(check?.name || '');
  const [target, setTarget] = useState(check?.policy_id ? 'pol:' + check.policy_id : check?.agent_id ? 'dev:' + check.agent_id : defaultPolicyId ? 'pol:' + defaultPolicyId : '');
  const initialType = check?.type || 'diskspace';
  const [cfgVal, setCfgVal] = useState(check?.config?.[CHECK_TYPES[initialType].cfg] || '');
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
  const t = CHECK_TYPES[type];
  const devices = Object.values(devMap);

  const save = async () => {
    setBusy(true);
    const config = {}; if (t.cfg) config[t.cfg] = cfgVal; if (type === 'script') config.shell = shell;
    const body = { type, name: name || t.label, config, interval_sec: Number(interval), enabled,
      failure_count: Number(failureCount), recovery_count: Number(recoveryCount), hysteresis: Number(hysteresis), cooldown_sec: Number(cooldown), timeout_sec: Number(timeout) };
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
          <Field label="Type"><Select value={type} onChange={(e) => { const next = e.target.value; setType(next); setCfgVal(check?.type === next ? check?.config?.[CHECK_TYPES[next].cfg] || '' : ''); if (next === 'cpuload' && Number(err) <= Number(warn)) { setWarn(80); setErr(90); } if ((next === 'diskspace' || next === 'memory') && Number(err) >= Number(warn)) { setWarn(20); setErr(10); } }}>{Object.entries(CHECK_TYPES).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}</Select></Field>
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t.label} /></Field>
        </div>
        <Field label="Apply to"><Combobox value={target} onChange={setTarget} placeholder="Select target…"
          options={[
            ...devices.map((d) => ({ value: 'dev:' + d.id, label: d.hostname, group: 'Devices' })),
            ...policies.map((p) => ({ value: 'pol:' + p.id, label: p.name + ' (policy)', group: 'Policies' })),
          ]} /></Field>
        {type === 'script' && <Field label="Shell"><Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>}
        {t.cfg && <Field label={t.cfgLabel}>{t.cfg === 'script' ? <CodeEditor value={cfgVal} onChange={setCfgVal} shell={shell} minHeight={120} /> : <Input value={cfgVal} onChange={(e) => setCfgVal(e.target.value)} placeholder={t.cfgPh} className="mono" />}</Field>}
        {t.dir !== 'state' && t.dir !== 'script' && (
          <div className="grid grid-cols-3 gap-3">
            <Field label={`Warn (${t.unit})`}><Input type="number" value={warn} onChange={(e) => setWarn(e.target.value)} /></Field>
            <Field label={`Error (${t.unit})`}><Input type="number" value={err} onChange={(e) => setErr(e.target.value)} /></Field>
            <Field label="Interval (s)"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>
          </div>
        )}
        {(t.dir === 'state' || t.dir === 'script') && <Field label="Interval (s)" className="w-40"><Input type="number" value={interval} onChange={(e) => setInterval(e.target.value)} /></Field>}
        <div className="rounded-xl border border-line bg-elevated/30 p-3 space-y-3"><div className="text-[12px] font-semibold text-ink">Reliability</div>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3"><Field label="Failures to alert"><Input type="number" min="1" max="100" value={failureCount} onChange={(e) => setFailureCount(e.target.value)} /></Field><Field label="Successes to recover"><Input type="number" min="1" max="100" value={recoveryCount} onChange={(e) => setRecoveryCount(e.target.value)} /></Field><Field label="Timeout (s)"><Input type="number" min="5" value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} /></Field><Field label="Cooldown (s)"><Input type="number" min="0" value={cooldown} onChange={(e) => setCooldown(e.target.value)} /></Field>{(t.dir === 'low' || t.dir === 'high') && <Field label="Hysteresis (%)"><Input type="number" min="0" max="100" step="0.1" value={hysteresis} onChange={(e) => setHysteresis(e.target.value)} /></Field>}</div>
        </div>
        <label className="flex items-center gap-2.5"><Toggle checked={enabled} onChange={(next) => { setEnabled(next); toast.info(next ? 'Check will be enabled when saved' : 'Check will be disabled when saved'); }} /><span className="text-[13px] text-ink">Enabled</span></label>
        <div className="flex items-start gap-2 rounded-lg border border-line bg-elevated/40 px-3 py-2 text-[11.5px] text-muted"><Icon name="info" size={14} className="mt-0.5 shrink-0 text-faint" />{t.dir === 'low' ? 'Alerts when the value drops below the threshold (for example low disk or memory).' : t.dir === 'high' ? 'Alerts when the value rises above the threshold (for example high CPU).' : type === 'ping' ? 'Alerts when the host is unreachable.' : t.dir === 'state' ? 'Alerts when the service is not running.' : 'Exit 0 passes, exit 1 warns, and every other exit code fails.'}</div>
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
// The build gives each source file its own block scope, so the policy drawer in
// screens-rmm2.jsx cannot reach these without going through window. It edits the
// checks and tasks a policy owns with the same editors this screen uses, rather
// than a second, drifting copy of the same forms.
window.CheckEditor = CheckEditor;
window.TaskEditor = TaskEditor;
