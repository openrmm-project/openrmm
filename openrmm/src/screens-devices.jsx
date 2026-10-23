/* screens-devices.jsx — Devices list + Device detail drawer */

// Who is on an endpoint right now. Simultaneous sessions stay allowed — one
// technician watching while another works is a legitimate hand-off — so this
// only ever announces; it never blocks. Two people each believing they are
// alone is the failure worth preventing.
function useRemotePresence() {
  const { can } = useAuth();
  const q = useQuery(['remote-presence'], () => (can('devices.view') ? api.listRemotePresence() : Promise.resolve({ viewers: [] })), { poll: 10000 });
  const byAgent = {};
  // The name comes resolved from the server. Looking it up here would have made
  // the banner depend on permission to list users, which most technicians do
  // not have — and the banner then read as a raw user id.
  (q.data?.viewers || []).forEach((v) => {
    // A guest is named as one, with the technician supervising them: an
    // outsider on the screen must not read as a colleague.
    const name = v.guest ? t('presence.guest', { name: v.user_name, sponsor: v.sponsor_name || t('presence.someone') }) : (v.user_name || v.user || t('presence.someone'));
    (byAgent[v.agent_id] = byAgent[v.agent_id] || []).push({ ...v, name });
  });
  return byAgent;
}

const PRESENCE_KINDS = ['native', 'files', 'terminal'];

function presenceSummary(viewers) {
  if (!viewers || !viewers.length) return '';
  const who = viewers.map((v) => v.name + ' (' + (PRESENCE_KINDS.includes(v.kind) ? t('presence.kind.' + v.kind) : v.kind) + ')');
  return t(who.length === 1 ? 'presence.one' : 'presence.many', { who: formatList(who) });
}

function DevicesScreen({ navigate }) {
  const devicesQ = useQuery(['devices'], () => api.listDevices(), { invalidateOn: ['devices'], poll: 10000 });
  const { can } = useAuth();
  const sitesQ = useQuery(['sites-all'], () => can('clients.view') ? api.listSites() : Promise.resolve({ sites: [] }), { invalidateOn: ['org'] });
  const clientsQ = useQuery(['clients-all'], () => can('clients.view') ? api.listClients() : Promise.resolve({ clients: [] }), { invalidateOn: ['org'] });
  const [q, setQ] = useState('');
  const [osF, setOsF] = useState('all');
  const [statusF, setStatusF] = useState('all');
  const [clientF, setClientF] = useState('all');
  const [selected, setSelected] = useState(null);
  const [bulkOpen, setBulkOpen] = useState(false);
  const presence = useRemotePresence();
  // Department names for the pending-operations panel. Moving and deleting
  // devices themselves happen in Clients & Sites, not here.
  const groupsQ = useQuery(['groups'], () => can('clients.view') ? api.listGroups() : Promise.resolve({ groups: [] }), { invalidateOn: ['groups', 'org'] });

  const devices = devicesQ.data?.devices || [];
  const groups = groupsQ.data?.groups || [];
  const sites = sitesQ.data?.sites || [];
  const clients = clientsQ.data?.clients || [];
  const siteName = (id) => sites.find((s) => s.id === id)?.name || '—';
  const clientOfSite = (id) => sites.find((s) => s.id === id)?.client_id || '';
  // A device belongs to a client (company) through its site; unassigned devices
  // and sites whose client is gone both fall back to the em dash.
  const clientName = (siteId) => clients.find((c) => c.id === clientOfSite(siteId))?.name || '—';

  const filtered = devices.filter((d) => {
    if (q && !(d.hostname.toLowerCase().includes(q.toLowerCase()) || (d.description || '').toLowerCase().includes(q.toLowerCase()) || d.tags.some((tag) => tag.includes(q.toLowerCase())))) return false;
    if (osF !== 'all' && window.osFamily(d.os) !== osF) return false;
    if (statusF !== 'all' && d.status !== statusF) return false;
    if (clientF !== 'all' && clientOfSite(d.site_id) !== clientF) return false;
    return true;
  });

  return (
    <Page wide>
      <PageHeader title={t('title.devices')} subtitle={tn('devices.agentCount', devices.length) + ' · ' + t('devices.onlineCount', { n: devices.filter((d) => d.status === 'online').length })}
        actions={can('jobs.create') && <Button icon="flash" onClick={() => setBulkOpen(true)}>{t('devices.deployMany')}</Button>} />

      <Card className="mb-4">
        <div className="flex flex-wrap items-center gap-2 p-2.5">
          <div className="relative flex-1 min-w-[220px]">
            <Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
            <input value={q} onChange={(e) => setQ(e.target.value)}  placeholder={t('devices.searchPlaceholder')} className={cx(inputCls, 'pl-9')} />
          </div>
          <Select value={osF} onChange={(e) => setOsF(e.target.value)} className="w-auto min-w-[120px]">
            <option value="all">{t('devices.filter.allOs')}</option><option>Windows</option><option>macOS</option><option>Linux</option>
          </Select>
          <Select value={statusF} onChange={(e) => setStatusF(e.target.value)} className="w-auto min-w-[120px]">
            <option value="all">{t('devices.filter.anyStatus')}</option><option value="online">{t('status.online')}</option><option value="offline">{t('status.offline')}</option>
          </Select>
          <Select value={clientF} onChange={(e) => setClientF(e.target.value)} className="w-auto min-w-[150px]">
            <option value="all">{t('devices.filter.allClients')}</option>
            {clients.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
        </div>
      </Card>

      <PendingDeviceOperations devices={devices} sites={sites} groups={groups} />

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
              <th className="font-semibold px-4 py-2.5">{t('devices.col.hostname')}</th>
              <th className="font-semibold px-3 py-2.5">{t('common.description')}</th>
              <th className="font-semibold px-3 py-2.5">{t('devices.col.os')}</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('devices.col.client')}</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">{t('devices.col.site')}</th>
              <th className="font-semibold px-3 py-2.5 hidden lg:table-cell">{t('devices.col.tags')}</th>
              <th className="font-semibold px-3 py-2.5">{t('devices.col.status')}</th>
              <th className="font-semibold px-4 py-2.5 text-right">{t('devices.col.lastSeen')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {devicesQ.loading && Array.from({ length: 6 }).map((_, i) => (
              <tr key={i}><td className="px-4 py-3" colSpan={8}><Skeleton className="h-5 w-full" /></td></tr>
            ))}
            {!devicesQ.loading && filtered.map((d) => (
              <tr key={d.id} onClick={() => setSelected(d.id)} className="cursor-pointer hover:bg-elevated/50 transition group">
                <td className="px-4 py-3">
                  <div className="flex items-center gap-2.5">
                    <span className={cx('h-2 w-2 rounded-full shrink-0', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
                    <div className="text-[13px] font-medium text-ink truncate group-hover:text-brandhi transition-colors">{d.hostname}</div>
                  </div>
                </td>
                <td className="px-3 py-3 text-[12.5px] truncate max-w-[220px]">{d.description ? <span className="text-ink">{d.description}</span> : <span className="text-faint">—</span>}</td>
                <td className="px-3 py-3"><div className="flex items-center gap-2 text-[12.5px] text-muted"><OsIcon os={d.os} size={15} /><span className="truncate max-w-[170px]" title={d.os}>{window.osLabel(d.os)}</span><span className="text-faint text-[11px] mono">{d.arch}</span>{presence[d.id]?.length ? <Tip label={presenceSummary(presence[d.id])}><span className="inline-flex items-center gap-1 text-[11px] text-brandhi border border-brandhi/40 rounded px-1.5 py-0.5"><Icon name="remote" size={11} />{presence[d.id].length > 1 ? presence[d.id].length : t('presence.inUse')}</span></Tip> : null}</div></td>
                <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted truncate max-w-[180px]">{clientName(d.site_id)}</td>
                <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{siteName(d.site_id)}</td>
                <td className="px-3 py-3 hidden lg:table-cell">
                  <div className="flex gap-1 flex-wrap max-w-[200px]">{d.tags.slice(0, 3).map((tag) => <span key={tag} className="rounded-md bg-elevated border border-line text-[10.5px] text-muted px-1.5 py-0.5">{tag}</span>)}</div>
                </td>
                <td className="px-3 py-3"><StatusPill status={d.status} size="sm" /></td>
                <td className="px-4 py-3 text-right text-[12px] text-muted tabular">{timeAgo(d.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!devicesQ.loading && filtered.length === 0 && <EmptyState icon="devices" title={t('devices.empty.title')} msg={t('devices.empty.msg')} />}
      </Card>

      <DeviceDetail id={selected} onClose={() => setSelected(null)} navigate={navigate} sites={sites} clients={clients} />
      <BulkDeployModal open={bulkOpen} onClose={() => setBulkOpen(false)} devices={devices} sites={sites} clients={clients} navigate={navigate} />
    </Page>
  );
}

// PendingDeviceOperations lists the moves and deletions the AI assistant or an
// MCP client has proposed. Nothing in them has happened: a person reviews each
// one here, and approving runs it with that person's permissions and scope.
function PendingDeviceOperations({ devices, sites, groups }) {
  const { can } = useAuth();
  // A server without the feature, or a caller without access, simply shows
  // nothing: this panel must never be what breaks the Devices screen.
  const opsQ = useQuery(['device-ops'], () => api.listDeviceOperations().catch(() => ({ operations: [] })), { invalidateOn: ['devices', 'device-ops'], poll: 15000 });
  const [review, setReview] = useState(null);
  const pending = window.DeviceBulk.awaiting(opsQ.data?.operations);
  if (!pending.length) return null;
  const names = {
    site: (id) => sites.find((s) => s.id === id)?.name,
    group: (id) => groups.find((g) => g.id === id)?.name,
  };
  return (
    <Card className="mb-3 border-warn/40">
      <div className="px-4 py-2.5 border-b border-line flex items-center gap-2">
        <Icon name="sparkle" size={15} className="text-warn" />
        <span className="text-[13px] font-semibold text-ink">{t('devices.pending.title')}</span>
        <span className="text-[12px] text-muted">{t('devices.pending.hint')}</span>
      </div>
      <div className="divide-y divide-line">
        {pending.map((op) => (
          <div key={op.id} className="flex items-center gap-3 px-4 py-2.5">
            <Icon name={op.kind === 'delete' ? 'trash' : 'building'} size={15} className={op.kind === 'delete' ? 'text-bad' : 'text-brandhi'} />
            <div className="flex-1 min-w-0">
              <div className="text-[13px] text-ink truncate">{window.DeviceBulk.describe(op, names)}</div>
              <div className="text-[11.5px] text-faint truncate">{window.DeviceBulk.proposer(op.proposed_by)} · {timeAgo(op.created_at)}{op.description ? ' · ' + op.description : ''}</div>
            </div>
            <Button size="sm" onClick={() => setReview(op)}>{t('action.review')}</Button>
          </div>
        ))}
      </div>
      <DeviceOperationReview op={review} onClose={() => setReview(null)} devices={devices} names={names} canDecide={review ? can(window.DeviceBulk.decidePermission(review)) : false} />
    </Card>
  );
}

function DeviceOperationReview({ op, onClose, devices, names, canDecide }) {
  const toast = useToast();
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { setTyped(''); }, [op && op.id]);
  if (!op) return null;
  const isDelete = op.kind === 'delete';
  const n = op.agent_ids.length;
  const listed = op.agent_ids.map((id) => devices.find((d) => d.id === id)).filter(Boolean);
  const missing = n - listed.length;
  const confirmed = !isDelete || window.DeviceBulk.confirmMatches(typed, n);
  const decide = async (approve) => {
    setBusy(true);
    try {
      if (approve) {
        const r = await api.approveDeviceOperation(op.id, isDelete ? n : undefined);
        if (r.failed && r.failed.length) toast.error(t('devices.review.partly'), t('devices.review.notDeleted', { list: formatList(r.failed.map((f) => f.hostname || f.agent_id)) }));
        else toast.success(t('devices.review.approved'), window.DeviceBulk.describe(op, names));
      } else {
        await api.rejectDeviceOperation(op.id);
        toast.info(t('devices.review.rejected'), t('devices.review.nothingChanged'));
      }
      window.dataBus.emit('device-ops'); window.dataBus.emit('devices');
      onClose();
    } catch (e) {
      toast.error(approve ? t('devices.review.notApproved') : t('devices.review.notRejected'), e.message);
      window.dataBus.emit('device-ops');
    } finally { setBusy(false); }
  };
  return (
    <Modal open={!!op} onClose={onClose} width="max-w-lg">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className={cx('h-9 w-9 rounded-xl flex items-center justify-center shrink-0', isDelete ? 'bg-badsoft text-bad' : 'bg-brandsoft text-brandhi')}><Icon name={isDelete ? 'trash' : 'building'} size={18} /></div>
          <div className="min-w-0">
            <div className="text-[15px] font-semibold text-ink">{window.DeviceBulk.describe(op, names)}</div>
            <div className="text-[12.5px] text-muted">{window.DeviceBulk.proposer(op.proposed_by)} · {timeAgo(op.created_at)}</div>
          </div>
        </div>
        {op.description && <div className="rounded-lg border border-line bg-elevated/40 px-3 py-2 text-[12.5px] text-ink">{op.description}</div>}
        <div className="rounded-xl border border-line max-h-56 overflow-y-auto scroll-area divide-y divide-line">
          {listed.map((d) => (
            <div key={d.id} className="flex items-center gap-2 px-3 py-1.5 text-[12.5px]">
              <span className={cx('h-2 w-2 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
              <span className="text-ink flex-1 truncate">{d.hostname}</span>
              <span className="text-faint">{names.site(d.site_id) || t('devices.unassigned')}</span>
            </div>
          ))}
          {missing > 0 && <div className="px-3 py-1.5 text-[12px] text-faint">{t('devices.review.missing', { devices: window.DeviceBulk.countLabel(missing) })}</div>}
        </div>
        {isDelete && <div className="text-[12.5px] text-muted">{t('devices.review.deleteWarning')}</div>}
        {!canDecide && <div className="text-[12.5px] text-warn">{t('devices.review.needsPerm', { perm: window.DeviceBulk.decidePermission(op) })}</div>}
        {canDecide && isDelete && (
          <Field label={t('ui.typeToConfirm', { word: n })}><Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={String(n)} autoFocus /></Field>
        )}
        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={busy}>{t('action.close')}</Button>
          {canDecide && <Button variant="ghost" onClick={() => decide(false)} disabled={busy}>{t('action.reject')}</Button>}
          {canDecide && <Button variant={isDelete ? 'danger' : 'primary'} loading={busy} disabled={!confirmed} onClick={() => decide(true)}>{isDelete ? t('devices.review.approveDelete') : t('devices.review.approveMove')}</Button>}
        </div>
      </div>
    </Modal>
  );
}

// BulkDeployModal creates ONE approval-gated batch across many devices. Targets
// are picked client-side (transparent review) — multi-select + "add a whole
// site" — and sent as agent_ids; the batch awaits approval in Jobs & Approvals.
function BulkDeployModal({ open, onClose, devices, sites, clients, navigate }) {
  const { can } = useAuth();
  const toast = useToast();
  const groupsQ = useQuery(['groups'], () => can('clients.view') ? api.listGroups() : Promise.resolve({ groups: [] }), {});
  const groups = groupsQ.data?.groups || [];
  const [sel, setSel] = useState({});
  const [q, setQ] = useState('');
  const [companyF, setCompanyF] = useState('');
  const [siteF, setSiteF] = useState('');
  const [deptF, setDeptF] = useState('');
  const [shell, setShell] = useState('bash');
  const [script, setScript] = useState('');
  const [desc, setDesc] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (open) { setSel({}); setQ(''); setCompanyF(''); setSiteF(''); setDeptF(''); setScript(''); setDesc(''); setShell('bash'); } }, [open]);

  const clientOfSite = (sid) => (sites.find((s) => s.id === sid) || {}).client_id;
  const companyOf = (d) => clientOfSite(d.site_id);
  const ids = Object.keys(sel).filter((k) => sel[k]);
  const toggle = (id) => setSel((s) => ({ ...s, [id]: !s[id] }));
  const filtered = devices.filter((d) =>
    (!q || d.hostname.toLowerCase().includes(q.toLowerCase()) || (d.description || '').toLowerCase().includes(q.toLowerCase())) &&
    (!companyF || companyOf(d) === companyF) &&
    (!siteF || d.site_id === siteF) &&
    (!deptF || d.group_id === deptF));
  const selectAll = () => { const n = { ...sel }; filtered.forEach((d) => { n[d.id] = true; }); setSel(n); };
  const selCompanies = new Set(devices.filter((d) => sel[d.id]).map((d) => companyOf(d) || '∅')).size;

  const submit = async () => {
    if (!ids.length || !script.trim()) return;
    setBusy(true);
    try {
      const r = await api.createBulkJob({ agent_ids: ids, shell, script, description: desc || t('deploy.defaultDesc', { devices: tn('bulk.devices', ids.length) }) });
      toast.success(t('deploy.created'), r.companies > 1 ? t('deploy.createdSplit', { jobs: tn('bulk.jobs', r.count), companies: r.companies }) : t('deploy.createdOne', { jobs: tn('bulk.jobs', r.count) }));
      onClose();
      navigate && navigate('jobs');
    } catch (e) { toast.error(t('deploy.failed'), e.message); } finally { setBusy(false); }
  };

  return (
    <Modal open={open} onClose={onClose} width="max-w-3xl">
      <div className="p-5 space-y-4">
        <div className="flex items-center gap-2.5">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="flash" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">{t('deploy.title')}</div><div className="text-[12.5px] text-muted">{t('deploy.subtitle')}</div></div>
        </div>
        <div className="grid md:grid-cols-2 gap-4">
          <div className="space-y-2">
            <div className="grid grid-cols-3 gap-1.5">
              <Select value={companyF} onChange={(e) => { setCompanyF(e.target.value); setSiteF(''); setDeptF(''); }} className="h-8 text-[12px]"><option value="">{t('deploy.allCompanies')}</option>{clients.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</Select>
              <Select value={siteF} onChange={(e) => { setSiteF(e.target.value); setDeptF(''); }} className="h-8 text-[12px]"><option value="">{t('deploy.allSites')}</option>{sites.filter((s) => !companyF || s.client_id === companyF).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select>
              <Select value={deptF} onChange={(e) => setDeptF(e.target.value)} disabled={!siteF} className="h-8 text-[12px]"><option value="">{siteF ? t('deploy.allDepts') : t('deploy.pickSite')}</option>{groups.filter((g) => g.site_id === siteF).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</Select>
            </div>
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('deploy.filterHost')} className="h-8" />
            <div className="flex items-center justify-between text-[11.5px] text-faint"><span>{t('deploy.selection', { selected: ids.length, companies: tn('deploy.companies', selCompanies), batches: tn('deploy.batches', selCompanies) })}</span><span className="flex gap-3"><button onClick={selectAll} className="hover:text-ink">{t('action.selectAll')}</button><button onClick={() => setSel({})} className="hover:text-ink">{t('action.clear')}</button></span></div>
            <div className="rounded-xl border border-line max-h-60 overflow-y-auto scroll-area divide-y divide-line">
              {filtered.map((d) => (
                <label key={d.id} className="flex items-center gap-2 px-3 py-2 text-[12.5px] cursor-pointer hover:bg-elevated/40">
                  <input type="checkbox" checked={!!sel[d.id]} onChange={() => toggle(d.id)} />
                  <span className={cx('h-1.5 w-1.5 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
                  <OsIcon os={d.os} size={13} /><span className="text-ink truncate flex-1">{d.hostname}</span>
                  {d.status !== 'online' && <span className="text-[10px] text-faint">{t('status.offline')}</span>}
                </label>
              ))}
              {filtered.length === 0 && <div className="px-3 py-6 text-center text-[12px] text-faint">{t('deploy.noDevices')}</div>}
            </div>
          </div>
          <div className="space-y-3">
            <Field label={t('deploy.shell')}><Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>
            <Field label={t('common.description')}><Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t('deploy.descPlaceholder')} /></Field>
            <Field label={t('deploy.script')}><CodeEditor value={script} onChange={setScript} shell={shell} minHeight={150} placeholder={t('deploy.scriptPlaceholder')} /></Field>
          </div>
        </div>
        <div className="flex items-center justify-between gap-3 pt-3 border-t border-line/60">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5 max-w-[60%]"><Icon name="shield" size={13} className="shrink-0" />{t('deploy.companyNote')}</div>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button>
            <Button variant="primary" icon="flash" loading={busy} disabled={!ids.length || !script.trim()} onClick={submit}>{t('deploy.create', { n: ids.length })}</Button>
          </div>
        </div>
      </div>
    </Modal>
  );
}

/* ----------------------------- Device detail ------------------------------ */
function DeviceDetail({ id, onClose, navigate, sites, clients }) {
  const { can } = useAuth();
  const toast = useToast();
  const [tab, setTab] = useState('inventory');
  useEffect(() => { setTab('inventory'); }, [id]);
  const devQ = useQuery([id, 'dev'], () => id ? api.getDevice(id) : Promise.resolve(null), { invalidateOn: ['devices'] });
  const d = devQ.data;
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const rustdeskEnabled = !!cfgQ.data?.remote?.rustdesk_enabled;
  const guestAccess = !!cfgQ.data?.remote?.guest_access;
  const meshEnabled = cfgQ.data?.remote?.meshcentral_enabled !== false;
  const capsQ = useQuery([id, 'remote-capabilities'], () => id ? api.getRemoteCapabilities(id) : Promise.resolve(null), { invalidateOn: ['devices'] });
  const nativeCaps = capsQ.data;
  const nativeUnavailable = nativeCaps?.capture === 'unavailable';
  // The agent reports why in a code (no_xorg, wayland_unsupported…); the menu
  // says it in words, and shows an unrecognised code as it came.
  const nativeHint = nativeUnavailable ? tEnum('remote.reason', nativeCaps.reason_code || 'unavailable') : (nativeCaps?.capture === 'unknown' ? t('remote.capUnknown') : t('remote.beta'));

  const [remoteBusy, setRemoteBusy] = useState(false);
  const presenceByAgent = useRemotePresence();
  const attached = presenceByAgent[id] || [];
  const [remote, setRemote] = useState(null); // { deviceId, hostname } for the native viewer
  const [term, setTerm] = useState(null);     // { deviceId, hostname } for the terminal viewer
  const [orgOpen, setOrgOpen] = useState(false);
  const [confirmDel, setConfirmDel] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const startRemote = async (provider) => {
    // Announced, never blocked: joining a colleague is sometimes the point.
    if (attached.length) toast.info(t('remote.alreadyConnected'), t('remote.alreadyConnectedTo', { who: presenceSummary(attached), host: d.hostname }));
    if (provider === 'native') { setRemote({ deviceId: id, hostname: d.hostname, os: d.os }); return; } // viewer brokers + reconnects
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, provider);
      window.open(s.connect_url, '_blank', 'noopener,noreferrer'); toast.success(t('remote.ready'), t('remote.openedTab', { provider }));
    } catch (e) { toast.error(t('remote.failed'), e.message); } finally { setRemoteBusy(false); }
  };
  // Native remote in a standalone browser tab (auth is shared via sessionStorage).
  const startRemoteNewTab = () => {
    const url = new URL(window.__BASE_PATH__ || '/', location.origin);
    url.searchParams.set('dev', id);
    url.searchParams.set('host', d.hostname);
    url.searchParams.set('os', d.os || '');
    window.open(url.toString(), '_blank');
  };
  const linkRemote = async (field, label) => {
    const v = window.prompt(t('remote.enterId', { label }));
    if (!v || !v.trim()) return;
    try { await api.setRemoteIds(id, { [field]: v.trim() }); toast.success(t('remote.linked', { label })); }
    catch (e) { toast.error(t('common.failed'), e.message); }
  };

  const groupsQ = useQuery(['groups'], () => can('clients.view') ? api.listGroups() : Promise.resolve({ groups: [] }), { invalidateOn: ['groups'] });
  const groups = groupsQ.data?.groups || [];
  const currentSite = d ? sites.find((s) => s.id === d.site_id) : null;
  const currentClient = currentSite ? clients.find((c) => c.id === currentSite.client_id) : null;
  const currentGroup = d ? groups.find((g) => g.id === d.group_id) : null;
  const orgLabel = currentSite ? [currentClient?.name, currentSite.name, currentGroup?.name].filter(Boolean).join(' / ') : t('devices.unassignedTitle');

  const saveOrg = async ({ siteId, groupId }) => {
    try {
      if ((d.group_id || '') && d.site_id !== siteId) await api.setDeviceGroup(id, null);
      if ((d.site_id || '') !== (siteId || '')) await api.setDeviceSite(id, siteId || null);
      if ((d.group_id || '') !== (groupId || '')) await api.setDeviceGroup(id, groupId || null);
      toast.success(t('org.updated')); setOrgOpen(false); devQ.refetch();
    } catch (e) { toast.error(t('org.updateFailed'), e.message); }
  };
  const deleteDevice = async () => {
    try { await api.deleteDevice(id); toast.info(t('device.deleted')); setConfirmDel(false); onClose(); window.dataBus.emit('devices'); }
    catch (e) { toast.error(t('device.deleteFailed'), e.message); }
  };

  return (
    <Drawer open={!!id} onClose={onClose} width="max-w-[860px]">
      {d && (
        <>
          <div className="px-5 pt-4 pb-4 border-b border-line">
            <div className="flex items-start gap-3">
              <div className="h-11 w-11 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted shrink-0"><OsIcon os={d.os} size={22} /></div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2.5">
                  <h2 className="text-[17px] font-semibold text-ink tracking-tight truncate">{d.hostname}</h2>
                  <StatusPill status={d.status} size="sm" pulse />
                </div>
                <div className="text-[12.5px] text-muted mt-0.5 flex items-center gap-2 flex-wrap">
                  <span title={d.os}>{window.osLabel(d.os)}</span><span className="text-faint">·</span><span className="mono text-faint">{d.arch}</span>
                  <span className="text-faint">·</span><span>{t('device.agentVersion', { version: d.version })}</span><span className="text-faint">·</span>
                  <span>{t('device.lastSeen', { when: timeAgo(d.last_seen) })}</span>
                </div>
                {attached.length > 0 && (
                  <div className="mt-2 flex items-start gap-2 rounded-lg border border-brandhi/40 bg-brandhi/10 px-2.5 py-1.5 text-[12px] text-ink">
                    <Icon name="remote" size={13} className="text-brandhi mt-0.5 shrink-0" />
                    <span>
                      {presenceSummary(attached)}.
                      <span className="text-muted"> {t('presence.since', { times: formatList(attached.map((v) => timeAgo(v.since))) })}</span>
                    </span>
                  </div>
                )}
                {d.description && <div className="text-[12px] text-faint mt-1">{d.description}</div>}
              </div>
              <button onClick={onClose} className="text-faint hover:text-ink p-1.5 -mr-1.5"><Icon name="close" size={18} /></button>
            </div>

            <div className="flex items-center gap-2 mt-4 flex-wrap">
              {(can('devices.remote') || can('devices.terminal') || can('devices.files')) && <Dropdown align="left" width="w-60" trigger={
                <Button variant="primary" icon="remote" iconRight="chevdown" loading={remoteBusy} disabled={d.status !== 'online'}>{t('device.remote')}</Button>
              }>
                <div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold">{t('device.connectVia')}</div>
                {can('devices.remote') && <MenuItem icon="remote" onClick={() => startRemote('native')} disabled={nativeUnavailable} hint={nativeHint}>{t('device.native')}</MenuItem>}
                {can('devices.remote') && <MenuItem icon="external" onClick={startRemoteNewTab} disabled={nativeUnavailable} hint={nativeUnavailable ? nativeHint : t('device.newTab')}>{t('device.nativeNewTab')}</MenuItem>}
                {can('devices.terminal') && <MenuItem icon="terminal" onClick={() => setTerm({ deviceId: id, hostname: d.hostname })} hint={t('device.elevated')}>{t('device.terminal')}</MenuItem>}
                {can('devices.files') && <MenuItem icon="folder" onClick={() => setRemote({ deviceId: id, hostname: d.hostname, os: d.os, filesOnly: true })}>{t('device.fileTransfer')}</MenuItem>}
                {can('devices.remote') && meshEnabled && <MenuItem icon="external" onClick={() => startRemote('meshcentral')} disabled={!d.mesh_id} hint={d.mesh_id ? '' : t('device.noNodeId')}>MeshCentral</MenuItem>}
                {can('devices.remote') && rustdeskEnabled && <MenuItem icon="external" onClick={() => startRemote('rustdesk')} hint="rustdesk://">RustDesk</MenuItem>}
                {can('devices.remote') && can('devices.remote.share') && guestAccess && <>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="users" onClick={() => setShareOpen(true)} hint={t('share.menuHint')}>{t('share.menu')}</MenuItem>
                </>}
                {can('devices.manage') && meshEnabled && <>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="external" onClick={() => linkRemote('mesh_id', t('remote.meshNodeId'))}>{t('device.linkMesh')}</MenuItem>
                </>}
              </Dropdown>}
              {can('jobs.create') && <Button variant="secondary" icon="terminal" onClick={() => setTab('run')}>{t('device.runScript')}</Button>}
              <div className="flex-1" />
              {can('clients.manage') && <><div className="min-w-0 max-w-[300px] text-right hidden sm:block"><div className="text-[10.5px] uppercase tracking-wide text-faint">{t('nav.clients')}</div><div className="text-[12px] text-muted truncate">{orgLabel}</div></div><Button variant="secondary" icon="building" onClick={() => setOrgOpen(true)}>{t('nav.clients')}</Button></>}
              {can('devices.delete') && <Button variant="danger" icon="trash" onClick={() => setConfirmDel(true)}>{t('action.delete')}</Button>}
            </div>
          </div>

          <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'inventory', label: t('device.tab.inventory') }, ...(can('jobs.view') ? [{ value: 'jobs', label: t('nav.jobs') }] : []), ...(can('checks.view') ? [{ value: 'checks', label: t('device.tab.checks') }] : []), ...(can('tasks.view') ? [{ value: 'tasks', label: t('device.tab.tasks') }] : []), ...(can('patches.view') ? [{ value: 'patches', label: t('nav.patches') }] : []), ...(can('jobs.create') ? [{ value: 'run', label: t('device.runScript') }] : []), ...(can('devices.manage') ? [{ value: 'notes', label: t('device.tab.notes') }] : [])]} />

          <div className="flex-1 overflow-y-auto scroll-area p-5">
            {tab === 'inventory' && <InventoryView id={id} />}
            {tab === 'jobs' && can('jobs.view') && <DeviceJobs id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'checks' && can('checks.view') && <DeviceChecks id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'tasks' && can('tasks.view') && <DeviceTasks id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'patches' && can('patches.view') && <DevicePatches id={id} hostname={d.hostname} />}
            {tab === 'run' && can('jobs.create') && <RunScriptForm device={d} onCreated={() => setTab('jobs')} />}
            {tab === 'notes' && can('devices.manage') && <DeviceNotes device={d} />}
          </div>
        </>
      )}
      {!d && <div className="flex-1 flex items-center justify-center"><Spinner size={22} /></div>}
      {remote && <RemoteViewer deviceId={remote.deviceId} hostname={remote.hostname} os={remote.os} filesOnly={remote.filesOnly} onClose={() => setRemote(null)} />}
      {term && <TerminalViewer deviceId={term.deviceId} hostname={term.hostname} onClose={() => setTerm(null)} />}
      {d && <ShareAccessModal open={shareOpen} onClose={() => setShareOpen(false)} device={d} />}
      {d && <DeviceOrgModal open={orgOpen} onClose={() => setOrgOpen(false)} device={d} clients={clients} sites={sites} groups={groups} onSave={saveOrg} />}
      {d && <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={deleteDevice} danger title={t('device.delete.title')} body={t('device.delete.body', { host: d.hostname })} confirmLabel={t('device.delete.confirm')} />}
    </Drawer>
  );
}

function DeviceOrgModal({ open, onClose, device, clients, sites, groups, onSave }) {
  const [q, setQ] = useState('');
  const [siteId, setSiteId] = useState('');
  const [groupId, setGroupId] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (open) { setQ(''); setSiteId(device.site_id || ''); setGroupId(device.group_id || ''); } }, [open, device.id]);
  const query = q.trim().toLowerCase();
  const matches = (v) => !query || (v || '').toLowerCase().includes(query);
  const groupsForSite = (sid) => groups.filter((g) => g.site_id === sid);
  const visibleClients = clients.filter((c) => matches(c.name) || sites.some((s) => s.client_id === c.id && (matches(s.name) || groupsForSite(s.id).some((g) => matches(g.name)))));
  const chooseSite = (sid) => { setSiteId(sid); setGroupId(''); };
  const chooseGroup = (sid, gid) => { setSiteId(sid); setGroupId(gid); };
  const save = async () => { setBusy(true); try { await onSave({ siteId, groupId }); } finally { setBusy(false); } };
  const changed = (siteId || '') !== (device.site_id || '') || (groupId || '') !== (device.group_id || '');

  return (
    <Modal open={open} onClose={onClose} width="max-w-2xl">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="building" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">{t('nav.clients')}</div><div className="text-[12.5px] text-muted">{t('org.assign', { host: device.hostname })}</div></div>
        </div>
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('org.search')} autoFocus />
        <div className="rounded-xl border border-line max-h-80 overflow-y-auto scroll-area p-1">
          <button onClick={() => chooseSite('')} className={cx('w-full flex items-center gap-2 rounded-lg px-3 py-2 text-left text-[12.5px] transition', !siteId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="close" size={13} />{t('devices.unassignedTitle')}</button>
          {visibleClients.map((c) => {
            const clientSites = sites.filter((s) => s.client_id === c.id && (matches(c.name) || matches(s.name) || groupsForSite(s.id).some((g) => matches(g.name))));
            return (
              <div key={c.id} className="mt-1">
                <div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold flex items-center gap-1.5"><Icon name="building" size={12} />{c.name}</div>
                {clientSites.map((s) => {
                  const selectedSite = siteId === s.id && !groupId;
                  const siteGroups = groupsForSite(s.id).filter((g) => matches(c.name) || matches(s.name) || matches(g.name));
                  return (
                    <div key={s.id}>
                      <button onClick={() => chooseSite(s.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-6 pr-3 py-2 text-left text-[12.5px] transition', selectedSite ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="pin" size={13} /><span className="flex-1 truncate">{s.name}</span><span className="text-[10.5px] text-faint">{t('org.site')}</span></button>
                      {siteGroups.map((g) => (
                        <button key={g.id} onClick={() => chooseGroup(s.id, g.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-11 pr-3 py-1.5 text-left text-[12.5px] transition', groupId === g.id ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="building" size={12} /><span className="flex-1 truncate">{g.name}</span><span className="text-[10.5px] text-faint">{t('org.department')}</span></button>
                      ))}
                    </div>
                  );
                })}
              </div>
            );
          })}
          {visibleClients.length === 0 && <div className="px-3 py-8 text-center text-[12px] text-faint">{t('org.noMatches')}</div>}
        </div>
        <div className="flex items-center justify-end gap-2"><Button variant="ghost" onClick={onClose} disabled={busy}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} disabled={!changed} onClick={save}>{t('org.save')}</Button></div>
      </div>
    </Modal>
  );
}

// DeviceNotes edits the device's identification fields: Description (shown in the
// device list — e.g. the user's name) and Note (a private observation, only here).
function DeviceNotes({ device }) {
  const { can } = useAuth();
  const toast = useToast();
  const editable = can('devices.manage');
  const [desc, setDesc] = useState(device.description || '');
  const [note, setNote] = useState(device.note || '');
  const [busy, setBusy] = useState(false);
  const dirty = desc !== (device.description || '') || note !== (device.note || '');

  const save = async () => {
    setBusy(true);
    try { await api.updateDevice(device.id, { description: desc, note }); toast.success(t('common.saved')); }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };

  return (
    <div className="max-w-2xl space-y-4">
      <Field label={t('common.description')} hint={t('notes.descHint')}>
        <Input value={desc} onChange={(e) => setDesc(e.target.value)} disabled={!editable} placeholder={t('notes.descPlaceholder')} />
      </Field>
      <Field label={t('notes.note')} hint={t('notes.noteHint')}>
        <Textarea value={note} onChange={(e) => setNote(e.target.value)} disabled={!editable} rows={8} placeholder={t('notes.notePlaceholder')} />
      </Field>
      {editable
        ? <div className="flex justify-end"><Button variant="primary" loading={busy} disabled={!dirty} onClick={save}>{t('action.save')}</Button></div>
        : <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />{t('notes.needPerm')}</div>}
    </div>
  );
}

// RemoteViewer is the native remote-desktop viewer: it connects to the relay as
// the viewer peer and multiplexes video (JPEG frames), input, chat and file
// transfer over the one socket. Pairs with cmd/rmm-remote.
//   binary [0x01][tile-delta] screen frame  (agent -> viewer; see encoder.go)
//   binary [0x02][id][bytes]  file chunk     (both directions)
//   text   {"t":...}          input / chat / file control ({t:'kf'} = request keyframe)
const CH_VIDEO = 0x01, CH_FILE = 0x02;
function relayProtocols(ticket) {
  try {
    const auth = JSON.parse(sessionStorage.getItem('rmm-ai-auth') || 'null');
    if (auth?.token) return ['rmm-v1', 'rmm-ticket.' + ticket, 'rmm-auth.' + auth.token];
  } catch (e) {}
  return ['rmm-v1', 'rmm-ticket.' + ticket];
}

// Why a guest's session closed, by relay close code (internal/remote/guest.go).
const GUEST_CLOSE_REASONS = { 4410: 'replaced', 4411: 'removed', 4412: 'sponsor_left', 4413: 'expired' };
const SHARE_LIFETIMES = [15, 60, 240, 480, 1440];

// GuestRequests sits at the top of a technician's own session: guests waiting
// for this technician's decision on this device, and those already in. Only
// the technician who sent an invitation sees its guest here, and accepting
// needs this session open — which is why it lives in the viewer.
function GuestRequests({ deviceId }) {
  const toast = useToast();
  const q = useQuery(['remote-shares-pending', deviceId], () => api.pendingRemoteShares(deviceId), { poll: 3000 });
  const pending = q.data?.pending || [];
  const guests = q.data?.guests || [];
  const act = async (fn) => {
    try { await fn(); q.refetch(); }
    catch (e) { toast.error(t('guest.actionFailed'), e.message); }
  };
  if (!pending.length && !guests.length) return null;
  return (<>
    {pending.map((g) => (
      <div key={g.id} className="pointer-events-auto w-full rounded-lg border border-brand bg-card px-3.5 py-2.5 shadow-lg flex flex-wrap items-center gap-2.5 text-[12.5px] text-ink">
        <Icon name="bell" size={15} className="text-brandhi shrink-0" />
        <span className="flex-1 min-w-[180px]">{t('guest.wantsToJoin', { name: g.guest_name, ip: g.guest_ip, mode: tEnum('guest.mode', g.mode) })}</span>
        <Button size="sm" variant="primary" icon="approve" onClick={() => act(() => api.acceptRemoteShare(g.id))}>{t('guest.accept')}</Button>
        <Button size="sm" variant="ghost" icon="reject" onClick={() => act(() => api.rejectRemoteShare(g.id))}>{t('guest.reject')}</Button>
      </div>
    ))}
    {guests.map((g) => (
      <div key={g.id} className="pointer-events-auto rounded-full border border-line2 bg-card px-3 py-1 shadow flex items-center gap-2 text-[11.5px] text-ink">
        <span className={cx('h-2 w-2 rounded-full', g.connected ? 'bg-ok' : 'bg-faint')} />
        <span>{t(g.connected ? 'guest.chipConnected' : 'guest.chipWaiting', { name: g.guest_name, mode: tEnum('guest.mode', g.mode) })}</span>
        <button onClick={() => act(() => api.closeRemoteShare(g.id))} className="text-bad hover:underline">{t('guest.remove')}</button>
      </div>
    ))}
  </>);
}

// ShareAccessModal issues an invitation for a device and lists the caller's
// open ones. The link and the code are shown once: only their hashes are kept.
function ShareAccessModal({ open, onClose, device }) {
  const toast = useToast();
  const [mode, setMode] = useState('view');
  const [minutes, setMinutes] = useState('60');
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState(null); // { link, code, expires_at, mode }
  const listQ = useQuery(['remote-shares', device.id, open], () => open ? api.listRemoteShares(device.id) : Promise.resolve({ shares: [] }), { poll: open ? 5000 : 0 });
  useEffect(() => { if (open) { setIssued(null); setMode('view'); setMinutes('60'); } }, [open, device.id]);
  const create = async () => {
    setBusy(true);
    try {
      const r = await api.createRemoteShare(device.id, { mode, minutes: Number(minutes) });
      const link = new URL((window.__BASE_PATH__ || '/') + 'join', location.origin).toString() + '#' + r.token;
      setIssued({ link, code: r.code, expires_at: r.share.expires_at, mode: r.share.mode });
      listQ.refetch();
      toast.success(t('share.created'));
    } catch (e) {
      toast.error(t('share.failed'), e.code === 'guest_access_disabled' ? t('share.disabled') : e.message);
    } finally { setBusy(false); }
  };
  const close = async (id) => {
    try { await api.closeRemoteShare(id); listQ.refetch(); }
    catch (e) { toast.error(t('guest.actionFailed'), e.message); }
  };
  const shares = listQ.data?.shares || [];
  return (
    <Modal open={open} onClose={onClose} width="max-w-xl">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="users" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">{t('share.title')}</div><div className="text-[12.5px] text-muted">{t('share.subtitle', { host: device.hostname })}</div></div>
        </div>
        {issued ? (
          <div className="space-y-3 rounded-xl border border-brand/30 bg-brandsoft/40 p-4">
            <Field label={t('share.link')}>
              <div className="flex gap-1.5"><Input readOnly value={issued.link} className="mono text-[11.5px]" onFocus={(e) => e.target.select()} /><CopyButton value={issued.link} /></div>
            </Field>
            <Field label={t('share.code')}>
              <div className="flex items-center gap-2"><span className="mono text-[22px] tracking-[0.3em] text-ink">{issued.code}</span><CopyButton value={issued.code} /></div>
            </Field>
            <div className="text-[12px] text-muted">{tEnum('guest.mode', issued.mode)} · {t('share.expires', { when: fmtTime(issued.expires_at) })}</div>
            <div className="flex items-start gap-2 text-[12px] text-warn"><Icon name="info" size={14} className="shrink-0 mt-0.5" /><span>{t('share.onceWarning')}</span></div>
            <div className="text-[12px] text-muted">{t('share.acceptHint')}</div>
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t('share.mode')}>
              <Select value={mode} onChange={(e) => setMode(e.target.value)}>
                <option value="view">{tEnum('guest.mode', 'view')}</option>
                <option value="control">{tEnum('guest.mode', 'control')}</option>
              </Select>
            </Field>
            <Field label={t('share.validFor')}>
              <Select value={minutes} onChange={(e) => setMinutes(e.target.value)}>
                {SHARE_LIFETIMES.map((m) => <option key={m} value={String(m)}>{tEnum('share.minutes', m)}</option>)}
              </Select>
            </Field>
          </div>
        )}
        <div>
          <div className="text-[11px] uppercase tracking-wide text-faint font-semibold mb-1.5">{t('share.open')}</div>
          {shares.length === 0 && <div className="text-[12px] text-faint">{t('share.none')}</div>}
          {shares.map((s) => (
            <div key={s.id} className="flex items-center gap-2 border-b border-line/50 py-1.5 text-[12px]">
              <span className="text-ink">{s.guest_name || tEnum('guest.mode', s.mode)}</span>
              <span className="text-[11px] px-1.5 py-0.5 rounded bg-elevated text-muted">{tEnum('share.status', s.status)}</span>
              <span className="text-faint">{t('share.expires', { when: fmtTime(s.expires_at) })}</span>
              <button onClick={() => close(s.id)} className="ml-auto text-bad hover:underline">{s.status === 'accepted' ? t('guest.remove') : t('share.revoke')}</button>
            </div>
          ))}
        </div>
        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>{t('action.close')}</Button>
          {!issued && <Button variant="primary" icon="link" loading={busy} onClick={create}>{t('share.create')}</Button>}
        </div>
      </div>
    </Modal>
  );
}

// GuestJoinScreen is the page an invitation link opens (/join#<token>), for
// someone with no account: name and code, then a wait for the technician, then
// the reduced viewer. The token leaves the address bar at once; after the code
// is accepted the page holds only the guest's own credential, for this tab.
const GUEST_STORE = 'rmm-guest';
function GuestJoinScreen() {
  const [token] = useState(() => (location.hash || '').replace(/^#/, ''));
  const [saved, setSaved] = useState(() => { try { return JSON.parse(sessionStorage.getItem(GUEST_STORE) || 'null'); } catch (e) { return null; } });
  const [phase, setPhase] = useState(saved ? 'waiting' : (token ? 'loading' : 'invalid'));
  const [technician, setTechnician] = useState(saved?.technician || '');
  const [mode, setMode] = useState(saved?.mode || 'view');
  const [name, setName] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [ended, setEnded] = useState('');
  useEffect(() => { if (location.hash) history.replaceState(null, '', location.pathname + location.search); }, []);
  useEffect(() => {
    if (phase !== 'loading') return;
    api.guest.lookup(token).then((r) => { setTechnician(r.technician); setMode(r.mode); setPhase('form'); }).catch(() => setPhase('invalid'));
  }, [phase]);
  const remember = (s) => { setSaved(s); try { s ? sessionStorage.setItem(GUEST_STORE, JSON.stringify(s)) : sessionStorage.removeItem(GUEST_STORE); } catch (e) {} };
  const finish = (reason) => { remember(null); setEnded(reason); setPhase('ended'); };
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true); setError('');
    try {
      const r = await api.guest.join(token, code.trim(), name.trim());
      remember({ credential: r.guest_credential, technician: r.technician, mode: r.mode });
      setTechnician(r.technician); setMode(r.mode); setPhase('waiting');
    } catch (err) {
      if (err.code === 'link_invalid') setPhase('invalid');
      else setError(err.code === 'wrong_code' ? t('guest.wrongCode') : err.code === 'name_required' ? t('guest.nameRequired') : err.message);
    } finally { setBusy(false); }
  };
  // While waiting, and while the technician is away, ask for the decision.
  useEffect(() => {
    if (!saved || (phase !== 'waiting' && phase !== 'paused')) return;
    let stopped = false;
    const tick = async () => {
      try {
        const st = await api.guest.status(saved.credential);
        if (stopped) return;
        if (st.status === 'accepted') setPhase(st.sponsor_present ? 'viewer' : 'paused');
        else if (st.status !== 'pending') finish(st.status);
      } catch (err) { if (!stopped && err.status === 401) finish('ended'); }
    };
    tick();
    const id = setInterval(tick, 3000);
    return () => { stopped = true; clearInterval(id); };
  }, [phase, saved && saved.credential]);
  const onEnd = (reason) => {
    // The technician stepped away, or the device dropped offline: wait for
    // either to come back while the invitation lasts.
    if (reason === 'sponsor_left' || reason === 'sponsor_absent' || reason === 'device_unavailable') setPhase('paused');
    else if (reason === 'share_expired' || reason === 'expired') finish('expired');
    else if (reason === 'replaced') finish('replaced');
    else finish('ended');
  };
  const guest = useMemo(() => saved ? { credential: saved.credential, control: saved.mode === 'control', technician: saved.technician, broker: () => api.guest.session(saved.credential), onEnd } : null, [saved && saved.credential]);

  if (phase === 'viewer' && guest) {
    return <RemoteViewer deviceId="" hostname={t('guest.sharedBy', { tech: technician })} os="" guest={guest} onClose={() => finish('left')} />;
  }
  return (
    <div className="min-h-full flex items-center justify-center p-4">
      <div className="w-full max-w-sm rounded-2xl border border-line bg-panel p-6 shadow-2xl space-y-4">
        <div className="flex items-center gap-2.5"><BrandMark size={28} /><span className="text-[15px] font-semibold text-ink">{t('guest.title')}</span></div>
        {phase === 'loading' && <div className="flex justify-center py-6"><Spinner size={20} /></div>}
        {phase === 'invalid' && <>
          <div className="text-[14px] font-semibold text-ink">{t('guest.invalidTitle')}</div>
          <p className="text-[12.5px] text-muted">{t('guest.invalidBody')}</p>
        </>}
        {phase === 'form' && (
          <form onSubmit={submit} className="space-y-3">
            <p className="text-[12.5px] text-muted">{t(mode === 'control' ? 'guest.invitedControl' : 'guest.invitedView', { tech: technician })}</p>
            <Field label={t('guest.name')}><Input value={name} onChange={(e) => setName(e.target.value)} maxLength={60} autoFocus autoComplete="name" /></Field>
            <Field label={t('guest.code')} hint={t('guest.codeHint', { tech: technician })}><Input value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))} inputMode="numeric" autoComplete="one-time-code" className="mono tracking-[0.3em]" /></Field>
            {error && <div className="text-[12px] text-bad">{error}</div>}
            <Button type="submit" variant="primary" className="w-full" loading={busy} disabled={!name.trim() || code.length !== 6}>{t('guest.join')}</Button>
          </form>
        )}
        {(phase === 'waiting' || phase === 'paused') && <>
          <div className="flex items-center gap-2.5"><Spinner size={16} /><span className="text-[14px] font-semibold text-ink">{t(phase === 'paused' ? 'guest.pausedTitle' : 'guest.waitingTitle', { tech: technician })}</span></div>
          <p className="text-[12.5px] text-muted">{t(phase === 'paused' ? 'guest.pausedBody' : 'guest.waitingBody', { tech: technician })}</p>
          <Button variant="secondary" className="w-full" onClick={() => finish('left')}>{t('guest.leave')}</Button>
        </>}
        {phase === 'ended' && <>
          <div className="text-[14px] font-semibold text-ink">{t('guest.endedTitle')}</div>
          <p className="text-[12.5px] text-muted">{tEnum('guest.ended', ended || 'ended')}</p>
        </>}
      </div>
    </div>
  );
}
window.GuestJoinScreen = GuestJoinScreen;
// guest, when set, makes this the reduced viewer of someone a technician let in
// (GuestJoinScreen): { credential, control, broker, onEnd }. The relay already
// drops everything a guest may not send or receive; this only leaves out the
// controls that would do nothing — files, chat, clipboard, the shortcuts, the
// input lock — and, in view-only mode, the mouse and keyboard.
function RemoteViewer({ deviceId, hostname, os, filesOnly, onClose, guest }) {
  const toast = useToast();
  const auth = useAuth() || { can: () => false };
  const can = auth.can;
  const guestRef = useRef(guest); guestRef.current = guest;
  const isGuest = !!guest;
  const control = !guest || !!guest.control;
  const allowFiles = !isGuest && (filesOnly || can('devices.files'));
  const isWin = os ? /^win/i.test(os) : true; // unknown host => Windows-style (legacy)
  const isMac = /darwin|macos|mac os/i.test(os || '');
  const shortcutLabel = isMac ? '⌘⌥Esc' : 'Ctrl+Alt+Delete';
  const canLock = !isGuest && (isWin || /linux/i.test(os || '')); // input lock: Windows + Linux (not macOS)
  const viewerRef = useRef(null);
  const canvasRef = useRef(null);
  const wsRef = useRef(null);
  const dlRef = useRef({});   // active downloads: id -> { name, chunks: [] }
  const tidRef = useRef(0);
  const openedFilesRef = useRef(false);
  const fileInputRef = useRef(null);
  const [status, setStatus] = useState('connecting');
  const [chat, setChat] = useState([]); // { from: 'tech'|'user', text }
  const [draft, setDraft] = useState('');
  const [fb, setFb] = useState(null); // file browser: null=closed; else { path, entries|null }
  const [fbSel, setFbSel] = useState([]); // selected entry names
  const [fbClip, setFbClip] = useState(null); // { op:'copy'|'cut', base, names }
  const [pathEdit, setPathEdit] = useState(''); // editable address bar
  useEffect(() => { if (fb) setPathEdit(fb.path); }, [fb && fb.path]);
  const [uac, setUac] = useState(false); // UAC prompts moved to the interactive desktop
  const [locked, setLocked] = useState(false); // host's physical keyboard/mouse blocked (Windows)
  const [ver, setVer] = useState(null);  // running sidecar version (staleness check)
  const [hostUser, setHostUser] = useState(''); // the account signed in at the machine (sidecar 0.18+)
  const [cad, setCad] = useState({ available: false, reason: t('viewer.waitingModule') });
  const [monitors, setMonitors] = useState({ list: [], selected: 0, all: true }); // host displays; empty unless there are two or more
  const [frameSize, setFrameSize] = useState({ width: 0, height: 0 });
  const [scale, setScale] = useState('fit');
  const [fullscreen, setFullscreen] = useState(false);
  const [notice, setNotice] = useState(null); // host-side notice (e.g. Wayland can't be captured)
  // One desktop session per operator per device: 'active' when this tab was
  // refused because another has it, 'replaced' when another tab took it over.
  // Either way this tab stops reconnecting until the operator chooses.
  const [conflict, setConflict] = useState(null);
  const takeOverRef = useRef(() => {});
  const [chatOpen, setChatOpen] = useState(!guest); // side chat panel visible
  const [unread, setUnread] = useState(0);
  const openRef = useRef(true);
  const noticeRef = useRef(null); // last notice toasted, so a reconnect loop doesn't repeat it
  const captureErrorRef = useRef(null); // the capture problem the endpoint last reported, so a later hello can retract it
  useEffect(() => { openRef.current = chatOpen; if (chatOpen) setUnread(0); }, [chatOpen]);

  const nextId = () => (tidRef.current = (tidRef.current + 1) & 0xff);
  const sendJSON = (o) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };

  useEffect(() => {
    const changed = () => setFullscreen(document.fullscreenElement === viewerRef.current);
    document.addEventListener('fullscreenchange', changed);
    return () => document.removeEventListener('fullscreenchange', changed);
  }, []);

  const toggleFullscreen = async () => {
    try {
      if (document.fullscreenElement === viewerRef.current) await document.exitFullscreen();
      else if (viewerRef.current?.requestFullscreen) await viewerRef.current.requestFullscreen();
    } catch (e) {
      toast.error(t('viewer.fullscreenFailed'), e.message || t('viewer.fullscreenDenied'));
    }
  };

  useEffect(() => {
    const cv = canvasRef.current;
    let stopped = false, retries = 0;

    const onMsg = (ev) => {
      if (typeof ev.data === 'string') {
        const m = JSON.parse(ev.data);
        if (m.t === 'hello') {
          setVer(m.v);
          setHostUser(m.user || '');
          setCad({ available: !!m.cad, reason: m.cad_error || (m.cad ? '' : t('viewer.updateModule')) });
          // A capture the endpoint cannot perform is the black screen this
          // viewer would otherwise show without explanation.
          if (m.capture_error) { captureErrorRef.current = m.capture_error; setNotice(m.capture_error); if (noticeRef.current !== m.capture_error) { noticeRef.current = m.capture_error; toast.error(t('viewer.captureBlocked'), m.capture_error); } }
          // A later hello without it means the permission arrived mid-session;
          // leaving the old banner up would contradict the picture beneath it.
          else if (window.RemoteInput.staleCaptureNotice(noticeRef.current, captureErrorRef.current, m)) { captureErrorRef.current = null; noticeRef.current = null; setNotice(null); }
        }
        else if (m.t === 'notice') { if (m.text) { setNotice(m.text); if (noticeRef.current !== m.text) { noticeRef.current = m.text; toast.info(t('viewer.remoteNotice'), m.text); } } else { noticeRef.current = null; setNotice(null); } }
        // The endpoint's clipboard, mirrored while the session is open, so a copy
        // on the remote screen can be pasted on this machine. Writing can be
        // refused by the browser when the tab is not focused; that is a missed
        // update, not an error worth interrupting the session for.
        else if (m.t === 'clip') {
          const text = window.RemoteInput.incomingClipText(m);
          if (text && navigator.clipboard?.writeText) { navigator.clipboard.writeText(text).catch(() => {}); }
        }
        // Lines from the person at the machine, and — carried over by the server
        // from the other sessions on this device — from the technician and
        // their guests (internal/api/api_guest_chat.go).
        else if (m.t === 'chat') { setChat((c) => [...c, { from: m.from, name: m.name, text: m.text }]); if (!openRef.current) setUnread((u) => u + 1); }
        else if (m.t === 'cad') { if (m.ok) { if (isMac) setCad({ available: true, reason: '' }); toast.info(t('viewer.shortcutSent'), `${hostname}: ${shortcutLabel}`); } else toast.error(t('viewer.shortcutFailed'), m.err || t('viewer.shortcutRejected')); }
        else if (m.t === 'paste') { if (m.ok && isMac) setCad({ available: true, reason: '' }); else if (!m.ok) toast.error(t('viewer.pasteFailed'), m.err || t('viewer.pasteRejected')); }
        else if (m.t === 'monitors') setMonitors({ list: m.list || [], selected: m.selected || 0, all: m.all !== false })
        else if (allowFiles && m.t === 'flist') setFb({ path: m.path, entries: m.entries || [] });
        else if (m.t === 'uac') { setUac(!!m.enabled); if (m.err) toast.error(t('viewer.uacFailed'), m.err); }
        else if (m.t === 'inputlocked') { setLocked(!!m.enabled); if (m.err) toast.error(t('viewer.lockFailed'), m.err); }
        else if (allowFiles && m.t === 'fmeta') dlRef.current[m.id] = { name: m.name, chunks: [] };
        else if (allowFiles && m.t === 'fend') {
          const d = dlRef.current[m.id];
          if (d && d.chunks) { saveBlob(new Blob(d.chunks), d.name); toast.success(t('viewer.downloaded'), d.name); delete dlRef.current[m.id]; }
        } else if (allowFiles && m.t === 'ferr') toast.error(t('viewer.transferFailed'), m.err || '');
        return;
      }
      const bytes = new Uint8Array(ev.data);
      if (bytes[0] === CH_VIDEO) {
        drawVideo(ev.data);
      } else if (allowFiles && bytes[0] === CH_FILE) {
        const d = dlRef.current[bytes[1]];
        if (d) d.chunks.push(ev.data.slice(2));
      }
    };

    // drawVideo composites a tile-delta frame (see cmd/rmm-remote/encoder.go):
    //   [1] flags  [2] frameW  [2] frameH  [2] tileCount
    //   per tile:  [2] x [2] y [2] w [2] h [4] jpegLen [jpeg…]
    // Only changed tiles arrive; a keyframe covers the whole screen (one tile,
    // or horizontal stripes). Tiles DECODE in parallel but PAINT in frame order
    // (drawChain): a slow whole-screen keyframe decode must never finish after —
    // and overwrite — newer delta tiles, or fresh content (e.g. a login field
    // that was just clicked/typed into) "vanishes" under the stale keyframe.
    let drawChain = Promise.resolve();
    const drawVideo = (buf) => {
      if (buf.byteLength < 8) return;
      const dv = new DataView(buf, 1);
      const fw = dv.getUint16(1), fh = dv.getUint16(3), n = dv.getUint16(5);
      let p = 7;
      const jobs = [];
      for (let i = 0; i < n; i++) {
        if (p + 12 > dv.byteLength) break;
        const x = dv.getUint16(p), y = dv.getUint16(p + 2);
        const len = dv.getUint32(p + 8);
        p += 12;
        const jpg = buf.slice(1 + p, 1 + p + len);
        p += len;
        jobs.push(createImageBitmap(new Blob([jpg], { type: 'image/jpeg' }))
          .then((bmp) => ({ x, y, bmp })).catch(() => null));
      }
      const decoded = Promise.all(jobs); // decode starts now, in parallel
      drawChain = drawChain.then(() => decoded).then((tiles) => {
        const cv2 = canvasRef.current;
        if (!cv2) return;
        if (cv2.width !== fw || cv2.height !== fh) {
          cv2.width = fw; cv2.height = fh;
        }
        setFrameSize((old) => old.width === fw && old.height === fh ? old : { width: fw, height: fh });
        const ctx = cv2.getContext('2d');
        for (const tile of tiles) { if (tile) { ctx.drawImage(tile.bmp, tile.x, tile.y); if (tile.bmp.close) tile.bmp.close(); } }
      }).catch(() => {});
    };

    // Broker a session and connect. On drop (e.g. user logoff tears the session
    // down) re-broker and reconnect — the agent re-spawns the SYSTEM sidecar into
    // the now-active session (login screen included).
    let sessionId = null; // this tab's last session, taken back without asking after a drop
    const connect = async (replace) => {
      setConflict(null);
      setCad({ available: false, reason: t('viewer.waitingModule') });
      setMonitors({ list: [], selected: 0, all: true });
      setVer(null);
      if (stopped) return;
      setStatus(retries ? 'reconnecting' : 'connecting');
      let wsUrl, viewerTicket;
      const g = guestRef.current;
      try {
        const s = g ? await g.broker() : await api.startRemote(deviceId, filesOnly ? 'files' : 'native', filesOnly ? undefined : { replace: !!replace, resume: sessionId || undefined });
        wsUrl = api.websocketURL('/ws/remote?session=' + encodeURIComponent(s.id) + '&role=viewer');
        viewerTicket = s.viewer_ticket;
        sessionId = s.id;
      } catch (e) {
        // A guest's broker refuses for a reason the guest page explains
        // (the technician left, the invitation ended); retrying here would not.
        // "too_soon" is only the server spacing out reconnects: retry below.
        if (g && e.code && e.code !== 'too_soon') { if (!stopped) { setStatus('closed'); g.onEnd(e.code); } return; }
        if (e.code === 'remote_session_active') { if (!stopped) { setConflict('active'); setStatus('closed'); } return; }
        if (!stopped && retries++ < 40) setTimeout(() => connect(), 3000);
        else setStatus('closed');
        return;
      }
      if (stopped) return;
      const ws = new WebSocket(wsUrl, g ? ['rmm-v1', 'rmm-ticket.' + viewerTicket, 'rmm-guest.' + g.credential] : relayProtocols(viewerTicket));
      wsRef.current = ws;
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        retries = 0; setStatus(filesOnly ? 'connected' : 'streaming'); setNotice(null);
        setLocked(false); // a fresh sidecar starts unlocked (reconnect after logoff/desktop switch)
        // File-transfer-only sessions never stream the screen, so don't ask for a
        // keyframe; jump straight into the browser. Desktop sessions request a frame.
        if (filesOnly && allowFiles) {
          if (!openedFilesRef.current) { openedFilesRef.current = true; browse(rootPath); }
        } else {
          try { ws.send(JSON.stringify({ t: 'kf' })); } catch (e) {}
        }
      };
      ws.onerror = () => {};
      ws.onclose = (ev) => {
        if (stopped) return;
        // A guest's session closed on purpose (internal/remote/guest.go): the
        // guest page says why, and reconnects itself when that makes sense.
        const gc = guestRef.current;
        if (gc && GUEST_CLOSE_REASONS[ev.code]) { setStatus('closed'); gc.onEnd(GUEST_CLOSE_REASONS[ev.code]); return; }
        // Codes from the relay (internal/remote/relay.go): the session is in
        // another tab. Reconnecting here would take it back from there.
        if (ev.code === 4409 || ev.code === 4410) { setConflict(ev.code === 4410 ? 'replaced' : 'active'); setStatus('closed'); return; }
        if (retries++ < 40) { setStatus('reconnecting'); setTimeout(() => connect(), 2000); }
        else setStatus('closed');
      };
      ws.onmessage = onMsg;
    };
    takeOverRef.current = () => { retries = 0; connect(true); };

    const send = (o) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };
    const norm = (e) => { const r = cv.getBoundingClientRect(); return { x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)) }; };
    const typing = () => window.RemoteInput.isTypingTarget(document.activeElement);
    let lastMove = 0;
    const onMove = (e) => { const now = Date.now(); if (now - lastMove < 33) return; lastMove = now; const p = norm(e); send({ t: 'm', x: p.x, y: p.y }); };
    // preventDefault() cancels the browser's default focus, so clicking the
    // remote screen would leave focus stuck in the chat input (typing() then
    // stays true and keystrokes keep going to the chat box, not the host).
    // Focus the canvas explicitly so a click on the screen takes over input.
    const onDown = (e) => { e.preventDefault(); cv.focus(); if (cv.setPointerCapture) cv.setPointerCapture(e.pointerId); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button < 0 ? 0 : e.button, down: true }); };
    const onUp = (e) => { e.preventDefault(); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button, down: false }); };
    const onWheel = (e) => { e.preventDefault(); send({ t: 'w', dy: e.deltaY }); };
    const onCtx = (e) => e.preventDefault();
    let skipFullscreenEscapeUp = false;
    const heldKeys = new Map();
    const releasedForPaste = new Set();
    const releasePasteModifiers = () => {
      for (const sourceCode of [17, 91, 93]) {
        const remoteCode = heldKeys.get(sourceCode);
        if (remoteCode == null) continue;
        send({ t: 'k', code: remoteCode, down: false });
        heldKeys.delete(sourceCode);
        releasedForPaste.add(sourceCode);
      }
    };
    const releaseHeldKeys = () => {
      for (const remoteCode of heldKeys.values()) send({ t: 'k', code: remoteCode, down: false });
      heldKeys.clear();
      releasedForPaste.clear();
    };
    const kd = (e) => {
      if (typing()) return;
      if (e.key === 'Escape' && document.fullscreenElement === viewerRef.current) { skipFullscreenEscapeUp = true; return; }
      // Not forwarded and, above all, not preventDefault()ed: that is what lets
      // the browser raise the paste event the clipboard text arrives on. The
      // endpoint presses its own paste shortcut when the text gets there.
      if (window.RemoteInput.isPasteShortcut(e)) { releasePasteModifiers(); return; }
      const code = window.RemoteInput.endpointKeyCode(e.keyCode, isMac);
      e.preventDefault();
      heldKeys.set(e.keyCode, code);
      send({ t: 'k', code, down: true });
    };
    const ku = (e) => {
      // A key may have gone down on the canvas just before focus moved to chat.
      // Still release that key remotely so the endpoint never keeps it held.
      if (typing() && !heldKeys.has(e.keyCode) && !releasedForPaste.has(e.keyCode)) return;
      if (e.key === 'Escape' && skipFullscreenEscapeUp) { skipFullscreenEscapeUp = false; return; }
      if (window.RemoteInput.isPasteShortcut(e)) return;
      e.preventDefault();
      if (releasedForPaste.delete(e.keyCode)) return;
      const code = heldKeys.get(e.keyCode) ?? window.RemoteInput.endpointKeyCode(e.keyCode, isMac);
      heldKeys.delete(e.keyCode);
      send({ t: 'k', code, down: false });
    };
    // The technician's clipboard, on their own paste keystroke. The paste event
    // carries the text with it, so nothing has to ask the browser for clipboard
    // read permission, and the endpoint applies the paste shortcut its own
    // platform uses — Command-V on a Mac, Control-V elsewhere — which a raw
    // Ctrl+V forwarded as keystrokes would get wrong. Only this direction: the
    // endpoint's clipboard is never read back, because silently collecting what
    // somebody copied on their own machine is surveillance, not support.
    const onPaste = (e) => {
      if (typing()) return;
      const message = window.RemoteInput.clipMessage(e.clipboardData && e.clipboardData.getData('text/plain'));
      if (!message) return;
      e.preventDefault();
      send(message);
    };
    // No screen/input wiring in file-transfer-only mode (the canvas isn't shown),
    // and none for a guest who may only watch. A guest never pastes.
    if (!filesOnly && cv && control) {
      cv.addEventListener('pointermove', onMove);
      cv.addEventListener('pointerdown', onDown);
      cv.addEventListener('pointerup', onUp);
      cv.addEventListener('pointercancel', onUp);
      cv.addEventListener('wheel', onWheel, { passive: false });
      cv.addEventListener('contextmenu', onCtx);
      window.addEventListener('keydown', kd);
      window.addEventListener('keyup', ku);
      if (!isGuest) window.addEventListener('paste', onPaste);
      window.addEventListener('blur', releaseHeldKeys);
    }
    connect();
    return () => {
      stopped = true;
      releaseHeldKeys();
      if (wsRef.current) wsRef.current.close();
      if (cv) {
        cv.removeEventListener('pointermove', onMove);
        cv.removeEventListener('pointerdown', onDown);
        cv.removeEventListener('pointerup', onUp);
        cv.removeEventListener('pointercancel', onUp);
        cv.removeEventListener('wheel', onWheel);
        cv.removeEventListener('contextmenu', onCtx);
      }
      window.removeEventListener('paste', onPaste);
      window.removeEventListener('keydown', kd);
      window.removeEventListener('keyup', ku);
      window.removeEventListener('blur', releaseHeldKeys);
    };
  }, [deviceId, filesOnly, allowFiles, isMac, isGuest, control]);

  // A guest's line goes to the server, which hands it to the technician's
  // session; the guest's own sidecar never sees it.
  const sendChat = () => { const text = draft.trim(); if (!text) return; sendJSON(isGuest ? { t: 'chat', text } : { t: 'chat', from: 'tech', text }); setChat((c) => [...c, { from: 'self', text }]); setDraft(''); };
  const chatLabel = (m) => m.from === 'self' ? (isGuest ? t('guest.chatYou') : t('viewer.techYou'))
    : m.from === 'guest' ? t('viewer.chatGuest', { name: m.name || '?' })
    : m.from === 'tech' ? t('guest.chatTech', { tech: guest?.technician || '' })
    : hostUser || (isGuest ? t('guest.chatUser') : hostname);
  const shortcutAvailable = window.RemoteInput.shortcutAvailable(isMac, cad.available);
  const sendCtrlAltDelete = () => {
    if (!shortcutAvailable) { toast.error(t('viewer.shortcutUnavailable'), cad.reason); return; }
    sendJSON({ t: 'cad' });
  };
  // Remote file browser: navigate folders, download a file, or
  // upload into the current folder — no path typing. Path semantics follow the
  // host OS: Windows uses "\\" + a drive list at the root; POSIX uses "/".
  const rootPath = isWin ? '' : '/';
  const joinPath = (base, name) => {
    if (isWin) return !base ? name : base.endsWith('\\') ? base + name : base + '\\' + name;
    return !base || base === '/' ? '/' + name : base.endsWith('/') ? base + name : base + '/' + name;
  };
  const parentPath = (p) => {
    if (isWin) {
      if (!p) return null;            // already at "This PC" (drive list)
      const trimmed = p.replace(/\\+$/, '');
      if (/^[A-Za-z]:$/.test(trimmed)) return '';        // drive root -> drive list
      const i = trimmed.lastIndexOf('\\');
      return i <= 2 ? trimmed.slice(0, 2) + '\\' : trimmed.slice(0, i);
    }
    if (!p || p === '/') return null;  // already at "/"
    const trimmed = p.replace(/\/+$/, '');
    const i = trimmed.lastIndexOf('/');
    return i <= 0 ? '/' : trimmed.slice(0, i);
  };
  const browse = (path) => { if (!allowFiles) return; const p = path == null ? rootPath : path; setFb({ path: p, entries: null }); setFbSel([]); sendJSON({ t: 'fls', id: nextId(), path: p }); };
  const downloadEntry = (name) => { if (allowFiles) sendJSON({ t: 'fget', id: nextId(), path: joinPath(fb.path, name) }); };
  const toggleSel = (name) => setFbSel((s) => s.includes(name) ? s.filter((x) => x !== name) : [...s, name]);
  const doMkdir = () => { const n = window.prompt(t('viewer.newFolderPrompt')); if (n) sendJSON({ t: 'fmkdir', id: nextId(), path: joinPath(fb.path, n) }); };
  const doDelete = () => { if (fbSel.length && window.confirm(t('viewer.deletePrompt', { items: tn('viewer.items', fbSel.length), host: hostname }))) { fbSel.forEach((n) => sendJSON({ t: 'fdel', id: nextId(), path: joinPath(fb.path, n) })); setFbSel([]); } };
  const doRename = () => { if (fbSel.length !== 1) return; const old = fbSel[0]; const n = window.prompt(t('viewer.renamePrompt'), old); if (n && n !== old) { sendJSON({ t: 'frename', id: nextId(), src: joinPath(fb.path, old), dst: joinPath(fb.path, n) }); setFbSel([]); } };
  const doClip = (op) => { if (fbSel.length) setFbClip({ op, base: fb.path, names: [...fbSel] }); };
  const doPaste = () => { if (!fbClip) return; fbClip.names.forEach((n) => sendJSON({ t: fbClip.op === 'cut' ? 'fmove' : 'fcopy', id: nextId(), src: joinPath(fbClip.base, n), dst: joinPath(fb.path, n) })); if (fbClip.op === 'cut') setFbClip(null); };
  const doDownloadSel = () => fbSel.forEach((n) => { const en = (fb.entries || []).find((e) => e.name === n); if (en && !en.dir) downloadEntry(n); });
  const uploadFile = async (file) => {
    if (!allowFiles || !file || !fb) return;
    const ws = wsRef.current; if (!ws || ws.readyState !== 1) return;
    const dest = joinPath(fb.path, file.name);
    const id = nextId();
    sendJSON({ t: 'fput', id, path: dest, size: file.size });
    const u8 = new Uint8Array(await file.arrayBuffer());
    const CHUNK = 64 * 1024;
    for (let off = 0; off < u8.length; off += CHUNK) {
      const slice = u8.subarray(off, Math.min(off + CHUNK, u8.length));
      const frame = new Uint8Array(slice.length + 2); frame[0] = CH_FILE; frame[1] = id; frame.set(slice, 2);
      ws.send(frame);
    }
    sendJSON({ t: 'fend', id });
    toast.success(t('viewer.uploadSent'), dest);
  };

  const statusCls = status === 'streaming' ? 'bg-oksoft text-ok' : status === 'error' || status === 'closed' ? 'bg-badsoft text-bad' : 'bg-warnsoft text-warn';
  const zoom = Number(scale);
  const canvasStyle = scale === 'fit' || !frameSize.width ? undefined : {
    width: Math.round(frameSize.width * zoom / 100) + 'px',
    height: Math.round(frameSize.height * zoom / 100) + 'px',
    maxWidth: 'none', maxHeight: 'none',
  };
  return (
    <div ref={viewerRef} className="fixed inset-0 z-[80] bg-black flex flex-col">
      <div className="flex items-center gap-2 px-2 sm:px-4 min-h-11 bg-elevated border-b border-line shrink-0 overflow-x-auto scroll-area">
        <Icon name={filesOnly ? 'devices' : 'remote'} size={15} className="text-muted" />
        <span className="text-[13px] font-semibold text-ink whitespace-nowrap">{filesOnly ? t('viewer.fileTransferOn', { host: hostname }) : hostname}</span>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{tEnum('viewer.status', status)}</span>
        {ver && !isGuest && <span className="text-[10px] text-faint">{t('viewer.sidecar', { version: ver })}</span>}
        <div className="ml-auto flex items-center gap-1 sm:gap-2 whitespace-nowrap">
          <input ref={fileInputRef} type="file" className="hidden" onChange={(e) => { uploadFile(e.target.files[0]); e.target.value = ''; }} />
          {isGuest && <span className={cx('text-[11px] px-2 py-0.5 rounded-md', control ? 'bg-warnsoft text-warn' : 'bg-elevated text-muted')}>{tEnum('guest.mode', control ? 'control' : 'view')}</span>}
          {!filesOnly && <>
            {!isGuest && <>
            <Button variant="ghost" size="sm" icon="key" onClick={sendCtrlAltDelete} disabled={status !== 'streaming' || !shortcutAvailable} title={isMac ? (cad.available ? t('viewer.forceQuit') : t('viewer.retryAfterGrant', { reason: cad.reason })) : !cad.available ? cad.reason : isWin ? t('viewer.winSecurity') : t('viewer.linuxCad')}>{shortcutLabel}</Button>
            {isWin && <Button variant={uac ? 'primary' : 'ghost'} size="sm" icon="shield" onClick={() => sendJSON({ t: 'uac', enable: !uac })} title={t('viewer.uacHint')}>UAC</Button>}
            {canLock && <Button variant={locked ? 'primary' : 'ghost'} size="sm" icon="lock" onClick={() => sendJSON({ t: 'lockinput', enable: !locked })} title={t('viewer.lockHint')}>{locked ? t('viewer.locked') : t('viewer.lockInput')}</Button>}
            {allowFiles && <Button variant="ghost" size="sm" icon="link" onClick={() => browse(rootPath)}>{t('viewer.files')}</Button>}
            </>}
            <Button variant={chatOpen ? 'primary' : 'ghost'} size="sm" icon="chat" onClick={() => setChatOpen((o) => !o)} title={t('viewer.chatToggle')}>{t('viewer.chat')}{unread ? ` (${unread})` : ''}</Button>
            {monitors.list.length > 1 && (
              <Select value={String(monitors.selected)} onChange={(e) => { const id = Number(e.target.value); setMonitors((m) => ({ ...m, selected: id })); sendJSON({ t: 'mon', id }); }}
                 aria-label={t('viewer.hostDisplay')} title={t('viewer.chooseDisplay')} className="h-7 w-[150px] bg-card py-0 text-[12px]">
                {monitors.all !== false && <option value="0">{t('viewer.allDisplays')}</option>}
                {monitors.list.map((m) => <option key={m.id} value={String(m.id)}>{t(m.primary ? 'viewer.displayMain' : 'viewer.display', { id: m.id, size: `${m.w}×${m.h}` })}</option>)}
              </Select>
            )}
            <span className="text-[10.5px] text-faint tabular" title={t('viewer.resolution')}>{frameSize.width ? `${frameSize.width}×${frameSize.height}` : '—×—'}</span>
            <Select value={scale} onChange={(e) => setScale(e.target.value)}  aria-label={t('viewer.scale')} title={t('viewer.scaleHint')} className="h-7 w-[132px] bg-card py-0 text-[12px]">
              <option value="fit">{t('viewer.fit')}</option>
              <option value="50">50%</option><option value="75">75%</option><option value="100">{t('viewer.native100')}</option><option value="125">125%</option>
            </Select>
            <Button variant={fullscreen ? 'primary' : 'ghost'} size="sm" icon={fullscreen ? 'fullscreenexit' : 'fullscreen'} onClick={toggleFullscreen} disabled={!document.fullscreenEnabled} title={fullscreen ? t('viewer.exitFullscreen') : t('viewer.openFullscreen')}>{fullscreen ? t('viewer.exitFullscreen') : t('viewer.fullscreen')}</Button>
          </>}
          <button onClick={onClose} className="text-faint hover:text-ink p-1.5"><Icon name="close" size={18} /></button>
        </div>
      </div>
      {allowFiles && fb && (
        <div className="absolute inset-0 z-[5] bg-black/55 flex items-center justify-center" onClick={() => filesOnly ? onClose() : setFb(null)}>
          <div className="w-[calc(100%-1rem)] max-w-[760px] h-[calc(100%-1rem)] sm:h-[80vh] bg-card border border-line rounded-xl flex flex-col overflow-hidden shadow-2xl" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2 px-3 h-11 border-b border-line">
              <Icon name="folder" size={15} className="text-muted" />
              <span className="text-[12.5px] font-semibold text-ink">{t('viewer.fileTransferOn', { host: hostname })}</span>
              <button onClick={() => filesOnly ? onClose() : setFb(null)} className="ml-auto text-faint hover:text-ink p-1"><Icon name="close" size={16} /></button>
            </div>
            {/* toolbar */}
            <div className="flex items-center gap-0.5 px-2 min-h-10 border-b border-line bg-elevated/40 overflow-x-auto scroll-area shrink-0">
              <Button variant="ghost" size="sm" icon="chevleft" title={t('viewer.up')} disabled={parentPath(fb.path) === null} onClick={() => browse(parentPath(fb.path))} />
              <Button variant="ghost" size="sm" icon="refresh" title={t('action.refresh')} onClick={() => browse(fb.path)} />
              <Button variant="ghost" size="sm" icon="plus" title={t('viewer.newFolder')} disabled={!fb.path} onClick={doMkdir} />
              <div className="w-px h-5 bg-line mx-1" />
              <Button variant="ghost" size="sm" icon="upload" title={t('viewer.uploadHere')} disabled={!fb.path} onClick={() => fileInputRef.current?.click()} />
              <Button variant="ghost" size="sm" icon="download" title={t('action.download')} disabled={!fbSel.length} onClick={doDownloadSel} />
              <Button variant="ghost" size="sm" icon="copy" title={t('action.copy')} disabled={!fbSel.length} onClick={() => doClip('copy')} />
              <Button variant="ghost" size="sm" icon="scissors" title={t('viewer.cut')} disabled={!fbSel.length} onClick={() => doClip('cut')} />
              <Button variant="ghost" size="sm" icon="edit" title={t('viewer.rename')} disabled={fbSel.length !== 1} onClick={doRename} />
              <Button variant="ghost" size="sm" icon="trash" title={t('action.delete')} disabled={!fbSel.length} onClick={doDelete} />
              {fbClip && <Button variant="secondary" size="sm" icon="approve" onClick={doPaste}>{t('viewer.paste', { n: fbClip.names.length })}</Button>}
              <span className="ml-auto text-[11px] text-faint pr-1">{fbSel.length ? t('viewer.selected', { n: fbSel.length }) : (fb.entries ? tn('viewer.items', fb.entries.length) : '')}</span>
            </div>
            {/* editable address bar */}
            <div className="px-2 h-10 flex items-center gap-2 border-b border-line shrink-0">
              <Input value={pathEdit} onChange={(e) => setPathEdit(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') browse(pathEdit.trim()); }} placeholder={isWin ? t('viewer.pathWin') : t('viewer.pathPosix')} className="text-[11.5px] mono h-7" />
              <Button variant="secondary" size="sm" icon="arrowright" onClick={() => browse(pathEdit.trim())} title={t('viewer.go')} />
            </div>
            {/* list */}
            <div className="flex-1 overflow-y-auto">
              {fb.entries === null && <div className="p-6 text-center text-faint text-[12px]">{t('common.loading')}</div>}
              {fb.entries && fb.entries.length === 0 && <div className="p-6 text-center text-faint text-[12px]">{t('viewer.emptyFolder')}</div>}
              {fb.entries && fb.entries.map((en, i) => (
                <div key={i} className={cx('flex items-center gap-2 px-3 py-1.5 border-b border-line/40', fbSel.includes(en.name) ? 'bg-brand/10' : 'hover:bg-elevated')}>
                  <input type="checkbox" checked={fbSel.includes(en.name)} onChange={() => toggleSel(en.name)} className="h-3.5 w-3.5 accent-brand shrink-0" />
                  <button onClick={() => en.dir ? browse(joinPath(fb.path, en.name)) : toggleSel(en.name)} onDoubleClick={() => !en.dir && downloadEntry(en.name)} className="flex items-center gap-2 flex-1 text-left min-w-0">
                    <Icon name={en.dir ? 'folder' : 'file'} size={14} className={en.dir ? 'text-brand' : 'text-faint'} />
                    <span className="text-[12.5px] text-ink truncate">{en.name}</span>
                  </button>
                  <span className="text-[11px] text-faint w-32 text-right shrink-0 hidden sm:block">{en.mod || ''}</span>
                  <span className="text-[11px] text-faint w-20 text-right shrink-0">{en.dir ? '' : fmtSize(en.size)}</span>
                </div>
              ))}
            </div>
            <div className="px-3 py-1.5 border-t border-line text-[11px] text-faint">{t('viewer.browserHelp')}</div>
          </div>
        </div>
      )}
      <div className="relative flex-1 flex overflow-hidden">
        {filesOnly ? (
          <div className="flex-1 flex items-center justify-center text-faint text-[12.5px]">
            {fb ? t('viewer.fileSession') : t('viewer.openingBrowser')}
          </div>
        ) : (<>
        <div className="relative flex-1 overflow-auto p-2 scroll-area">
          <canvas ref={canvasRef} tabIndex={0} style={canvasStyle} className={cx('outline-none remote-canvas', scale === 'fit' ? 'absolute inset-0 m-auto max-w-full max-h-full' : 'block mx-auto')} />
          {conflict && (
            <div className="absolute inset-0 z-[3] flex items-center justify-center bg-black/70 p-4">
              <div className="max-w-sm rounded-xl border border-line bg-card p-5 text-center shadow-2xl">
                <Icon name="remote" size={22} className="mx-auto text-warn" />
                <div className="mt-2 text-[14px] font-semibold text-ink">{t(conflict === 'replaced' ? 'viewer.replacedTitle' : 'viewer.activeTitle')}</div>
                <p className="mt-1.5 text-[12.5px] text-muted">{t(conflict === 'replaced' ? 'viewer.replacedBody' : 'viewer.activeBody', { host: hostname })}</p>
                <div className="mt-4 flex justify-center gap-2">
                  <Button variant="secondary" size="sm" onClick={onClose}>{t('action.close')}</Button>
                  <Button variant="primary" size="sm" icon="remote" onClick={() => takeOverRef.current()}>{t('viewer.continueHere')}</Button>
                </div>
              </div>
            </div>
          )}
          {/* Solid colours on purpose: the palette is CSS variables, and Tailwind
              emits nothing for an opacity modifier on one (bg-panel/95), which
              left these see-through over the black viewer. */}
          <div className="absolute top-3 left-1/2 -translate-x-1/2 z-[2] w-[min(92%,640px)] flex flex-col items-center gap-2 pointer-events-none">
            {!isGuest && can('devices.remote.share') && <GuestRequests deviceId={deviceId} />}
            {notice && (
              <div className="pointer-events-auto max-w-full rounded-lg border border-warn bg-card px-3.5 py-2 text-[12px] text-ink flex items-start gap-2 shadow-lg">
                <Icon name="info" size={14} className="text-warn shrink-0 mt-0.5" />
                <span>{notice}</span>
                <button onClick={() => setNotice(null)} className="text-faint hover:text-ink shrink-0"><Icon name="close" size={13} /></button>
              </div>
            )}
          </div>
        </div>
        {chatOpen && <div className="absolute inset-0 z-[4] w-full border-l border-line bg-app flex flex-col sm:static sm:w-72 sm:shrink-0">
          <div className="px-3 h-9 flex items-center text-[11.5px] font-semibold text-muted border-b border-line">{t('viewer.chat')}<button onClick={() => setChatOpen(false)} className="ml-auto text-faint hover:text-ink" title={t('viewer.collapseChat')}><Icon name="chevright" size={16} /></button></div>
          <div className="flex-1 overflow-y-auto p-3 space-y-2">
            {chat.length === 0 && <div className="text-[11.5px] text-faint">{isGuest ? t('guest.chatEmpty') : t('viewer.noMessages', { host: hostname })}</div>}
            {chat.map((m, i) => (
              <div key={i} className={cx('text-[12px] max-w-[85%] rounded-lg px-2.5 py-1.5', m.from === 'self' ? 'ml-auto bg-brand text-white shadow-sm shadow-brand/20' : 'mr-auto bg-elevated text-ink border border-line2')}>
                <div className={cx('text-[10px] mb-0.5 font-semibold', m.from === 'self' ? 'text-white/75' : m.from === 'guest' ? 'text-warn' : m.from === 'tech' ? 'text-brandhi' : 'text-run')}>{chatLabel(m)}</div>{m.text}
              </div>
            ))}
          </div>
          <div className="p-2 border-t border-line flex gap-1.5">
            <Input value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') sendChat(); }} placeholder={t('viewer.message')} maxLength={isGuest ? 1000 : undefined} className="text-[12px]" />
            <Button variant="primary" size="sm" icon="send" onClick={sendChat} disabled={!draft.trim()} />
          </div>
        </div>}
        </>)}
      </div>
    </div>
  );
}

// adoptDynamicStyles lets xterm.js render under our strict CSP without loosening it.
//
// xterm builds four <style> elements at runtime and fills them with values it can
// only know then — theme colours, cell dimensions, font metrics. `style-src-elem
// 'self'` blocks exactly that, which is why the terminal came up as a blank black
// screen. Neither a static stylesheet nor a CSP hash can cover content computed at
// runtime, and xterm has no nonce option (the library never reads one).
//
// A CSSStyleSheet built with `new CSSStyleSheet()` and adopted is not a <style>
// *element*, so style-src-elem does not govern it and it applies under the policy
// unchanged. Verified in Chrome against this exact CSP: the element form does not
// apply, the constructed form does.
//
// So we intercept the style elements xterm creates and mirror their text into one
// adopted sheet. The element is still inserted and stays inert, which is harmless:
// xterm's selectors are document-wide (`#xterm-<id> …`, `.xterm …`), so where the
// rules live does not change what they match.
//
// The alternative was a per-request nonce injected by nginx. It would work, but it
// needs the same interception to stamp the nonce onto xterm's elements, plus
// server-side templating of a static file and a dev/prod split. This keeps the fix
// entirely in the client and identical in both.
function adoptDynamicStyles() {
  if (window.__rmmStyleShim || typeof CSSStyleSheet !== 'function' || !('adoptedStyleSheets' in Document.prototype)) return;
  window.__rmmStyleShim = true;
  const sheets = new WeakMap();
  const nativeCreate = document.createElement.bind(document);

  const sheetFor = (el) => {
    let sheet = sheets.get(el);
    if (!sheet) {
      sheet = new CSSStyleSheet();
      sheets.set(el, sheet);
      document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
    }
    return sheet;
  };
  const mirror = (el, css) => {
    // A malformed rule must never take the terminal down with it.
    try { sheetFor(el).replaceSync(String(css == null ? '' : css)); } catch (e) {}
  };

  document.createElement = function (tagName, options) {
    const el = nativeCreate(tagName, options);
    if (String(tagName).toLowerCase() !== 'style') return el;
    // xterm writes through textContent in three places and innerText in one, so
    // both are mirrored. The value is still written to the element itself, which
    // keeps anything that reads it back working.
    for (const prop of ['textContent', 'innerText']) {
      const base = Object.getOwnPropertyDescriptor(Node.prototype, prop)
        || Object.getOwnPropertyDescriptor(HTMLElement.prototype, prop);
      if (!base || !base.set) continue;
      Object.defineProperty(el, prop, {
        configurable: true,
        get() { return base.get.call(this); },
        set(v) { base.set.call(this, v); mirror(this, v); },
      });
    }
    return el;
  };
}

// TerminalViewer is the native "Terminal" remote provider's viewer: an xterm.js
// terminal bridged to an elevated shell on the endpoint. It brokers a session
// (provider 'terminal'), connects to the relay as the viewer peer, and exchanges
// raw bytes with the agent — keystrokes out (binary), shell output in (binary),
// plus JSON control ({t:'resize'} out; {t:'hello'|'exit'|'error'} in). Pairs with
// cmd/agent/terminal.go.
function TerminalViewer({ deviceId, hostname, onClose }) {
  const toast = useToast();
  const elRef = useRef(null);
  const wsRef = useRef(null);
  const fitRef = useRef(null);
  const [status, setStatus] = useState('connecting');
  const [info, setInfo] = useState(null); // { shell, user, elevated, os }

  useEffect(() => {
    if (!window.Terminal) { toast.error(t('term.unavailable'), t('term.xtermFailed')); setStatus('closed'); return; }
    adoptDynamicStyles(); // must precede construction: xterm styles on open()
    const term = new window.Terminal({
      cursorBlink: true, fontSize: 13, scrollback: 5000,
      fontFamily: 'JetBrains Mono, ui-monospace, Menlo, Consolas, monospace', // i18n-ignore: a font stack
      theme: { background: '#0b0e14', foreground: '#d6deeb' },
    });
    const Fit = window.FitAddon && window.FitAddon.FitAddon;
    const fit = Fit ? new Fit() : null;
    if (fit) term.loadAddon(fit);
    term.open(elRef.current);
    const doFit = () => { if (fit) { try { fit.fit(); } catch (e) {} } };
    doFit();
    fitRef.current = fit;

    const enc = new TextEncoder();
    const sendResize = () => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify({ t: 'resize', cols: term.cols, rows: term.rows })); };
    term.onData((d) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(enc.encode(d)); });
    term.onResize(sendResize);

    let stopped = false;
    const connect = async () => {
      setStatus('connecting');
      let wsUrl, viewerTicket;
      try {
        const s = await api.startRemote(deviceId, 'terminal');
        wsUrl = api.websocketURL('/ws/remote?session=' + encodeURIComponent(s.id) + '&role=viewer');
        viewerTicket = s.viewer_ticket;
      } catch (e) { setStatus('closed'); toast.error(t('term.failed'), e.message); return; }
      if (stopped) return;
      const ws = new WebSocket(wsUrl, relayProtocols(viewerTicket));
      wsRef.current = ws;
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => { setStatus('connected'); doFit(); sendResize(); term.focus(); };
      ws.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          try {
            const m = JSON.parse(ev.data);
            if (m.t === 'hello') setInfo(m);
            else if (m.t === 'exit') { term.write('\r\n\x1b[33m' + t('term.ended') + '\x1b[0m\r\n'); setStatus('closed'); }
            else if (m.t === 'error') term.write('\r\n\x1b[31m' + (m.msg || 'error') + '\x1b[0m\r\n');
          } catch (e) {}
          return;
        }
        term.write(new Uint8Array(ev.data));
      };
      ws.onclose = () => { if (!stopped) setStatus('closed'); };
      ws.onerror = () => {};
    };

    const onWinResize = () => doFit();
    window.addEventListener('resize', onWinResize);
    connect();
    return () => {
      stopped = true;
      window.removeEventListener('resize', onWinResize);
      if (wsRef.current) wsRef.current.close();
      term.dispose();
    };
  }, [deviceId]);

  const statusCls = status === 'connected' ? 'bg-oksoft text-ok' : status === 'closed' ? 'bg-badsoft text-bad' : 'bg-warnsoft text-warn';
  return (
    <div className="fixed inset-0 z-[80] bg-[#0b0e14] flex flex-col">
      <div className="flex items-center gap-2 px-2 sm:px-4 min-h-11 bg-elevated border-b border-line shrink-0 overflow-x-auto scroll-area">
        <Icon name="terminal" size={15} className="text-muted" />
        <span className="text-[13px] font-semibold text-ink">{hostname}</span>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{tEnum('viewer.status', status)}</span>
        {info && (
          <span className="text-[10.5px] text-faint flex items-center gap-2">
            <span className="mono">{info.shell}</span>
            {info.elevated
              ? <span className="text-ok inline-flex items-center gap-1"><Icon name="shield" size={11} />{t('device.elevated')}{info.user ? ` · ${info.user}` : ''}</span>
              : (info.user && <span>{info.user}</span>)}
          </span>
        )}
        <div className="ml-auto flex items-center gap-2">
          <button onClick={onClose} className="text-faint hover:text-ink p-1.5"><Icon name="close" size={18} /></button>
        </div>
      </div>
      <div ref={elRef} className="flex-1 overflow-hidden p-2" />
    </div>
  );
}
window.TerminalViewer = TerminalViewer;

// fmtSize renders a byte count compactly (KB/MB/GB).
function fmtSize(b) {
  if (!b) return '0 KB';
  if (b < 1024 * 1024) return Math.max(1, (b / 1024) | 0).toLocaleString() + ' KB';
  if (b < 1024 * 1024 * 1024) return (b / 1048576).toFixed(1) + ' MB';
  return (b / 1073741824).toFixed(2) + ' GB';
}

// saveBlob triggers a browser download of a received remote file.
function saveBlob(blob, name) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = name || 'download';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/* inventory */
function InfoGrid({ rows }) {
  return (
    <div className="grid grid-cols-2 gap-x-6 gap-y-2.5">
      {rows.map(([k, v]) => (
        <div key={k} className="flex items-center justify-between gap-3 border-b border-line/60 pb-2">
          <span className="text-[12px] text-faint">{k}</span>
          <span className={cx('text-[12.5px] text-ink font-medium text-right mono', String(v || '').includes('\n') ? 'whitespace-pre-line break-words' : 'truncate')}>{v}</span>
        </div>
      ))}
    </div>
  );
}
function InventorySection({ title, icon, children, className = '' }) {
  return (
    <Card className={cx('p-4', className)}>
      <div className="flex items-center gap-2 mb-3 text-[12.5px] font-semibold text-ink"><Icon name={icon} size={14} className="text-muted" />{title}</div>
      {children}
    </Card>
  );
}
function inventoryBytes(bytes) {
  const n = Number(bytes);
  return Number.isFinite(n) && n > 0 ? Math.round(n / 1e9) + ' GB' : '';
}
function inventoryText(v) {
  return v == null || String(v).trim() === '' ? t('inv.notReported') : String(v).trim();
}
function inventoryList(items, formatter) {
  const lines = (items || []).map(formatter).filter(Boolean);
  return lines.length ? lines.join('\n') : t('inv.notReported');
}
function hardwareRows(hw, cpu, mem) {
  const mb = hw.motherboard || {};
  const motherboard = [mb.manufacturer, mb.product || mb.model, mb.version].filter(Boolean).join(' ');
  const motherboardSerial = mb.serial ? 'SN ' + mb.serial : '';
  // Fold the (old) CPU & Memory section into the static rows: cores + live load
  // ride along the Processor row, and live used% rides along the Memory row.
  const procBase = inventoryText(hw.processor || cpu.model);
  const procExtra = [cpu.cores ? t('inv.cores', { n: cpu.cores }) : '', cpu.load_pct != null ? t('inv.load', { pct: cpu.load_pct }) : ''].filter(Boolean).join(' · ');
  const memTotal = inventoryBytes(hw.memory_total_bytes) || (mem.total_gb ? mem.total_gb + ' GB' : '');
  const memVal = memTotal ? (memTotal + (mem.used_pct != null ? ' · ' + t('inv.used', { pct: mem.used_pct }) : '')) : t('inv.notReported');
  return [
    [t('inv.serviceTag'), inventoryText(hw.service_tag || hw.serial_number)],
    [t('inv.systemModel'), inventoryText([hw.manufacturer, hw.system_model].filter(Boolean).join(' '))],
    [t('inv.processor'), procExtra ? procBase + ' · ' + procExtra : procBase],
    [t('inv.memory'), memVal],
    [t('inv.motherboard'), inventoryText([motherboard, motherboardSerial].filter(Boolean).join('\n'))],
    [t('inv.videoCard'), inventoryList(hw.gpus, (g) => [g.name || g.model, inventoryBytes(g.memory_bytes) || g.memory, g.driver_version].filter(Boolean).join(' · '))],
    [t('inv.storage'), inventoryList(hw.storage_devices, (d) => [d.model || d.name, d.type || d.interface, inventoryBytes(d.size_bytes)].filter(Boolean).join(' · '))],
    [t('inv.bios'), inventoryText(hw.bios)],
  ];
}
function InventoryView({ id }) {
  const invQ = useQuery([id, 'inv'], () => api.getInventory(id), {});
  const [raw, setRaw] = useState(false);
  if (invQ.loading) return <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-28 w-full rounded-2xl" />)}</div>;
  const data = invQ.data?.data || {};
  const sys = data.system || {}, cpu = data.cpu || {}, mem = data.memory || {}, net = data.network || {};
  const disks = data.disks || [], hw = data.hardware || {}, sw = data.software || [];
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-[11.5px] text-faint">{t('inv.collected', { when: timeAgo(invQ.data?.collected_at) })}</span>
        <button onClick={() => setRaw((r) => !r)} className="text-[12px] text-muted hover:text-ink flex items-center gap-1.5"><Icon name="script" size={13} />{raw ? t('inv.structured') : t('inv.rawJson')}</button>
      </div>
      {raw ? <CodeBlock code={JSON.stringify(data, null, 2)} shell="bash" label="inventory.json" maxHeight={520} /> : (
        <div className="grid md:grid-cols-2 gap-4">
          <InventorySection title={t('inv.system')} icon="devices">
            <InfoGrid rows={[[t('inv.hostname'), sys.hostname], [t('inv.os'), sys.os], [t('inv.arch'), sys.arch], [t('inv.domain'), sys.domain], [t('inv.uptime'), t('inv.hours', { n: sys.uptime_hours || 0 })]]} />
          </InventorySection>
          <InventorySection title={t('inv.hardware')} icon="wrench" className="md:col-span-2">
            <InfoGrid rows={hardwareRows(hw, cpu, mem)} />
          </InventorySection>
          <InventorySection title={t('inv.storage')} icon="disk">
            <div className="space-y-2.5">
              {disks.map((dk, i) => {
                const usedPct = Math.round((1 - dk.free_gb / dk.total_gb) * 100);
                return (
                  <div key={i}>
                    <div className="flex items-center justify-between text-[12px] mb-1"><span className="mono text-ink">{dk.mount} <span className="text-faint">{dk.fs}</span></span><span className="text-muted">{t('inv.free', { free: dk.free_gb, total: dk.total_gb })}</span></div>
                    <progress value={usedPct} max="100" className={cx('disk-usage', usedPct > 88 ? 'bad' : usedPct > 75 ? 'warn' : '')} />
                  </div>
                );
              })}
            </div>
          </InventorySection>
          <InventorySection title={t('inv.network')} icon="network">
            {(net.interfaces || []).map((ifc, i) => <InfoGrid key={i} rows={[[t('inv.interface'), ifc.name], ['IPv4', ifc.ipv4], ['MAC', ifc.mac]]} />)}
            {(net.interfaces || []).length === 0 && <div className="text-[12.5px] text-faint">{t('inv.noInterfaces')}</div>}
          </InventorySection>
          <InstalledSoftware software={sw} />
        </div>
      )}
    </div>
  );
}

/* Installed software: a workstation reports hundreds of entries, and rendering
   all of them buried the rest of the inventory tab under a wall of names. Shows
   a first page with a filter, and opens on demand. */
function InstalledSoftware({ software }) {
  const PREVIEW = 12;
  const [expanded, setExpanded] = useState(false);
  const [q, setQ] = useState('');
  const list = software || [];
  const ql = q.trim().toLowerCase();
  const matching = !ql ? list : list.filter((s) => String(s.name || '').toLowerCase().includes(ql) || String(s.version || '').toLowerCase().includes(ql));
  // A search is a request to see what matches, so it opens the list on its own.
  const shown = expanded || ql ? matching : matching.slice(0, PREVIEW);
  const hidden = matching.length - shown.length;

  return (
    <Card className="p-4 md:col-span-2">
      <div className="flex items-center gap-2 mb-3 text-[12.5px] font-semibold text-ink">
        <Icon name="grid" size={14} className="text-muted" />{t('inv.software')}
        <span className="text-faint font-normal">({list.length})</span>
        {list.length > PREVIEW && (
          <div className="ml-auto relative">
            <Icon name="search" size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-faint" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('inv.filterSoftware')} className="h-7 pl-7 text-[12px] w-[200px]" />
          </div>
        )}
      </div>
      <div className="grid sm:grid-cols-2 gap-x-6 gap-y-1.5">
        {shown.map((s, i) => <div key={i} className="flex items-center justify-between border-b border-line/60 pb-1.5"><span className="text-[12.5px] text-ink">{s.name}</span><span className="text-[11.5px] text-faint mono">{s.version}</span></div>)}
      </div>
      {list.length === 0 && <div className="text-[12.5px] text-faint">{t('inv.noSoftware')}</div>}
      {list.length > 0 && !matching.length && <div className="text-[12.5px] text-faint">{t('inv.nothingMatches', { q })}</div>}
      {hidden > 0 && (
        <Button variant="ghost" size="sm" icon="chevdown" className="mt-3" onClick={() => setExpanded(true)}>
          {t('inv.showMore', { n: hidden })}
        </Button>
      )}
      {expanded && !ql && matching.length > PREVIEW && (
        <Button variant="ghost" size="sm" icon="chevdown" className="mt-3" onClick={() => setExpanded(false)}>
          {t('inv.showLess')}
        </Button>
      )}
    </Card>
  );
}

/* device jobs tab */
function DeviceJobs({ id, navigate, onClose }) {
  const jobsQ = useQuery([id, 'jobs'], () => api.listJobs({ agent_id: id }), { invalidateOn: ['jobs'], poll: 3000 });
  const jobs = jobsQ.data?.jobs || [];
  if (jobsQ.loading) return <div className="space-y-2">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-16 w-full rounded-xl" />)}</div>;
  if (!jobs.length) return <EmptyState icon="flash" title={t('djobs.empty')} msg={t('djobs.emptyMsg')} />;
  return (
    <div className="space-y-2">
      {jobs.map((j) => (
        <button key={j.id} onClick={() => { onClose(); navigate('jobs', { focus: j.id }); }} className="w-full text-left rounded-xl border border-line bg-card hover:border-line2 hover:bg-elevated/40 transition p-3.5 flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2"><span className="text-[13px] text-ink font-medium truncate">{j.description}</span>{j.created_by.startsWith('ai:') && <Icon name="sparkle" size={12} className="text-brandhi" />}</div>
            <div className="text-[11.5px] text-faint mt-0.5">{j.created_by} · {timeAgo(j.created_at)}{j.exit_code != null && <span className="mono"> · {t('djobs.exit', { code: j.exit_code })}</span>}</div>
          </div>
          <StatusPill status={j.status} size="sm" pulse />
        </button>
      ))}
    </div>
  );
}

// Where a check or task on this device comes from: assigned to the device, or
// a policy on its department, site or client.
function MonitoringSource({ row }) {
  if (!row.policy_id) return <span>{t('dmon.direct')}</span>;
  return <span className="inline-flex items-center gap-1 text-brandhi"><Icon name="policy" size={12} />{row.policy_name || t('dmon.policy')}</span>;
}

// The link to the screen where these are created and changed: the drawer only
// shows what applies here.
function MonitoringFooter({ hint, screen, label, navigate, onClose }) {
  return (
    <div className="flex items-center justify-between gap-3 pt-1">
      <p className="text-[11.5px] text-faint">{hint}</p>
      {navigate && <Button size="sm" variant="ghost" icon="arrowright" onClick={() => { onClose(); navigate(screen); }}>{label}</Button>}
    </div>
  );
}

/* device checks tab: the enabled checks the scheduler runs on this device, with
   this device's latest state */
function DeviceChecks({ id, navigate, onClose }) {
  const checksQ = useQuery([id, 'device-checks'], () => api.listDeviceChecks(id), { invalidateOn: ['checks', 'policies'], poll: 10000 });
  const checks = checksQ.data?.checks || [];
  if (checksQ.loading) return <div className="space-y-2">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-16 w-full rounded-xl" />)}</div>;
  if (checksQ.error) return <EmptyState icon="alert" title={t('dmon.loadFailed')} msg={checksQ.error.message} />;
  const footer = <MonitoringFooter hint={t('dchecks.hint')} screen="checks" label={t('dchecks.manage')} navigate={navigate} onClose={onClose} />;
  if (!checks.length) return <div className="space-y-3"><EmptyState icon="check" title={t('dchecks.empty')} msg={t('dchecks.emptyMsg')} />{footer}</div>;
  return (
    <div className="space-y-2">
      {checks.map((c) => {
        const ct = window.checkType(c.type);
        // The value is shown only when there is no output: a check's output
        // usually states it already ("92% used").
        const value = c.value != null && !c.output ? Math.round(c.value * 10) / 10 + (ct.unit || '') : null;
        return (
          <div key={c.id} className="rounded-xl border border-line bg-card p-3.5 flex items-start gap-3">
            <Icon name={ct.icon} size={15} className="text-muted mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] text-ink font-medium truncate">{c.name}</div>
              <div className="text-[11.5px] text-faint mt-0.5 flex flex-wrap items-center gap-x-1.5">
                <span>{ct.label}</span><span>·</span><span>{fmtInterval(c.interval_sec)}</span><span>·</span><MonitoringSource row={c} />
              </div>
              {c.output && <div className="text-[11.5px] text-muted mono mt-1 truncate" title={c.output}>{c.output}</div>}
              <div className="text-[11px] text-faint mt-0.5">
                {c.last_result_at ? t('dchecks.lastResult', { when: timeAgo(c.last_result_at) }) : t('dchecks.noResult')}
                {value != null && <span className="mono"> · {value}</span>}
              </div>
            </div>
            <StatusPill status={c.status} size="sm" />
          </div>
        );
      })}
      {footer}
    </div>
  );
}

/* device tasks tab: the enabled tasks the scheduler runs on this device, with
   their last run here */
function DeviceTasks({ id, navigate, onClose }) {
  const tasksQ = useQuery([id, 'device-tasks'], () => api.listDeviceTasks(id), { invalidateOn: ['tasks', 'policies', 'jobs'], poll: 15000 });
  const tasks = tasksQ.data?.tasks || [];
  if (tasksQ.loading) return <div className="space-y-2">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-16 w-full rounded-xl" />)}</div>;
  if (tasksQ.error) return <EmptyState icon="alert" title={t('dmon.loadFailed')} msg={tasksQ.error.message} />;
  const footer = <MonitoringFooter hint={t('dtasks.hint')} screen="tasks" label={t('dtasks.manage')} navigate={navigate} onClose={onClose} />;
  if (!tasks.length) return <div className="space-y-3"><EmptyState icon="tasks" title={t('dtasks.empty')} msg={t('dtasks.emptyMsg')} />{footer}</div>;
  return (
    <div className="space-y-2">
      {tasks.map((task) => (
        <div key={task.id} className="rounded-xl border border-line bg-card p-3.5 flex items-start gap-3">
          <Icon name="tasks" size={15} className="text-muted mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-[13px] text-ink font-medium truncate">{task.name}</div>
            <div className="text-[11.5px] text-faint mt-0.5 flex flex-wrap items-center gap-x-1.5">
              <span className="inline-flex items-center gap-1"><Icon name="clock" size={12} />{window.taskSchedule(task)}</span><span>·</span><MonitoringSource row={task} />
            </div>
            <div className="text-[11px] text-faint mt-0.5">{task.last_run_at ? t('dtasks.lastRun', { when: timeAgo(task.last_run_at) }) : t('dtasks.neverRun')}</div>
          </div>
        </div>
      ))}
      {footer}
    </div>
  );
}

/* run script form */
function RunScriptForm({ device, onCreated }) {
  const { can } = useAuth();
  const toast = useToast();
  const defShell = device.os.toLowerCase().includes('win') ? 'powershell' : 'bash';
  const [shell, setShell] = useState(defShell);
  const [desc, setDesc] = useState('');
  const [script, setScript] = useState(defShell === 'powershell' ? 'Get-Service | Where-Object Status -eq "Running" | Select-Object -First 10' : 'uptime\ndf -h /'); // i18n-ignore: a sample script
  const [busy, setBusy] = useState(false);
  const [runNow, setRunNow] = useState(false);
  const scriptsQ = useQuery(['scripts-pick'], () => can('scripts.view') ? api.listScripts() : Promise.resolve({ scripts: [] }), { invalidateOn: ['scripts'] });
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const canDirect = can('jobs.manage') && can('settings.manage') && cfgQ.data?.server?.allow_unapproved_jobs;
  const direct = canDirect && runNow;

  if (!can('jobs.create')) return <EmptyState icon="shield" title={t('run.readOnly')} msg={t('run.needPerm')} />;

  const submit = async () => {
    setBusy(true);
    try {
      await api.createJob({ agent_id: device.id, shell, script, description: desc || t('run.defaultDesc'), require_approval: !direct });
      if (direct) toast.success(t('run.dispatched'), t('run.runningOn', { host: device.hostname }));
      else toast.success(t('run.created'), t('run.queued'));
      onCreated();
    } catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };

  return (
    <div className="space-y-4 max-w-2xl">
      <div className={cx('flex items-start gap-2.5 rounded-xl border px-3.5 py-3', direct ? 'border-bad/25 bg-badsoft' : 'border-warn/25 bg-warnsoft')}>
        <Icon name="info" size={16} className={cx('mt-0.5 shrink-0', direct ? 'text-bad' : 'text-warn')} />
        <div className="text-[12.5px] text-ink leading-relaxed">{direct
          ? t('run.directOn', { host: device.hostname })
          : t('run.gated', { host: device.hostname })}</div>
      </div>
      {canDirect && (
        <label className="flex items-center gap-2.5 cursor-pointer select-none">
          <input type="checkbox" checked={runNow} onChange={(e) => setRunNow(e.target.checked)} className="h-4 w-4 accent-bad" />
          <span className="text-[12.5px] text-ink">{t('run.immediately')}</span>
          <span className="text-[11.5px] text-faint">{t('run.immediatelyHint')}</span>
        </label>
      )}
      <div className="flex gap-3">
        <Field label={t('deploy.shell')} className="w-44">
          <Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select>
        </Field>
        {/* Inserts a copy and springs back to the placeholder. Leaving the chosen
            name selected read as "this job is bound to that library script", so
            an operator writing something ad-hoc looked for a way to deselect —
            when the editor below was theirs to type in all along. Nothing about
            the job references the library: only shell, script and description
            are submitted. */}
        <Field label={t('run.fromLibrary')} className="flex-1" hint={t('run.fromLibraryHint')}>
          <Select value="" onChange={(e) => {
            const s = (scriptsQ.data?.scripts || []).find((x) => x.id === e.target.value);
            if (!s) return;
            setScript(s.body); setShell(s.shell); setDesc(s.name);
            toast.info(t('run.loaded', { name: s.name }), t('run.loadedHint'));
          }}>
            <option value="">{t('run.startFrom')}</option>
            {(scriptsQ.data?.scripts || []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </Select>
        </Field>
      </div>
      <Field label={t('common.description')}><Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t('run.whatDoes')} /></Field>
      <Field label={t('deploy.script')}><CodeEditor value={script} onChange={setScript} shell={shell} minHeight={220} /></Field>
      <div className="flex justify-end gap-2">
        <Button variant="primary" icon="plus" loading={busy} onClick={submit} disabled={!script.trim()}>{direct ? t('run.runNowOn', { host: device.hostname }) : t('run.createGated')}</Button>
      </div>
    </div>
  );
}

// DevicePatches is the device-drawer Patches tab: missing patches + scan/install/
// reboot actions. Self-contained (no cross-file component dependency).
function DevicePatches({ id, hostname }) {
  const { can } = useAuth();
  const toast = useToast();
  // A reboot takes the machine away from whoever is using it: asked, not fired.
  const [confirmReboot, setConfirmReboot] = useState(false);
  const q = useQuery([id, 'dpatches'], () => api.devicePatches(id), { invalidateOn: ['patches'], poll: 5000 });
  const patches = q.data?.patches || [];
  const approved = patches.filter((p) => p.status === 'approved').length;
  const classColor = (c) => (c === 'security' || c === 'critical') ? 'text-bad' : (c === 'important' || c === 'moderate') ? 'text-warn' : 'text-faint';
  const act = async (fn, msg) => { try { await fn(); toast.info(msg); window.dataBus.emit('patches'); } catch (e) { toast.error(t('common.failed'), e.message); } };
  return (
    <div>
      <div className="flex items-center gap-2 mb-3 flex-wrap">
        <div className="text-[12.5px] text-muted flex-1">{approved ? t('dpatch.summary', { items: tn('viewer.items', patches.length), approved }) : tn('viewer.items', patches.length)}</div>
        {can('patches.manage') && <Button size="sm" variant="secondary" icon="refresh" onClick={() => act(() => api.scanDevicePatches(id), t('dpatch.scanRequested'))}>{t('dpatch.scan')}</Button>}
        {can('patches.manage') && <Button size="sm" variant="primary" icon="download" disabled={!approved} onClick={() => act(() => api.installDevicePatches(id, []), t('dpatch.installing'))}>{t('dpatch.installApproved')}</Button>}
        {can('patches.manage') && <Button size="sm" variant="ghost" icon="refresh" onClick={() => setConfirmReboot(true)}>{t('reboot.button')}</Button>}
      </div>
      {q.loading ? <Skeleton className="h-20 w-full" />
        : !patches.length ? <EmptyState icon="shield" title={t('dpatch.none')} msg={t('dpatch.noneMsg')} />
          : <div className="space-y-1.5">{patches.map((p) => (
            <div key={p.patch_id} className="flex items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2 text-[12.5px]">
              <span className={cx('text-[10.5px] font-semibold uppercase tracking-wide w-16 shrink-0', classColor(p.classification))}>{tEnum('patch.class', p.classification)}</span>
              <span className="flex-1 min-w-0 truncate text-ink" title={p.title}>{p.title || p.identifier}</span>
              {p.kb && <span className="mono text-faint shrink-0">{p.kb}</span>}
              <span className={cx('text-[11px] shrink-0', p.status === 'installed' ? 'text-ok' : p.status === 'failed' ? 'text-bad' : p.status === 'approved' ? 'text-brandhi' : 'text-faint')}>{tEnum('patch.status', p.status)}</span>
            </div>
          ))}</div>}
      <ConfirmDialog open={confirmReboot} onClose={() => setConfirmReboot(false)} danger
        title={t('reboot.title', { host: hostname || '' })} body={t('reboot.body')}
        confirmLabel={t('reboot.confirm')} onConfirm={() => act(() => api.rebootDevice(id), t('reboot.requested'))} />
      {patches.some((p) => p.status === 'failed') && <div className="text-[11.5px] text-bad mt-2">{t('dpatch.someFailed')}</div>}
    </div>
  );
}

window.DevicesScreen = DevicesScreen;
window.DeviceDetail = DeviceDetail;
window.DevicePatches = DevicePatches;
window.RemoteViewer = RemoteViewer;
