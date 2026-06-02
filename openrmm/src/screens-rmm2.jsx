/* screens-rmm2.jsx — Part 2: Scripts, Policies, Clients & Sites, Users & Settings */

/* ============================ SCRIPT LIBRARY ============================== */
function ScriptsScreen() {
  const { can } = useAuth();
  const scriptsQ = useQuery(['scripts'], () => api.listScripts(), { invalidateOn: ['scripts'] });
  const [editing, setEditing] = useState(null);
  const [q, setQ] = useState('');
  const scripts = (scriptsQ.data?.scripts || []).filter((s) => !q || s.name.toLowerCase().includes(q.toLowerCase()) || s.category.toLowerCase().includes(q.toLowerCase()));

  return (
    <Page wide>
      <PageHeader title="Script library" subtitle="Reusable scripts. Running one creates an approval-gated job."
        actions={can('admin') && <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>New script</Button>} />
      <Card className="mb-4"><div className="flex items-center gap-2 p-2.5"><div className="relative flex-1"><Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search scripts…" className={cx(inputCls, 'pl-9')} /></div></div></Card>
      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {scripts.map((s) => (
          <button key={s.id} onClick={() => setEditing(s)} className="text-left rounded-2xl border border-line bg-card hover:border-line2 hover:bg-elevated/40 transition p-4 group">
            <div className="flex items-start justify-between gap-2 mb-2">
              <div className="h-9 w-9 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="script" size={17} /></div>
              <span className="rounded-md bg-elevated border border-line text-[10.5px] text-muted px-1.5 py-0.5 mono">{s.shell}</span>
            </div>
            <div className="text-[13.5px] font-semibold text-ink group-hover:text-brandhi transition-colors">{s.name}</div>
            <div className="text-[12px] text-muted mt-1 line-clamp-2 leading-snug">{s.description}</div>
            <div className="text-[11px] text-faint mt-2.5 flex items-center gap-1.5"><Icon name="grid" size={11} />{s.category}</div>
          </button>
        ))}
      </div>
      {!scriptsQ.loading && scripts.length === 0 && <Card><EmptyState icon="script" title="No scripts" msg="Add a script to the library." /></Card>}
      {editing && <ScriptEditor script={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </Page>
  );
}

function ScriptEditor({ script, onClose }) {
  const { can } = useAuth();
  const toast = useToast();
  const devMap = useDeviceMap();
  const [name, setName] = useState(script?.name || '');
  const [desc, setDesc] = useState(script?.description || '');
  const [category, setCategory] = useState(script?.category || 'Maintenance');
  const [shell, setShell] = useState(script?.shell || 'bash');
  const [body, setBody] = useState(script?.body || '');
  const [busy, setBusy] = useState(false);
  const [runOpen, setRunOpen] = useState(false);
  const readOnly = !can('admin');

  const save = async () => {
    setBusy(true);
    try { const b = { name, description: desc, category, shell, body }; if (script) await api.updateScript(script.id, b); else await api.createScript(b); toast.success(script ? 'Script saved' : 'Script created'); onClose(); }
    catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };
  const del = async () => { await api.deleteScript(script.id); toast.info('Script deleted'); onClose(); };

  return (
    <Drawer open onClose={onClose} width="max-w-[760px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-9 w-9 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="script" size={17} /></div>
        <div className="flex-1"><div className="text-[15px] font-semibold text-ink">{script ? script.name : 'New script'}</div><div className="text-[12px] text-faint">{readOnly ? 'Read-only' : 'Editing'}</div></div>
        {can('tech') && script && <Button variant="secondary" icon="play" onClick={() => setRunOpen(true)}>Run on device</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} disabled={readOnly} placeholder="Disk cleanup" /></Field>
          <Field label="Category"><Input value={category} onChange={(e) => setCategory(e.target.value)} disabled={readOnly} /></Field>
        </div>
        <Field label="Description"><Input value={desc} onChange={(e) => setDesc(e.target.value)} disabled={readOnly} /></Field>
        <Field label="Shell" className="w-44"><Select value={shell} onChange={(e) => setShell(e.target.value)} disabled={readOnly}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option></Select></Field>
        <Field label="Body">{readOnly ? <CodeBlock code={body} shell={shell} maxHeight={420} /> : <CodeEditor value={body} onChange={setBody} shell={shell} minHeight={300} />}</Field>
      </div>
      {!readOnly && (
        <div className="px-5 py-3.5 border-t border-line flex justify-between gap-2">
          {script ? <Button variant="danger" icon="trash" onClick={del}>Delete</Button> : <span />}
          <div className="flex gap-2"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} onClick={save} disabled={!name || !body}>{script ? 'Save script' : 'Create script'}</Button></div>
        </div>
      )}
      {runOpen && <RunOnDevice script={{ name, shell, body }} devMap={devMap} onClose={() => setRunOpen(false)} onDone={onClose} />}
    </Drawer>
  );
}

function RunOnDevice({ script, devMap, onClose, onDone }) {
  const toast = useToast();
  const [dev, setDev] = useState('');
  const [busy, setBusy] = useState(false);
  const devices = Object.values(devMap);
  const run = async () => {
    setBusy(true);
    try { await api.runScript(dev, { shell: script.shell, script: script.body, description: 'Run script: ' + script.name, name: script.name }); toast.success('Job created', 'Queued for approval on ' + devMap[dev].hostname); onClose(); onDone(); }
    catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={onClose} width="max-w-md">
      <div className="p-5">
        <div className="text-[15px] font-semibold text-ink mb-1">Run “{script.name}”</div>
        <div className="text-[12.5px] text-muted mb-4">Creates an approval-gated job — nothing runs until approved.</div>
        <Field label="Target device"><Select value={dev} onChange={(e) => setDev(e.target.value)}><option value="">Choose device…</option>{devices.map((d) => <option key={d.id} value={d.id}>{d.hostname} ({d.status})</option>)}</Select></Field>
        <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" icon="plus" loading={busy} disabled={!dev} onClick={run}>Create job</Button></div>
      </div>
    </Modal>
  );
}

/* ================================ POLICIES ================================ */
function PoliciesScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const policiesQ = useQuery(['policies'], () => api.listPolicies(), { invalidateOn: ['policies'] });
  const [detail, setDetail] = useState(null);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState('');
  const policies = policiesQ.data?.policies || [];

  const create = async () => { const p = await api.createPolicy({ name: newName }); setCreating(false); setNewName(''); toast.success('Policy created'); setDetail(p); };

  return (
    <Page wide>
      <PageHeader title="Policies" subtitle="Bundle checks and tasks, then assign them to clients, sites, or agents — scope is inherited."
        actions={can('admin') && <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>New policy</Button>} />
      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {policies.map((p) => <PolicyCard key={p.id} policy={p} onClick={() => setDetail(p)} />)}
      </div>
      {detail && <PolicyDetail policy={detail} onClose={() => setDetail(null)} />}
      <Modal open={creating} onClose={() => setCreating(false)} width="max-w-md"><div className="p-5"><div className="text-[15px] font-semibold text-ink mb-4">New policy</div><Field label="Name"><Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Servers — baseline" autoFocus /></Field><div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setCreating(false)}>Cancel</Button><Button variant="primary" onClick={create} disabled={!newName}>Create</Button></div></div></Modal>
    </Page>
  );
}

function PolicyCard({ policy, onClick }) {
  const checksQ = useQuery([policy.id, 'pc'], () => api.listChecks({ policy_id: policy.id }), { invalidateOn: ['checks'] });
  const tasksQ = useQuery([policy.id, 'pt'], () => api.listTasks({ policy_id: policy.id }), { invalidateOn: ['tasks'] });
  const asgQ = useQuery([policy.id, 'pa'], () => api.listAssignments(policy.id), { invalidateOn: ['policies'] });
  return (
    <button onClick={onClick} className="text-left rounded-2xl border border-line bg-card hover:border-line2 hover:bg-elevated/40 transition p-4 group">
      <div className="flex items-center gap-2.5 mb-2"><div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="policy" size={17} /></div><div className="text-[14px] font-semibold text-ink group-hover:text-brandhi transition-colors">{policy.name}</div></div>
      <div className="text-[12px] text-muted line-clamp-2 leading-snug min-h-[32px]">{policy.description || 'No description'}</div>
      <div className="flex items-center gap-3 mt-3 text-[11.5px] text-faint">
        <span className="flex items-center gap-1"><Icon name="check" size={12} />{(checksQ.data?.checks || []).length} checks</span>
        <span className="flex items-center gap-1"><Icon name="tasks" size={12} />{(tasksQ.data?.tasks || []).length} tasks</span>
        <span className="flex items-center gap-1"><Icon name="link" size={12} />{(asgQ.data?.assignments || []).length} assigned</span>
      </div>
    </button>
  );
}

function PolicyDetail({ policy, onClose }) {
  const { can } = useAuth();
  const toast = useToast();
  const [tab, setTab] = useState('checks');
  const checksQ = useQuery([policy.id, 'pcd'], () => api.listChecks({ policy_id: policy.id }), { invalidateOn: ['checks'] });
  const tasksQ = useQuery([policy.id, 'ptd'], () => api.listTasks({ policy_id: policy.id }), { invalidateOn: ['tasks'] });
  const asgQ = useQuery([policy.id, 'pad'], () => api.listAssignments(policy.id), { invalidateOn: ['policies'] });
  const clientsQ = useQuery(['cl-pol'], () => api.listClients(), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['st-pol'], () => api.listSites(), { invalidateOn: ['org'] });
  const devMap = useDeviceMap();
  const [addScope, setAddScope] = useState('');

  const checks = checksQ.data?.checks || [], tasks = tasksQ.data?.tasks || [], asg = asgQ.data?.assignments || [];
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const scopeName = (a) => a.scope_type === 'agent' ? devMap[a.scope_id]?.hostname : a.scope_type === 'site' ? sites.find((s) => s.id === a.scope_id)?.name : clients.find((c) => c.id === a.scope_id)?.name;

  const addAssignment = async () => { if (!addScope) return; const [type, id] = addScope.split(':'); await api.createAssignment(policy.id, { scope_type: type, scope_id: id }); setAddScope(''); toast.success('Assigned', 'Checks & tasks now apply to that scope.'); };
  const removeAssignment = async (aid) => { await api.deleteAssignment(aid); toast.info('Assignment removed'); };

  return (
    <Drawer open onClose={onClose} width="max-w-[720px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-10 w-10 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="policy" size={19} /></div>
        <div className="flex-1"><h2 className="text-[16px] font-semibold text-ink">{policy.name}</h2><div className="text-[12px] text-muted">{policy.description || 'No description'}</div></div>
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'checks', label: 'Checks', count: checks.length }, { value: 'tasks', label: 'Tasks', count: tasks.length }, { value: 'assign', label: 'Assignments', count: asg.length }]} />
      <div className="flex-1 overflow-y-auto scroll-area p-5">
        {tab === 'checks' && (checks.length ? <div className="space-y-2">{checks.map((c) => { const t = CHECK_TYPES[c.type]; return <div key={c.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3"><Icon name={t.icon} size={15} className="text-muted" /><div className="flex-1"><div className="text-[13px] text-ink font-medium">{c.name}</div><div className="text-[11px] text-faint">{t.label} · {c.warn_threshold != null ? `warn ${c.warn_threshold}/err ${c.err_threshold}` : ''}</div></div><StatusPill status={c.latest?.status || 'passing'} size="sm" /></div>; })}</div> : <EmptyState icon="check" title="No checks in this policy" />)}
        {tab === 'tasks' && (tasks.length ? <div className="space-y-2">{tasks.map((t) => <div key={t.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3"><Icon name="tasks" size={15} className="text-muted" /><div className="flex-1"><div className="text-[13px] text-ink font-medium">{t.name}</div><div className="text-[11px] text-faint">{t.schedule_kind === 'daily' ? 'Daily at ' + minuteToHHMM(t.run_at_minute) : 'Every ' + Math.round(t.interval_sec / 60) + 'm'}</div></div><Toggle checked={t.enabled} onChange={() => { }} disabled /></div>)}</div> : <EmptyState icon="tasks" title="No tasks in this policy" />)}
        {tab === 'assign' && (
          <div className="space-y-4">
            <div className="flex items-start gap-2.5 rounded-xl border border-brand/25 bg-brandsoft px-3.5 py-3"><Icon name="info" size={16} className="text-brandhi mt-0.5 shrink-0" /><div className="text-[12.5px] text-ink leading-relaxed">Assigning to a <b>client</b> or <b>site</b> pushes every check and task in this policy to <b>all agents in that scope</b>. Agent-level assignments target a single device.</div></div>
            {can('admin') && (
              <div className="flex items-center gap-2">
                <Select value={addScope} onChange={(e) => setAddScope(e.target.value)} className="flex-1"><option value="">Add an assignment…</option><optgroup label="Clients">{clients.map((c) => <option key={c.id} value={'client:' + c.id}>{c.name} (all sites)</option>)}</optgroup><optgroup label="Sites">{sites.map((s) => <option key={s.id} value={'site:' + s.id}>{s.name}</option>)}</optgroup><optgroup label="Agents">{Object.values(devMap).map((d) => <option key={d.id} value={'agent:' + d.id}>{d.hostname}</option>)}</optgroup></Select>
                <Button variant="primary" icon="plus" onClick={addAssignment} disabled={!addScope}>Assign</Button>
              </div>
            )}
            <div className="space-y-2">
              {asg.map((a) => (
                <div key={a.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-2.5">
                  <span className={cx('inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase', a.scope_type === 'agent' ? 'text-run bg-runsoft border-run/25' : a.scope_type === 'site' ? 'text-warn bg-warnsoft border-warn/25' : 'text-brandhi bg-brandsoft border-brand/25')}>{a.scope_type}</span>
                  <span className="flex-1 text-[13px] text-ink">{scopeName(a) || a.scope_id}</span>
                  {can('admin') && <button onClick={() => removeAssignment(a.id)} className="text-faint hover:text-bad p-1"><Icon name="trash" size={15} /></button>}
                </div>
              ))}
              {asg.length === 0 && <div className="text-center text-[12.5px] text-faint py-4">Not assigned anywhere yet.</div>}
            </div>
          </div>
        )}
      </div>
    </Drawer>
  );
}

/* ============================ CLIENTS & SITES ============================= */
function ClientsScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const clientsQ = useQuery(['clients'], () => api.listClients(), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['sites'], () => api.listSites(), { invalidateOn: ['org'] });
  const devicesQ = useQuery(['cl-dev'], () => api.listDevices(), { invalidateOn: ['devices'] });
  const [sel, setSel] = useState(null); // site id
  const [creatingClient, setCreatingClient] = useState(false);
  const [name, setName] = useState('');

  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [], devices = devicesQ.data?.devices || [];
  const siteCount = (siteId) => devices.filter((d) => d.site_id === siteId).length;
  const clientCount = (cid) => devices.filter((d) => sites.find((s) => s.id === d.site_id && s.client_id === cid)).length;
  const selSite = sites.find((s) => s.id === sel);

  const addClient = async () => { await api.createClient(name); setName(''); setCreatingClient(false); toast.success('Client created'); };

  return (
    <Page wide>
      <PageHeader title="Clients & Sites" subtitle="Organize your fleet into clients and sites. Enroll agents per site."
        actions={can('admin') && <Button variant="primary" icon="plus" onClick={() => setCreatingClient(true)}>New client</Button>} />
      <div className="grid lg:grid-cols-[360px_1fr] gap-5">
        <div className="space-y-3">
          {clients.map((c) => <ClientNode key={c.id} client={c} sites={sites.filter((s) => s.client_id === c.id)} sel={sel} setSel={setSel} siteCount={siteCount} clientCount={clientCount} />)}
          {clients.length === 0 && <Card><EmptyState icon="building" title="No clients" /></Card>}
        </div>
        <div>{selSite ? <SiteDetail site={selSite} client={clients.find((c) => c.id === selSite.client_id)} devices={devices.filter((d) => d.site_id === sel)} /> : <Card className="h-full"><EmptyState icon="pin" title="Select a site" msg="Pick a site to see its devices and enrollment command." /></Card>}</div>
      </div>
      <Modal open={creatingClient} onClose={() => setCreatingClient(false)} width="max-w-md"><div className="p-5"><div className="text-[15px] font-semibold text-ink mb-4">New client</div><Field label="Client name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Northwind Trading" autoFocus /></Field><div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setCreatingClient(false)}>Cancel</Button><Button variant="primary" onClick={addClient} disabled={!name}>Create</Button></div></div></Modal>
    </Page>
  );
}

function ClientNode({ client, sites, sel, setSel, siteCount, clientCount }) {
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(true);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  const addSite = async () => { await api.createSite(client.id, name); setName(''); setAdding(false); toast.success('Site created'); };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-3.5 py-3">
        <button onClick={() => setOpen((o) => !o)} className="text-faint hover:text-ink"><Icon name={open ? 'chevdown' : 'chevright'} size={15} /></button>
        <Icon name="building" size={16} className="text-muted" />
        <div className="flex-1 min-w-0"><div className="text-[13.5px] font-semibold text-ink truncate">{client.name}</div></div>
        <span className="text-[11px] text-faint">{clientCount(client.id)} devices</span>
      </div>
      {open && (
        <div className="border-t border-line">
          {sites.map((s) => (
            <button key={s.id} onClick={() => setSel(s.id)} className={cx('w-full flex items-center gap-2.5 pl-10 pr-3.5 py-2.5 transition text-left border-b border-line/60 last:border-0', sel === s.id ? 'bg-brandsoft' : 'hover:bg-elevated/40')}>
              <Icon name="pin" size={13} className={sel === s.id ? 'text-brandhi' : 'text-faint'} />
              <span className={cx('flex-1 text-[12.5px]', sel === s.id ? 'text-ink font-medium' : 'text-muted')}>{s.name}</span>
              <span className="text-[11px] text-faint tabular">{siteCount(s.id)}</span>
            </button>
          ))}
          {can('admin') && (adding ? (
            <div className="flex items-center gap-2 pl-10 pr-3 py-2"><Input value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && addSite()} placeholder="Site name" className="h-8" autoFocus /><Button size="sm" variant="primary" onClick={addSite} disabled={!name}>Add</Button></div>
          ) : <button onClick={() => setAdding(true)} className="w-full flex items-center gap-2 pl-10 pr-3.5 py-2 text-[12px] text-faint hover:text-muted transition"><Icon name="plus" size={13} />Add site</button>)}
        </div>
      )}
    </Card>
  );
}

function SiteDetail({ site, client, devices }) {
  const { can } = useAuth();
  const toast = useToast();
  const [enrollCmd, setEnrollCmd] = useState(null);
  const [busy, setBusy] = useState(false);
  const genEnroll = async () => { setBusy(true); try { const t = await api.createEnrollToken({ description: 'Enroll for ' + site.name, site_id: site.id, uses: 50, ttl_hours: 168 }); setEnrollCmd(`curl -fsSL ${api.API_BASE}/install.sh | sudo bash -s -- \\\n  --server ${api.API_BASE} \\\n  --token ${t.token} \\\n  --site "${site.name}"`); toast.success('Enrollment token created'); } finally { setBusy(false); } };
  return (
    <Card className="p-5">
      <div className="flex items-start gap-3 mb-4">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="pin" size={19} /></div>
        <div className="flex-1"><h2 className="text-[16px] font-semibold text-ink">{site.name}</h2><div className="text-[12px] text-muted">{client?.name} · {devices.length} devices</div></div>
      </div>
      <div className="mb-5">
        <div className="text-[12px] font-semibold text-muted mb-2">Devices at this site</div>
        <div className="space-y-1.5">
          {devices.map((d) => <div key={d.id} className="flex items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2"><span className={cx('h-2 w-2 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} /><OsIcon os={d.os} size={14} className="text-muted" /><span className="text-[12.5px] text-ink flex-1">{d.hostname}</span><span className="text-[11px] text-faint">{timeAgo(d.last_seen)}</span></div>)}
          {devices.length === 0 && <div className="text-[12.5px] text-faint py-3 text-center">No devices yet.</div>}
        </div>
      </div>
      {can('admin') && (
        <div>
          <div className="flex items-center justify-between mb-2"><div className="text-[12px] font-semibold text-muted">Enroll a new agent</div>{enrollCmd && <CopyButton value={enrollCmd} label="Copy command" />}</div>
          {enrollCmd ? <CodeBlock code={enrollCmd} shell="bash" label="install agent" showCopy={false} maxHeight={160} /> : <Button variant="secondary" icon="key" loading={busy} onClick={genEnroll}>Generate enrollment command</Button>}
        </div>
      )}
    </Card>
  );
}

window.ScriptsScreen = ScriptsScreen;
window.PoliciesScreen = PoliciesScreen;
window.ClientsScreen = ClientsScreen;

/* ============================ USERS & SETTINGS =========================== */
function SettingsScreen() {
  const { can, role } = useAuth();
  const [tab, setTab] = useState('users');
  return (
    <Page>
      <PageHeader title="Users & Settings" subtitle="Manage access and review your instance configuration." />
      <Tabs className="mb-5" value={tab} onChange={setTab} tabs={[{ value: 'users', label: 'Users' }, { value: 'config', label: 'Configuration' }, { value: 'enroll', label: 'Enrollment' }]} />
      {tab === 'users' && <UsersPanel canAdmin={can('admin')} />}
      {tab === 'config' && <ConfigPanel />}
      {tab === 'enroll' && <EnrollPanel canAdmin={can('admin')} />}
    </Page>
  );
}

function UsersPanel({ canAdmin }) {
  const toast = useToast();
  const usersQ = useQuery(['users'], () => api.listUsers(), { invalidateOn: ['users'] });
  const [creating, setCreating] = useState(false);
  const [username, setUsername] = useState('');
  const [newRole, setNewRole] = useState('tech');
  const [confirmDel, setConfirmDel] = useState(null);
  const users = usersQ.data?.users || [];

  const create = async () => { try { await api.createUser({ username, role: newRole }); setUsername(''); setCreating(false); toast.success('User created'); } catch (e) { toast.error('Failed', e.message); } };
  const del = async (id) => { try { await api.deleteUser(id); toast.info('User removed'); } catch (e) { toast.error('Failed', e.message); } };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">Users <span className="text-faint font-normal">({users.length})</span></div>
        {canAdmin && <Button size="sm" variant="primary" icon="plus" onClick={() => setCreating(true)}>Add user</Button>}
      </div>
      {!canAdmin && <div className="px-4 py-2.5 text-[12px] text-faint bg-elevated/30 border-b border-line flex items-center gap-1.5"><Icon name="info" size={13} />User management requires the admin role.</div>}
      <table className="w-full text-left">
        <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line"><th className="font-semibold px-4 py-2.5">User</th><th className="font-semibold px-3 py-2.5">Role</th><th className="font-semibold px-3 py-2.5">Created</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th></tr></thead>
        <tbody className="divide-y divide-line">
          {users.map((u) => (
            <tr key={u.id} className="hover:bg-elevated/40 transition">
              <td className="px-4 py-3"><div className="flex items-center gap-2.5"><Avatar name={u.username} size={28} /><span className="text-[13px] text-ink font-medium">{u.username}</span></div></td>
              <td className="px-3 py-3"><RoleBadge role={u.role} /></td>
              <td className="px-3 py-3 text-[12px] text-muted tabular">{fmtTime(u.created_at)}</td>
              <td className="px-4 py-3 text-right">{canAdmin && u.username !== 'admin' && <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(u)}>Remove</Button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <Modal open={creating} onClose={() => setCreating(false)} width="max-w-md"><div className="p-5"><div className="text-[15px] font-semibold text-ink mb-4">Add user</div><div className="space-y-3"><Field label="Username"><Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="tech@company" autoFocus /></Field><Field label="Role"><Select value={newRole} onChange={(e) => setNewRole(e.target.value)}><option value="readonly">readonly — view only</option><option value="tech">tech — run, approve & ack</option><option value="admin">admin — manage everything</option></Select></Field></div><div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setCreating(false)}>Cancel</Button><Button variant="primary" onClick={create} disabled={!username}>Create user</Button></div></div></Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => del(confirmDel.id)} danger title="Remove user?" body={<>This revokes <b>{confirmDel?.username}</b>'s access immediately.</>} confirmLabel="Remove user" />
    </Card>
  );
}

function ConfigItem({ label, value, mono }) {
  return <div className="flex items-center justify-between py-2.5 border-b border-line/60 last:border-0"><span className="text-[12.5px] text-muted">{label}</span><span className={cx('text-[12.5px] text-ink font-medium', mono && 'mono')}>{value}</span></div>;
}
function ConfigPanel() {
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const c = cfgQ.data;
  if (!c) return <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-2xl" />)}</div>;
  return (
    <div className="grid md:grid-cols-2 gap-4">
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="sparkle" size={15} /></div><div className="text-[13px] font-semibold text-ink">AI provider</div><span className="ml-auto"><StatusPill status="online" label="Configured" size="sm" /></span></div>
        <ConfigItem label="Provider" value={c.ai.provider} /><ConfigItem label="Model" value={c.ai.model} mono /><ConfigItem label="Base URL" value={c.ai.base_url} mono />
      </Card>
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="remote" size={15} /></div><div className="text-[13px] font-semibold text-ink">Remote access</div></div>
        <ConfigItem label="Default provider" value={c.remote.default} /><ConfigItem label="Available" value={c.remote.providers.join(', ')} mono />
      </Card>
      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={15} /></div><div className="text-[13px] font-semibold text-ink">Server</div></div>
        <ConfigItem label="Version" value={c.server.version} mono /><ConfigItem label="API base URL" value={c.server.base_url} mono />
        <div className="text-[11.5px] text-faint mt-3 flex items-center gap-1.5"><Icon name="info" size={13} />Configuration is read-only here — it's set in your server's environment.</div>
      </Card>
    </div>
  );
}

function EnrollPanel({ canAdmin }) {
  const toast = useToast();
  const [token, setToken] = useState(null);
  const [busy, setBusy] = useState(false);
  const gen = async () => { setBusy(true); try { const t = await api.createEnrollToken({ description: 'Manual enrollment', uses: 100, ttl_hours: 168 }); setToken(t); toast.success('Enrollment token created', 'Shown once — copy it now.'); } finally { setBusy(false); } };
  if (!canAdmin) return <Card><EmptyState icon="shield" title="Admin only" msg="Enrollment management requires the admin role." /></Card>;
  return (
    <Card className="p-5 max-w-2xl">
      <div className="text-[14px] font-semibold text-ink mb-1">Enroll new agents</div>
      <div className="text-[12.5px] text-muted mb-4">Generate a token, then run the install command on the target machine. The token is shown only once.</div>
      <Button variant="primary" icon="key" loading={busy} onClick={gen}>Create enrollment token</Button>
      {token && (
        <div className="mt-4 space-y-3 animate-fade-in">
          <div className="flex items-center gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2"><Icon name="key" size={14} className="text-warn" /><span className="text-[12.5px] mono text-ink flex-1 truncate">{token.token}</span><CopyButton value={token.token} /></div>
          <div><div className="flex items-center justify-between mb-2"><div className="text-[12px] font-semibold text-muted">Install command</div><CopyButton value={`curl -fsSL ${api.API_BASE}/install.sh | sudo bash -s -- --server ${api.API_BASE} --token ${token.token}`} label="Copy" /></div>
            <CodeBlock code={`curl -fsSL ${api.API_BASE}/install.sh | sudo bash -s -- \\\n  --server ${api.API_BASE} \\\n  --token ${token.token}`} shell="bash" label="install agent" showCopy={false} />
          </div>
          <div className="text-[11.5px] text-faint">Valid for {token.ttl_hours}h · up to {token.uses} enrollments.</div>
        </div>
      )}
    </Card>
  );
}

window.SettingsScreen = SettingsScreen;
