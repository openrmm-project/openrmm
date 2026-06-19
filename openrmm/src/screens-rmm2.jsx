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
  const [confirmDel, setConfirmDel] = useState(false);
  const del = async () => { try { await api.deletePolicy(policy.id); toast.info('Policy deleted'); onClose(); } catch (e) { toast.error('Failed', e.message); } };

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
      {can('admin') && (
        <div className="px-5 py-3.5 border-t border-line flex justify-end">
          <Button variant="danger" icon="trash" onClick={() => setConfirmDel(true)}>Delete policy</Button>
        </div>
      )}
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={del} danger title="Delete policy?" body={<>This removes <b>{policy.name}</b> with its checks, tasks and assignments. This cannot be undone.</>} confirmLabel="Delete policy" />
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
        <div>{selSite ? <SiteDetail site={selSite} client={clients.find((c) => c.id === selSite.client_id)} devices={devices.filter((d) => d.site_id === sel)} clients={clients} sites={sites} /> : <Card className="h-full"><EmptyState icon="pin" title="Select a site" msg="Pick a site to see its devices and enrollment command." /></Card>}</div>
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
  const [renaming, setRenaming] = useState(false);
  const [rename, setRename] = useState(client.name);
  const [confirmDel, setConfirmDel] = useState(false);
  const addSite = async () => { await api.createSite(client.id, name); setName(''); setAdding(false); toast.success('Site created'); };
  const saveRename = async () => { const n = rename.trim(); if (!n || n === client.name) { setRenaming(false); return; } try { await api.updateClient(client.id, n); window.dataBus.emit('org'); toast.success('Client renamed'); setRenaming(false); } catch (e) { toast.error('Failed', e.message); } };
  const delClient = async () => { try { await api.deleteClient(client.id); window.dataBus.emit('org'); window.dataBus.emit('devices'); window.dataBus.emit('groups'); toast.info('Client deleted'); } catch (e) { toast.error('Failed', e.message); } };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-3.5 py-3">
        <button onClick={() => setOpen((o) => !o)} className="text-faint hover:text-ink"><Icon name={open ? 'chevdown' : 'chevright'} size={15} /></button>
        <Icon name="building" size={16} className="text-muted" />
        <div className="flex-1 min-w-0">
          {renaming
            ? <div className="flex items-center gap-1.5"><Input value={rename} onChange={(e) => setRename(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') saveRename(); if (e.key === 'Escape') setRenaming(false); }} className="h-7" autoFocus /><Button size="sm" variant="primary" onClick={saveRename}>Save</Button><Button size="sm" variant="ghost" onClick={() => setRenaming(false)}>Cancel</Button></div>
            : <div className="text-[13.5px] font-semibold text-ink truncate">{client.name}</div>}
        </div>
        {!renaming && <span className="text-[11px] text-faint">{clientCount(client.id)} devices</span>}
        {can('admin') && !renaming && (
          <Dropdown align="right" width="w-48" trigger={<button className="text-faint hover:text-ink p-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={16} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(client.name); setRenaming(true); }}>Rename client</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDel(true)}>Delete client</MenuItem>
          </Dropdown>
        )}
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
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={delClient} danger title="Delete client?" body={<>This removes <b>{client.name}</b> with all its sites and departments. Its devices become unassigned. This cannot be undone.</>} confirmLabel="Delete client" />
    </Card>
  );
}

// Cross-platform install commands for an enrollment token. Linux & macOS share
// the bash install.sh (it auto-detects via uname); Windows uses install.ps1.
function EnrollCommands({ token, site }) {
  const [os, setOs] = useState('linux');
  const base = api.API_BASE;
  const siteArg = site ? ` \\\n  --site "${site.name}"` : '';
  const nixCmd = `curl -fsSL ${base}/install.sh | sudo bash -s -- \\\n  --server ${base} \\\n  --token ${token}${siteArg}`;
  const winCmd = `# Run PowerShell as Administrator\n& ([scriptblock]::Create((iwr -useb ${base}/install.ps1))) -Server ${base} -Token ${token}`;
  const isWin = os === 'windows';
  const cmd = isWin ? winCmd : nixCmd;
  const OSES = [{ v: 'linux', l: 'Linux' }, { v: 'macos', l: 'macOS' }, { v: 'windows', l: 'Windows' }];
  return (
    <div>
      <div className="flex items-center justify-between mb-2 gap-2">
        <div className="inline-flex rounded-lg border border-line bg-elevated p-0.5">
          {OSES.map((o) => (
            <button key={o.v} onClick={() => setOs(o.v)}
              className={cx('px-3 h-7 rounded-md text-[12px] font-medium transition-colors', os === o.v ? 'bg-card text-ink shadow-sm' : 'text-faint hover:text-muted')}>{o.l}</button>
          ))}
        </div>
        <CopyButton value={cmd} label="Copy command" size="sm" variant="ghost" />
      </div>
      <CodeBlock code={cmd} shell={isWin ? 'powershell' : 'bash'} label="install agent" showCopy={false} maxHeight={180} />
      {isWin && <div className="text-[11.5px] text-faint mt-2">Run in an <span className="text-ink font-medium">elevated PowerShell</span> (Run as Administrator).</div>}
      {os === 'macos' && <div className="text-[11.5px] text-faint mt-2">Same installer as Linux — it auto-detects macOS (Intel &amp; Apple Silicon).</div>}
    </div>
  );
}

function SiteDetail({ site, client, devices, clients, sites }) {
  const { can } = useAuth();
  const toast = useToast();
  const [enrollToken, setEnrollToken] = useState(null);
  const [busy, setBusy] = useState(false);
  const groupsQ = useQuery(['groups'], () => api.listGroups(), { invalidateOn: ['groups'] });
  const depts = (groupsQ.data?.groups || []).filter((g) => g.site_id === site.id);
  const deptCount = (gid) => devices.filter((d) => d.group_id === gid).length;
  const [renaming, setRenaming] = useState(false);
  const [rename, setRename] = useState(site.name);
  const [creatingDept, setCreatingDept] = useState(false);
  const [newDept, setNewDept] = useState('');
  const [confirmDelSite, setConfirmDelSite] = useState(false);
  const [confirmDelDept, setConfirmDelDept] = useState(null);
  const [moveDevice, setMoveDevice] = useState(null);
  const [confirmDelDevice, setConfirmDelDevice] = useState(null);
  useEffect(() => { setRenaming(false); setRename(site.name); setCreatingDept(false); setNewDept(''); }, [site.id]);

  const saveRename = async () => { const n = rename.trim(); if (!n || n === site.name) { setRenaming(false); return; } try { await api.updateSite(site.id, n); window.dataBus.emit('org'); toast.success('Site renamed'); setRenaming(false); } catch (e) { toast.error('Failed', e.message); } };
  const addDept = async () => { if (!newDept.trim()) return; try { await api.createGroup({ name: newDept.trim(), site_id: site.id }); setNewDept(''); setCreatingDept(false); window.dataBus.emit('groups'); toast.success('Department created'); } catch (e) { toast.error('Failed', e.message); } };
  const delDept = async () => { const g = confirmDelDept; if (!g) return; try { await api.deleteGroup(g.id); window.dataBus.emit('groups'); window.dataBus.emit('devices'); toast.info('Department deleted'); } catch (e) { toast.error('Failed', e.message); } };
  const delSite = async () => { try { await api.deleteSite(site.id); window.dataBus.emit('org'); window.dataBus.emit('devices'); window.dataBus.emit('groups'); toast.info('Site deleted'); } catch (e) { toast.error('Failed', e.message); } };
  const delDevice = async () => { const d = confirmDelDevice; if (!d) return; try { await api.deleteDevice(d.id); window.dataBus.emit('devices'); toast.info('Device deleted'); } catch (e) { toast.error('Failed to delete device', e.message); } };
  const saveDeviceMove = async ({ siteId, groupId }) => {
    const d = moveDevice;
    if (!d) return;
    try {
      if ((d.group_id || '') && d.site_id !== siteId) await api.setDeviceGroup(d.id, null);
      if ((d.site_id || '') !== (siteId || '')) await api.setDeviceSite(d.id, siteId || null);
      if ((d.group_id || '') !== (groupId || '')) await api.setDeviceGroup(d.id, groupId || null);
      window.dataBus.emit('devices'); setMoveDevice(null); toast.success('Device moved');
    } catch (e) { toast.error('Failed to move device', e.message); }
  };
  const genEnroll = async () => { setBusy(true); try { const t = await api.createEnrollToken({ description: 'Enroll for ' + site.name, site_id: site.id, uses: 50, ttl_hours: 168 }); setEnrollToken(t.token); toast.success('Enrollment token created'); } finally { setBusy(false); } };
  return (
    <Card className="p-5">
      <div className="flex items-start gap-3 mb-4">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="pin" size={19} /></div>
        <div className="flex-1 min-w-0">
          {renaming
            ? <div className="flex items-center gap-2"><Input value={rename} onChange={(e) => setRename(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') saveRename(); if (e.key === 'Escape') setRenaming(false); }} className="h-8" autoFocus /><Button size="sm" variant="primary" onClick={saveRename}>Save</Button><Button size="sm" variant="ghost" onClick={() => setRenaming(false)}>Cancel</Button></div>
            : <h2 className="text-[16px] font-semibold text-ink truncate">{site.name}</h2>}
          <div className="text-[12px] text-muted">{client?.name} · {devices.length} devices</div>
        </div>
        {can('admin') && !renaming && (
          <Dropdown align="right" width="w-52" trigger={<button className="text-faint hover:text-ink p-1.5 -mr-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={18} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(site.name); setRenaming(true); }}>Rename site</MenuItem>
            <MenuItem icon="building" onClick={() => setCreatingDept(true)}>Create department</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDelSite(true)}>Delete site</MenuItem>
          </Dropdown>
        )}
      </div>
      <div className="mb-5">
        <div className="text-[12px] font-semibold text-muted mb-2">Devices at this site</div>
        <div className="space-y-1.5">
          {devices.map((d) => (
            <div key={d.id} className="grid grid-cols-[auto_minmax(0,1fr)_minmax(0,1fr)_auto_auto] items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2">
              <span className={cx('h-2 w-2 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
              <div className="min-w-0 flex items-center gap-2"><OsIcon os={d.os} size={14} className="text-muted shrink-0" /><span className="text-[12.5px] text-ink truncate">{d.hostname}</span></div>
              <div className="text-[12px] text-muted truncate">{d.description || <span className="text-faint">No description</span>}</div>
              <span className="text-[11px] text-faint whitespace-nowrap">{timeAgo(d.last_seen)}</span>
              {can('admin') && (
                <Dropdown align="right" width="w-44" trigger={<button className="text-faint hover:text-ink p-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={15} fill /></button>}>
                  <MenuItem icon="pin" onClick={() => setMoveDevice(d)}>Move</MenuItem>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="trash" onClick={() => setConfirmDelDevice(d)}>Delete device</MenuItem>
                </Dropdown>
              )}
            </div>
          ))}
          {devices.length === 0 && <div className="text-[12.5px] text-faint py-3 text-center">No devices yet.</div>}
        </div>
      </div>
      {can('admin') && (depts.length > 0 || creatingDept) && (
        <div className="mb-5">
          <div className="text-[12px] font-semibold text-muted mb-2">Departments <span className="text-faint font-normal">(optional)</span></div>
          <div className="space-y-1.5">
            {depts.map((g) => (
              <div key={g.id} className="flex items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2">
                <Icon name="building" size={14} className="text-muted" />
                <span className="text-[12.5px] text-ink flex-1">{g.name}</span>
                <span className="text-[11px] text-faint">{deptCount(g.id)} devices</span>
                <button onClick={() => setConfirmDelDept(g)} title="Delete department" className="text-faint hover:text-bad p-0.5"><Icon name="trash" size={13} /></button>
              </div>
            ))}
            {creatingDept && (
              <div className="flex items-center gap-2 pt-1">
                <Input value={newDept} onChange={(e) => setNewDept(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addDept(); if (e.key === 'Escape') { setCreatingDept(false); setNewDept(''); } }} placeholder="Department name (e.g. Finance)" className="h-8" autoFocus />
                <Button size="sm" variant="primary" onClick={addDept} disabled={!newDept.trim()}>Add</Button>
                <Button size="sm" variant="ghost" onClick={() => { setCreatingDept(false); setNewDept(''); }}>Cancel</Button>
              </div>
            )}
          </div>
        </div>
      )}
      {can('admin') && (
        <div>
          <div className="text-[12px] font-semibold text-muted mb-2">Enroll a new agent</div>
          {enrollToken ? <EnrollCommands token={enrollToken} site={site} /> : <Button variant="secondary" icon="key" loading={busy} onClick={genEnroll}>Generate enrollment command</Button>}
        </div>
      )}
      <ConfirmDialog open={confirmDelSite} onClose={() => setConfirmDelSite(false)} onConfirm={delSite} danger title="Delete site?" body={<>This removes <b>{site.name}</b> and its departments. Its devices become unassigned. This cannot be undone.</>} confirmLabel="Delete site" />
      <ConfirmDialog open={!!confirmDelDept} onClose={() => setConfirmDelDept(null)} onConfirm={delDept} danger title="Delete department?" body={<>This removes the department <b>{confirmDelDept?.name}</b>. Its devices stay, but their department is cleared.</>} confirmLabel="Delete department" />
      <DeviceMoveModal open={!!moveDevice} onClose={() => setMoveDevice(null)} device={moveDevice} clients={clients} sites={sites} groups={groupsQ.data?.groups || []} onSave={saveDeviceMove} />
      <ConfirmDialog open={!!confirmDelDevice} onClose={() => setConfirmDelDevice(null)} onConfirm={delDevice} danger title="Delete device?" body={<>This permanently removes <b>{confirmDelDevice?.hostname}</b> from OpenRMM. This cannot be undone.</>} confirmLabel="Delete device" />
    </Card>
  );
}

function DeviceMoveModal({ open, onClose, device, clients, sites, groups, onSave }) {
  const [q, setQ] = useState('');
  const [siteId, setSiteId] = useState('');
  const [groupId, setGroupId] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (open && device) { setQ(''); setSiteId(device.site_id || ''); setGroupId(device.group_id || ''); } }, [open, device?.id]);
  if (!device) return null;
  const query = q.trim().toLowerCase();
  const matches = (v) => !query || (v || '').toLowerCase().includes(query);
  const groupsForSite = (sid) => groups.filter((g) => g.site_id === sid);
  const visibleClients = clients.filter((c) => matches(c.name) || sites.some((s) => s.client_id === c.id && (matches(s.name) || groupsForSite(s.id).some((g) => matches(g.name)))));
  const chooseSite = (sid) => { setSiteId(sid); setGroupId(''); };
  const chooseGroup = (sid, gid) => { setSiteId(sid); setGroupId(gid); };
  const changed = (siteId || '') !== (device.site_id || '') || (groupId || '') !== (device.group_id || '');
  const save = async () => { setBusy(true); try { await onSave({ siteId, groupId }); } finally { setBusy(false); } };

  return (
    <Modal open={open} onClose={onClose} width="max-w-2xl">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="pin" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">Move device</div><div className="text-[12.5px] text-muted">Choose where <b className="text-ink">{device.hostname}</b> belongs in Clients & Sites.</div></div>
        </div>
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search company, site, or department…" autoFocus />
        <div className="rounded-xl border border-line max-h-80 overflow-y-auto scroll-area p-1">
          <button onClick={() => chooseSite('')} className={cx('w-full flex items-center gap-2 rounded-lg px-3 py-2 text-left text-[12.5px] transition', !siteId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="close" size={13} />Unassigned</button>
          {visibleClients.map((c) => {
            const clientSites = sites.filter((s) => s.client_id === c.id && (matches(c.name) || matches(s.name) || groupsForSite(s.id).some((g) => matches(g.name))));
            return <div key={c.id} className="mt-1"><div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold flex items-center gap-1.5"><Icon name="building" size={12} />{c.name}</div>{clientSites.map((s) => {
              const siteGroups = groupsForSite(s.id).filter((g) => matches(c.name) || matches(s.name) || matches(g.name));
              return <div key={s.id}><button onClick={() => chooseSite(s.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-6 pr-3 py-2 text-left text-[12.5px] transition', siteId === s.id && !groupId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="pin" size={13} /><span className="flex-1 truncate">{s.name}</span><span className="text-[10.5px] text-faint">site</span></button>{siteGroups.map((g) => <button key={g.id} onClick={() => chooseGroup(s.id, g.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-11 pr-3 py-1.5 text-left text-[12.5px] transition', groupId === g.id ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="building" size={12} /><span className="flex-1 truncate">{g.name}</span><span className="text-[10.5px] text-faint">department</span></button>)}</div>;
            })}</div>;
          })}
          {visibleClients.length === 0 && <div className="px-3 py-8 text-center text-[12px] text-faint">No matches.</div>}
        </div>
        <div className="flex items-center justify-end gap-2"><Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button><Button variant="primary" loading={busy} disabled={!changed} onClick={save}>Move device</Button></div>
      </div>
    </Modal>
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
      {tab === 'users' && <><ChangePasswordCard /><TwoFactorCard /><UsersPanel canAdmin={can('admin')} /></>}
      {tab === 'config' && <ConfigPanel />}
      {tab === 'enroll' && <EnrollPanel canAdmin={can('admin')} />}
    </Page>
  );
}

// ChangePasswordCard lets the CURRENT user change their own password (self-service,
// any role). Verifies the current password server-side and enforces a minimum length.
function ChangePasswordCard() {
  const toast = useToast();
  const [cur, setCur] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const tooShort = next.length > 0 && next.length < 8;
  const mismatch = confirm.length > 0 && next !== confirm;
  const canSubmit = cur && next.length >= 8 && next === confirm && !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    try {
      await api.changePassword(cur, next);
      setCur(''); setNext(''); setConfirm('');
      toast.success('Password changed', 'Other sessions were signed out.');
    } catch (e) { toast.error('Could not change password', e.message); }
    finally { setBusy(false); }
  };

  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="key" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">Change password</div>
      </div>
      <div className="text-[12.5px] text-muted mb-3">Set a new password for your account. Minimum 8 characters; other sessions are signed out.</div>
      <div className="space-y-2.5 max-w-md">
        <Field label="Current password"><Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
        <Field label="New password" hint={tooShort ? 'At least 8 characters' : undefined}>
          <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" placeholder="••••••••" className={tooShort ? 'border-bad' : ''} />
        </Field>
        <Field label="Confirm new password" hint={mismatch ? "Passwords don't match" : undefined}>
          <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" placeholder="••••••••"
            onKeyDown={(e) => { if (e.key === 'Enter') submit(); }} className={mismatch ? 'border-bad' : ''} />
        </Field>
        <Button variant="primary" icon="key" loading={busy} disabled={!canSubmit} onClick={submit}>Change password</Button>
      </div>
    </Card>
  );
}

// TwoFactorCard manages the CURRENT user's TOTP 2FA (self-service, any role).
function TwoFactorCard() {
  const toast = useToast();
  const meQ = useQuery(['me'], () => api.me(), { invalidateOn: ['me'] });
  const enabled = !!meQ.data?.totp_enabled;
  const [setup, setSetup] = useState(null); // { secret, uri } during enrollment
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [recovery, setRecovery] = useState(null); // string[] shown once after enable/regen
  const qrRef = useRef(null);

  useEffect(() => {
    if (setup && qrRef.current && window.QRCode) {
      qrRef.current.innerHTML = '';
      try { new window.QRCode(qrRef.current, { text: setup.uri, width: 168, height: 168, correctLevel: window.QRCode.CorrectLevel.M }); } catch (e) {}
    }
  }, [setup]);

  const start = async () => { setBusy(true); try { setSetup(await api.setup2FA()); setCode(''); } catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); } };
  const activate = async () => { setBusy(true); try { const r = await api.enable2FA(code.trim()); setSetup(null); setCode(''); meQ.refetch(); setRecovery(r.recovery_codes || []); toast.success('Two-factor enabled'); } catch (e) { toast.error('Could not enable', e.message); } finally { setBusy(false); } };
  const disable = async () => { setBusy(true); try { await api.disable2FA(code.trim()); setCode(''); meQ.refetch(); toast.info('Two-factor disabled'); } catch (e) { toast.error('Could not disable', e.message); } finally { setBusy(false); } };
  const regen = async () => { setBusy(true); try { const r = await api.regenRecoveryCodes(code.trim()); setCode(''); setRecovery(r.recovery_codes || []); toast.success('New recovery codes generated'); } catch (e) { toast.error('Could not regenerate', e.message); } finally { setBusy(false); } };

  // downloadCodes saves the recovery codes as a text file (client-side).
  const downloadCodes = () => {
    const text = 'OpenRMM two-factor recovery codes\nKeep these somewhere safe. Each code works once.\n\n' + (recovery || []).join('\n') + '\n';
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const a = document.createElement('a'); a.href = url; a.download = 'rmm-ai-recovery-codes.txt';
    document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="shield" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">Two-factor authentication</div>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md ml-auto', enabled ? 'bg-oksoft text-ok' : 'bg-elevated text-faint')}>{enabled ? 'Enabled' : 'Disabled'}</span>
      </div>
      <div className="text-[12.5px] text-muted mb-3">Protect your account with a TOTP authenticator app (Google Authenticator, Authy, 1Password…).</div>

      {recovery && (
        <div className="mb-4 rounded-xl border border-warn/40 bg-warn/[0.04] p-3.5 animate-scale-in">
          <div className="flex items-center gap-2 mb-1.5"><Icon name="key" size={14} className="text-warn" /><span className="text-[12.5px] font-semibold text-ink">Recovery codes</span></div>
          <div className="text-[11.5px] text-muted mb-2.5">Save these now — each works <b>once</b> and they <b>won't be shown again</b>. Use one to sign in if you lose your authenticator.</div>
          <div className="grid grid-cols-2 gap-x-6 gap-y-1 rounded-lg border border-line bg-app px-3 py-2.5 mono text-[12.5px] text-ink">
            {recovery.map((c, i) => <div key={i} className="tracking-wide">{c}</div>)}
          </div>
          <div className="flex items-center gap-2 mt-3">
            <Button size="sm" variant="secondary" icon="download" onClick={downloadCodes}>Download</Button>
            <CopyButton value={recovery.join('\n')} size="sm" variant="ghost" />
            <Button size="sm" variant="primary" className="ml-auto" onClick={() => setRecovery(null)}>I saved them</Button>
          </div>
        </div>
      )}

      {enabled && !setup && (
        <div className="space-y-2">
          <div className="flex items-end gap-2">
            <Field label="Enter a current code" className="flex-1"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" inputMode="numeric" className="mono tracking-widest" /></Field>
            <Button variant="secondary" icon="refresh" loading={busy} disabled={code.trim().length !== 6} onClick={regen} title="Generate a new set of recovery codes (invalidates the old ones)">New recovery codes</Button>
            <Button variant="danger" loading={busy} disabled={code.trim().length !== 6} onClick={disable}>Disable 2FA</Button>
          </div>
          <div className="text-[11px] text-faint">A code disables 2FA or regenerates your recovery codes.</div>
        </div>
      )}

      {!enabled && !setup && <Button variant="primary" icon="shield" loading={busy} onClick={start}>Enable 2FA</Button>}

      {setup && (
        <div className="space-y-3 animate-fade-in">
          <div className="flex gap-4 flex-wrap">
            <div className="rounded-xl border border-line bg-white p-2 shrink-0" ref={qrRef} />
            <div className="min-w-0 flex-1">
              <div className="text-[12px] text-muted mb-1">1. Scan the QR, or add this secret manually:</div>
              <div className="flex items-center gap-2 rounded-lg border border-line bg-elevated px-2.5 py-1.5"><span className="text-[12.5px] mono text-ink break-all flex-1">{setup.secret}</span><CopyButton value={setup.secret} size="sm" variant="ghost" /></div>
              <div className="text-[11px] text-faint mt-1.5">Issuer: OpenRMM · 6 digits · 30s</div>
            </div>
          </div>
          <div className="flex items-end gap-2">
            <Field label="2. Enter the 6-digit code to activate" className="flex-1"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" inputMode="numeric" autoFocus className="mono tracking-widest" /></Field>
            <Button variant="ghost" onClick={() => { setSetup(null); setCode(''); }}>Cancel</Button>
            <Button variant="primary" loading={busy} disabled={code.trim().length !== 6} onClick={activate}>Activate</Button>
          </div>
        </div>
      )}
    </Card>
  );
}

function UsersPanel({ canAdmin }) {
  const toast = useToast();
  const usersQ = useQuery(['users'], () => api.listUsers(), { invalidateOn: ['users'] });
  const [creating, setCreating] = useState(false);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [newRole, setNewRole] = useState('tech');
  const [scopes, setScopes] = useState([]); // [] = full access
  const [confirmDel, setConfirmDel] = useState(null);
  const [confirmReset, setConfirmReset] = useState(null);
  const [editUser, setEditUser] = useState(null);   // user whose scope is being edited
  const [editScopes, setEditScopes] = useState([]);
  const [editLoading, setEditLoading] = useState(false);
  const [editSaving, setEditSaving] = useState(false);
  const [roleBusy, setRoleBusy] = useState('');
  const [pendingRole, setPendingRole] = useState(null);
  const [confirmScopeSave, setConfirmScopeSave] = useState(false);
  const users = usersQ.data?.users || [];

  const reset = () => { setUsername(''); setPassword(''); setNewRole('tech'); setScopes([]); setCreating(false); };
  const create = async () => {
    try {
      await api.createUser({ username, password, role: newRole, scopes: scopes.map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })) });
      reset(); toast.success('User created', scopes.length ? scopes.length + ' scope(s)' : 'full access');
    } catch (e) { toast.error('Failed', e.message); }
  };
  const del = async (id) => { try { await api.deleteUser(id); toast.info('User removed'); } catch (e) { toast.error('Failed', e.message); } };
  const reset2fa = async (u) => { try { await api.reset2FA(u.id); toast.info('2FA reset', u.username + ' can now sign in with their password'); usersQ.refetch(); } catch (e) { toast.error('Failed', e.message); } };
  const changeRole = async () => {
    const u = pendingRole?.user, role = pendingRole?.role;
    if (!u || !role || role === u.role) { setPendingRole(null); return; }
    setRoleBusy(u.id);
    try {
      await api.updateUserRole(u.id, role);
      toast.success('Role updated', u.username + ' is now ' + role);
      setPendingRole(null);
      usersQ.refetch();
    } catch (e) { toast.error('Failed to update role', e.message); }
    finally { setRoleBusy(''); }
  };

  // Open the scope editor: fetch the user's current scopes first, then render the
  // picker (so its mode initializes from the loaded selection).
  const openEdit = async (u) => {
    setEditUser(u); setEditLoading(true); setEditScopes([]);
    try {
      const r = await api.getUserScopes(u.id);
      setEditScopes((r.scopes || []).map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })));
    } catch (e) { toast.error('Failed to load scope', e.message); }
    finally { setEditLoading(false); }
  };
  const saveEdit = async () => {
    setEditSaving(true);
    try {
      await api.setUserScopes(editUser.id, editScopes.map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })));
      toast.success('Scope updated', editScopes.length ? editScopes.length + ' scope(s)' : 'full access');
      setConfirmScopeSave(false);
      setEditUser(null);
    } catch (e) { toast.error('Failed', e.message); }
    finally { setConfirmScopeSave(false); setEditSaving(false); }
  };

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
              <td className="px-3 py-3">
                {canAdmin && u.username !== 'admin'
                  ? <Select value={u.role} disabled={roleBusy === u.id} onChange={(e) => setPendingRole({ user: u, role: e.target.value })} className="h-8 min-w-28"><option value="readonly">readonly</option><option value="tech">tech</option><option value="admin">admin</option></Select>
                  : <RoleBadge role={u.role} />}
              </td>
              <td className="px-3 py-3 text-[12px] text-muted tabular">{fmtTime(u.created_at)}</td>
              <td className="px-4 py-3 text-right">
                {canAdmin && (
                  <div className="flex items-center justify-end gap-1">
                    {u.role !== 'admin' && <Button size="sm" variant="ghost" icon="shield" onClick={() => openEdit(u)}>Scope</Button>}
                    {u.totp_enabled && <Button size="sm" variant="ghost" icon="key" onClick={() => setConfirmReset(u)} title="Disable this user's 2FA so they can sign in and re-enroll">Reset 2FA</Button>}
                    {u.username !== 'admin' && <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(u)}>Remove</Button>}
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <Modal open={creating} onClose={reset} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-4">Add user</div>
          <div className="space-y-3">
            <Field label="Username"><Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="tech@company" autoFocus /></Field>
            <Field label="Password"><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="a strong password" /></Field>
            <Field label="Role"><Select value={newRole} onChange={(e) => setNewRole(e.target.value)}><option value="readonly">readonly — view (+ remote)</option><option value="tech">tech — run, approve & ack</option><option value="admin">admin — manage everything</option></Select></Field>
            {newRole === 'admin'
              ? <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Admins always have full access (scope does not apply).</div>
              : <Field label="Access scope"><ScopePicker scopes={scopes} setScopes={setScopes} /></Field>}
          </div>
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={reset}>Cancel</Button><Button variant="primary" onClick={create} disabled={!username || !password}>Create user</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => del(confirmDel.id)} danger title="Remove user?" body={<>This revokes <b>{confirmDel?.username}</b>'s access immediately.</>} confirmLabel="Remove user" />
      <Modal open={!!editUser} onClose={() => { if (!editSaving) { setConfirmScopeSave(false); setEditUser(null); } }} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">Access scope</div>
          <div className="text-[12px] text-muted mb-4">Define which Companies, Sites, Departments or machines <b className="text-ink">{editUser?.username}</b> can access. Empty = full access.</div>
          {editLoading
            ? <div className="py-8 flex items-center justify-center gap-2 text-faint text-[12.5px]"><Spinner size={14} />Loading current scope…</div>
            : <ScopePicker key={editUser?.id} scopes={editScopes} setScopes={setEditScopes} />}
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setEditUser(null)} disabled={editSaving}>Cancel</Button><Button variant="primary" loading={editSaving} disabled={editLoading} onClick={() => setConfirmScopeSave(true)}>Save scope</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmReset} onClose={() => setConfirmReset(null)} onConfirm={() => reset2fa(confirmReset)} danger title="Reset two-factor?" body={<>This turns off 2FA for <b>{confirmReset?.username}</b> and invalidates their recovery codes. They'll sign in with just their password until they re-enroll. Use only to recover a locked-out user.</>} confirmLabel="Reset 2FA" />
      <ConfirmDialog open={!!pendingRole} onClose={() => setPendingRole(null)} onConfirm={changeRole} title="Change user role?" body={<>Change <b>{pendingRole?.user?.username}</b> from <b>{pendingRole?.user?.role}</b> to <b>{pendingRole?.role}</b>? This changes what they can access and manage.</>} confirmLabel="Change role" />
      <ConfirmDialog open={confirmScopeSave} onClose={() => setConfirmScopeSave(false)} onConfirm={saveEdit} title="Save access scope?" body={<>Update access for <b>{editUser?.username}</b> to {editScopes.length ? <b>{editScopes.length} selected scope(s)</b> : <b>full access</b>}?</>} confirmLabel="Save scope" />
    </Card>
  );
}

// ScopePicker builds a user's access scope as a lazy-loaded, searchable tree
// (Company → Site → Machine). Scales to thousands of machines: sites load when
// a company is expanded, machines when a site is expanded. Empty = full access.
// Checking a company covers all its sites/machines; checking a site covers its
// machines. scopes is [{scope_type, scope_id}]; selection keys are "type:id".
function ScopePicker({ scopes, setScopes }) {
  const [clients, setClients] = useState(null);
  const [sitesBy, setSitesBy] = useState({});   // clientId -> [sites]
  const [devsBy, setDevsBy] = useState({});      // siteId -> [devices]
  const [groups, setGroups] = useState(null);    // departments across all sites
  const [expC, setExpC] = useState({});          // expanded clients
  const [expS, setExpS] = useState({});          // expanded sites
  const [q, setQ] = useState('');
  const [allSites, setAllSites] = useState(null); // every site (for search + parent lookup)
  const [found, setFound] = useState([]);          // device search results
  // Mode is independent of the selection so "Restricted" works before any node is
  // picked (empty selection still means full access on save — hinted below).
  const [mode, setMode] = useState(scopes.length > 0 ? 'restricted' : 'full');
  const restricted = mode === 'restricted';
  const searching = q.trim().length > 0;
  const scopeKey = scopes.map((s) => s.scope_type + ':' + s.scope_id).sort().join('|');
  const sel = new Set(scopes.map((s) => s.scope_type + ':' + s.scope_id));

  const loadClients = async () => { try { const r = await api.listClients(); setClients(r.clients || []); return r.clients || []; } catch (e) { setClients([]); return []; } };
  const loadGroups = async () => { try { const r = await api.listGroups(); setGroups(r.groups || []); return r.groups || []; } catch (e) { setGroups([]); return []; } };
  const loadSites = async (cid) => { if (sitesBy[cid]) return sitesBy[cid]; try { const r = await api.listSites(cid); const sites = r.sites || []; setSitesBy((m) => ({ ...m, [cid]: sites })); return sites; } catch (e) { return []; } };
  const loadDevs = async (sid) => { if (devsBy[sid]) return devsBy[sid]; try { const r = await api.listDevices({ site_id: sid }); const devs = r.devices || []; setDevsBy((m) => ({ ...m, [sid]: devs })); return devs; } catch (e) { return []; } };

  useEffect(() => { if (restricted) { if (clients === null) loadClients(); if (groups === null) loadGroups(); } }, [mode]);

  // When editing an existing user, expand the tree down to the saved scopes so
  // the admin immediately sees the selected company/site/department/device.
  useEffect(() => {
    if (!restricted || !scopeKey) return;
    let cancelled = false;
    (async () => {
      const [cr, sr, gr, dr] = await Promise.all([api.listClients(), api.listSites(), api.listGroups(), api.listDevices()]);
      if (cancelled) return;
      const cs = cr.clients || [], ss = sr.sites || [], gs = gr.groups || [], ds = dr.devices || [];
      const siteByID = {}; ss.forEach((s) => { siteByID[s.id] = s; });
      const groupByID = {}; gs.forEach((g) => { groupByID[g.id] = g; });
      const devByID = {}; ds.forEach((d) => { devByID[d.id] = d; });
      const nextC = {}, nextS = {};
      const includeSite = (sid) => { const st = siteByID[sid]; if (!st) return; nextC[st.client_id] = true; nextS[st.id] = true; };
      scopes.forEach((sc) => {
        if (sc.scope_type === 'client') nextC[sc.scope_id] = true;
        if (sc.scope_type === 'site') includeSite(sc.scope_id);
        if (sc.scope_type === 'group') includeSite(groupByID[sc.scope_id]?.site_id);
        if (sc.scope_type === 'agent') includeSite(devByID[sc.scope_id]?.site_id);
      });
      const nextSitesBy = {}; Object.keys(nextC).forEach((cid) => { nextSitesBy[cid] = ss.filter((s) => s.client_id === cid); });
      const nextDevsBy = {}; Object.keys(nextS).forEach((sid) => { nextDevsBy[sid] = ds.filter((d) => d.site_id === sid); });
      setClients(cs); setAllSites(ss); setGroups(gs);
      setExpC((m) => ({ ...m, ...nextC })); setExpS((m) => ({ ...m, ...nextS }));
      setSitesBy((m) => ({ ...m, ...nextSitesBy })); setDevsBy((m) => ({ ...m, ...nextDevsBy }));
    })();
    return () => { cancelled = true; };
  }, [restricted, scopeKey]);

  // Search across the whole org (not just loaded/expanded nodes): devices via the
  // server (?q=), companies/sites/departments client-side. Debounced.
  useEffect(() => {
    if (!searching) { setFound([]); return; }
    if (allSites === null) api.listSites().then((r) => setAllSites(r.sites || [])).catch(() => setAllSites([]));
    if (groups === null) api.listGroups().then((r) => setGroups(r.groups || [])).catch(() => setGroups([]));
    const t = setTimeout(() => { api.listDevices({ q: q.trim() }).then((r) => setFound(r.devices || [])).catch(() => setFound([])); }, 250);
    return () => clearTimeout(t);
  }, [q]);

  const toggle = (type, id) => {
    const k = type + ':' + id;
    setScopes(sel.has(k) ? scopes.filter((s) => s.scope_type + ':' + s.scope_id !== k) : [...scopes, { scope_type: type, scope_id: id }]);
  };
  const match = (name) => !q || (name || '').toLowerCase().includes(q.toLowerCase());
  const groupsForSite = (sid) => (groups || []).filter((g) => g.site_id === sid);
  const deviceCovered = (siteCovered, d) => siteCovered || (d.group_id && sel.has('group:' + d.group_id));

  const Row = ({ depth, icon, name, checked, disabled, onCheck, expandable, expanded, onExpand, hint }) => (
    <div className="flex items-center gap-1.5 py-1 hover:bg-elevated rounded" style={{ paddingLeft: depth * 18 + 4 }}>
      {expandable
        ? <button onClick={onExpand} className="text-faint hover:text-ink w-4"><Icon name={expanded ? 'chevdown' : 'chevright'} size={14} /></button>
        : <span className="w-4" />}
      <input type="checkbox" checked={checked} disabled={disabled} onChange={onCheck} className="h-3.5 w-3.5 accent-brand disabled:opacity-50" />
      <Icon name={icon} size={13} className="text-muted shrink-0" />
      <span className={cx('text-[12.5px] truncate', disabled ? 'text-faint' : 'text-ink')}>{name}</span>
      {hint && <span className="text-[10.5px] text-faint">· {hint}</span>}
    </div>
  );

  return (
    <div className="space-y-2">
      <div className="flex gap-4 text-[12px]">
        <label className="flex items-center gap-1.5 cursor-pointer"><input type="radio" checked={!restricted} onChange={() => { setMode('full'); setScopes([]); }} className="accent-brand" />Full access</label>
        <label className="flex items-center gap-1.5 cursor-pointer"><input type="radio" checked={restricted} onChange={() => { setMode('restricted'); if (clients === null) loadClients(); if (groups === null) loadGroups(); }} className="accent-brand" />Restricted to…</label>
        {restricted && <span className="ml-auto text-[11px] text-faint">{scopes.length} selected</span>}
      </div>
      {restricted && (
        <div className="rounded-xl border border-line overflow-hidden">
          <div className="p-2 border-b border-line"><Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search company/site/department/device…" className="text-[12px] h-8" /></div>
          <div className="max-h-72 overflow-y-auto p-1">
            {searching ? (() => {
              if (allSites === null || clients === null || groups === null) return <div className="p-3 text-center text-faint text-[12px]">Searching…</div>;
              const m = (n) => (n || '').toLowerCase().includes(q.trim().toLowerCase());
              const siteObj = {}; allSites.forEach((s) => { siteObj[s.id] = s; });
              const clientObj = {}; clients.forEach((c) => { clientObj[c.id] = c; });
              const devBySite = {}; found.forEach((d) => { (devBySite[d.site_id] = devBySite[d.site_id] || []).push(d); });
              const groupBySite = {}; groups.forEach((g) => { (groupBySite[g.site_id] = groupBySite[g.site_id] || []).push(g); });
              const siteIds = new Set([...Object.keys(devBySite), ...allSites.filter((s) => m(s.name)).map((s) => s.id), ...groups.filter((g) => m(g.name)).map((g) => g.site_id)]);
              const clientIds = new Set(clients.filter((c) => m(c.name)).map((c) => c.id));
              siteIds.forEach((sid) => { const s = siteObj[sid]; if (s) clientIds.add(s.client_id); });
              if (clientIds.size === 0) return <div className="p-3 text-center text-faint text-[12px]">No results for “{q}”.</div>;
              return [...clientIds].map((cid) => {
                const c = clientObj[cid]; if (!c) return null;
                const clientCov = sel.has('client:' + cid);
                const sitesUnder = [...siteIds].map((id) => siteObj[id]).filter((s) => s && s.client_id === cid);
                return (
                  <div key={cid}>
                    <Row depth={0} icon="building" name={c.name} checked={clientCov} onCheck={() => toggle('client', cid)} />
                    {sitesUnder.map((s) => {
                      const siteCov = clientCov || sel.has('site:' + s.id);
                      return (
                        <div key={s.id}>
                          <Row depth={1} icon="pin" name={s.name} checked={siteCov} disabled={clientCov} onCheck={() => toggle('site', s.id)} />
                          {(groupBySite[s.id] || []).filter((g) => m(g.name)).map((g) => <Row key={g.id} depth={2} icon="building" name={g.name} hint="department" checked={siteCov || sel.has('group:' + g.id)} disabled={siteCov} onCheck={() => toggle('group', g.id)} />)}
                          {(devBySite[s.id] || []).map((d) => { const cov = deviceCovered(siteCov, d); return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={d.status} checked={cov || sel.has('agent:' + d.id)} disabled={cov} onCheck={() => toggle('agent', d.id)} />; })}
                        </div>
                      );
                    })}
                  </div>
                );
              });
            })() : <>
            {clients === null && <div className="p-3 text-center text-faint text-[12px]">Loading…</div>}
            {clients && clients.length === 0 && <div className="p-3 text-center text-faint text-[12px]">No companies yet.</div>}
            {(clients || []).filter((c) => match(c.name) || expC[c.id]).map((c) => (
              <div key={c.id}>
                <Row depth={0} icon="building" name={c.name} checked={sel.has('client:' + c.id)} onCheck={() => toggle('client', c.id)}
                  expandable expanded={!!expC[c.id]} onExpand={() => { setExpC((m) => ({ ...m, [c.id]: !m[c.id] })); loadSites(c.id); }} />
                {expC[c.id] && (sitesBy[c.id] || []).filter((st) => match(st.name)).map((st) => {
                  const siteCovered = sel.has('client:' + c.id) || sel.has('site:' + st.id);
                  const siteGroups = groupsForSite(st.id).filter((g) => match(g.name));
                  return (
                    <div key={st.id}>
                      <Row depth={1} icon="pin" name={st.name} checked={siteCovered} disabled={sel.has('client:' + c.id)} onCheck={() => toggle('site', st.id)}
                        expandable expanded={!!expS[st.id]} onExpand={() => { setExpS((m) => ({ ...m, [st.id]: !m[st.id] })); loadDevs(st.id); }} />
                      {expS[st.id] && siteGroups.map((g) => <Row key={g.id} depth={2} icon="building" name={g.name} hint="department" checked={siteCovered || sel.has('group:' + g.id)} disabled={siteCovered} onCheck={() => toggle('group', g.id)} />)}
                      {expS[st.id] && (devsBy[st.id] || []).filter((d) => match(d.hostname)).map((d) => {
                        const covered = deviceCovered(siteCovered, d);
                        return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={d.status} checked={covered || sel.has('agent:' + d.id)} disabled={covered} onCheck={() => toggle('agent', d.id)} />;
                      })}
                      {expS[st.id] && siteGroups.length === 0 && (devsBy[st.id] || []).length === 0 && <div style={{ paddingLeft: 2 * 18 + 26 }} className="text-[11px] text-faint py-1">no departments or devices</div>}
                    </div>
                  );
                })}
                {expC[c.id] && (sitesBy[c.id] || []).length === 0 && <div style={{ paddingLeft: 18 + 26 }} className="text-[11px] text-faint py-1">no sites</div>}
              </div>
            ))}
            </>}
          </div>
        </div>
      )}
      {restricted && scopes.length === 0
        ? <div className="text-[11px] text-warn flex items-center gap-1.5"><Icon name="alert" size={12} />Select at least one item — with no selection the user has full access.</div>
        : <div className="text-[11px] text-faint">Selecting a company covers all its sites/departments/devices (the union of the selections).</div>}
    </div>
  );
}

function ConfigItem({ label, value, mono }) {
  return <div className="flex items-center justify-between py-2.5 border-b border-line/60 last:border-0"><span className="text-[12.5px] text-muted">{label}</span><span className={cx('text-[12.5px] text-ink font-medium', mono && 'mono')}>{value}</span></div>;
}
function ConfigPanel() {
  const { can } = useAuth();
  const toast = useToast();
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const c = cfgQ.data;
  const [form, setForm] = useState(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!c) return;
    setForm({
      ai_provider: c.ai.provider || 'openai-compatible',
      ai_base_url: c.ai.base_url || '',
      ai_model: c.ai.model || '',
      ai_api_key: '',
      remote_default: c.remote.default || 'native',
      meshcentral_url: c.remote.meshcentral_url || '',
      meshcentral_user: c.remote.meshcentral_user || 'admin',
      meshcentral_token_key: '',
      meshcentral_enabled: c.remote.meshcentral_enabled !== false,
      rustdesk_enabled: !!c.remote.rustdesk_enabled,
    });
  }, [c]);
  const set = (k, v) => setForm((f) => ({ ...f, [k]: v }));
  const save = async () => {
    setSaving(true);
    try {
      await api.updateConfig({
        ai: { provider: form.ai_provider, base_url: form.ai_base_url, model: form.ai_model, api_key: form.ai_api_key },
        remote: { default: form.remote_default, meshcentral_url: form.meshcentral_url, meshcentral_user: form.meshcentral_user, meshcentral_token_key: form.meshcentral_token_key, meshcentral_enabled: form.meshcentral_enabled, rustdesk_enabled: form.rustdesk_enabled },
      });
      set('ai_api_key', ''); set('meshcentral_token_key', '');
      toast.success('Configuration saved', 'Runtime settings updated without exposing secrets.');
    } catch (e) { toast.error('Failed to save configuration', e.message); }
    finally { setSaving(false); }
  };
  if (!c) return <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-2xl" />)}</div>;
  if (!form) return null;
  return (
    <div className="grid md:grid-cols-2 gap-4">
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="sparkle" size={15} /></div><div className="text-[13px] font-semibold text-ink">AI provider</div><span className="ml-auto"><StatusPill status={c.ai.configured ? 'online' : 'warning'} label={c.ai.configured ? 'Configured' : 'Missing key'} size="sm" /></span></div>
        <div className="space-y-3">
          <Field label="Provider"><Input value={form.ai_provider} disabled={!can('admin')} onChange={(e) => set('ai_provider', e.target.value)} placeholder="openai-compatible" /></Field>
          <Field label="Base URL"><Input className="mono" value={form.ai_base_url} disabled={!can('admin')} onChange={(e) => set('ai_base_url', e.target.value)} placeholder="https://api.openai.com/v1" /></Field>
          <Field label="Model"><Input className="mono" value={form.ai_model} disabled={!can('admin')} onChange={(e) => set('ai_model', e.target.value)} placeholder="gpt-4o-mini" /></Field>
          <Field label="API key" hint={c.ai.api_key_set ? 'Already set. Leave blank to keep the current key.' : 'Write-only. It will never be displayed again.'}><Input type="password" value={form.ai_api_key} disabled={!can('admin')} onChange={(e) => set('ai_api_key', e.target.value)} placeholder={c.ai.api_key_set ? '••••••••••••••••' : 'sk-...'} autoComplete="new-password" /></Field>
        </div>
      </Card>
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="remote" size={15} /></div><div className="text-[13px] font-semibold text-ink">Remote access</div></div>
        <div className="space-y-3">
          <Field label="Default provider"><Select value={form.remote_default} disabled={!can('admin')} onChange={(e) => set('remote_default', e.target.value)}>{c.remote.providers.map((p) => <option key={p} value={p}>{p}</option>)}</Select></Field>
          <Field label="MeshCentral" hint="When disabled, the MeshCentral option is hidden from the device Remote menu."><Select value={form.meshcentral_enabled ? 'on' : 'off'} disabled={!can('admin')} onChange={(e) => set('meshcentral_enabled', e.target.value === 'on')}><option value="on">Enabled</option><option value="off">Disabled</option></Select></Field>
          <Field label="MeshCentral URL"><Input className="mono" value={form.meshcentral_url} disabled={!can('admin')} onChange={(e) => set('meshcentral_url', e.target.value)} placeholder="https://mesh.example.com" /></Field>
          <Field label="MeshCentral user"><Input value={form.meshcentral_user} disabled={!can('admin')} onChange={(e) => set('meshcentral_user', e.target.value)} placeholder="admin" /></Field>
          <Field label="MeshCentral token key" hint={c.remote.meshcentral_token_set ? 'Already set. Leave blank to keep it.' : 'Optional write-only secret for login-token embedding.'}><Input type="password" value={form.meshcentral_token_key} disabled={!can('admin')} onChange={(e) => set('meshcentral_token_key', e.target.value)} placeholder={c.remote.meshcentral_token_set ? '••••••••••••••••' : 'hex loginTokenKey'} autoComplete="new-password" /></Field>
          <Field label="RustDesk" hint="Shows a RustDesk option in the device Remote menu; reads the device id on demand and opens rustdesk://<id>. The RustDesk client is installed out-of-band."><Select value={form.rustdesk_enabled ? 'on' : 'off'} disabled={!can('admin')} onChange={(e) => set('rustdesk_enabled', e.target.value === 'on')}><option value="off">Disabled</option><option value="on">Enabled</option></Select></Field>
        </div>
      </Card>
      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={15} /></div><div className="text-[13px] font-semibold text-ink">Server</div></div>
        <ConfigItem label="Version" value={c.server.version} mono /><ConfigItem label="API base URL" value={c.server.base_url} mono />
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Secrets are write-only and encrypted in the database; blank secret fields preserve current values.</div>
          {can('admin') ? <Button variant="primary" icon="check" loading={saving} onClick={save}>Save configuration</Button> : <div className="text-[12px] text-faint">Admin role required to edit.</div>}
        </div>
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
          <div><div className="text-[12px] font-semibold text-muted mb-2">Install command</div>
            <EnrollCommands token={token.token} />
          </div>
          <div className="text-[11.5px] text-faint">Valid for {token.ttl_hours}h · up to {token.uses} enrollments.</div>
        </div>
      )}
    </Card>
  );
}

window.SettingsScreen = SettingsScreen;
