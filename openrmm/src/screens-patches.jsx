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
  return <span className={cx('inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-wide', PATCH_CLASS_STYLE[c] || PATCH_CLASS_STYLE.other)}>{c}</span>;
}

const COMPLIANCE_STYLE = {
  compliant: { c: 'text-ok bg-oksoft border-ok/25', label: 'Compliant' },
  at_risk: { c: 'text-warn bg-warnsoft border-warn/25', label: 'At risk' },
  critical: { c: 'text-bad bg-badsoft border-bad/25', label: 'Critical' },
  unknown: { c: 'text-faint bg-elevated border-line2', label: 'Not scanned' },
};
function ComplianceBadge({ s }) {
  const m = COMPLIANCE_STYLE[s] || COMPLIANCE_STYLE.unknown;
  return <span className={cx('inline-flex items-center gap-1.5 rounded-full border px-2 py-[3px] text-[11.5px] font-medium', m.c)}>{m.label}</span>;
}

const PATCH_CLASSES = ['security', 'critical', 'important', 'moderate', 'optional', 'other'];
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

function PatchesScreen({ navigate }) {
  const [tab, setTab] = useState('compliance');
  return (
    <div className="p-5 max-w-[1400px] mx-auto">
      <div className="mb-4">
        <h1 className="text-[18px] font-semibold text-ink">Patches</h1>
        <p className="text-[13px] text-muted mt-0.5">Inventory, approve and roll out OS updates across the fleet — with maintenance windows, auto-approval and reboot orchestration.</p>
      </div>
      <Tabs value={tab} onChange={setTab} className="mb-4" tabs={[
        { value: 'compliance', label: 'Compliance' },
        { value: 'catalog', label: 'Catalog' },
        { value: 'policies', label: 'Policies' },
      ]} />
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
  const scan = async (id) => { try { await api.scanDevicePatches(id); toast.info('Scan requested', 'Results arrive shortly.'); } catch (e) { toast.error('Scan failed', e.message); } };
  const scanAll = async () => { for (const r of rows) { try { await api.scanDevicePatches(r.agent_id); } catch (e) { /* offline */ } } toast.info('Scan requested for online devices'); };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-4 h-12 border-b border-line">
        <div className="relative flex-1 max-w-xs">
          <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Filter devices…" className="pl-9 h-8" />
        </div>
        <div className="flex-1" />
        {can('tech') && <Button size="sm" variant="secondary" icon="refresh" onClick={scanAll}>Scan all</Button>}
      </div>
      {q.loading ? <div className="p-4 space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}</div>
        : !rows.length ? <EmptyState icon="shield" title="No devices" msg="No in-scope devices have reported patch state yet." />
          : (
            <div className="overflow-auto scroll-area max-h-[calc(100vh-260px)]">
              <table className="w-full text-[12.5px] border-collapse">
                <thead className="sticky top-0 bg-elevated/95 backdrop-blur text-left text-faint">
                  <tr>{['Host', 'OS', 'Status', 'Missing', 'Security', 'Failed', 'Reboot', 'Last scan', ''].map((h) => <th key={h} className="font-medium px-4 py-2.5 whitespace-nowrap border-b border-line">{h}</th>)}</tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr key={r.agent_id} className="border-b border-line/60 hover:bg-white/[0.02]">
                      <td className="px-4 py-2 text-ink font-medium">{r.hostname}</td>
                      <td className="px-4 py-2"><span className="inline-flex items-center gap-1.5"><OsIcon os={r.os} />{r.os}</span></td>
                      <td className="px-4 py-2"><ComplianceBadge s={r.status} /></td>
                      <td className="px-4 py-2 tabular">{r.missing_total}</td>
                      <td className={cx('px-4 py-2 tabular', r.missing_security > 0 && 'text-bad font-semibold')}>{r.missing_security}</td>
                      <td className={cx('px-4 py-2 tabular', r.failed > 0 && 'text-bad')}>{r.failed}</td>
                      <td className="px-4 py-2">{r.reboot_pending ? <span className="text-warn inline-flex items-center gap-1"><Icon name="refresh" size={12} />pending</span> : <span className="text-faint">—</span>}</td>
                      <td className="px-4 py-2 text-faint whitespace-nowrap">{r.last_scan ? timeAgo(r.last_scan) : 'never'}</td>
                      <td className="px-4 py-2 text-right">{can('tech') && <Button size="sm" variant="ghost" icon="refresh" onClick={() => scan(r.agent_id)}>Scan</Button>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
    </Card>
  );
}

function CatalogTab() {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery(['catalog'], () => api.patchCatalog(), { invalidateOn: ['patches'] });
  const [search, setSearch] = useState('');
  const rows = (q.data?.patches || []).filter((p) => !search || (p.title || '').toLowerCase().includes(search.toLowerCase()) || (p.identifier || '').toLowerCase().includes(search.toLowerCase()));
  const decide = async (p, decision) => {
    try { await api.approvePatch(p.id, { scope_type: '', scope_id: '', decision }); toast.success(decision === 'approve' ? 'Patch approved (fleet-wide)' : 'Patch declined'); window.dataBus.emit('patches'); }
    catch (e) { toast.error('Failed', e.message); }
  };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-4 h-12 border-b border-line">
        <div className="relative flex-1 max-w-xs">
          <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Filter patches…" className="pl-9 h-8" />
        </div>
      </div>
      {q.loading ? <div className="p-4 space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}</div>
        : !rows.length ? <EmptyState icon="shield" title="No patches" msg="No missing patches in scope. Run a scan from the Compliance tab." />
          : (
            <div className="overflow-auto scroll-area max-h-[calc(100vh-260px)]">
              <table className="w-full text-[12.5px] border-collapse">
                <thead className="sticky top-0 bg-elevated/95 backdrop-blur text-left text-faint">
                  <tr>{['Class', 'Title', 'OS', 'KB', 'Devices', 'Approved', 'Reboot', ''].map((h) => <th key={h} className="font-medium px-4 py-2.5 whitespace-nowrap border-b border-line">{h}</th>)}</tr>
                </thead>
                <tbody>
                  {rows.map((p) => (
                    <tr key={p.id} className="border-b border-line/60 hover:bg-white/[0.02]">
                      <td className="px-4 py-2"><ClassBadge c={p.classification} /></td>
                      <td className="px-4 py-2 text-ink max-w-[420px] truncate" title={p.title}>{p.title || p.identifier}</td>
                      <td className="px-4 py-2"><OsIcon os={p.os} /></td>
                      <td className="px-4 py-2 mono text-muted">{p.kb || '—'}</td>
                      <td className="px-4 py-2 tabular">{p.missing_count}</td>
                      <td className="px-4 py-2 tabular text-ok">{p.approved_count || ''}</td>
                      <td className="px-4 py-2">{p.reboot_required ? <Icon name="refresh" size={13} className="text-warn" /> : <span className="text-faint">—</span>}</td>
                      <td className="px-4 py-2 text-right">
                        {can('tech') && (
                          <span className="inline-flex gap-1.5">
                            <Button size="sm" variant="success" icon="approve" onClick={() => decide(p, 'approve')}>Approve</Button>
                            <Button size="sm" variant="ghost" icon="reject" onClick={() => decide(p, 'decline')}>Decline</Button>
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
  const pols = q.data?.policies || [];
  const del = async (p) => { try { await api.deletePatchPolicy(p.id); window.dataBus.emit('patch-pol'); toast.info('Policy deleted'); } catch (e) { toast.error('Failed', e.message); } };
  return (
    <div className="space-y-3">
      {can('admin') && <div className="flex justify-end"><Button variant="primary" icon="plus" onClick={() => setEditing({})}>New policy</Button></div>}
      {q.loading ? <Card className="p-4"><Skeleton className="h-16 w-full" /></Card>
        : !pols.length ? <Card><EmptyState icon="policy" title="No patch policies" msg="Create one to auto-approve patches and define maintenance windows." /></Card>
          : pols.map((p) => (
            <Card key={p.id} className="p-4">
              <div className="flex items-start gap-3">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-[14px] font-semibold text-ink">{p.name}</span>
                    {!p.enabled && <span className="text-[10.5px] uppercase text-faint border border-line2 rounded px-1.5 py-0.5">disabled</span>}
                  </div>
                  <div className="text-[12px] text-muted mt-1.5 flex flex-wrap gap-x-4 gap-y-1">
                    <span>Auto-approve: {p.auto_approve?.length ? p.auto_approve.join(', ') : '—'} {p.auto_approve?.length ? `(after ${p.auto_approve_age_days}d)` : ''}</span>
                    <span>Window: {p.window_days?.length ? p.window_days.map((d) => WEEKDAYS[d]).join(',') : 'any day'} {minuteToHHMM(p.window_start_min)}–{minuteToHHMM(p.window_end_min)}</span>
                    <span>Reboot: {p.reboot}</span>
                    {p.exclusions?.length ? <span>Excludes: {p.exclusions.join(', ')}</span> : null}
                  </div>
                </div>
                {can('admin') && (
                  <div className="flex gap-1.5 shrink-0">
                    <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing(p)}>Edit</Button>
                    <Button size="sm" variant="ghost" icon="trash" onClick={() => del(p)} />
                  </div>
                )}
              </div>
            </Card>
          ))}
      {editing && <PolicyModal policy={editing} onClose={() => setEditing(null)} />}
    </div>
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
  const [busy, setBusy] = useState(false);
  const toMin = (hhmm) => { const [h, m] = (hhmm || '0:0').split(':').map(Number); return (h || 0) * 60 + (m || 0); };
  const toggle = (arr, v, set) => set(arr.includes(v) ? arr.filter((x) => x !== v) : [...arr, v]);
  const save = async () => {
    setBusy(true);
    const body = {
      name, enabled, auto_approve: auto, auto_approve_age_days: Number(age),
      exclusions: excl.split(',').map((s) => s.trim()).filter(Boolean),
      window_days: days, window_start_min: toMin(start), window_end_min: toMin(end), reboot,
    };
    try {
      if (isNew) await api.createPatchPolicy(body); else await api.updatePatchPolicy(policy.id, body);
      window.dataBus.emit('patch-pol'); toast.success('Policy saved'); onClose();
    } catch (e) { toast.error('Save failed', e.message); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={onClose} width="max-w-2xl">
      <div className="p-5 space-y-4 max-h-[85vh] overflow-auto scroll-area">
        <div className="text-[15px] font-semibold text-ink">{isNew ? 'New patch policy' : 'Edit patch policy'}</div>
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Monthly security patching" autoFocus /></Field>
        <label className="flex items-center gap-2 text-[13px] text-ink"><Toggle checked={enabled} onChange={setEnabled} />Enabled</label>

        <Field label="Auto-approve classifications" hint="Patches of these classes are approved automatically once old enough.">
          <div className="flex flex-wrap gap-1.5">
            {PATCH_CLASSES.map((c) => (
              <button key={c} type="button" onClick={() => toggle(auto, c, setAuto)}
                className={cx('px-2.5 py-1 rounded-lg border text-[12px] capitalize transition', auto.includes(c) ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>{c}</button>
            ))}
          </div>
        </Field>
        <Field label="Auto-approve after (days)"><Input type="number" min="0" value={age} onChange={(e) => setAge(e.target.value)} className="w-32" /></Field>

        <Field label="Maintenance window — days" hint="Empty = any day.">
          <div className="flex flex-wrap gap-1.5">
            {WEEKDAYS.map((d, i) => (
              <button key={d} type="button" onClick={() => toggle(days, i, setDays)}
                className={cx('px-2.5 py-1 rounded-lg border text-[12px] transition', days.includes(i) ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>{d}</button>
            ))}
          </div>
        </Field>
        <div className="flex gap-3">
          <Field label="Window start" className="flex-1"><Input type="time" value={start} onChange={(e) => setStart(e.target.value)} /></Field>
          <Field label="Window end" className="flex-1"><Input type="time" value={end} onChange={(e) => setEnd(e.target.value)} /></Field>
          <Field label="Reboot" className="flex-1"><Select value={reboot} onChange={(e) => setReboot(e.target.value)}>
            <option value="never">never</option><option value="if_required">if required</option><option value="always">always</option>
          </Select></Field>
        </div>
        <Field label="Exclusions" hint="Comma-separated patch identifiers or classifications to never install."><Input value={excl} onChange={(e) => setExcl(e.target.value)} placeholder="KB5001234, optional" /></Field>

        {!isNew && <PolicyAssignments policyId={policy.id} />}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={save}>Save policy</Button>
        </div>
      </div>
    </Modal>
  );
}

function PolicyAssignments({ policyId }) {
  const toast = useToast();
  const q = useQuery(['patch-assign', policyId], () => api.listPatchAssignments(policyId), { invalidateOn: ['patch-assign'] });
  const clientsQ = useQuery(['clients'], () => api.listClients(), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['sites'], () => api.listSites(), { invalidateOn: ['org'] });
  const [val, setVal] = useState('');
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const assigns = q.data?.assignments || [];
  const label = (a) => {
    if (a.scope_type === 'client') return 'Client: ' + (clients.find((c) => c.id === a.scope_id)?.name || a.scope_id);
    if (a.scope_type === 'site') return 'Site: ' + (sites.find((s) => s.id === a.scope_id)?.name || a.scope_id);
    return a.scope_type + ': ' + a.scope_id;
  };
  const add = async () => {
    if (!val) return; const [scope_type, scope_id] = val.split(':');
    try { await api.addPatchAssignment(policyId, { scope_type, scope_id }); setVal(''); window.dataBus.emit('patch-assign'); } catch (e) { toast.error('Failed', e.message); }
  };
  const remove = async (a) => { try { await api.deletePatchAssignment(a.id); window.dataBus.emit('patch-assign'); } catch (e) { toast.error('Failed', e.message); } };
  return (
    <Field label="Assigned to" hint="Agents inherit this policy via their client/site.">
      <div className="space-y-1.5">
        {assigns.map((a) => (
          <div key={a.id} className="flex items-center gap-2 text-[12.5px] bg-elevated/50 rounded-lg px-2.5 py-1.5">
            <Icon name="building" size={13} className="text-faint" /><span className="flex-1 text-ink">{label(a)}</span>
            <button onClick={() => remove(a)} className="text-faint hover:text-bad"><Icon name="close" size={14} /></button>
          </div>
        ))}
        <div className="flex gap-2">
          <Select value={val} onChange={(e) => setVal(e.target.value)} className="flex-1">
            <option value="">Add assignment…</option>
            <optgroup label="Clients">{clients.map((c) => <option key={c.id} value={'client:' + c.id}>{c.name}</option>)}</optgroup>
            <optgroup label="Sites">{sites.map((s) => <option key={s.id} value={'site:' + s.id}>{s.name}</option>)}</optgroup>
          </Select>
          <Button variant="secondary" onClick={add} disabled={!val}>Add</Button>
        </div>
      </div>
    </Field>
  );
}

window.PatchesScreen = PatchesScreen;
window.ClassBadge = ClassBadge;
window.ComplianceBadge = ComplianceBadge;
