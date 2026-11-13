/* screens-patches.jsx — Patch management: compliance, catalog (approval) and
   policies (maintenance windows / auto-approve / exclusions / reboot). */

const PATCH_CLASS_STYLE = {
  security: 'text-bad bg-badsoft border-bad/25',
  critical: 'text-bad bg-badsoft border-bad/25',
  important: 'text-warn bg-warnsoft border-warn/25',
  moderate: 'text-warn bg-warnsoft border-warn/25',
  optional: 'text-faint bg-elevated border-line2',
  other: 'text-faint bg-elevated border-line2',
};
function ClassBadge({ c }) {
  return <span className={cx('inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-wide', PATCH_CLASS_STYLE[c] || PATCH_CLASS_STYLE.other)}>{tEnum('patch.class', c)}</span>;
}

// Label: t('comp.<status>').
const COMPLIANCE_STYLE = {
  compliant: 'text-ok bg-oksoft border-ok/25',
  at_risk: 'text-warn bg-warnsoft border-warn/25',
  critical: 'text-bad bg-badsoft border-bad/25',
  unknown: 'text-faint bg-elevated border-line2',
};
// A single field of the wrong shape used to blank the whole screen: a policy
// whose auto_approve arrived as "security" instead of ["security"] threw on
// .join and took the React tree down with it. Nothing renders a list without
// going through here now — a malformed field costs that field, not the page.
function asList(value) {
  if (Array.isArray(value)) return value;
  if (typeof value === 'string') return value.split(',').map((v) => v.trim()).filter(Boolean);
  return [];
}

// A catalog row's approval, worked out by the server from where its devices
// are. Approved stays approved through the install: a claimed or failed install
// is not a reason to ask for the decision again. Label: t('patch.approval.<a>').
const APPROVAL_STYLE = {
  pending: 'text-faint bg-elevated border-line2',
  approved: 'text-ok bg-oksoft border-ok/25',
  declined: 'text-bad bg-badsoft border-bad/25',
  partial: 'text-warn bg-warnsoft border-warn/25',
};
function ApprovalBadge({ a }) {
  const key = APPROVAL_STYLE[a] ? a : 'pending';
  return <span className={cx('inline-flex items-center rounded-full border px-2 py-[3px] text-[11.5px] font-medium', APPROVAL_STYLE[key])}>{tEnum('patch.approval', key)}</span>;
}
// The catalog's lists. A patch is in a list when at least one of its devices
// is in that state, so one approved for a client and declined for another is
// in both: the same rule as ?approval= on GET /api/patches.
const n0 = (v) => Number(v) || 0;
const PATCH_LISTS = {
  all: () => true,
  pending: (p) => n0(p.pending_count) > 0,
  approved: (p) => n0(p.approved_count) + n0(p.installing_count) + n0(p.failed_count) + n0(p.installed_count) > 0,
  declined: (p) => n0(p.declined_count) > 0,
};
// Where a patch's devices are, in the words the device's own patch list uses.
const DEVICE_STATES = [['missing', 'pending_count'], ['approved', 'approved_count'], ['installing', 'installing_count'], ['failed', 'failed_count'], ['installed', 'installed_count'], ['excluded', 'declined_count']];
function devicesWithPatch(p) { return DEVICE_STATES.reduce((sum, [, field]) => sum + n0(p[field]), 0); }
function approvalBreakdown(p) {
  return DEVICE_STATES.filter(([, field]) => n0(p[field]) > 0).map(([status, field]) => tEnum('patch.status', status) + ' ' + n0(p[field])).join(' · ');
}

function ComplianceBadge({ s }) {
  const key = COMPLIANCE_STYLE[s] ? s : 'unknown';
  return <span className={cx('inline-flex items-center gap-1.5 rounded-full border px-2 py-[3px] text-[11.5px] font-medium', COMPLIANCE_STYLE[key])}>{t('comp.' + key)}</span>;
}

const PATCH_CLASSES = ['security', 'critical', 'important', 'moderate', 'optional', 'other'];
// Sunday first, the index the server stores; names come from weekdayShort().
const WEEKDAY_INDEXES = [0, 1, 2, 3, 4, 5, 6];

function PatchesScreen({ navigate }) {
  const { can } = useAuth();
  const [tab, setTab] = useState('compliance');
  const tabs = [
    { value: 'compliance', label: t('patches.tab.compliance') },
    { value: 'catalog', label: t('patches.tab.catalog') },
    ...(can('patches.policies.manage') ? [{ value: 'policies', label: t('patches.tab.policies') }] : []),
  ];
  return (
    <div className="p-5 max-w-[1400px] mx-auto">
      <div className="mb-4">
        <h1 className="text-[18px] font-semibold text-ink">{t('title.patches')}</h1>
        <p className="text-[13px] text-muted mt-0.5">{t('patches.subtitle')}</p>
      </div>
      <Tabs value={tab} onChange={setTab} className="mb-4" tabs={tabs} />
      {tab === 'compliance' && <ComplianceTab navigate={navigate} />}
      {tab === 'catalog' && <CatalogTab />}
      {tab === 'policies' && <PoliciesTab />}
    </div>
  );
}

function ComplianceTab() {
  const toast = useToast();
  const { can } = useAuth();
  const q = useQuery(['compliance'], () => api.compliance(), { invalidateOn: ['devices', 'patches'], poll: 15000 });
  const [search, setSearch] = useState('');
  const rows = (q.data?.rows || []).filter((r) => !search || (r.hostname || '').toLowerCase().includes(search.toLowerCase()));
  const scan = async (id) => { try { await api.scanDevicePatches(id); toast.info(t('dpatch.scanRequested'), t('patches.scanRequestedMsg')); } catch (e) { toast.error(t('patches.scanFailed'), e.message); } };
  const scanAll = async () => { for (const r of rows) { try { await api.scanDevicePatches(r.agent_id); } catch (e) { /* offline */ } } toast.info(t('patches.scanAllRequested')); };
  const install = async (r) => { try { await api.installDevicePatches(r.agent_id, []); toast.info(t('dpatch.installing'), t('patches.installingOn', { host: r.hostname })); window.dataBus.emit('patches'); } catch (e) { toast.error(t('patches.installFailed'), e.message); } };
  // A reboot takes the machine away from whoever is using it, so it is asked
  // for, not fired on the first click.
  const [rebootRow, setRebootRow] = useState(null);
  const reboot = async (r) => { try { await api.rebootDevice(r.agent_id); toast.info(t('reboot.requested'), t('reboot.requestedOn', { host: r.hostname })); window.dataBus.emit('patches'); } catch (e) { toast.error(t('reboot.failed'), e.message); } };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-4 h-12 border-b border-line">
        <div className="relative flex-1 max-w-xs">
          <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('patches.filterDevices')} className="pl-9 h-8" />
        </div>
        <div className="flex-1" />
        {can('patches.manage') && <Button size="sm" variant="secondary" icon="refresh" onClick={scanAll}>{t('patches.scanAll')}</Button>}
      </div>
      {q.loading ? <div className="p-4 space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}</div>
        : !rows.length ? <EmptyState icon="shield" title={t('patches.noDevices')} msg={t('patches.noDevicesMsg')} />
          : (
            <div className="overflow-auto scroll-area max-h-[calc(100vh-260px)]">
              <table className="w-full text-[12.5px] border-collapse">
                <thead className="sticky top-0 bg-elevated/95 backdrop-blur text-left text-faint">
                  <tr>{[t('patches.col.host'), t('devices.col.os'), t('patches.col.status'), t('patches.col.missing'), t('patches.col.security'), t('patches.col.approved'), t('patches.col.failed'), t('patches.col.reboot'), t('patches.col.lastScan'), ''].map((h, i) => <th key={i} className="font-medium px-4 py-2.5 whitespace-nowrap border-b border-line">{h}</th>)}</tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr key={r.agent_id} className="border-b border-line/60 hover:bg-white/[0.02]">
                      <td className="px-4 py-2 text-ink font-medium">{r.hostname}</td>
                      <td className="px-4 py-2"><span className="inline-flex items-center gap-1.5"><OsIcon os={r.os} />{r.os}</span></td>
                      <td className="px-4 py-2"><ComplianceBadge s={r.status} /></td>
                      <td className="px-4 py-2 tabular">{r.missing_total}</td>
                      <td className={cx('px-4 py-2 tabular', r.missing_security > 0 && 'text-bad font-semibold')}>{r.missing_security}</td>
                      <td className={cx('px-4 py-2 tabular', r.approved > 0 && 'text-ok font-semibold')}>{r.approved || '—'}</td>
                      <td className={cx('px-4 py-2 tabular', r.failed > 0 && 'text-bad')}>{r.failed}</td>
                      <td className="px-4 py-2">{r.reboot_pending ? <span className="text-warn inline-flex items-center gap-1"><Icon name="refresh" size={12} />{t('patches.pending')}</span> : <span className="text-faint">—</span>}</td>
                      <td className="px-4 py-2 text-faint whitespace-nowrap">{r.last_scan ? timeAgo(r.last_scan) : t('patches.never')}</td>
                      <td className="px-4 py-2 text-right whitespace-nowrap">{can('patches.manage') && (
                        <span className="inline-flex gap-1.5 justify-end">
                          <Button size="sm" variant="ghost" icon="refresh" onClick={() => scan(r.agent_id)}>{t('dpatch.scan')}</Button>
                          <Button size="sm" variant="primary" icon="download" disabled={!r.approved} title={r.approved ? '' : t('patches.approveFirst')} onClick={() => install(r)}>{r.approved ? t('patches.installN', { n: r.approved }) : t('patches.install')}</Button>
                          {r.reboot_pending && <Button size="sm" variant="secondary" icon="refresh" onClick={() => setRebootRow(r)}>{t('reboot.button')}</Button>}
                        </span>
                      )}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
      <ConfirmDialog open={!!rebootRow} onClose={() => setRebootRow(null)} danger
        title={t('reboot.title', { host: rebootRow?.hostname || '' })} body={t('reboot.body')}
        confirmLabel={t('reboot.confirm')} onConfirm={() => reboot(rebootRow)} />
    </Card>
  );
}

function CatalogTab() {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery(['catalog'], () => api.patchCatalog(), { invalidateOn: ['patches'] });
  const [search, setSearch] = useState('');
  const [list, setList] = useState('all');
  const all = q.data?.patches || [];
  const inList = (p) => (PATCH_LISTS[list] || PATCH_LISTS.all)(p);
  const rows = all.filter((p) => inList(p) && (!search || (p.title || '').toLowerCase().includes(search.toLowerCase()) || (p.identifier || '').toLowerCase().includes(search.toLowerCase())));
  const decide = async (p, decision) => {
    try { await api.approvePatch(p.id, { scope_type: '', scope_id: '', decision }); toast.success(decision === 'approve' ? t('patches.approved') : t('patches.declined'), decision === 'approve' ? t('patches.approvedHint') : undefined); window.dataBus.emit('patches'); }
    catch (e) { toast.error(t('common.failed'), e.message); }
  };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-start gap-2.5 px-4 py-2.5 border-b border-line bg-brandsoft/40 text-[12px] text-muted">
        <Icon name="info" size={14} className="text-brandhi mt-0.5 shrink-0" />
        <span>{t('patches.catalogInfo')}</span>
      </div>
      <div className="flex flex-wrap items-center gap-2 px-4 py-2 border-b border-line">
        <div className="relative flex-1 min-w-[200px] max-w-xs">
          <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('patches.filterPatches')} className="pl-9 h-8" />
        </div>
        <div className="flex flex-wrap gap-1.5" role="group" aria-label={t('patches.listLabel')}>
          {Object.keys(PATCH_LISTS).map((key) => (
            <button key={key} type="button" aria-pressed={list === key} onClick={() => setList(key)}
              className={cx('h-8 px-2.5 rounded-lg border text-[12px] transition inline-flex items-center gap-1.5', list === key ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>
              {t('patches.list.' + key)}<span className="tabular text-faint">{all.filter(PATCH_LISTS[key]).length}</span>
            </button>
          ))}
        </div>
      </div>
      {q.loading ? <div className="p-4 space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}</div>
        : !all.length ? <EmptyState icon="shield" title={t('patches.none')} msg={t('patches.noneMsg')} />
          : !rows.length ? <EmptyState icon="shield" title={t('patches.noneInList')} />
          : (
            <div className="overflow-auto scroll-area max-h-[calc(100vh-260px)]">
              <table className="w-full text-[12.5px] border-collapse">
                <thead className="sticky top-0 bg-elevated/95 backdrop-blur text-left text-faint">
                  <tr>{[t('patches.col.class'), t('patches.col.title'), t('devices.col.os'), 'KB', t('patches.col.devices'), t('patches.col.approval'), t('patches.col.reboot'), ''].map((h, i) => <th key={i} className="font-medium px-4 py-2.5 whitespace-nowrap border-b border-line">{h}</th>)}</tr>
                </thead>
                <tbody>
                  {rows.map((p) => (
                    <tr key={p.id} className="border-b border-line/60 hover:bg-white/[0.02]">
                      <td className="px-4 py-2"><ClassBadge c={p.classification} /></td>
                      <td className="px-4 py-2 text-ink max-w-[420px] truncate" title={p.title}>{p.title || p.identifier}</td>
                      <td className="px-4 py-2"><OsIcon os={p.os} /></td>
                      <td className="px-4 py-2 mono text-muted">{p.kb || '—'}</td>
                      <td className="px-4 py-2 tabular">{devicesWithPatch(p)}</td>
                      <td className="px-4 py-2">
                        <ApprovalBadge a={p.approval} />
                        <div className={cx('text-[11px] mt-1 whitespace-nowrap', n0(p.failed_count) > 0 ? 'text-bad' : 'text-faint')}>{approvalBreakdown(p)}</div>
                      </td>
                      <td className="px-4 py-2">{p.reboot_required ? <Icon name="refresh" size={13} className="text-warn" /> : <span className="text-faint">—</span>}</td>
                      <td className="px-4 py-2 text-right">
                        {can('patches.manage') && (
                          <span className="inline-flex gap-1.5 items-center justify-end">
                            {/* Approving again is also how a failed install is retried. */}
                            {p.approval !== 'approved'
                              ? <Button size="sm" variant="success" icon="approve" onClick={() => decide(p, 'approve')}>{t('action.approve')}</Button>
                              : n0(p.failed_count) > 0 && <Button size="sm" variant="secondary" icon="refresh" title={t('patches.retryHint')} onClick={() => decide(p, 'approve')}>{t('patches.retry')}</Button>}
                            {p.approval !== 'declined' && <Button size="sm" variant="ghost" icon="reject" title={p.approval === 'approved' ? t('patches.revoke') : t('patches.decline')} onClick={() => decide(p, 'decline')}>{t('patches.decline')}</Button>}
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
    </Card>
  );
}

function PoliciesTab() {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery(['patch-pol'], () => api.listPatchPolicies(), { invalidateOn: ['patch-pol'] });
  const [editing, setEditing] = useState(null);
  const [activating, setActivating] = useState(null);
  const pols = q.data?.policies || [];
  const del = async (p) => { try { await api.deletePatchPolicy(p.id); window.dataBus.emit('patch-pol'); toast.info(t('patchpol.deleted')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  return (
    <div className="space-y-3">
      {can('patches.policies.manage') && <div className="flex justify-end"><Button variant="primary" icon="plus" onClick={() => setEditing({})}>{t('patchpol.new')}</Button></div>}
      {q.loading ? <Card className="p-4"><Skeleton className="h-16 w-full" /></Card>
        : !pols.length ? <Card><EmptyState icon="policy" title={t('patchpol.empty')} msg={t('patchpol.emptyMsg')} /></Card>
          : pols.map((p) => (
            <Card key={p.id} className="p-4">
              <div className="flex items-start gap-3">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-[14px] font-semibold text-ink">{p.name}</span>
                    {!p.enabled && <span className="text-[10.5px] uppercase text-faint border border-line2 rounded px-1.5 py-0.5">{t('patchpol.disabled')}</span>}
                    {p.pending && <ProposalBadge />}
                  </div>
                  {p.pending && <div className="text-[12px] text-warn mt-1">{t('patchpol.pendingHint', { who: p.proposed_by })}</div>}
                  <div className="text-[12px] text-muted mt-1.5 flex flex-wrap gap-x-4 gap-y-1">
                    <span>{t('patchpol.autoApprove', { classes: asList(p.auto_approve).length ? asList(p.auto_approve).map((c) => tEnum('patch.class', c)).join(', ') : '—' })} {asList(p.auto_approve).length && p.auto_approve_age_days ? t('patchpol.afterDays', { n: p.auto_approve_age_days }) : ''}</span>
                    <span>{t('patchpol.window', { days: asList(p.window_days).length ? asList(p.window_days).map((d) => weekdayShort(Number(d))).join(', ') : t('patchpol.anyDay'), start: minuteToHHMM(p.window_start_min), end: minuteToHHMM(p.window_end_min) + ' ' + scheduleZone(p.timezone) })}</span>
                    <span>{t('patchpol.reboot', { mode: p.reboot ? tEnum('patchpol.reboot', p.reboot) : '—' })}</span>
                    {asList(p.exclusions).length ? <span>{t('patchpol.excludes', { list: asList(p.exclusions).join(', ') })}</span> : null}
                  </div>
                </div>
                {can('patches.policies.manage') && (
                  <div className="flex gap-1.5 shrink-0">
                    {!p.enabled && <Button size="sm" variant="primary" icon="check" onClick={() => setActivating(p)}>{t('patchpol.activate')}</Button>}
                    <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing(p)}>{t('action.edit')}</Button>
                    <Button size="sm" variant="ghost" icon="trash" onClick={() => del(p)} />
                  </div>
                )}
              </div>
            </Card>
          ))}
      {editing && <PolicyModal policy={editing} onClose={() => setEditing(null)} />}
      {activating && <PatchPolicyActivation policy={activating} onClose={() => setActivating(null)} />}
    </div>
  );
}

// PatchPolicyActivation switches a patch policy on after the person read what
// it will approve, install and reboot, and where. Sealed like a policy review:
// a change after it was opened makes the server refuse and the review reload.
function PatchPolicyActivation({ policy, onClose }) {
  const { can } = useAuth();
  const toast = useToast();
  const clientsQ = useQuery(['clients'], () => can('clients.view') ? api.listClients() : Promise.resolve({ clients: [] }), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['sites'], () => can('clients.view') ? api.listSites() : Promise.resolve({ sites: [] }), { invalidateOn: ['org'] });
  const devMap = useDeviceMap();
  const [review, setReview] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const load = async () => {
    try { setReview(await api.reviewPatchPolicy(policy.id)); }
    catch (e) { setError(e.message); }
  };
  useEffect(() => { load(); }, [policy.id]);
  const activate = async () => {
    if (!review) return;
    setBusy(true); setError('');
    try {
      await api.activatePatchPolicy(policy.id, review.fingerprint);
      window.dataBus.emit('patch-pol'); toast.success(t('patchpol.activated'), policy.name); onClose();
    } catch (e) {
      if (e.status === 409) { setError(t('review.stale')); await load(); }
      else setError(e.message);
    } finally { setBusy(false); }
  };
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const scopeLabel = (a) => {
    if (a.scope_type === 'global') return t('pol.globalName');
    if (a.scope_type === 'client') return t('patchpol.clientScope', { name: clients.find((c) => c.id === a.scope_id)?.name || a.scope_id });
    if (a.scope_type === 'site') return t('patchpol.siteScope', { name: sites.find((x) => x.id === a.scope_id)?.name || a.scope_id });
    return devMap[a.scope_id]?.hostname || a.scope_id;
  };
  const p = review?.policy || policy;
  const classes = asList(p.auto_approve);
  return (
    <ReviewModal open onClose={onClose} busy={busy} error={error} onConfirm={activate}
      title={t('patchpol.activateTitle', { name: policy.name })} intro={t('patchpol.activateIntro')} confirmLabel={t('patchpol.activate')}>
      {!review ? <Skeleton className="h-24 w-full" /> : (
        <>
          <ReviewSection title={t('pol.reviewScopes')}>
            {review.assignments.length
              ? <ul className="space-y-1">{review.assignments.map((a) => <li key={a.id} className="text-[12.5px] text-ink flex flex-wrap items-center gap-1.5">{scopeLabel(a)}<TargetChips os={a.os} role={a.role} /></li>)}</ul>
              : <p className="text-[12.5px] text-warn">{t('pol.reviewNoScopes')}</p>}
          </ReviewSection>
          {(review.exclusions || []).length > 0 && (
            <ReviewSection title={t('pol.reviewExclusions')}>
              <p className="text-[12.5px] text-ink">{review.exclusions.map((e) => e.hostname || e.agent_id).join(', ')}</p>
            </ReviewSection>
          )}
          <ReviewSection title={t('patchpol.reviewSettings')}>
            {p.proposed_by && <ReviewRow label={t('review.proposedByLabel')}>{p.proposed_by}</ReviewRow>}
            <ReviewRow label={t('patchpol.autoClasses')}>{classes.length ? classes.map((c) => tEnum('patch.class', c)).join(', ') + ' ' + t('patchpol.afterDays', { n: p.auto_approve_age_days }) : '—'}</ReviewRow>
            <ReviewRow label={t('patchpol.windowDays')}>{asList(p.window_days).length ? asList(p.window_days).map((d) => weekdayShort(Number(d))).join(', ') : t('patchpol.anyDay')}</ReviewRow>
            <ReviewRow label={t('patchpol.windowStart')}>{minuteToHHMM(p.window_start_min)} – {minuteToHHMM(p.window_end_min)} ({p.timezone || 'UTC'})</ReviewRow>
            <ReviewRow label={t('patchpol.rebootLabel')}>{tEnum('patchpol.reboot', p.reboot)}</ReviewRow>
            <ReviewRow label={t('patchpol.exclusions')}>{asList(p.exclusions).length ? asList(p.exclusions).join(', ') : '—'}</ReviewRow>
          </ReviewSection>
        </>
      )}
    </ReviewModal>
  );
}

function PolicyModal({ policy, onClose }) {
  const toast = useToast();
  const isNew = !policy.id;
  const [name, setName] = useState(policy.name || '');
  const [enabled, setEnabled] = useState(policy.enabled !== false);
  const [auto, setAuto] = useState(policy.auto_approve || []);
  const [age, setAge] = useState(policy.auto_approve_age_days ?? 7);
  const [excl, setExcl] = useState((policy.exclusions || []).join(', '));
  const [days, setDays] = useState(policy.window_days || []);
  const [start, setStart] = useState(minuteToHHMM(policy.window_start_min ?? 0));
  const [end, setEnd] = useState(minuteToHHMM(policy.window_end_min ?? 1439));
  const [reboot, setReboot] = useState(policy.reboot || 'if_required');
  // A new policy's window starts in this browser's zone; an existing one keeps
  // its own, and one from before zones existed shows as the UTC it opens in.
  const [timezone, setTimezone] = useState(isNew ? browserTimezone() : scheduleZone(policy.timezone));
  const [busy, setBusy] = useState(false);
  const toMin = (hhmm) => { const [h, m] = (hhmm || '0:0').split(':').map(Number); return (h || 0) * 60 + (m || 0); };
  const toggle = (arr, v, set) => set(arr.includes(v) ? arr.filter((x) => x !== v) : [...arr, v]);
  const save = async () => {
    setBusy(true);
    const body = {
      name, enabled, auto_approve: auto, auto_approve_age_days: Number(age),
      exclusions: excl.split(',').map((s) => s.trim()).filter(Boolean),
      window_days: days, window_start_min: toMin(start), window_end_min: toMin(end), timezone, reboot,
    };
    try {
      if (isNew) await api.createPatchPolicy(body); else await api.updatePatchPolicy(policy.id, body);
      window.dataBus.emit('patch-pol'); toast.success(t('patchpol.saved')); onClose();
    } catch (e) { toast.error(t('patchpol.saveFailed'), e.message); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={onClose} width="max-w-2xl">
      <div className="p-5 space-y-4 max-h-[85vh] overflow-auto scroll-area">
        <div className="text-[15px] font-semibold text-ink">{isNew ? t('patchpol.newTitle') : t('patchpol.editTitle')}</div>
        <Field label={t('patchpol.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('patchpol.namePlaceholder')} autoFocus /></Field>
        {policy.pending
          ? <p role="note" className="text-[12.5px] text-warn">{t('patchpol.pendingHint', { who: policy.proposed_by })}</p>
          : <label className="flex items-center gap-2 text-[13px] text-ink"><Toggle checked={enabled} onChange={setEnabled} />{t('patchpol.enabled')}</label>}

        <Field label={t('patchpol.autoClasses')} hint={t('patchpol.autoClassesHint')}>
          <div className="flex flex-wrap gap-1.5">
            {PATCH_CLASSES.map((c) => (
              <button key={c} type="button" onClick={() => toggle(auto, c, setAuto)}
                className={cx('px-2.5 py-1 rounded-lg border text-[12px] capitalize transition', auto.includes(c) ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>{tEnum('patch.class', c)}</button>
            ))}
          </div>
        </Field>
        <Field label={t('patchpol.afterDaysLabel')}><Input type="number" min="0" value={age} onChange={(e) => setAge(e.target.value)} className="w-32" /></Field>

        <Field label={t('patchpol.windowDays')} hint={t('patchpol.windowDaysHint')}>
          <div className="flex flex-wrap gap-1.5">
            {WEEKDAY_INDEXES.map((i) => (
              <button key={i} type="button" onClick={() => toggle(days, i, setDays)}
                className={cx('px-2.5 py-1 rounded-lg border text-[12px] transition', days.includes(i) ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>{weekdayShort(i)}</button>
            ))}
          </div>
        </Field>
        <div className="flex gap-3">
          <Field label={t('patchpol.windowStart')} className="flex-1"><Input type="time" value={start} onChange={(e) => setStart(e.target.value)} /></Field>
          <Field label={t('patchpol.windowEnd')} className="flex-1"><Input type="time" value={end} onChange={(e) => setEnd(e.target.value)} /></Field>
          <Field label={t('patchpol.rebootLabel')} className="flex-1"><Select value={reboot} onChange={(e) => setReboot(e.target.value)}>
            <option value="never">{t('patchpol.reboot.never')}</option><option value="if_required">{t('patchpol.reboot.if_required')}</option><option value="always">{t('patchpol.reboot.always')}</option>
          </Select></Field>
        </div>
        <Field label={t('schedule.timezone')} hint={t('schedule.windowTimezoneHint')}><TimezoneSelect value={timezone} onChange={setTimezone} /></Field>
        <Field label={t('patchpol.exclusions')} hint={t('patchpol.exclusionsHint')}><Input value={excl} onChange={(e) => setExcl(e.target.value)} placeholder="KB5001234, optional" /></Field>{/* i18n-ignore: example of the values the server accepts */}

        {!isNew && <PolicyAssignments policyId={policy.id} />}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={save}>{t('patchpol.save')}</Button>
        </div>
      </div>
    </Modal>
  );
}

function PolicyAssignments({ policyId }) {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery(['patch-assign', policyId], () => api.listPatchAssignments(policyId), { invalidateOn: ['patch-assign'] });
  const clientsQ = useQuery(['clients'], () => can('clients.view') ? api.listClients() : Promise.resolve({ clients: [] }), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['sites'], () => can('clients.view') ? api.listSites() : Promise.resolve({ sites: [] }), { invalidateOn: ['org'] });
  const [val, setVal] = useState('');
  const [os, setOs] = useState('');
  const [role, setRole] = useState('');
  const [excluded, setExcluded] = useState('');
  const devMap = useDeviceMap();
  const exclQ = useQuery(['patch-excl', policyId], () => api.listPatchPolicyExclusions(policyId), { invalidateOn: ['patch-assign'] });
  const exclusions = exclQ.data?.exclusions || [];
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const assigns = q.data?.assignments || [];
  const label = (a) => {
    if (a.scope_type === 'global') return t('pol.globalName');
    if (a.scope_type === 'client') return t('patchpol.clientScope', { name: clients.find((c) => c.id === a.scope_id)?.name || a.scope_id });
    if (a.scope_type === 'site') return t('patchpol.siteScope', { name: sites.find((s) => s.id === a.scope_id)?.name || a.scope_id });
    if (a.scope_type === 'agent') return devMap[a.scope_id]?.hostname || a.scope_id;
    return a.scope_type + ': ' + a.scope_id;
  };
  // Searchable pickers: a fleet can have hundreds of clients, sites and devices.
  const scopeOptions = [
    { value: 'global:', label: t('pol.globalOption'), group: t('pol.group.global') },
    ...clients.map((c) => ({ value: 'client:' + c.id, label: c.name, group: t('patchpol.clients') })),
    ...sites.map((s) => ({ value: 'site:' + s.id, label: s.name, group: t('patchpol.sites') })),
    ...Object.values(devMap).map((d) => ({ value: 'agent:' + d.id, label: d.hostname, group: t('pol.group.agents') })),
  ];
  const excludable = Object.values(devMap).filter((d) => !exclusions.some((e) => e.agent_id === d.id))
    .map((d) => ({ value: d.id, label: d.hostname, group: t('pol.group.agents') }));
  const add = async () => {
    if (!val) return; const [scope_type, scope_id] = val.split(':');
    try { await api.addPatchAssignment(policyId, { scope_type, scope_id: scope_id || '', os, role }); setVal(''); setOs(''); setRole(''); window.dataBus.emit('patch-assign'); } catch (e) { toast.error(t('common.failed'), e.message); }
  };
  const exclude = async () => {
    if (!excluded) return;
    try { await api.addPatchPolicyExclusion(policyId, excluded); setExcluded(''); window.dataBus.emit('patch-assign'); } catch (e) { toast.error(t('common.failed'), e.message); }
  };
  const include = async (agentId) => {
    try { await api.removePatchPolicyExclusion(policyId, agentId); window.dataBus.emit('patch-assign'); } catch (e) { toast.error(t('common.failed'), e.message); }
  };
  const remove = async (a) => { try { await api.deletePatchAssignment(a.id); window.dataBus.emit('patch-assign'); } catch (e) { toast.error(t('common.failed'), e.message); } };
  return (
    <Field label={t('patchpol.assignedTo')} hint={t('patchpol.assignedHint')}>
      <div className="space-y-1.5">
        {assigns.map((a) => (
          <div key={a.id} className="flex items-center gap-2 text-[12.5px] bg-elevated/50 rounded-lg px-2.5 py-1.5">
            <Icon name="building" size={13} className="text-faint" /><span className="flex-1 text-ink flex flex-wrap items-center gap-1.5">{label(a)}<TargetChips os={a.os} role={a.role} /></span>
            <button onClick={() => remove(a)} className="text-faint hover:text-bad"><Icon name="close" size={14} /></button>
          </div>
        ))}
        <div className="flex flex-wrap gap-2">
          <Combobox value={val} onChange={setVal} className="flex-1 min-w-[200px]" placeholder={t('patchpol.addAssignment')} options={scopeOptions} />
          <TargetSelects os={os} role={role} onOs={setOs} onRole={setRole} />
          <Button variant="secondary" onClick={add} disabled={!val}>{t('action.add')}</Button>
        </div>
        <div className="pt-2 text-[12px] text-muted">{t('pol.exclusionsInfo')}</div>
        {exclusions.map((e) => (
          <div key={e.agent_id} className="flex items-center gap-2 text-[12.5px] bg-elevated/50 rounded-lg px-2.5 py-1.5">
            <Icon name="close" size={13} className="text-faint" /><span className="flex-1 text-ink">{e.hostname || devMap[e.agent_id]?.hostname || e.agent_id}</span>
            <button onClick={() => include(e.agent_id)} aria-label={t('pol.includeBack')} className="text-faint hover:text-ink"><Icon name="close" size={14} /></button>
          </div>
        ))}
        <div className="flex gap-2">
          <Combobox value={excluded} onChange={setExcluded} className="flex-1" placeholder={t('pol.addExclusion')} options={excludable} />
          <Button variant="secondary" onClick={exclude} disabled={!excluded}>{t('pol.exclude')}</Button>
        </div>
      </div>
    </Field>
  );
}

window.PatchesScreen = PatchesScreen;
window.ClassBadge = ClassBadge;
window.ComplianceBadge = ComplianceBadge;
