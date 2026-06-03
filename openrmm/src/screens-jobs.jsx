/* screens-jobs.jsx — Jobs & Approval queue (safety-critical) + Audit log */

const DESTRUCTIVE = /\b(rm\s+-rf|Remove-Item|mkfs|dd\s+if=|format\s|shutdown|reboot|Stop-Service|Disable-|Set-ExecutionPolicy|Uninstall|del\s+\/|rd\s+\/s|DROP\s+TABLE|reg\s+delete|Remove-MpPreference|Clear-RecycleBin)\b/i;

function useDeviceMap() {
  const q = useQuery(['devmap'], () => api.listDevices(), { invalidateOn: ['devices'] });
  const map = {};
  (q.data?.devices || []).forEach((d) => { map[d.id] = d; });
  return map;
}

function JobsScreen({ route }) {
  const [tab, setTab] = useState('awaiting');
  const jobsQ = useQuery(['jobs-all'], () => api.listJobs(), { invalidateOn: ['jobs'], poll: 2500 });
  const devMap = useDeviceMap();
  const [focus, setFocus] = useState(route?.params?.focus || null);

  const jobs = jobsQ.data?.jobs || [];
  const awaiting = jobs.filter((j) => j.status === 'awaiting_approval');
  const list = tab === 'awaiting' ? awaiting : jobs;

  return (
    <Page wide>
      <PageHeader title="Jobs & Approvals" subtitle="Every action runs only after a human approves it. Scripts are shown in full before they run." />

      <div className="flex items-center justify-between mb-4">
        <Tabs value={tab} onChange={setTab} tabs={[{ value: 'awaiting', label: 'Awaiting approval', count: awaiting.length }, { value: 'all', label: 'All jobs', count: jobs.length }]} />
        <div className="flex items-center gap-1.5 text-[11.5px] text-faint"><Spinner size={11} className="text-faint" />Live · polling</div>
      </div>

      {tab === 'awaiting' && awaiting.length > 0 && (
        <div className="flex items-start gap-2.5 rounded-xl border border-warn/25 bg-warnsoft px-4 py-3 mb-4">
          <Icon name="shield" size={17} className="text-warn mt-0.5 shrink-0" />
          <div className="text-[12.5px] text-ink leading-relaxed">{awaiting.length} job{awaiting.length > 1 ? 's are' : ' is'} waiting for a decision. Review the full script for each before approving — approval is a deliberate action and is recorded in the audit log.</div>
        </div>
      )}

      <div className="space-y-3">
        {jobsQ.loading && Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-2xl" />)}
        {!jobsQ.loading && list.length === 0 && (
          <Card><EmptyState icon={tab === 'awaiting' ? 'approve' : 'flash'} title={tab === 'awaiting' ? 'Approval queue is clear' : 'No jobs yet'} msg={tab === 'awaiting' ? 'Nothing is waiting on you right now.' : 'Jobs created manually or by the AI will appear here.'} /></Card>
        )}
        {!jobsQ.loading && list.map((j) => (
          <JobCard key={j.id} job={j} device={devMap[j.agent_id]} defaultOpen={tab === 'awaiting' || focus === j.id} focused={focus === j.id} />
        ))}
      </div>
    </Page>
  );
}

function JobCard({ job, device, defaultOpen, focused }) {
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(defaultOpen);
  const [confirm, setConfirm] = useState(false);
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
    try { await api.approveJob(job.id); toast.success('Approved', 'Dispatching to ' + (device?.hostname || job.agent_id)); }
    catch (e) { toast.error('Could not approve', e.message); } finally { setBusy(false); }
  };
  const reject = async () => {
    setBusy(true);
    try { await api.rejectJob(job.id); toast.info('Rejected', 'The job was discarded.'); }
    catch (e) { toast.error('Could not reject', e.message); } finally { setBusy(false); }
  };
  const cancel = async () => {
    setBusy(true);
    try { await api.cancelJob(job.id); toast.info('Canceling', 'Stopping the job on ' + (device?.hostname || job.agent_id) + '…'); }
    catch (e) { toast.error('Could not cancel', e.message); } finally { setBusy(false); }
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
            {ai && <span className="inline-flex items-center gap-1 rounded-md bg-brandsoft border border-brand/25 text-brandhi px-1.5 py-0.5 text-[10px] font-semibold">AI-proposed</span>}
            {destructive && isAwaiting && <span className="inline-flex items-center gap-1 rounded-md bg-badsoft border border-bad/25 text-bad px-1.5 py-0.5 text-[10px] font-semibold"><Icon name="alert" size={10} />Destructive</span>}
          </div>
          <div className="text-[12px] text-faint mt-1 flex items-center gap-1.5 flex-wrap">
            <OsIcon os={device?.os} size={12} /><span className="text-muted">{device?.hostname || job.agent_id}</span>
            <span>·</span><span className="mono">{job.shell}</span>
            <span>·</span><span>by {ai ? <span className="text-brandhi">AI assistant</span> : job.created_by}</span>
            <span>·</span><span>{timeAgo(job.created_at)}</span>
            {job.approved_by && <><span>·</span><span>approved by {job.approved_by}</span></>}
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
              <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="eye" size={13} />Preview only — nothing has run yet.</div>
              <div className="flex items-center gap-2">
                {can('tech') ? (
                  <>
                    <Button variant="danger" icon="reject" onClick={reject} disabled={busy}>Reject</Button>
                    <Button variant="success" icon="approve" loading={busy} onClick={() => destructive ? setConfirm(true) : approve()}>Approve & run</Button>
                  </>
                ) : (
                  <span className="inline-flex items-center gap-1.5 text-[12px] text-faint"><Icon name="shield" size={13} />Approval requires the tech role</span>
                )}
              </div>
            </div>
          )}
          {inFlight && (
            <div className="flex items-center justify-between gap-3 pt-1">
              <div className="flex items-center gap-2 text-[12.5px] text-run">
                {job.status === 'running' ? <><Spinner size={14} className="text-run" />Executing on {device?.hostname}…</> : <><Icon name="clock" size={13} />{job.status === 'sent' ? 'Sent — starting…' : 'Queued (waiting for agent)'}</>}
              </div>
              {can('tech') && <Button variant="danger" icon="reject" onClick={cancel} loading={busy}>Cancel</Button>}
            </div>
          )}
        </div>
      )}

      <ConfirmDialog open={confirm} onClose={() => setConfirm(false)} onConfirm={approve} danger requireType="RUN" confirmLabel="Approve & run"
        title="This script looks destructive" body={<>It matches patterns that modify or remove data on <b>{device?.hostname}</b>. Re-read the script above, then type <b>RUN</b> to confirm you want to execute it.</>} />
    </Card>
  );
}

/* ================================= AUDIT ================================== */
function AuditScreen() {
  const auditQ = useQuery(['audit'], () => api.listAudit(200), { invalidateOn: ['audit'], poll: 6000 });
  const [q, setQ] = useState('');
  const [actorF, setActorF] = useState('all');
  const entries = auditQ.data?.entries || [];
  const actors = Array.from(new Set(entries.map((e) => e.actor)));

  const filtered = entries.filter((e) => {
    if (actorF !== 'all' && e.actor !== actorF) return false;
    if (q) { const blob = (e.actor + ' ' + e.action + ' ' + e.target + ' ' + JSON.stringify(e.detail)).toLowerCase(); if (!blob.includes(q.toLowerCase())) return false; }
    return true;
  });

  return (
    <Page>
      <PageHeader title="Audit log" subtitle="An immutable record of who did what. Read-only." />
      <Card className="mb-4"><div className="flex flex-wrap items-center gap-2 p-2.5">
        <div className="relative flex-1 min-w-[220px]">
          <Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search actor, action, target…" className={cx(inputCls, 'pl-9')} />
        </div>
        <Select value={actorF} onChange={(e) => setActorF(e.target.value)} className="w-auto min-w-[160px]">
          <option value="all">All actors</option>{actors.map((a) => <option key={a} value={a}>{a}</option>)}
        </Select>
      </div></Card>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
            <th className="font-semibold px-4 py-2.5">Time</th><th className="font-semibold px-3 py-2.5">Actor</th>
            <th className="font-semibold px-3 py-2.5">Action</th><th className="font-semibold px-3 py-2.5">Target</th>
            <th className="font-semibold px-4 py-2.5">Detail</th>
          </tr></thead>
          <tbody className="divide-y divide-line">
            {filtered.map((e) => {
              const m = ACTION_META[e.action] || { icon: 'dot', c: 'text-faint' };
              const ai = e.actor.startsWith('ai:');
              return (
                <tr key={e.id} className="hover:bg-elevated/40 transition align-top">
                  <td className="px-4 py-2.5 text-[12px] text-muted whitespace-nowrap tabular">{fmtTime(e.created_at)}</td>
                  <td className="px-3 py-2.5"><span className={cx('text-[12.5px] font-medium', ai ? 'text-brandhi' : 'text-ink')}>{ai ? 'AI assistant' : e.actor}</span></td>
                  <td className="px-3 py-2.5"><span className={cx('inline-flex items-center gap-1.5 text-[12px]', m.c)}><Icon name={m.icon} size={13} /><span className="mono text-muted">{e.action}</span></span></td>
                  <td className="px-3 py-2.5 text-[12px] mono text-faint">{e.target}</td>
                  <td className="px-4 py-2.5 text-[12px] text-muted max-w-[360px]"><span className="line-clamp-2">{Object.entries(e.detail || {}).map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : v}`).join(' · ') || '—'}</span></td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {filtered.length === 0 && <EmptyState icon="audit" title="No entries" msg="Nothing matches your filters." />}
      </Card>
    </Page>
  );
}

window.JobsScreen = JobsScreen;
window.JobCard = JobCard;
window.AuditScreen = AuditScreen;
window.useDeviceMap = useDeviceMap;
