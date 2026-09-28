/* screens-jobs.jsx — Jobs & Approval queue (safety-critical) + Audit log */

const DESTRUCTIVE = /\b(rm\s+-rf|Remove-Item|mkfs|dd\s+if=|format\s|shutdown|reboot|Stop-Service|Disable-|Set-ExecutionPolicy|Uninstall|del\s+\/|rd\s+\/s|DROP\s+TABLE|reg\s+delete|Remove-MpPreference|Clear-RecycleBin)\b/i;

function useDeviceMap() {
  const { can } = useAuth();
  const q = useQuery(['devmap'], () => can('devices.view') ? api.listDevices() : Promise.resolve({ devices: [] }), { invalidateOn: ['devices'] });
  const map = {};
  (q.data?.devices || []).forEach((d) => { map[d.id] = d; });
  return map;
}

function JobsScreen({ route }) {
  // Deep-linking to a specific job (e.g. from a device's Jobs tab) must land on
  // "All jobs" — an already-executed job isn't in the awaiting-approval list, so
  // defaulting to that tab would hide it and drop the user on the approval queue.
  const [tab, setTab] = useState(route?.params?.focus ? 'all' : 'awaiting');
  const jobsQ = useQuery(['jobs-all'], () => api.listJobs(), { invalidateOn: ['jobs'], poll: 2500 });
  const devMap = useDeviceMap();
  const [focus, setFocus] = useState(route?.params?.focus || null);

  const jobs = jobsQ.data?.jobs || [];
  const awaiting = jobs.filter((j) => j.status === 'awaiting_approval');
  const list = tab === 'awaiting' ? awaiting : jobs;
  // Group jobs that share a batch_id (a bulk deployment) into one card.
  const byBatch = {};
  list.forEach((j) => { if (j.batch_id) (byBatch[j.batch_id] = byBatch[j.batch_id] || []).push(j); });
  const seenBatch = new Set();

  return (
    <Page wide>
      <PageHeader title={t('title.jobs')} subtitle={t('jobs.subtitle')} />

      <div className="flex items-center justify-between mb-4">
        <Tabs value={tab} onChange={setTab} tabs={[{ value: 'awaiting', label: t('jobs.tab.awaiting'), count: awaiting.length }, { value: 'all', label: t('jobs.tab.all'), count: jobs.length }]} />
        <div className="flex items-center gap-1.5 text-[11.5px] text-faint"><Spinner size={11} className="text-faint" />{t('jobs.live')}</div>
      </div>

      {tab === 'awaiting' && awaiting.length > 0 && (
        <div className="flex items-start gap-2.5 rounded-xl border border-warn/25 bg-warnsoft px-4 py-3 mb-4">
          <Icon name="shield" size={17} className="text-warn mt-0.5 shrink-0" />
          <div className="text-[12.5px] text-ink leading-relaxed">{tn('jobs.waiting', awaiting.length)}</div>
        </div>
      )}

      <div className="space-y-3">
        {jobsQ.loading && Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-2xl" />)}
        {!jobsQ.loading && list.length === 0 && (
          <Card><EmptyState icon={tab === 'awaiting' ? 'approve' : 'flash'} title={tab === 'awaiting' ? t('jobs.queueClear') : t('djobs.empty')} msg={tab === 'awaiting' ? t('jobs.queueClearMsg') : t('jobs.emptyMsg')} /></Card>
        )}
        {!jobsQ.loading && list.map((j) => {
          if (j.batch_id) {
            if (seenBatch.has(j.batch_id)) return null;
            seenBatch.add(j.batch_id);
            return <BatchCard key={'b-' + j.batch_id} jobs={byBatch[j.batch_id]} devMap={devMap} defaultOpen={tab === 'awaiting'} />;
          }
          return <JobCard key={j.id} job={j} device={devMap[j.agent_id]} defaultOpen={tab === 'awaiting' || focus === j.id} focused={focus === j.id} />;
        })}
      </div>
    </Page>
  );
}

function JobCard({ job, device, defaultOpen, focused }) {
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(defaultOpen);
  const [confirm, setConfirm] = useState(false);
  const [askComplete, setAskComplete] = useState(false);
  const [busy, setBusy] = useState(false);
  const ref = useRef(null);
  useEffect(() => { if (focused && ref.current) ref.current.scrollIntoView({ block: 'center', behavior: 'smooth' }); }, []);

  const ai = job.created_by.startsWith('ai:');
  const isAwaiting = job.status === 'awaiting_approval';
  const destructive = DESTRUCTIVE.test(job.script);
  const done = job.status === 'completed' || job.status === 'failed';
  const inFlight = job.status === 'pending' || job.status === 'sent' || job.status === 'running';

  const approve = async () => {
    setBusy(true);
    try { await api.approveJob(job.id); toast.success(t('devices.review.approved'), t('jobs.approvedDispatching', { host: device?.hostname || job.agent_id })); }
    catch (e) { toast.error(t('jobs.couldNotApprove'), e.message); } finally { setBusy(false); }
  };
  const reject = async () => {
    setBusy(true);
    try { await api.rejectJob(job.id); toast.info(t('devices.review.rejected'), t('jobs.discarded')); }
    catch (e) { toast.error(t('jobs.couldNotReject'), e.message); } finally { setBusy(false); }
  };
  const cancel = async () => {
    setBusy(true);
    try { await api.cancelJob(job.id); toast.info(t('jobs.canceling'), t('jobs.stoppingOn', { host: device?.hostname || job.agent_id })); }
    catch (e) { toast.error(t('jobs.couldNotCancel'), e.message); } finally { setBusy(false); }
  };
  const complete = async () => {
    setBusy(true);
    try { await api.completeJob(job.id, true); toast.success(t('jobs.markedComplete'), t('jobs.finalizedManually')); }
    catch (e) { toast.error(t('jobs.couldNotComplete'), e.message); } finally { setBusy(false); }
  };

  return (
    <Card ref={ref} className={cx('overflow-hidden transition-all', isAwaiting && 'border-warn/30', focused && 'ring-2 ring-brand/40')}>
      <div className="flex items-start gap-3 p-4">
        <div className={cx('h-9 w-9 rounded-xl flex items-center justify-center shrink-0 border', ai ? 'bg-brandsoft border-brand/30 text-brandhi' : 'bg-elevated border-line text-muted')}>
          <Icon name={ai ? 'sparkle' : 'terminal'} size={17} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[14px] font-semibold text-ink">{job.description}</span>
            {ai && <span className="inline-flex items-center gap-1 rounded-md bg-brandsoft border border-brand/25 text-brandhi px-1.5 py-0.5 text-[10px] font-semibold">{t('jobs.aiProposed')}</span>}
            {destructive && isAwaiting && <span className="inline-flex items-center gap-1 rounded-md bg-badsoft border border-bad/25 text-bad px-1.5 py-0.5 text-[10px] font-semibold"><Icon name="alert" size={10} />{t('jobs.destructive')}</span>}
          </div>
          <div className="text-[12px] text-faint mt-1 flex items-center gap-1.5 flex-wrap">
            <OsIcon os={device?.os} size={12} /><span className="text-muted">{device?.hostname || job.agent_id}</span>
            <span>·</span><span className="mono">{job.shell}</span>
            <span>·</span><span>{ai ? <span className="text-brandhi">{t('jobs.by', { who: t('jobs.aiAssistant') })}</span> : t('jobs.by', { who: job.created_by })}</span>
            <span>·</span><span>{timeAgo(job.created_at)}</span>
            {job.approved_by && <><span>·</span><span>{t('jobs.approvedBy', { who: job.approved_by })}</span></>}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <StatusPill status={job.status} pulse />
          <button onClick={() => setOpen((o) => !o)} className="text-faint hover:text-ink p-1 transition"><Icon name={open ? 'chevdown' : 'chevright'} size={16} /></button>
        </div>
      </div>

      {open && (
        <div className="px-4 pb-4 space-y-3 animate-fade-in">
          <CodeBlock code={job.script} shell={job.shell} dryRun={isAwaiting} maxHeight={280} />
          {done && (job.stdout || job.stderr) && <OutputBlock stdout={job.stdout} stderr={job.stderr} exitCode={job.exit_code} />}

          {isAwaiting && (
            <div className="flex items-center justify-between gap-3 pt-1">
              <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="eye" size={13} />{t('jobs.previewOnly')}</div>
              <div className="flex items-center gap-2">
                {can('jobs.manage') && (
                  <>
                    <Button variant="danger" icon="reject" onClick={reject} disabled={busy}>{t('action.reject')}</Button>
                    <Button variant="success" icon="approve" loading={busy} onClick={() => destructive ? setConfirm(true) : approve()}>{t('jobs.approveRun')}</Button>
                  </>
                )}
              </div>
            </div>
          )}
          {inFlight && (
            <div className="flex items-center justify-between gap-3 pt-1">
              <div className="flex items-center gap-2 text-[12.5px] text-run">
                {job.status === 'running' ? <><Spinner size={14} className="text-run" />{t('jobs.executingOn', { host: device?.hostname || job.agent_id })}</> : <><Icon name="clock" size={13} />{job.status === 'sent' ? t('jobs.sentStarting') : t('jobs.queuedAgent')}</>}
              </div>
              {can('jobs.manage') && <div className="flex items-center gap-2">
                <Button variant="ghost" icon="approve" onClick={() => setAskComplete(true)} disabled={busy}>{t('jobs.markComplete')}</Button>
                <Button variant="danger" icon="reject" onClick={cancel} loading={busy}>{t('action.cancel')}</Button>
              </div>}
            </div>
          )}
        </div>
      )}

      {/* RUN stays RUN in every language: it is the word typed to confirm, not a label. */}
      <ConfirmDialog open={confirm} onClose={() => setConfirm(false)} onConfirm={approve} danger requireType="RUN" confirmLabel={t('jobs.approveRun')}
        title={t('jobs.destructiveTitle')} body={t('jobs.destructiveBody', { host: device?.hostname || job.agent_id })} />
      <ConfirmDialog open={askComplete} onClose={() => setAskComplete(false)} onConfirm={complete} confirmLabel={t('jobs.markComplete')}
        title={t('jobs.completeTitle')} body={t('jobs.completeBody', { host: device?.hostname || job.agent_id })} />
    </Card>
  );
}

// BatchCard groups the jobs of one bulk deployment (shared batch_id): one script,
// many targets, one approval. The review/approve flow — drop targets, then approve
// once for all remaining.
function BatchCard({ jobs, devMap, defaultOpen }) {
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(defaultOpen);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  const head = jobs[0];
  const batchId = head.batch_id;
  const ai = head.created_by.startsWith('ai:');
  const destructive = DESTRUCTIVE.test(head.script);
  const awaiting = jobs.filter((j) => j.status === 'awaiting_approval');
  const isAwaiting = awaiting.length > 0;
  const counts = jobs.reduce((a, j) => { a[j.status] = (a[j.status] || 0) + 1; return a; }, {});

  const approveAll = async () => {
    setBusy(true);
    try { const r = await api.approveBatch(batchId); toast.success(t('jobs.deploymentApproved'), t('jobs.dispatchingN', { jobs: tn('bulk.jobs', r.approved ?? awaiting.length) })); }
    catch (e) { toast.error(t('jobs.couldNotApprove'), e.message); } finally { setBusy(false); }
  };
  const rejectAll = async () => {
    setBusy(true);
    try { const r = await api.rejectBatch(batchId); toast.info(t('jobs.deploymentRejected'), t('jobs.discardedN', { jobs: tn('bulk.jobs', r.rejected ?? awaiting.length) })); }
    catch (e) { toast.error(t('jobs.couldNotReject'), e.message); } finally { setBusy(false); }
  };
  const removeOne = async (j) => {
    try { await api.rejectJob(j.id); toast.info(t('jobs.targetRemoved'), t('jobs.droppedFrom', { host: devMap[j.agent_id]?.hostname || j.agent_id })); }
    catch (e) { toast.error(t('common.failed'), e.message); }
  };

  return (
    <Card className={cx('overflow-hidden transition-all', isAwaiting && 'border-warn/30')}>
      <div className="flex items-start gap-3 p-4">
        <div className={cx('h-9 w-9 rounded-xl flex items-center justify-center shrink-0 border', ai ? 'bg-brandsoft border-brand/30 text-brandhi' : 'bg-elevated border-line text-muted')}>
          <Icon name="devices" size={17} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[14px] font-semibold text-ink">{head.description || t('jobs.bulkDeployment')}</span>
            <span className="inline-flex items-center gap-1 rounded-md bg-elevated border border-line text-muted px-1.5 py-0.5 text-[10px] font-semibold">{t('jobs.bulkN', { devices: tn('bulk.devices', jobs.length) })}</span>
            {ai && <span className="inline-flex items-center gap-1 rounded-md bg-brandsoft border border-brand/25 text-brandhi px-1.5 py-0.5 text-[10px] font-semibold">{t('jobs.aiProposed')}</span>}
            {destructive && isAwaiting && <span className="inline-flex items-center gap-1 rounded-md bg-badsoft border border-bad/25 text-bad px-1.5 py-0.5 text-[10px] font-semibold"><Icon name="alert" size={10} />{t('jobs.destructive')}</span>}
          </div>
          <div className="text-[12px] text-faint mt-1 flex items-center gap-1.5 flex-wrap">
            <span className="mono">{head.shell}</span><span>·</span>
            <span>{ai ? <span className="text-brandhi">{t('jobs.by', { who: t('jobs.aiAssistant') })}</span> : t('jobs.by', { who: head.created_by })}</span><span>·</span>
            <span>{timeAgo(head.created_at)}</span>
            {Object.entries(counts).map(([s, n]) => <span key={s} className="inline-flex items-center gap-1"><span>·</span><StatusPill status={s} size="sm" /> {n}</span>)}
          </div>
        </div>
        <button onClick={() => setOpen((o) => !o)} className="text-faint hover:text-ink p-1 transition shrink-0"><Icon name={open ? 'chevdown' : 'chevright'} size={16} /></button>
      </div>

      {open && (
        <div className="px-4 pb-4 space-y-3 animate-fade-in">
          <CodeBlock code={head.script} shell={head.shell} dryRun={isAwaiting} maxHeight={240} />
          <div className="rounded-xl border border-line divide-y divide-line">
            {jobs.map((j) => (
              <div key={j.id} className="flex items-center gap-2 px-3 py-2 text-[12.5px]">
                <OsIcon os={devMap[j.agent_id]?.os} size={13} />
                <span className="text-ink truncate flex-1">{devMap[j.agent_id]?.hostname || j.agent_id}</span>
                <StatusPill status={j.status} size="sm" />
                {j.status === 'awaiting_approval' && can('jobs.manage') && <button onClick={() => removeOne(j)} title={t('jobs.removeTarget')} className="text-faint hover:text-bad p-0.5"><Icon name="close" size={14} /></button>}
              </div>
            ))}
          </div>
          {isAwaiting && (
            <div className="flex items-center justify-between gap-3 pt-1">
              <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="eye" size={13} />{t('jobs.batchPreview', { n: awaiting.length })}</div>
              <div className="flex items-center gap-2">
                {can('jobs.manage') && (
                  <>
                    <Button variant="danger" icon="reject" onClick={rejectAll} disabled={busy}>{t('jobs.rejectAll')}</Button>
                    <Button variant="success" icon="approve" loading={busy} onClick={() => destructive ? setConfirm(true) : approveAll()}>{t('jobs.approveN', { n: awaiting.length })}</Button>
                  </>
                )}
              </div>
            </div>
          )}
        </div>
      )}

      <ConfirmDialog open={confirm} onClose={() => setConfirm(false)} onConfirm={approveAll} danger requireType="RUN" confirmLabel={t('jobs.approveN', { n: awaiting.length })}
        title={t('jobs.batchDestructiveTitle')} body={t('jobs.batchDestructiveBody', { devices: tn('bulk.devices', awaiting.length) })} />
    </Card>
  );
}

/* ================================= AUDIT ================================== */
function AuditScreen() {
  const { can } = useAuth();

  // Filtering and paging happen on the SERVER. The previous version fetched 200
  // rows and filtered them in the browser, which quietly meant every search
  // answered "among the 200 most recent" — under a day on an active fleet, and
  // exactly the wrong answer for the question an audit log is kept for.
  const PAGE = 50;
  const [page, setPage] = useState(0);
  // A stack of the cursors that led here, so Previous is a step back rather
  // than a recomputed offset. Offset is wrong for this table: the trail is
  // append-only and read newest-first, so entries arrive at the TOP and every
  // later page shifts under a reader who is paging while others work — showing
  // the same rows twice, which reads as "this happened more than once".
  const [cursors, setCursors] = useState([]);
  const [actorF, setActorF] = useState('');
  const [actionF, setActionF] = useState('');
  const [targetF, setTargetF] = useState('');
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');
  const [exporting, setExporting] = useState(false);
  const toast = useToast();

  // A date input gives a local calendar day; the API wants an instant. `until`
  // becomes the START of the following day so the chosen day is included —
  // otherwise "1st to 1st" silently matches nothing, which reads as "nothing
  // happened" rather than as a mistake.
  const dayStart = (d) => (d ? new Date(d + 'T00:00:00').toISOString() : '');
  const dayAfter = (d) => {
    if (!d) return '';
    const next = new Date(d + 'T00:00:00');
    next.setDate(next.getDate() + 1);
    return next.toISOString();
  };

  const query = {
    limit: PAGE,
    ...(cursors.length ? { cursor: cursors[cursors.length - 1] } : {}),
    ...(actorF ? { actor: actorF } : {}),
    ...(actionF ? { action: actionF + '.' } : {}),
    ...(targetF.trim() ? { target: targetF.trim() } : {}),
    ...(since ? { since: dayStart(since) } : {}),
    ...(until ? { until: dayAfter(until) } : {}),
  };
  const key = ['audit', JSON.stringify(query)];
  // Polling only on the first page with no filters: refreshing under someone
  // who is paging through an investigation moves the rows they are reading.
  const live = page === 0 && !actorF && !actionF && !targetF && !since && !until;
  const auditQ = useQuery(key, () => api.listAudit(query), {
    invalidateOn: ['audit'], poll: live ? 6000 : 0,
  });
  const facetsQ = useQuery(['audit-facets'], () => api.listAuditFacets(), { invalidateOn: ['audit'] });

  const entries = auditQ.data?.entries || [];
  const total = auditQ.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE));
  const nextCursor = auditQ.data?.next_cursor;

  const usersQ = useQuery(['users'], () => can('users.manage') ? api.listUsers() : Promise.resolve({ users: [] }), { invalidateOn: ['users'] });
  const userMap = {};
  (usersQ.data?.users || []).forEach((u) => { userMap[u.id] = u.username; });
  const displayActor = (a) => {
    if (!a) return a;
    if (a.startsWith('ai:')) { const u = userMap[a.slice(3)]; return u ? t('audit.aiAs', { user: u }) : t('audit.aiAssistant'); }
    return userMap[a] || a;
  };

  // target is the id of whatever the action acted on (device, job, batch, user…).
  // For a faithful audit report we prefer the label snapshotted into detail at
  // write time (survives device deletion/rename), then fall back to live
  // resolution (device -> hostname, user -> username), then the raw UUID. The
  // UUID always stays the unique key — shown on hover (title) and in the CSV.
  const devMap = useDeviceMap();
  const displayTarget = (e) => {
    const target = e.target;
    const d = e.detail || {};
    const snap = d.hostname || d.host || d.name;
    const live2 = target && (devMap[target]?.hostname || userMap[target]);
    const byAgent = d.agent && devMap[d.agent]?.hostname;
    return snap || live2 || byAgent || target;
  };

  const resetFilters = () => {
    setActorF(''); setActionF(''); setTargetF(''); setSince(''); setUntil('');
    setPage(0); setCursors([]);
  };
  // Changing a filter invalidates the cursors: they point into the previous
  // result set, and following one would page through the wrong query.
  const onFilter = (setter) => (e) => { setter(e.target.value); setPage(0); setCursors([]); };

  // Export covers the WHOLE filtered set, not the page on screen — an export
  // that silently stops at 50 rows would be read as the complete record. It
  // pages the API to collect them, and says so if it hits the ceiling.
  const EXPORT_MAX = 5000;
  const exportCSV = async () => {
    setExporting(true);
    try {
      const collected = [];
      for (let offset = 0; offset < EXPORT_MAX; offset += 500) {
        const batch = await api.listAudit({ ...query, limit: 500, offset });
        const rows = batch.entries || [];
        collected.push(...rows);
        if (rows.length < 500 || collected.length >= (batch.total ?? 0)) break;
      }
      const esc = (v) => '"' + String(v == null ? '' : v).replace(/"/g, '""') + '"';
      const detailStr = (d) => Object.entries(d || {}).map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : v}`).join(' · ');
      // English on purpose: scripts read these columns, whatever the console's language.
      const header = ['Time', 'Actor', 'Actor ID', 'Action', 'Target', 'Target ID', 'Detail'];
      const rows = collected.map((e) => [e.created_at, displayActor(e.actor), e.actor, e.action, displayTarget(e), e.target, detailStr(e.detail)]);
      const csv = [header, ...rows].map((r) => r.map(esc).join(',')).join('\r\n');
      downloadText('audit-' + new Date().toISOString().slice(0, 10) + '.csv', csv, 'text/csv');
      if (total > collected.length) {
        toast.info(t('audit.truncated'), t('audit.truncatedMsg', { count: collected.length, total }));
      }
    } catch (err) {
      toast.error(t('audit.exportFailed'), err.message);
    } finally {
      setExporting(false);
    }
  };

  const hasFilters = actorF || actionF || targetF || since || until;

  return (
    <Page>
      <PageHeader title={t('title.audit')} subtitle={t('audit.subtitle')} />

      <Card className="mb-4"><div className="flex flex-wrap items-end gap-2 p-2.5">
        <Field label={t('audit.from')} className="w-[150px]">
          <Input type="date" value={since} onChange={onFilter(setSince)} max={until || undefined} />
        </Field>
        <Field label={t('audit.to')} className="w-[150px]">
          <Input type="date" value={until} onChange={onFilter(setUntil)} min={since || undefined} />
        </Field>
        <Field label={t('audit.actor')} className="w-auto min-w-[170px]">
          <Select value={actorF} onChange={onFilter(setActorF)}>
            <option value="">{t('audit.allActors')}</option>
            {(facetsQ.data?.actors || []).map((a) => <option key={a} value={a}>{displayActor(a)}</option>)}
          </Select>
        </Field>
        <Field label={t('audit.action')} className="w-auto min-w-[150px]">
          <Select value={actionF} onChange={onFilter(setActionF)}>
            <option value="">{t('audit.allActions')}</option>
            {(facetsQ.data?.actions || []).map((a) => <option key={a} value={a}>{tEnum('audit.res', a)}</option>)}
          </Select>
        </Field>
        <Field label={t('audit.targetId')} className="flex-1 min-w-[200px]"
          hint={t('audit.targetIdHint')}>
          <Input className="mono" value={targetF} onChange={onFilter(setTargetF)} placeholder="uuid…" /> {/* i18n-ignore: a format, not words */}
        </Field>
        {hasFilters && <Button variant="ghost" onClick={resetFilters}>{t('action.clear')}</Button>}
        <Button variant="secondary" icon="download" onClick={exportCSV}
          disabled={!total || exporting}>
          {exporting ? t('audit.exporting') : t('audit.exportCsv', { n: total })}
        </Button>
      </div></Card>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">{t('audit.col.time')}</th><th className="font-semibold px-3 py-2.5">{t('audit.actor')}</th>
            <th className="font-semibold px-3 py-2.5">{t('audit.action')}</th><th className="font-semibold px-3 py-2.5">{t('audit.col.target')}</th>
            <th className="font-semibold px-4 py-2.5">{t('audit.col.detail')}</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {entries.map((e) => {
              const m = ACTION_META[e.action] || { icon: 'dot', c: 'text-faint' };
              return (
                <tr key={e.id} className="hover:bg-elevated/40 transition align-top">
                  <td className="px-4 py-3 text-[12.5px] text-muted whitespace-nowrap" title={e.created_at}>{fmtTime(e.created_at)}</td>
                  <td className="px-3 py-3 text-[13px] text-ink" title={e.actor}>{displayActor(e.actor)}</td>
                  <td className="px-3 py-3" title={e.action}><span className={cx('inline-flex items-center gap-1.5 text-[12.5px]', m.c)}><Icon name={m.icon} size={13} />{window.auditActionLabel(e.action)}</span></td>
                  <td className="px-3 py-3 text-[12.5px] text-muted" title={e.target}>{displayTarget(e)}</td>
                  <td className="px-4 py-3 text-[12px] text-faint">{Object.entries(e.detail || {}).map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : v}`).join(' · ')}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!auditQ.loading && !entries.length && (
          <EmptyState icon="audit" title={t('audit.empty')}
            msg={hasFilters ? t('audit.noMatch') : t('audit.nothingYet')} />
        )}
      </Card>

      {(total > PAGE || page > 0) && (
        <div className="flex items-center justify-between mt-3 text-[12.5px] text-muted">
          <span>{t('audit.shownOf', { shown: entries.length, total })}</span>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="ghost" disabled={page === 0}
              onClick={() => { setCursors(cursors.slice(0, -1)); setPage(page - 1); }}>{t('audit.previous')}</Button>
            <span className="text-faint">{t('audit.pageOf', { page: page + 1, pages })}</span>
            <Button size="sm" variant="ghost" disabled={!nextCursor}
              onClick={() => { setCursors([...cursors, nextCursor]); setPage(page + 1); }}>{t('audit.next')}</Button>
          </div>
        </div>
      )}
    </Page>
  );
}

window.JobsScreen = JobsScreen;
window.JobCard = JobCard;
window.AuditScreen = AuditScreen;
window.useDeviceMap = useDeviceMap;
