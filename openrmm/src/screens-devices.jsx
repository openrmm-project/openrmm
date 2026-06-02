/* screens-devices.jsx — Devices list + Device detail drawer */

function DevicesScreen({ navigate }) {
  const devicesQ = useQuery(['devices'], () => api.listDevices(), { invalidateOn: ['devices'], poll: 10000 });
  const sitesQ = useQuery(['sites-all'], () => api.listSites(), { invalidateOn: ['org'] });
  const clientsQ = useQuery(['clients-all'], () => api.listClients(), { invalidateOn: ['org'] });
  const [q, setQ] = useState('');
  const [osF, setOsF] = useState('all');
  const [statusF, setStatusF] = useState('all');
  const [siteF, setSiteF] = useState('all');
  const [selected, setSelected] = useState(null);

  const devices = devicesQ.data?.devices || [];
  const sites = sitesQ.data?.sites || [];
  const clients = clientsQ.data?.clients || [];
  const siteName = (id) => sites.find((s) => s.id === id)?.name || '—';
  const osFamily = (os) => { const o = os.toLowerCase(); return o.includes('win') ? 'Windows' : o.includes('mac') ? 'macOS' : 'Linux'; };

  const filtered = devices.filter((d) => {
    if (q && !(d.hostname.toLowerCase().includes(q.toLowerCase()) || (d.description || '').toLowerCase().includes(q.toLowerCase()) || d.tags.some((t) => t.includes(q.toLowerCase())))) return false;
    if (osF !== 'all' && osFamily(d.os) !== osF) return false;
    if (statusF !== 'all' && d.status !== statusF) return false;
    if (siteF !== 'all' && d.site_id !== siteF) return false;
    return true;
  });

  return (
    <Page wide>
      <PageHeader title="Devices" subtitle={`${devices.length} agents · ${devices.filter((d) => d.status === 'online').length} online`} />

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
          <Select value={siteF} onChange={(e) => setSiteF(e.target.value)} className="w-auto min-w-[150px]">
            <option value="all">All sites</option>
            {clients.map((c) => <optgroup key={c.id} label={c.name}>{sites.filter((s) => s.client_id === c.id).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</optgroup>)}
          </Select>
        </div>
      </Card>

      <Card className="overflow-hidden">
        <table className="w-full text-left">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line">
              <th className="font-semibold px-4 py-2.5">Hostname</th>
              <th className="font-semibold px-3 py-2.5">OS</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Site</th>
              <th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Tags</th>
              <th className="font-semibold px-3 py-2.5">Status</th>
              <th className="font-semibold px-4 py-2.5 text-right">Last seen</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {devicesQ.loading && Array.from({ length: 6 }).map((_, i) => (
              <tr key={i}><td className="px-4 py-3" colSpan={6}><Skeleton className="h-5 w-full" /></td></tr>
            ))}
            {!devicesQ.loading && filtered.map((d) => (
              <tr key={d.id} onClick={() => setSelected(d.id)} className="cursor-pointer hover:bg-elevated/50 transition group">
                <td className="px-4 py-3">
                  <div className="flex items-center gap-2.5">
                    <span className={cx('h-2 w-2 rounded-full shrink-0', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
                    <div className="min-w-0">
                      <div className="text-[13px] font-medium text-ink truncate group-hover:text-brandhi transition-colors">{d.hostname}</div>
                      {d.description && <div className="text-[11px] text-faint truncate">{d.description}</div>}
                    </div>
                  </div>
                </td>
                <td className="px-3 py-3"><div className="flex items-center gap-2 text-[12.5px] text-muted"><OsIcon os={d.os} size={15} /><span className="truncate max-w-[170px]">{d.os}</span><span className="text-faint text-[11px] mono">{d.arch}</span></div></td>
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
    </Page>
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

  const [remoteBusy, setRemoteBusy] = useState(false);
  const startRemote = async (provider) => {
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, provider);
      if (provider === 'rustdesk') { toast.info('RustDesk session', 'Triggering ' + s.connect_url); }
      else { window.open(s.connect_url, '_blank'); toast.success('Remote session ready', provider + ' opened in a new tab'); }
    } catch (e) { toast.error('Remote failed', e.message); } finally { setRemoteBusy(false); }
  };

  const setSite = async (siteId) => { await api.setDeviceSite(id, siteId); toast.success('Site updated'); };

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
                  <span>{d.os}</span><span className="text-faint">·</span><span className="mono text-faint">{d.arch}</span>
                  <span className="text-faint">·</span><span>agent v{d.version}</span><span className="text-faint">·</span>
                  <span>last seen {timeAgo(d.last_seen)}</span>
                </div>
                {d.description && <div className="text-[12px] text-faint mt-1">{d.description}</div>}
              </div>
              <button onClick={onClose} className="text-faint hover:text-ink p-1.5 -mr-1.5"><Icon name="close" size={18} /></button>
            </div>

            <div className="flex items-center gap-2 mt-4 flex-wrap">
              <Dropdown align="left" width="w-60" trigger={
                <Button variant="primary" icon="remote" iconRight="chevdown" loading={remoteBusy} disabled={d.status !== 'online'}>Remote</Button>
              }>
                <div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold">Connect via</div>
                <MenuItem icon="remote" onClick={() => startRemote('native')} hint="built-in">Native</MenuItem>
                <MenuItem icon="external" onClick={() => startRemote('meshcentral')} disabled={!d.mesh_id} hint={d.mesh_id ? '' : 'no agent'}>MeshCentral</MenuItem>
                <MenuItem icon="link" onClick={() => startRemote('rustdesk')} disabled={!d.rustdesk_id} hint={d.rustdesk_id || 'no id'}>RustDesk</MenuItem>
              </Dropdown>
              {can('tech') && <Button variant="secondary" icon="terminal" onClick={() => setTab('run')}>Run script</Button>}
              <div className="flex-1" />
              <Gate min="admin">
                <div className="flex items-center gap-2">
                  <span className="text-[11.5px] text-faint flex items-center gap-1"><Icon name="pin" size={13} />Site</span>
                  <Select value={d.site_id || ''} onChange={(e) => setSite(e.target.value)} className="w-auto min-w-[160px] h-8">
                    {clients.map((c) => <optgroup key={c.id} label={c.name}>{sites.filter((s) => s.client_id === c.id).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</optgroup>)}
                  </Select>
                </div>
              </Gate>
            </div>
          </div>

          <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'inventory', label: 'Inventory' }, { value: 'jobs', label: 'Jobs' }, { value: 'run', label: 'Run script' }]} />

          <div className="flex-1 overflow-y-auto scroll-area p-5">
            {tab === 'inventory' && <InventoryView id={id} />}
            {tab === 'jobs' && <DeviceJobs id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'run' && <RunScriptForm device={d} onCreated={() => setTab('jobs')} />}
          </div>
        </>
      )}
      {!d && <div className="flex-1 flex items-center justify-center"><Spinner size={22} /></div>}
    </Drawer>
  );
}

/* inventory */
function InfoGrid({ rows }) {
  return (
    <div className="grid grid-cols-2 gap-x-6 gap-y-2.5">
      {rows.map(([k, v]) => (
        <div key={k} className="flex items-center justify-between gap-3 border-b border-line/60 pb-2">
          <span className="text-[12px] text-faint">{k}</span>
          <span className="text-[12.5px] text-ink font-medium text-right truncate mono">{v}</span>
        </div>
      ))}
    </div>
  );
}
function InventorySection({ title, icon, children }) {
  return (
    <Card className="p-4">
      <div className="flex items-center gap-2 mb-3 text-[12.5px] font-semibold text-ink"><Icon name={icon} size={14} className="text-muted" />{title}</div>
      {children}
    </Card>
  );
}
function InventoryView({ id }) {
  const invQ = useQuery([id, 'inv'], () => api.getInventory(id), {});
  const [raw, setRaw] = useState(false);
  if (invQ.loading) return <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-28 w-full rounded-2xl" />)}</div>;
  const data = invQ.data?.data || {};
  const sys = data.system || {}, cpu = data.cpu || {}, mem = data.memory || {}, net = data.network || {};
  const disks = data.disks || [], sw = data.software || [];
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
          <InventorySection title="CPU & Memory" icon="cpu">
            <InfoGrid rows={[['Model', cpu.model], ['Cores', cpu.cores], ['Load', (cpu.load_pct || 0) + '%'], ['Memory', (mem.total_gb || 0) + ' GB'], ['Mem used', (mem.used_pct || 0) + '%']]} />
          </InventorySection>
          <InventorySection title="Storage" icon="disk">
            <div className="space-y-2.5">
              {disks.map((dk, i) => {
                const usedPct = Math.round((1 - dk.free_gb / dk.total_gb) * 100);
                return (
                  <div key={i}>
                    <div className="flex items-center justify-between text-[12px] mb-1"><span className="mono text-ink">{dk.mount} <span className="text-faint">{dk.fs}</span></span><span className="text-muted">{dk.free_gb} / {dk.total_gb} GB free</span></div>
                    <div className="h-1.5 rounded-full bg-elevated overflow-hidden"><div className={cx('h-full rounded-full', usedPct > 88 ? 'bg-bad' : usedPct > 75 ? 'bg-warn' : 'bg-brand')} style={{ width: usedPct + '%' }} /></div>
                  </div>
                );
              })}
            </div>
          </InventorySection>
          <InventorySection title="Network" icon="network">
            {(net.interfaces || []).map((ifc, i) => <InfoGrid key={i} rows={[['Interface', ifc.name], ['IPv4', ifc.ipv4], ['MAC', ifc.mac]]} />)}
          </InventorySection>
          <Card className="p-4 md:col-span-2">
            <div className="flex items-center gap-2 mb-3 text-[12.5px] font-semibold text-ink"><Icon name="grid" size={14} className="text-muted" />Installed software <span className="text-faint font-normal">({sw.length})</span></div>
            <div className="grid sm:grid-cols-2 gap-x-6 gap-y-1.5">
              {sw.map((s, i) => <div key={i} className="flex items-center justify-between border-b border-line/60 pb-1.5"><span className="text-[12.5px] text-ink">{s.name}</span><span className="text-[11.5px] text-faint mono">{s.version}</span></div>)}
            </div>
          </Card>
        </div>
      )}
    </div>
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
  const scriptsQ = useQuery(['scripts-pick'], () => api.listScripts(), { invalidateOn: ['scripts'] });

  if (!can('tech')) return <EmptyState icon="shield" title="Read-only access" msg="Running scripts requires the tech or admin role." />;

  const submit = async () => {
    setBusy(true);
    try {
      await api.createJob({ agent_id: device.id, shell, script, description: desc || 'Manual script run', require_approval: true });
      toast.success('Job created', 'Queued for approval — nothing has run yet.');
      onCreated();
    } catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };

  return (
    <div className="space-y-4 max-w-2xl">
      <div className="flex items-start gap-2.5 rounded-xl border border-warn/25 bg-warnsoft px-3.5 py-3">
        <Icon name="info" size={16} className="text-warn mt-0.5 shrink-0" />
        <div className="text-[12.5px] text-ink leading-relaxed">This creates an <b>approval-gated job</b>. It is a dry-run preview until a tech or admin approves it — nothing executes on {device.hostname} yet.</div>
      </div>
      <div className="flex gap-3">
        <Field label="Shell" className="w-44">
          <Select value={shell} onChange={(e) => setShell(e.target.value)}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option></Select>
        </Field>
        <Field label="From library" className="flex-1">
          <Select defaultValue="" onChange={(e) => { const s = (scriptsQ.data?.scripts || []).find((x) => x.id === e.target.value); if (s) { setScript(s.body); setShell(s.shell); setDesc(s.name); } }}>
            <option value="">Start from a saved script…</option>
            {(scriptsQ.data?.scripts || []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </Select>
        </Field>
      </div>
      <Field label="Description"><Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="What does this do?" /></Field>
      <Field label="Script"><CodeEditor value={script} onChange={setScript} shell={shell} minHeight={220} /></Field>
      <div className="flex justify-end gap-2">
        <Button variant="primary" icon="plus" loading={busy} onClick={submit} disabled={!script.trim()}>Create approval-gated job</Button>
      </div>
    </div>
  );
}

window.DevicesScreen = DevicesScreen;
window.DeviceDetail = DeviceDetail;
