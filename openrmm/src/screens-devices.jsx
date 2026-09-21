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
    (byAgent[v.agent_id] = byAgent[v.agent_id] || []).push({ ...v, name: v.user_name || v.user || 'someone' });
  });
  return byAgent;
}

const PRESENCE_KIND = { native: 'remote desktop', files: 'file transfer', terminal: 'terminal' };

function presenceSummary(viewers) {
  if (!viewers || !viewers.length) return '';
  const who = viewers.map((v) => v.name + ' (' + (PRESENCE_KIND[v.kind] || v.kind) + ')');
  return who.length === 1
    ? who[0] + ' is connected remotely'
    : who.slice(0, -1).join(', ') + ' and ' + who[who.length - 1] + ' are connected remotely';
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

  const devices = devicesQ.data?.devices || [];
  const sites = sitesQ.data?.sites || [];
  const clients = clientsQ.data?.clients || [];
  const siteName = (id) => sites.find((s) => s.id === id)?.name || '—';
  const clientOfSite = (id) => sites.find((s) => s.id === id)?.client_id || '';
  // A device belongs to a client (company) through its site; unassigned devices
  // and sites whose client is gone both fall back to the em dash.
  const clientName = (siteId) => clients.find((c) => c.id === clientOfSite(siteId))?.name || '—';

  const filtered = devices.filter((d) => {
    if (q && !(d.hostname.toLowerCase().includes(q.toLowerCase()) || (d.description || '').toLowerCase().includes(q.toLowerCase()) || d.tags.some((t) => t.includes(q.toLowerCase())))) return false;
    if (osF !== 'all' && window.osFamily(d.os) !== osF) return false;
    if (statusF !== 'all' && d.status !== statusF) return false;
    if (clientF !== 'all' && clientOfSite(d.site_id) !== clientF) return false;
    return true;
  });

  return (
    <Page wide>
      <PageHeader title="Devices" subtitle={`${devices.length} agents · ${devices.filter((d) => d.status === 'online').length} online`}
        actions={can('jobs.create') && <Button icon="flash" onClick={() => setBulkOpen(true)}>Deploy to many</Button>} />

      <Card className="mb-4">
        <div className="flex flex-wrap items-center gap-2 p-2.5">
          <div className="relative flex-1 min-w-[220px]">
            <Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search hostname, tag, owner…" className={cx(inputCls, 'pl-9')} />
          </div>
          <Select value={osF} onChange={(e) => setOsF(e.target.value)} className="w-auto min-w-[120px]">
            <option value="all">All OS</option><option>Windows</option><option>macOS</option><option>Linux</option>
          </Select>
          <Select value={statusF} onChange={(e) => setStatusF(e.target.value)} className="w-auto min-w-[120px]">
            <option value="all">Any status</option><option value="online">Online</option><option value="offline">Offline</option>
          </Select>
          <Select value={clientF} onChange={(e) => setClientF(e.target.value)} className="w-auto min-w-[150px]">
            <option value="all">All clients</option>
            {clients.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
        </div>
      </Card>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
              <th className="font-semibold px-4 py-2.5">Hostname</th>
              <th className="font-semibold px-3 py-2.5">Description</th>
              <th className="font-semibold px-3 py-2.5">OS</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Client</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Site</th>
              <th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Tags</th>
              <th className="font-semibold px-3 py-2.5">Status</th>
              <th className="font-semibold px-4 py-2.5 text-right">Last seen</th>
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
                <td className="px-3 py-3"><div className="flex items-center gap-2 text-[12.5px] text-muted"><OsIcon os={d.os} size={15} /><span className="truncate max-w-[170px]" title={d.os}>{window.osLabel(d.os)}</span><span className="text-faint text-[11px] mono">{d.arch}</span>{presence[d.id]?.length ? <Tip label={presenceSummary(presence[d.id])}><span className="inline-flex items-center gap-1 text-[11px] text-brandhi border border-brandhi/40 rounded px-1.5 py-0.5"><Icon name="remote" size={11} />{presence[d.id].length > 1 ? presence[d.id].length : 'in use'}</span></Tip> : null}</div></td>
                <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted truncate max-w-[180px]">{clientName(d.site_id)}</td>
                <td className="px-3 py-3 hidden md:table-cell text-[12.5px] text-muted">{siteName(d.site_id)}</td>
                <td className="px-3 py-3 hidden lg:table-cell">
                  <div className="flex gap-1 flex-wrap max-w-[200px]">{d.tags.slice(0, 3).map((t) => <span key={t} className="rounded-md bg-elevated border border-line text-[10.5px] text-muted px-1.5 py-0.5">{t}</span>)}</div>
                </td>
                <td className="px-3 py-3"><StatusPill status={d.status} size="sm" /></td>
                <td className="px-4 py-3 text-right text-[12px] text-muted tabular">{timeAgo(d.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!devicesQ.loading && filtered.length === 0 && <EmptyState icon="devices" title="No devices match" msg="Try clearing filters or search." />}
      </Card>

      <DeviceDetail id={selected} onClose={() => setSelected(null)} navigate={navigate} sites={sites} clients={clients} />
      <BulkDeployModal open={bulkOpen} onClose={() => setBulkOpen(false)} devices={devices} sites={sites} clients={clients} navigate={navigate} />
    </Page>
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
      const r = await api.createBulkJob({ agent_ids: ids, shell, script, description: desc || ('Bulk deploy · ' + ids.length + ' devices') });
      toast.success('Deployment created', r.companies > 1 ? (r.count + ' jobs · 1 batch per company (' + r.companies + ')') : (r.count + ' job(s) awaiting approval'));
      onClose();
      navigate && navigate('jobs');
    } catch (e) { toast.error('Deployment failed', e.message); } finally { setBusy(false); }
  };

  return (
    <Modal open={open} onClose={onClose} width="max-w-3xl">
      <div className="p-5 space-y-4">
        <div className="flex items-center gap-2.5">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="flash" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">Deploy to many devices</div><div className="text-[12.5px] text-muted">One approval-gated batch across every selected device.</div></div>
        </div>
        <div className="grid md:grid-cols-2 gap-4">
          <div className="space-y-2">
            <div className="grid grid-cols-3 gap-1.5">
              <Select value={companyF} onChange={(e) => { setCompanyF(e.target.value); setSiteF(''); setDeptF(''); }} className="h-8 text-[12px]"><option value="">All companies</option>{clients.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</Select>
              <Select value={siteF} onChange={(e) => { setSiteF(e.target.value); setDeptF(''); }} className="h-8 text-[12px]"><option value="">All sites</option>{sites.filter((s) => !companyF || s.client_id === companyF).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select>
              <Select value={deptF} onChange={(e) => setDeptF(e.target.value)} disabled={!siteF} className="h-8 text-[12px]"><option value="">{siteF ? 'All depts' : 'Dept (pick site)'}</option>{groups.filter((g) => g.site_id === siteF).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</Select>
            </div>
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter by hostname…" className="h-8" />
            <div className="flex items-center justify-between text-[11.5px] text-faint"><span>{ids.length} selected · {selCompanies} compan{selCompanies === 1 ? 'y' : 'ies'} → {selCompanies} batch{selCompanies === 1 ? '' : 'es'}</span><span className="flex gap-3"><button onClick={selectAll} className="hover:text-ink">Select all</button><button onClick={() => setSel({})} className="hover:text-ink">Clear</button></span></div>
            <div className="rounded-xl border border-line max-h-60 overflow-y-auto scroll-area divide-y divide-line">
              {filtered.map((d) => (
                <label key={d.id} className="flex items-center gap-2 px-3 py-2 text-[12.5px] cursor-pointer hover:bg-elevated/40">
                  <input type="checkbox" checked={!!sel[d.id]} onChange={() => toggle(d.id)} />
                  <span className={cx('h-1.5 w-1.5 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
                  <OsIcon os={d.os} size={13} /><span className="text-ink truncate flex-1">{d.hostname}</span>
                  {d.status !== 'online' && <span className="text-[10px] text-faint">offline</span>}
                </label>
              ))}
              {filtered.length === 0 && <div className="px-3 py-6 text-center text-[12px] text-faint">No devices.</div>}
            </div>
          </div>
          <div className="space-y-3">
            <Field label="Shell"><Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>
            <Field label="Description"><Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="e.g. Install 7-zip on Finance" /></Field>
            <Field label="Script"><CodeEditor value={script} onChange={setScript} shell={shell} minHeight={150} placeholder="# runs on every selected device" /></Field>
          </div>
        </div>
        <div className="flex items-center justify-between gap-3 pt-3 border-t border-line/60">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5 max-w-[60%]"><Icon name="shield" size={13} className="shrink-0" />A deployment never spans companies — a multi-company selection splits into one batch per company, each awaiting approval.</div>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button variant="primary" icon="flash" loading={busy} disabled={!ids.length || !script.trim()} onClick={submit}>Create deployment ({ids.length})</Button>
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
  const meshEnabled = cfgQ.data?.remote?.meshcentral_enabled !== false;
  const capsQ = useQuery([id, 'remote-capabilities'], () => id ? api.getRemoteCapabilities(id) : Promise.resolve(null), { invalidateOn: ['devices'] });
  const nativeCaps = capsQ.data;
  const nativeUnavailable = nativeCaps?.capture === 'unavailable';
  const nativeHint = nativeUnavailable ? (nativeCaps.reason_code || 'unavailable') : (nativeCaps?.capture === 'unknown' ? 'capability unknown' : 'beta');

  const [remoteBusy, setRemoteBusy] = useState(false);
  const presenceByAgent = useRemotePresence();
  const attached = presenceByAgent[id] || [];
  const [remote, setRemote] = useState(null); // { deviceId, hostname } for the native viewer
  const [term, setTerm] = useState(null);     // { deviceId, hostname } for the terminal viewer
  const [orgOpen, setOrgOpen] = useState(false);
  const [confirmDel, setConfirmDel] = useState(false);
  const startRemote = async (provider) => {
    // Announced, never blocked: joining a colleague is sometimes the point.
    if (attached.length) toast.info('Someone is already connected', presenceSummary(attached) + ' to ' + d.hostname + '.');
    if (provider === 'native') { setRemote({ deviceId: id, hostname: d.hostname, os: d.os }); return; } // viewer brokers + reconnects
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, provider);
      window.open(s.connect_url, '_blank', 'noopener,noreferrer'); toast.success('Remote session ready', provider + ' opened in a new tab');
    } catch (e) { toast.error('Remote failed', e.message); } finally { setRemoteBusy(false); }
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
    const v = window.prompt('Enter the ' + label + ' for this device:');
    if (!v || !v.trim()) return;
    try { await api.setRemoteIds(id, { [field]: v.trim() }); toast.success(label + ' linked'); }
    catch (e) { toast.error('Failed', e.message); }
  };

  const groupsQ = useQuery(['groups'], () => can('clients.view') ? api.listGroups() : Promise.resolve({ groups: [] }), { invalidateOn: ['groups'] });
  const groups = groupsQ.data?.groups || [];
  const currentSite = d ? sites.find((s) => s.id === d.site_id) : null;
  const currentClient = currentSite ? clients.find((c) => c.id === currentSite.client_id) : null;
  const currentGroup = d ? groups.find((g) => g.id === d.group_id) : null;
  const orgLabel = currentSite ? [currentClient?.name, currentSite.name, currentGroup?.name].filter(Boolean).join(' / ') : 'Unassigned';

  const saveOrg = async ({ siteId, groupId }) => {
    try {
      if ((d.group_id || '') && d.site_id !== siteId) await api.setDeviceGroup(id, null);
      if ((d.site_id || '') !== (siteId || '')) await api.setDeviceSite(id, siteId || null);
      if ((d.group_id || '') !== (groupId || '')) await api.setDeviceGroup(id, groupId || null);
      toast.success('Clients & Sites updated'); setOrgOpen(false); devQ.refetch();
    } catch (e) { toast.error('Failed to update Clients & Sites', e.message); }
  };
  const deleteDevice = async () => {
    try { await api.deleteDevice(id); toast.info('Device deleted'); setConfirmDel(false); onClose(); window.dataBus.emit('devices'); }
    catch (e) { toast.error('Failed to delete device', e.message); }
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
                  <span className="text-faint">·</span><span>agent v{d.version}</span><span className="text-faint">·</span>
                  <span>last seen {timeAgo(d.last_seen)}</span>
                </div>
                {attached.length > 0 && (
                  <div className="mt-2 flex items-start gap-2 rounded-lg border border-brandhi/40 bg-brandhi/10 px-2.5 py-1.5 text-[12px] text-ink">
                    <Icon name="remote" size={13} className="text-brandhi mt-0.5 shrink-0" />
                    <span>
                      {presenceSummary(attached)}.
                      <span className="text-muted"> Since {attached.map((v) => timeAgo(v.since)).join(', ')}. You can still connect — both sessions share the screen.</span>
                    </span>
                  </div>
                )}
                {d.description && <div className="text-[12px] text-faint mt-1">{d.description}</div>}
              </div>
              <button onClick={onClose} className="text-faint hover:text-ink p-1.5 -mr-1.5"><Icon name="close" size={18} /></button>
            </div>

            <div className="flex items-center gap-2 mt-4 flex-wrap">
              {(can('devices.remote') || can('devices.terminal') || can('devices.files')) && <Dropdown align="left" width="w-60" trigger={
                <Button variant="primary" icon="remote" iconRight="chevdown" loading={remoteBusy} disabled={d.status !== 'online'}>Remote</Button>
              }>
                <div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold">Connect via</div>
                {can('devices.remote') && <MenuItem icon="remote" onClick={() => startRemote('native')} disabled={nativeUnavailable} hint={nativeHint}>Native</MenuItem>}
                {can('devices.remote') && <MenuItem icon="external" onClick={startRemoteNewTab} disabled={nativeUnavailable} hint={nativeUnavailable ? nativeHint : 'new tab'}>Native — new tab</MenuItem>}
                {can('devices.terminal') && <MenuItem icon="terminal" onClick={() => setTerm({ deviceId: id, hostname: d.hostname })} hint="elevated">Terminal</MenuItem>}
                {can('devices.files') && <MenuItem icon="folder" onClick={() => setRemote({ deviceId: id, hostname: d.hostname, os: d.os, filesOnly: true })}>File transfer</MenuItem>}
                {can('devices.remote') && meshEnabled && <MenuItem icon="external" onClick={() => startRemote('meshcentral')} disabled={!d.mesh_id} hint={d.mesh_id ? '' : 'no node id'}>MeshCentral</MenuItem>}
                {can('devices.remote') && rustdeskEnabled && <MenuItem icon="external" onClick={() => startRemote('rustdesk')} hint="rustdesk://">RustDesk</MenuItem>}
                {can('devices.manage') && meshEnabled && <>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="external" onClick={() => linkRemote('mesh_id', 'MeshCentral node id')}>Link MeshCentral node…</MenuItem>
                </>}
              </Dropdown>}
              {can('jobs.create') && <Button variant="secondary" icon="terminal" onClick={() => setTab('run')}>Run script</Button>}
              <div className="flex-1" />
              {can('clients.manage') && <><div className="min-w-0 max-w-[300px] text-right hidden sm:block"><div className="text-[10.5px] uppercase tracking-wide text-faint">Clients & Sites</div><div className="text-[12px] text-muted truncate">{orgLabel}</div></div><Button variant="secondary" icon="building" onClick={() => setOrgOpen(true)}>Clients & Sites</Button></>}
              {can('devices.delete') && <Button variant="danger" icon="trash" onClick={() => setConfirmDel(true)}>Delete</Button>}
            </div>
          </div>

          <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'inventory', label: 'Inventory' }, ...(can('jobs.view') ? [{ value: 'jobs', label: 'Jobs' }] : []), ...(can('patches.view') ? [{ value: 'patches', label: 'Patches' }] : []), ...(can('jobs.create') ? [{ value: 'run', label: 'Run script' }] : []), ...(can('devices.manage') ? [{ value: 'notes', label: 'Notes' }] : [])]} />

          <div className="flex-1 overflow-y-auto scroll-area p-5">
            {tab === 'inventory' && <InventoryView id={id} />}
            {tab === 'jobs' && can('jobs.view') && <DeviceJobs id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'patches' && can('patches.view') && <DevicePatches id={id} />}
            {tab === 'run' && can('jobs.create') && <RunScriptForm device={d} onCreated={() => setTab('jobs')} />}
            {tab === 'notes' && can('devices.manage') && <DeviceNotes device={d} />}
          </div>
        </>
      )}
      {!d && <div className="flex-1 flex items-center justify-center"><Spinner size={22} /></div>}
      {remote && <RemoteViewer deviceId={remote.deviceId} hostname={remote.hostname} os={remote.os} filesOnly={remote.filesOnly} onClose={() => setRemote(null)} />}
      {term && <TerminalViewer deviceId={term.deviceId} hostname={term.hostname} onClose={() => setTerm(null)} />}
      {d && <DeviceOrgModal open={orgOpen} onClose={() => setOrgOpen(false)} device={d} clients={clients} sites={sites} groups={groups} onSave={saveOrg} />}
      {d && <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={deleteDevice} danger title="Delete device?" body={<>This permanently removes <b>{d.hostname}</b> from OpenRMM. This cannot be undone.</>} confirmLabel="Delete device" />}
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
          <div><div className="text-[15px] font-semibold text-ink">Clients & Sites</div><div className="text-[12.5px] text-muted">Assign <b className="text-ink">{device.hostname}</b> to a site or an optional department.</div></div>
        </div>
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search company, site, or department…" autoFocus />
        <div className="rounded-xl border border-line max-h-80 overflow-y-auto scroll-area p-1">
          <button onClick={() => chooseSite('')} className={cx('w-full flex items-center gap-2 rounded-lg px-3 py-2 text-left text-[12.5px] transition', !siteId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="close" size={13} />Unassigned</button>
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
                      <button onClick={() => chooseSite(s.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-6 pr-3 py-2 text-left text-[12.5px] transition', selectedSite ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="pin" size={13} /><span className="flex-1 truncate">{s.name}</span><span className="text-[10.5px] text-faint">site</span></button>
                      {siteGroups.map((g) => (
                        <button key={g.id} onClick={() => chooseGroup(s.id, g.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-11 pr-3 py-1.5 text-left text-[12.5px] transition', groupId === g.id ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="building" size={12} /><span className="flex-1 truncate">{g.name}</span><span className="text-[10.5px] text-faint">department</span></button>
                      ))}
                    </div>
                  );
                })}
              </div>
            );
          })}
          {visibleClients.length === 0 && <div className="px-3 py-8 text-center text-[12px] text-faint">No matches.</div>}
        </div>
        <div className="flex items-center justify-end gap-2"><Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button><Button variant="primary" loading={busy} disabled={!changed} onClick={save}>Save assignment</Button></div>
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
    try { await api.updateDevice(device.id, { description: desc, note }); toast.success('Saved'); }
    catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };

  return (
    <div className="max-w-2xl space-y-4">
      <Field label="Description" hint="Shown in the device list to help identify this machine — e.g. the person who uses it.">
        <Input value={desc} onChange={(e) => setDesc(e.target.value)} disabled={!editable} placeholder="e.g. João Silva — Finance laptop" />
      </Field>
      <Field label="Note" hint="Private observation. Only shown here (in this tab), never in the device list.">
        <Textarea value={note} onChange={(e) => setNote(e.target.value)} disabled={!editable} rows={8} placeholder="Internal notes about this device…" />
      </Field>
      {editable
        ? <div className="flex justify-end"><Button variant="primary" loading={busy} disabled={!dirty} onClick={save}>Save</Button></div>
        : <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Device metadata permission needed to edit.</div>}
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
function RemoteViewer({ deviceId, hostname, os, filesOnly, onClose }) {
  const toast = useToast();
  const auth = useAuth() || { can: () => false };
  const can = auth.can;
  const allowFiles = filesOnly || can('devices.files');
  const isWin = os ? /^win/i.test(os) : true; // unknown host => Windows-style (legacy)
  const isMac = /darwin|macos|mac os/i.test(os || '');
  const shortcutLabel = isMac ? '⌘⌥Esc' : 'Ctrl+Alt+Delete';
  const canLock = isWin || /linux/i.test(os || ''); // input lock: Windows + Linux (not macOS)
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
  const [cad, setCad] = useState({ available: false, reason: 'Waiting for the remote module…' });
  const [monitors, setMonitors] = useState({ list: [], selected: 0, all: true }); // host displays; empty unless there are two or more
  const [frameSize, setFrameSize] = useState({ width: 0, height: 0 });
  const [scale, setScale] = useState('fit');
  const [fullscreen, setFullscreen] = useState(false);
  const [notice, setNotice] = useState(null); // host-side notice (e.g. Wayland can't be captured)
  const [chatOpen, setChatOpen] = useState(true); // side chat panel visible
  const [unread, setUnread] = useState(0);
  const openRef = useRef(true);
  const noticeRef = useRef(null); // last notice toasted, so a reconnect loop doesn't repeat it
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
      toast.error('Full screen failed', e.message || 'The browser denied full-screen mode.');
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
          setCad({ available: !!m.cad, reason: m.cad_error || (m.cad ? '' : 'Update the remote module to use this shortcut.') });
          // A capture the endpoint cannot perform is the black screen this
          // viewer would otherwise show without explanation.
          if (m.capture_error) { setNotice(m.capture_error); if (noticeRef.current !== m.capture_error) { noticeRef.current = m.capture_error; toast.error('Screen capture blocked', m.capture_error); } }
        }
        else if (m.t === 'notice') { if (m.text) { setNotice(m.text); if (noticeRef.current !== m.text) { noticeRef.current = m.text; toast.info('Remote notice', m.text); } } else { noticeRef.current = null; setNotice(null); } }
        // The endpoint's clipboard, mirrored while the session is open, so a copy
        // on the remote screen can be pasted on this machine. Writing can be
        // refused by the browser when the tab is not focused; that is a missed
        // update, not an error worth interrupting the session for.
        else if (m.t === 'clip') {
          const text = window.RemoteInput.incomingClipText(m);
          if (text && navigator.clipboard?.writeText) { navigator.clipboard.writeText(text).catch(() => {}); }
        }
        else if (m.t === 'chat') { setChat((c) => [...c, { from: m.from, text: m.text }]); if (m.from === 'user' && !openRef.current) setUnread((u) => u + 1); }
        else if (m.t === 'cad') { if (m.ok) { if (isMac) setCad({ available: true, reason: '' }); toast.info('Shortcut request sent', `${hostname}: ${shortcutLabel}`); } else toast.error('Shortcut failed', m.err || 'The device rejected the shortcut.'); }
        else if (m.t === 'paste') { if (m.ok && isMac) setCad({ available: true, reason: '' }); else if (!m.ok) toast.error('Paste failed', m.err || 'The device rejected the paste shortcut.'); }
        else if (m.t === 'monitors') setMonitors({ list: m.list || [], selected: m.selected || 0, all: m.all !== false })
        else if (allowFiles && m.t === 'flist') setFb({ path: m.path, entries: m.entries || [] });
        else if (m.t === 'uac') { setUac(!!m.enabled); if (m.err) toast.error('UAC toggle failed', m.err); }
        else if (m.t === 'inputlocked') { setLocked(!!m.enabled); if (m.err) toast.error('Input lock failed', m.err); }
        else if (allowFiles && m.t === 'fmeta') dlRef.current[m.id] = { name: m.name, chunks: [] };
        else if (allowFiles && m.t === 'fend') {
          const d = dlRef.current[m.id];
          if (d && d.chunks) { saveBlob(new Blob(d.chunks), d.name); toast.success('Downloaded', d.name); delete dlRef.current[m.id]; }
        } else if (allowFiles && m.t === 'ferr') toast.error('Transfer failed', m.err || '');
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
        for (const t of tiles) { if (t) { ctx.drawImage(t.bmp, t.x, t.y); if (t.bmp.close) t.bmp.close(); } }
      }).catch(() => {});
    };

    // Broker a session and connect. On drop (e.g. user logoff tears the session
    // down) re-broker and reconnect — the agent re-spawns the SYSTEM sidecar into
    // the now-active session (login screen included).
    const connect = async () => {
      setCad({ available: false, reason: 'Waiting for the remote module…' });
      setMonitors({ list: [], selected: 0, all: true });
      setVer(null);
      if (stopped) return;
      setStatus(retries ? 'reconnecting' : 'connecting');
      let wsUrl, viewerTicket;
      try {
        const s = await api.startRemote(deviceId, filesOnly ? 'files' : 'native');
        wsUrl = api.websocketURL('/ws/remote?session=' + encodeURIComponent(s.id) + '&role=viewer');
        viewerTicket = s.viewer_ticket;
      } catch (e) {
        if (!stopped && retries++ < 40) setTimeout(connect, 3000);
        else setStatus('closed');
        return;
      }
      if (stopped) return;
      const ws = new WebSocket(wsUrl, relayProtocols(viewerTicket));
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
      ws.onclose = () => {
        if (stopped) return;
        if (retries++ < 40) { setStatus('reconnecting'); setTimeout(connect, 2000); }
        else setStatus('closed');
      };
      ws.onmessage = onMsg;
    };

    const send = (o) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };
    const norm = (e) => { const r = cv.getBoundingClientRect(); return { x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)) }; };
    const typing = () => window.RemoteInput.isTypingTarget(document.activeElement);
    let lastMove = 0;
    const onMove = (e) => { const t = Date.now(); if (t - lastMove < 33) return; lastMove = t; const p = norm(e); send({ t: 'm', x: p.x, y: p.y }); };
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
    // No screen/input wiring in file-transfer-only mode (the canvas isn't shown).
    if (!filesOnly && cv) {
      cv.addEventListener('pointermove', onMove);
      cv.addEventListener('pointerdown', onDown);
      cv.addEventListener('pointerup', onUp);
      cv.addEventListener('pointercancel', onUp);
      cv.addEventListener('wheel', onWheel, { passive: false });
      cv.addEventListener('contextmenu', onCtx);
      window.addEventListener('keydown', kd);
      window.addEventListener('keyup', ku);
      window.addEventListener('paste', onPaste);
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
  }, [deviceId, filesOnly, allowFiles, isMac]);

  const sendChat = () => { const t = draft.trim(); if (!t) return; sendJSON({ t: 'chat', from: 'tech', text: t }); setChat((c) => [...c, { from: 'tech', text: t }]); setDraft(''); };
  const shortcutAvailable = window.RemoteInput.shortcutAvailable(isMac, cad.available);
  const sendCtrlAltDelete = () => {
    if (!shortcutAvailable) { toast.error('Shortcut unavailable', cad.reason); return; }
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
      const t = p.replace(/\\+$/, '');
      if (/^[A-Za-z]:$/.test(t)) return '';        // drive root -> drive list
      const i = t.lastIndexOf('\\');
      return i <= 2 ? t.slice(0, 2) + '\\' : t.slice(0, i);
    }
    if (!p || p === '/') return null;  // already at "/"
    const t = p.replace(/\/+$/, '');
    const i = t.lastIndexOf('/');
    return i <= 0 ? '/' : t.slice(0, i);
  };
  const browse = (path) => { if (!allowFiles) return; const p = path == null ? rootPath : path; setFb({ path: p, entries: null }); setFbSel([]); sendJSON({ t: 'fls', id: nextId(), path: p }); };
  const downloadEntry = (name) => { if (allowFiles) sendJSON({ t: 'fget', id: nextId(), path: joinPath(fb.path, name) }); };
  const toggleSel = (name) => setFbSel((s) => s.includes(name) ? s.filter((x) => x !== name) : [...s, name]);
  const doMkdir = () => { const n = window.prompt('New folder name:'); if (n) sendJSON({ t: 'fmkdir', id: nextId(), path: joinPath(fb.path, n) }); };
  const doDelete = () => { if (fbSel.length && window.confirm('Delete ' + fbSel.length + ' item(s) on ' + hostname + '?')) { fbSel.forEach((n) => sendJSON({ t: 'fdel', id: nextId(), path: joinPath(fb.path, n) })); setFbSel([]); } };
  const doRename = () => { if (fbSel.length !== 1) return; const old = fbSel[0]; const n = window.prompt('Rename to:', old); if (n && n !== old) { sendJSON({ t: 'frename', id: nextId(), src: joinPath(fb.path, old), dst: joinPath(fb.path, n) }); setFbSel([]); } };
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
    toast.success('Upload sent', dest);
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
        <span className="text-[13px] font-semibold text-ink whitespace-nowrap">{filesOnly ? `File transfer · ${hostname}` : hostname}</span>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{status}</span>
        {ver && <span className="text-[10px] text-faint">sidecar v{ver}</span>}
        <div className="ml-auto flex items-center gap-1 sm:gap-2 whitespace-nowrap">
          <input ref={fileInputRef} type="file" className="hidden" onChange={(e) => { uploadFile(e.target.files[0]); e.target.value = ''; }} />
          {!filesOnly && <>
            <Button variant="ghost" size="sm" icon="key" onClick={sendCtrlAltDelete} disabled={status !== 'streaming' || !shortcutAvailable} title={isMac ? (cad.available ? 'Open Force Quit Applications' : `${cad.reason} Click to retry after granting it.`) : !cad.available ? cad.reason : isWin ? 'Request the Windows security screen' : 'Send Ctrl+Alt+Delete; action depends on the Linux desktop settings'}>{shortcutLabel}</Button>
            {isWin && <Button variant={uac ? 'primary' : 'ghost'} size="sm" icon="shield" onClick={() => sendJSON({ t: 'uac', enable: !uac })} title="Move UAC prompts to the controllable desktop">UAC</Button>}
            {canLock && <Button variant={locked ? 'primary' : 'ghost'} size="sm" icon="lock" onClick={() => sendJSON({ t: 'lockinput', enable: !locked })} title="Block the host's physical keyboard & mouse (user + login screen)">{locked ? 'Locked' : 'Lock input'}</Button>}
            {allowFiles && <Button variant="ghost" size="sm" icon="link" onClick={() => browse(rootPath)}>Files</Button>}
            <Button variant={chatOpen ? 'primary' : 'ghost'} size="sm" icon="chat" onClick={() => setChatOpen((o) => !o)} title="Toggle chat panel">Chat{unread ? ` (${unread})` : ''}</Button>
            {monitors.list.length > 1 && (
              <Select value={String(monitors.selected)} onChange={(e) => { const id = Number(e.target.value); setMonitors((m) => ({ ...m, selected: id })); sendJSON({ t: 'mon', id }); }}
                aria-label="Host display" title="Choose which of the host's displays to stream" className="h-7 w-[150px] bg-card py-0 text-[12px]">
                {monitors.all !== false && <option value="0">All displays</option>}
                {monitors.list.map((m) => <option key={m.id} value={String(m.id)}>{`Display ${m.id} · ${m.w}×${m.h}${m.primary ? ' (main)' : ''}`}</option>)}
              </Select>
            )}
            <span className="text-[10.5px] text-faint tabular" title="Remote screen resolution">{frameSize.width ? `${frameSize.width}×${frameSize.height}` : '—×—'}</span>
            <Select value={scale} onChange={(e) => setScale(e.target.value)} aria-label="Screen scale" title="Adjust remote screen scale" className="h-7 w-[132px] bg-card py-0 text-[12px]">
              <option value="fit">Fit to window</option>
              <option value="50">50%</option><option value="75">75%</option><option value="100">100% (native)</option><option value="125">125%</option>
            </Select>
            <Button variant={fullscreen ? 'primary' : 'ghost'} size="sm" icon={fullscreen ? 'fullscreenexit' : 'fullscreen'} onClick={toggleFullscreen} disabled={!document.fullscreenEnabled} title={fullscreen ? 'Exit full screen' : 'Open full screen'}>{fullscreen ? 'Exit full screen' : 'Full screen'}</Button>
          </>}
          <button onClick={onClose} className="text-faint hover:text-ink p-1.5"><Icon name="close" size={18} /></button>
        </div>
      </div>
      {allowFiles && fb && (
        <div className="absolute inset-0 z-[5] bg-black/55 flex items-center justify-center" onClick={() => filesOnly ? onClose() : setFb(null)}>
          <div className="w-[calc(100%-1rem)] max-w-[760px] h-[calc(100%-1rem)] sm:h-[80vh] bg-card border border-line rounded-xl flex flex-col overflow-hidden shadow-2xl" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2 px-3 h-11 border-b border-line">
              <Icon name="folder" size={15} className="text-muted" />
              <span className="text-[12.5px] font-semibold text-ink">File transfer · {hostname}</span>
              <button onClick={() => filesOnly ? onClose() : setFb(null)} className="ml-auto text-faint hover:text-ink p-1"><Icon name="close" size={16} /></button>
            </div>
            {/* toolbar */}
            <div className="flex items-center gap-0.5 px-2 min-h-10 border-b border-line bg-elevated/40 overflow-x-auto scroll-area shrink-0">
              <Button variant="ghost" size="sm" icon="chevleft" title="Up" disabled={parentPath(fb.path) === null} onClick={() => browse(parentPath(fb.path))} />
              <Button variant="ghost" size="sm" icon="refresh" title="Refresh" onClick={() => browse(fb.path)} />
              <Button variant="ghost" size="sm" icon="plus" title="New folder" disabled={!fb.path} onClick={doMkdir} />
              <div className="w-px h-5 bg-line mx-1" />
              <Button variant="ghost" size="sm" icon="upload" title="Upload here" disabled={!fb.path} onClick={() => fileInputRef.current?.click()} />
              <Button variant="ghost" size="sm" icon="download" title="Download" disabled={!fbSel.length} onClick={doDownloadSel} />
              <Button variant="ghost" size="sm" icon="copy" title="Copy" disabled={!fbSel.length} onClick={() => doClip('copy')} />
              <Button variant="ghost" size="sm" icon="scissors" title="Cut" disabled={!fbSel.length} onClick={() => doClip('cut')} />
              <Button variant="ghost" size="sm" icon="edit" title="Rename" disabled={fbSel.length !== 1} onClick={doRename} />
              <Button variant="ghost" size="sm" icon="trash" title="Delete" disabled={!fbSel.length} onClick={doDelete} />
              {fbClip && <Button variant="secondary" size="sm" icon="approve" onClick={doPaste}>Paste {fbClip.names.length}</Button>}
              <span className="ml-auto text-[11px] text-faint pr-1">{fbSel.length ? fbSel.length + ' selected' : (fb.entries ? fb.entries.length + ' items' : '')}</span>
            </div>
            {/* editable address bar */}
            <div className="px-2 h-10 flex items-center gap-2 border-b border-line shrink-0">
              <Input value={pathEdit} onChange={(e) => setPathEdit(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') browse(pathEdit.trim()); }} placeholder={isWin ? 'Type a path, e.g. C:\\Users\\' : 'Type a path, e.g. /home/'} className="text-[11.5px] mono h-7" />
              <Button variant="secondary" size="sm" icon="arrowright" onClick={() => browse(pathEdit.trim())} title="Go" />
            </div>
            {/* list */}
            <div className="flex-1 overflow-y-auto">
              {fb.entries === null && <div className="p-6 text-center text-faint text-[12px]">Loading…</div>}
              {fb.entries && fb.entries.length === 0 && <div className="p-6 text-center text-faint text-[12px]">Empty folder.</div>}
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
            <div className="px-3 py-1.5 border-t border-line text-[11px] text-faint">Folder: click to open · file: double-click to download · checkboxes to select for Download / Copy / Cut / Delete.</div>
          </div>
        </div>
      )}
      <div className="relative flex-1 flex overflow-hidden">
        {filesOnly ? (
          <div className="flex-1 flex items-center justify-center text-faint text-[12.5px]">
            {fb ? 'File transfer session' : 'Opening file browser…'}
          </div>
        ) : (<>
        <div className="relative flex-1 overflow-auto p-2 scroll-area">
          <canvas ref={canvasRef} tabIndex={0} style={canvasStyle} className={cx('outline-none remote-canvas', scale === 'fit' ? 'absolute inset-0 m-auto max-w-full max-h-full' : 'block mx-auto')} />
          {notice && (
            <div className="absolute top-3 left-1/2 -translate-x-1/2 max-w-[80%] rounded-lg border border-warn/40 bg-warn/[0.12] backdrop-blur px-3.5 py-2 text-[12px] text-ink flex items-start gap-2 shadow-lg">
              <Icon name="info" size={14} className="text-warn shrink-0 mt-0.5" />
              <span>{notice}</span>
              <button onClick={() => setNotice(null)} className="text-faint hover:text-ink shrink-0"><Icon name="close" size={13} /></button>
            </div>
          )}
        </div>
        {chatOpen && <div className="absolute inset-0 z-[4] w-full border-l border-line bg-app flex flex-col sm:static sm:w-72 sm:shrink-0">
          <div className="px-3 h-9 flex items-center text-[11.5px] font-semibold text-muted border-b border-line">Chat<button onClick={() => setChatOpen(false)} className="ml-auto text-faint hover:text-ink" title="Collapse chat"><Icon name="chevright" size={16} /></button></div>
          <div className="flex-1 overflow-y-auto p-3 space-y-2">
            {chat.length === 0 && <div className="text-[11.5px] text-faint">No messages. A chat window opens on {hostname}.</div>}
            {chat.map((m, i) => (
              <div key={i} className={cx('text-[12px] max-w-[85%] rounded-lg px-2.5 py-1.5', m.from === 'tech' ? 'ml-auto bg-brand text-white shadow-sm shadow-brand/20' : 'mr-auto bg-elevated text-ink border border-line2')}>
                <div className={cx('text-[10px] mb-0.5 font-semibold', m.from === 'tech' ? 'text-white/75' : 'text-run')}>{m.from === 'tech' ? 'Technician · You' : hostname}</div>{m.text}
              </div>
            ))}
          </div>
          <div className="p-2 border-t border-line flex gap-1.5">
            <Input value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') sendChat(); }} placeholder="Message…" className="text-[12px]" />
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
    if (!window.Terminal) { toast.error('Terminal unavailable', 'xterm.js failed to load'); setStatus('closed'); return; }
    adoptDynamicStyles(); // must precede construction: xterm styles on open()
    const term = new window.Terminal({
      cursorBlink: true, fontSize: 13, scrollback: 5000,
      fontFamily: 'JetBrains Mono, ui-monospace, Menlo, Consolas, monospace',
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
      } catch (e) { setStatus('closed'); toast.error('Terminal failed', e.message); return; }
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
            else if (m.t === 'exit') { term.write('\r\n\x1b[33m[session ended]\x1b[0m\r\n'); setStatus('closed'); }
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
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{status}</span>
        {info && (
          <span className="text-[10.5px] text-faint flex items-center gap-2">
            <span className="mono">{info.shell}</span>
            {info.elevated
              ? <span className="text-ok inline-flex items-center gap-1"><Icon name="shield" size={11} />elevated{info.user ? ` · ${info.user}` : ''}</span>
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
  return v == null || String(v).trim() === '' ? 'Not reported' : String(v).trim();
}
function inventoryList(items, formatter) {
  const lines = (items || []).map(formatter).filter(Boolean);
  return lines.length ? lines.join('\n') : 'Not reported';
}
function hardwareRows(hw, cpu, mem) {
  const mb = hw.motherboard || {};
  const motherboard = [mb.manufacturer, mb.product || mb.model, mb.version].filter(Boolean).join(' ');
  const motherboardSerial = mb.serial ? 'SN ' + mb.serial : '';
  // Fold the (old) CPU & Memory section into the static rows: cores + live load
  // ride along the Processor row, and live used% rides along the Memory row.
  const procBase = inventoryText(hw.processor || cpu.model);
  const procExtra = [cpu.cores ? cpu.cores + ' cores' : '', cpu.load_pct != null ? cpu.load_pct + '% load' : ''].filter(Boolean).join(' · ');
  const memTotal = inventoryBytes(hw.memory_total_bytes) || (mem.total_gb ? mem.total_gb + ' GB' : '');
  const memVal = memTotal ? (memTotal + (mem.used_pct != null ? ' · ' + mem.used_pct + '% used' : '')) : 'Not reported';
  return [
    ['Service tag', inventoryText(hw.service_tag || hw.serial_number)],
    ['System model', inventoryText([hw.manufacturer, hw.system_model].filter(Boolean).join(' '))],
    ['Processor', procExtra ? procBase + ' · ' + procExtra : procBase],
    ['Memory', memVal],
    ['Motherboard', inventoryText([motherboard, motherboardSerial].filter(Boolean).join('\n'))],
    ['Video card', inventoryList(hw.gpus, (g) => [g.name || g.model, inventoryBytes(g.memory_bytes) || g.memory, g.driver_version].filter(Boolean).join(' · '))],
    ['Storage', inventoryList(hw.storage_devices, (d) => [d.model || d.name, d.type || d.interface, inventoryBytes(d.size_bytes)].filter(Boolean).join(' · '))],
    ['BIOS', inventoryText(hw.bios)],
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
        <span className="text-[11.5px] text-faint">Collected {timeAgo(invQ.data?.collected_at)}</span>
        <button onClick={() => setRaw((r) => !r)} className="text-[12px] text-muted hover:text-ink flex items-center gap-1.5"><Icon name="script" size={13} />{raw ? 'Structured view' : 'Raw JSON'}</button>
      </div>
      {raw ? <CodeBlock code={JSON.stringify(data, null, 2)} shell="bash" label="inventory.json" maxHeight={520} /> : (
        <div className="grid md:grid-cols-2 gap-4">
          <InventorySection title="System" icon="devices">
            <InfoGrid rows={[['Hostname', sys.hostname], ['OS', sys.os], ['Arch', sys.arch], ['Domain', sys.domain], ['Uptime', (sys.uptime_hours || 0) + ' h']]} />
          </InventorySection>
          <InventorySection title="Hardware" icon="wrench" className="md:col-span-2">
            <InfoGrid rows={hardwareRows(hw, cpu, mem)} />
          </InventorySection>
          <InventorySection title="Storage" icon="disk">
            <div className="space-y-2.5">
              {disks.map((dk, i) => {
                const usedPct = Math.round((1 - dk.free_gb / dk.total_gb) * 100);
                return (
                  <div key={i}>
                    <div className="flex items-center justify-between text-[12px] mb-1"><span className="mono text-ink">{dk.mount} <span className="text-faint">{dk.fs}</span></span><span className="text-muted">{dk.free_gb} / {dk.total_gb} GB free</span></div>
                    <progress value={usedPct} max="100" className={cx('disk-usage', usedPct > 88 ? 'bad' : usedPct > 75 ? 'warn' : '')} />
                  </div>
                );
              })}
            </div>
          </InventorySection>
          <InventorySection title="Network" icon="network">
            {(net.interfaces || []).map((ifc, i) => <InfoGrid key={i} rows={[['Interface', ifc.name], ['IPv4', ifc.ipv4], ['MAC', ifc.mac]]} />)}
            {(net.interfaces || []).length === 0 && <div className="text-[12.5px] text-faint">No network interfaces reported yet.</div>}
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
        <Icon name="grid" size={14} className="text-muted" />Installed software
        <span className="text-faint font-normal">({list.length})</span>
        {list.length > PREVIEW && (
          <div className="ml-auto relative">
            <Icon name="search" size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-faint" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter software…" className="h-7 pl-7 text-[12px] w-[200px]" />
          </div>
        )}
      </div>
      <div className="grid sm:grid-cols-2 gap-x-6 gap-y-1.5">
        {shown.map((s, i) => <div key={i} className="flex items-center justify-between border-b border-line/60 pb-1.5"><span className="text-[12.5px] text-ink">{s.name}</span><span className="text-[11.5px] text-faint mono">{s.version}</span></div>)}
      </div>
      {list.length === 0 && <div className="text-[12.5px] text-faint">No software inventory reported yet.</div>}
      {list.length > 0 && !matching.length && <div className="text-[12.5px] text-faint">Nothing matches “{q}”.</div>}
      {hidden > 0 && (
        <Button variant="ghost" size="sm" icon="chevdown" className="mt-3" onClick={() => setExpanded(true)}>
          Show {hidden} more
        </Button>
      )}
      {expanded && !ql && matching.length > PREVIEW && (
        <Button variant="ghost" size="sm" icon="chevdown" className="mt-3" onClick={() => setExpanded(false)}>
          Show less
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
  if (!jobs.length) return <EmptyState icon="flash" title="No jobs yet" msg="Run a script or let the AI propose one." />;
  return (
    <div className="space-y-2">
      {jobs.map((j) => (
        <button key={j.id} onClick={() => { onClose(); navigate('jobs', { focus: j.id }); }} className="w-full text-left rounded-xl border border-line bg-card hover:border-line2 hover:bg-elevated/40 transition p-3.5 flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2"><span className="text-[13px] text-ink font-medium truncate">{j.description}</span>{j.created_by.startsWith('ai:') && <Icon name="sparkle" size={12} className="text-brandhi" />}</div>
            <div className="text-[11.5px] text-faint mt-0.5">{j.created_by} · {timeAgo(j.created_at)}{j.exit_code != null && <span className="mono"> · exit {j.exit_code}</span>}</div>
          </div>
          <StatusPill status={j.status} size="sm" pulse />
        </button>
      ))}
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
  const [script, setScript] = useState(defShell === 'powershell' ? 'Get-Service | Where-Object Status -eq "Running" | Select-Object -First 10' : 'uptime\ndf -h /');
  const [busy, setBusy] = useState(false);
  const [runNow, setRunNow] = useState(false);
  const scriptsQ = useQuery(['scripts-pick'], () => can('scripts.view') ? api.listScripts() : Promise.resolve({ scripts: [] }), { invalidateOn: ['scripts'] });
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const canDirect = can('jobs.manage') && can('settings.manage') && cfgQ.data?.server?.allow_unapproved_jobs;
  const direct = canDirect && runNow;

  if (!can('jobs.create')) return <EmptyState icon="shield" title="Read-only access" msg="Creating jobs requires job creation permission." />;

  const submit = async () => {
    setBusy(true);
    try {
      await api.createJob({ agent_id: device.id, shell, script, description: desc || 'Manual script run', require_approval: !direct });
      if (direct) toast.success('Job dispatched', 'Running on ' + device.hostname + ' now.');
      else toast.success('Job created', 'Queued for approval — nothing has run yet.');
      onCreated();
    } catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };

  return (
    <div className="space-y-4 max-w-2xl">
      <div className={cx('flex items-start gap-2.5 rounded-xl border px-3.5 py-3', direct ? 'border-bad/25 bg-badsoft' : 'border-warn/25 bg-warnsoft')}>
        <Icon name="info" size={16} className={cx('mt-0.5 shrink-0', direct ? 'text-bad' : 'text-warn')} />
        <div className="text-[12.5px] text-ink leading-relaxed">{direct
          ? <>Direct execution is <b>on</b>: this runs on {device.hostname} <b>immediately</b>, with no approval step.</>
          : <>This creates an <b>approval-gated job</b>. It is a dry-run preview until someone with job management permission approves it — nothing executes on {device.hostname} yet.</>}</div>
      </div>
      {canDirect && (
        <label className="flex items-center gap-2.5 cursor-pointer select-none">
          <input type="checkbox" checked={runNow} onChange={(e) => setRunNow(e.target.checked)} className="h-4 w-4 accent-bad" />
          <span className="text-[12.5px] text-ink">Run immediately (skip approval)</span>
          <span className="text-[11.5px] text-faint">— configuration managers only; enabled on this instance</span>
        </label>
      )}
      <div className="flex gap-3">
        <Field label="Shell" className="w-44">
          <Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select>
        </Field>
        {/* Inserts a copy and springs back to the placeholder. Leaving the chosen
            name selected read as "this job is bound to that library script", so
            an operator writing something ad-hoc looked for a way to deselect —
            when the editor below was theirs to type in all along. Nothing about
            the job references the library: only shell, script and description
            are submitted. */}
        <Field label="From library" className="flex-1" hint="Copies the saved script into the editor; edit it freely afterwards.">
          <Select value="" onChange={(e) => {
            const s = (scriptsQ.data?.scripts || []).find((x) => x.id === e.target.value);
            if (!s) return;
            setScript(s.body); setShell(s.shell); setDesc(s.name);
            toast.info('Loaded "' + s.name + '"', 'Edit it below — the job is not tied to the library.');
          }}>
            <option value="">Start from a saved script…</option>
            {(scriptsQ.data?.scripts || []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </Select>
        </Field>
      </div>
      <Field label="Description"><Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="What does this do?" /></Field>
      <Field label="Script"><CodeEditor value={script} onChange={setScript} shell={shell} minHeight={220} /></Field>
      <div className="flex justify-end gap-2">
        <Button variant="primary" icon="plus" loading={busy} onClick={submit} disabled={!script.trim()}>{direct ? 'Run now on ' + device.hostname : 'Create approval-gated job'}</Button>
      </div>
    </div>
  );
}

// DevicePatches is the device-drawer Patches tab: missing patches + scan/install/
// reboot actions. Self-contained (no cross-file component dependency).
function DevicePatches({ id }) {
  const { can } = useAuth();
  const toast = useToast();
  const q = useQuery([id, 'dpatches'], () => api.devicePatches(id), { invalidateOn: ['patches'], poll: 5000 });
  const patches = q.data?.patches || [];
  const approved = patches.filter((p) => p.status === 'approved').length;
  const classColor = (c) => (c === 'security' || c === 'critical') ? 'text-bad' : (c === 'important' || c === 'moderate') ? 'text-warn' : 'text-faint';
  const act = async (fn, msg) => { try { await fn(); toast.info(msg); window.dataBus.emit('patches'); } catch (e) { toast.error('Failed', e.message); } };
  return (
    <div>
      <div className="flex items-center gap-2 mb-3 flex-wrap">
        <div className="text-[12.5px] text-muted flex-1">{patches.length} item(s){approved ? ` · ${approved} approved` : ''}</div>
        {can('patches.manage') && <Button size="sm" variant="secondary" icon="refresh" onClick={() => act(() => api.scanDevicePatches(id), 'Scan requested')}>Scan</Button>}
        {can('patches.manage') && <Button size="sm" variant="primary" icon="download" disabled={!approved} onClick={() => act(() => api.installDevicePatches(id, []), 'Installing approved patches')}>Install approved</Button>}
        {can('patches.manage') && <Button size="sm" variant="ghost" icon="refresh" onClick={() => act(() => api.rebootDevice(id), 'Reboot requested')}>Reboot</Button>}
      </div>
      {q.loading ? <Skeleton className="h-20 w-full" />
        : !patches.length ? <EmptyState icon="shield" title="No missing patches" msg="Run a scan to check for OS updates." />
          : <div className="space-y-1.5">{patches.map((p) => (
            <div key={p.patch_id} className="flex items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2 text-[12.5px]">
              <span className={cx('text-[10.5px] font-semibold uppercase tracking-wide w-16 shrink-0', classColor(p.classification))}>{p.classification}</span>
              <span className="flex-1 min-w-0 truncate text-ink" title={p.title}>{p.title || p.identifier}</span>
              {p.kb && <span className="mono text-faint shrink-0">{p.kb}</span>}
              <span className={cx('text-[11px] shrink-0', p.status === 'installed' ? 'text-ok' : p.status === 'failed' ? 'text-bad' : p.status === 'approved' ? 'text-brandhi' : 'text-faint')}>{p.status}</span>
            </div>
          ))}</div>}
      {patches.some((p) => p.status === 'failed') && <div className="text-[11.5px] text-bad mt-2">Some installs failed — check the device error detail / try again.</div>}
    </div>
  );
}

window.DevicesScreen = DevicesScreen;
window.DeviceDetail = DeviceDetail;
window.DevicePatches = DevicePatches;
window.RemoteViewer = RemoteViewer;
