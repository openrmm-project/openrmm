/* screens-rmm2.jsx — Part 2: Scripts, Policies, Clients & Sites, Users & Settings */

/* ============================ SCRIPT LIBRARY ============================== */
function ScriptsScreen() {
  const { can } = useAuth();
  const scriptsQ = useQuery(['scripts'], () => api.listScripts(), { invalidateOn: ['scripts'] });
  const categoriesQ = useQuery(['script-cats'], () => api.listScriptCategories(), { invalidateOn: ['scripts'] });
  const [editing, setEditing] = useState(null);
  const [managing, setManaging] = useState(false);
  const [q, setQ] = useState('');
  const [cat, setCat] = useState('');
  const categories = categoriesQ.data?.categories || [];
  const scripts = (scriptsQ.data?.scripts || [])
    .filter((s) => !cat || s.category === cat)
    .filter((s) => !q || s.name.toLowerCase().includes(q.toLowerCase()) || s.category.toLowerCase().includes(q.toLowerCase()));

  return (
    <Page wide>
      <PageHeader title="Script library" subtitle="Reusable scripts. Running one creates an approval-gated job."
        actions={can('scripts.manage') && <div className="flex items-center gap-2">
          <Button variant="ghost" icon="grid" onClick={() => setManaging(true)}>Categories</Button>
          <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>New script</Button>
        </div>} />
      <Card className="mb-4"><div className="flex items-center gap-2 p-2.5">
        <div className="relative flex-1"><Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search scripts…" className={cx(inputCls, 'pl-9')} /></div>
        <Select value={cat} onChange={(e) => setCat(e.target.value)} className="w-auto min-w-[170px]">
          <option value="">Every category</option>
          {categories.map((c) => <option key={c.name} value={c.name}>{c.name} ({c.count})</option>)}
        </Select>
      </div></Card>
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
      {editing && <ScriptEditor script={editing === 'new' ? null : editing} onClose={() => setEditing(null)} categories={categories} />}
      {managing && <CategoryManager categories={categories} onClose={() => setManaging(false)} />}
    </Page>
  );
}

// Creating, renaming and removing categories. A category is registered in its
// own right, so it can be named here before any script is filed under it —
// naming the filing scheme first is how people actually sort things.
function CategoryManager({ categories, onClose }) {
  const toast = useToast();
  const [editing, setEditing] = useState(null);  // { name, value } being renamed
  const [confirm, setConfirm] = useState(null);  // { kind, from, to, count }
  const [adding, setAdding] = useState(null);    // the name being created
  const [busy, setBusy] = useState(false);

  const create = async () => {
    const name = (adding || '').trim();
    if (!name) { setAdding(null); return; }
    setBusy(true);
    try {
      await api.createScriptCategory(name);
      setAdding(null);
      toast.success('Category created', 'File scripts under it from the script editor.');
    } catch (e) { toast.error('Could not create the category', e.message); }
    finally { setBusy(false); }
  };
  const apply = async () => {
    if (!confirm) return;
    setBusy(true);
    try {
      const { scripts } = confirm.kind === 'clear'
        ? await api.deleteScriptCategory(confirm.from)
        : await api.renameScriptCategory(confirm.from, confirm.to);
      setEditing(null); setConfirm(null);
      toast.success(confirm.kind === 'clear' ? 'Category removed' : confirm.kind === 'merge' ? 'Categories merged' : 'Category renamed',
        scripts + ' script' + (scripts === 1 ? '' : 's') + ' updated');
    } catch (e) { toast.error('Failed', e.message); }
    finally { setBusy(false); }
  };
  const askRename = (c) => {
    const to = (editing?.value || '').trim();
    if (!to || to === c.name) { setEditing(null); return; }
    // Renaming onto a name already in use is a merge, not a rename. The operator
    // has to be told, because the two stop being distinguishable afterwards.
    const merging = categories.some((x) => x.name !== c.name && x.name.toLowerCase() === to.toLowerCase());
    setConfirm({ kind: merging ? 'merge' : 'rename', from: c.name, to, count: c.count });
  };

  return (
    <Modal open onClose={() => { if (!busy) onClose(); }} width="max-w-lg">
      <div className="p-5">
        <div className="flex items-start justify-between gap-3 mb-1">
          <div className="text-[15px] font-semibold text-ink">Script categories</div>
          {!adding && <Button size="sm" variant="ghost" icon="plus" onClick={() => setAdding('')}>Add category</Button>}
        </div>
        <div className="text-[12px] text-muted mb-4">Create one here and file scripts into it from the script editor. Renaming one onto another merges them.</div>
        {adding !== null && (
          <div className="flex items-center gap-2 rounded-xl border border-brand/30 bg-brandsoft px-3 py-2 mb-2">
            <Icon name="grid" size={14} className="text-brandhi shrink-0" />
            <Input value={adding} autoFocus placeholder="Backup" className="h-8 flex-1"
              onChange={(e) => setAdding(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') create(); if (e.key === 'Escape') setAdding(null); }} />
            <Button size="sm" variant="ghost" onClick={() => setAdding(null)} disabled={busy}>Cancel</Button>
            <Button size="sm" variant="primary" loading={busy} disabled={!adding.trim()} onClick={create}>Create</Button>
          </div>
        )}
        {categories.length === 0
          ? <EmptyState icon="grid" title="No categories yet" msg="Add one, or save a script under a new name." />
          : <div className="space-y-1.5 max-h-[380px] overflow-y-auto scroll-area">
            {categories.map((c) => (
              <div key={c.name} className="flex items-center gap-2 rounded-xl border border-line bg-card px-3 py-2">
                {editing?.name === c.name ? (
                  <Input value={editing.value} autoFocus className="h-8 flex-1"
                    onChange={(e) => setEditing({ name: c.name, value: e.target.value })}
                    onKeyDown={(e) => { if (e.key === 'Enter') askRename(c); if (e.key === 'Escape') setEditing(null); }} />
                ) : (
                  <><Icon name="grid" size={14} className="text-muted shrink-0" />
                    <span className="flex-1 text-[13px] text-ink truncate">{c.name}</span>
                    <span className="text-[11.5px] text-faint tabular">{c.count} script{c.count === 1 ? '' : 's'}</span></>
                )}
                {editing?.name === c.name
                  ? <><Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button><Button size="sm" variant="primary" onClick={() => askRename(c)}>Rename</Button></>
                  : <><Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing({ name: c.name, value: c.name })} title={'Rename ' + c.name} />
                    <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirm({ kind: 'clear', from: c.name, to: '', count: c.count })} title={'Remove ' + c.name} /></>}
              </div>
            ))}
          </div>}
        <div className="flex justify-end mt-5"><Button variant="ghost" onClick={onClose} disabled={busy}>Done</Button></div>
      </div>
      <ConfirmDialog open={!!confirm} onClose={() => setConfirm(null)} onConfirm={apply} danger={confirm?.kind !== 'rename'}
        title={confirm?.kind === 'clear' ? 'Remove this category?' : confirm?.kind === 'merge' ? 'Merge the two categories?' : 'Rename this category?'}
        confirmLabel={confirm?.kind === 'clear' ? 'Remove category' : confirm?.kind === 'merge' ? 'Merge' : 'Rename'}
        body={confirm && (confirm.kind === 'clear'
          ? <>The category <b>{confirm.from}</b> is removed{confirm.count > 0 ? <>, and its <b>{confirm.count}</b> script{confirm.count === 1 ? '' : 's'} become uncategorized</> : ' (nothing is filed under it)'}. The scripts themselves are kept.</>
          : confirm.kind === 'merge'
            ? <><b>{confirm.from}</b> and <b>{confirm.to}</b> become one. The <b>{confirm.count}</b> script{confirm.count === 1 ? '' : 's'} under <b>{confirm.from}</b> move across, and the two stop being separable.</>
            : <>Rename <b>{confirm.from}</b> to <b>{confirm.to}</b>, across <b>{confirm.count}</b> script{confirm.count === 1 ? '' : 's'}.</>)} />
    </Modal>
  );
}

function ScriptEditor({ script, onClose, categories = [] }) {
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
  const readOnly = !can('scripts.manage');

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
        {can('jobs.create') && script && <Button variant="secondary" icon="play" onClick={() => setRunOpen(true)}>Run on device</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} disabled={readOnly} placeholder="Disk cleanup" /></Field>
          {/* Pick an existing category or name a new one. A plain text field was
              how "Networking" and "Netwoking" both came to exist. */}
          <Field label="Category" hint={readOnly ? undefined : 'Pick one in use, or type a name to create it.'}>
            {/* The label is the bare name: `creatable` decides whether to offer
                "Create X" by comparing what was typed against these labels, so a
                decorated label would offer to create a category that exists. */}
            <Combobox value={category} onChange={setCategory} disabled={readOnly} creatable placeholder="Uncategorized"
              options={categories.map((c) => ({ value: c.name, label: c.name }))} />
          </Field>
        </div>
        <Field label="Description"><Input value={desc} onChange={(e) => setDesc(e.target.value)} disabled={readOnly} /></Field>
        <Field label="Shell" className="w-44"><Select value={shell} onChange={(e) => setShell(e.target.value)} disabled={readOnly}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>
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
  // The API has always accepted a description; the form never sent one, so every
  // policy read "No description" for good — there was no way to set it later.
  const [newDesc, setNewDesc] = useState('');
  const policies = policiesQ.data?.policies || [];

  const resetCreate = () => { setCreating(false); setNewName(''); setNewDesc(''); };
  const create = async () => {
    try {
      const p = await api.createPolicy({ name: newName.trim(), description: newDesc.trim() });
      resetCreate(); toast.success('Policy created'); setDetail(p);
    } catch (e) { toast.error('Could not create policy', e.message); }
  };
  // Any non-GET already invalidates every query, so the card list refreshes
  // itself; what it cannot do is update the drawer's own copy of the policy, so
  // a rename has to be handed back or the header keeps the old name.
  const onPolicySaved = (p) => setDetail(p);

  return (
    <Page wide>
      <PageHeader title="Policies" subtitle="Bundle checks and tasks, then assign them to clients, sites, or agents — scope is inherited."
        actions={can('policies.manage') && <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>New policy</Button>} />
      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {policies.map((p) => <PolicyCard key={p.id} policy={p} onClick={() => setDetail(p)} />)}
      </div>
      {detail && <PolicyDetail policy={detail} onClose={() => setDetail(null)} onSaved={onPolicySaved} />}
      <Modal open={creating} onClose={resetCreate} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-4">New policy</div>
          <div className="space-y-3">
            <Field label="Name"><Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Servers — baseline" autoFocus /></Field>
            <Field label="Description" hint="Optional. What this policy is for — shown on the card and in the drawer.">
              <Input value={newDesc} onChange={(e) => setNewDesc(e.target.value)} placeholder="Baseline checks for every production server" />
            </Field>
          </div>
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={resetCreate}>Cancel</Button><Button variant="primary" onClick={create} disabled={!newName.trim()}>Create</Button></div>
        </div>
      </Modal>
    </Page>
  );
}

function PolicyCard({ policy, onClick }) {
  const { can } = useAuth();
  const checksQ = useQuery([policy.id, 'pc'], () => can('checks.view') ? api.listChecks({ policy_id: policy.id }) : Promise.resolve({ checks: [] }), { invalidateOn: ['checks'] });
  const tasksQ = useQuery([policy.id, 'pt'], () => can('tasks.view') ? api.listTasks({ policy_id: policy.id }) : Promise.resolve({ tasks: [] }), { invalidateOn: ['tasks'] });
  const asgQ = useQuery([policy.id, 'pa'], () => api.listAssignments(policy.id), { invalidateOn: ['policies'] });
  return (
    <button onClick={onClick} className="text-left rounded-2xl border border-line bg-card hover:border-line2 hover:bg-elevated/40 transition p-4 group">
      <div className="flex items-center gap-2.5 mb-2"><div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="policy" size={17} /></div><div><div className="text-[14px] font-semibold text-ink group-hover:text-brandhi transition-colors">{policy.name}</div>{awaitingReview(policy) && <div className="mt-1"><ProposalBadge /></div>}</div></div>
      <div className="text-[12px] text-muted line-clamp-2 leading-snug min-h-[32px]">{policy.description || 'No description'}</div>
      <div className="flex items-center gap-3 mt-3 text-[11.5px] text-faint">
        <span className="flex items-center gap-1"><Icon name="check" size={12} />{(checksQ.data?.checks || []).length} checks</span>
        <span className="flex items-center gap-1"><Icon name="tasks" size={12} />{(tasksQ.data?.tasks || []).length} tasks</span>
        <span className="flex items-center gap-1"><Icon name="link" size={12} />{(asgQ.data?.assignments || []).length} assigned</span>
      </div>
    </button>
  );
}

function PolicyDetail({ policy, onClose, onSaved }) {
  const { can } = useAuth();
  const toast = useToast();
  const [tab, setTab] = useState('checks');
  const [editing, setEditing] = useState(null); // { name, description } while renaming
  const [savingEdit, setSavingEdit] = useState(false);
  const checksQ = useQuery([policy.id, 'pcd'], () => can('checks.view') ? api.listChecks({ policy_id: policy.id }) : Promise.resolve({ checks: [] }), { invalidateOn: ['checks'] });
  const tasksQ = useQuery([policy.id, 'ptd'], () => can('tasks.view') ? api.listTasks({ policy_id: policy.id }) : Promise.resolve({ tasks: [] }), { invalidateOn: ['tasks'] });
  const asgQ = useQuery([policy.id, 'pad'], () => api.listAssignments(policy.id), { invalidateOn: ['policies'] });
  const clientsQ = useQuery(['cl-pol'], () => can('clients.view') ? api.listClients() : Promise.resolve({ clients: [] }), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['st-pol'], () => can('clients.view') ? api.listSites() : Promise.resolve({ sites: [] }), { invalidateOn: ['org'] });
  const devMap = useDeviceMap();
  const [addScope, setAddScope] = useState('');
  // Editing a policy's checks and tasks in place. The editors come from
  // screens-rmm.jsx via window (each source file is its own block scope), so the
  // forms here are the same ones the Checks and Scheduled tasks screens use
  // rather than a second copy that would drift.
  const scriptsQ = useQuery(['scripts-pol'], () => can('scripts.view') ? api.listScripts() : Promise.resolve({ scripts: [] }), { invalidateOn: ['scripts'] });
  const policiesQ = useQuery(['pols-pol'], () => api.listPolicies(), { invalidateOn: ['policies'] });
  const [editCheck, setEditCheck] = useState(null); // check object, or 'new'
  const [editTask, setEditTask] = useState(null);   // task object, or 'new'
  const [confirmToggleTask, setConfirmToggleTask] = useState(null);
  const canChecks = can('checks.manage'), canTasks = can('tasks.manage');

  // Task updates go through PUT, so the server revalidates the whole schedule;
  // resend the task unchanged apart from the flag being flipped.
  const toggleTask = async (t) => {
    try {
      await api.updateTask(t.id, { ...t, enabled: !t.enabled });
      toast.success(t.enabled ? 'Task disabled' : 'Task enabled', t.name);
    } catch (e) { toast.error('Failed to update task', e.message); }
    finally { setConfirmToggleTask(null); }
  };

  const checks = checksQ.data?.checks || [], tasks = tasksQ.data?.tasks || [], asg = asgQ.data?.assignments || [];
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const scopeName = (a) => a.scope_type === 'agent' ? devMap[a.scope_id]?.hostname : a.scope_type === 'site' ? sites.find((s) => s.id === a.scope_id)?.name : clients.find((c) => c.id === a.scope_id)?.name;

  const addAssignment = async () => { if (!addScope) return; const [type, id] = addScope.split(':'); await api.createAssignment(policy.id, { scope_type: type, scope_id: id }); setAddScope(''); toast.success('Assigned', 'Checks & tasks now apply to that scope.'); };
  const removeAssignment = async (aid) => { await api.deleteAssignment(aid); toast.info('Assignment removed'); };
  const [confirmDel, setConfirmDel] = useState(false);
  const del = async () => { try { await api.deletePolicy(policy.id); toast.info('Policy deleted'); onClose(); } catch (e) { toast.error('Failed', e.message); } };
  const saveEdit = async () => {
    if (!editing?.name.trim()) return;
    setSavingEdit(true);
    try {
      const updated = await api.updatePolicy(policy.id, { name: editing.name.trim(), description: editing.description.trim() });
      setEditing(null);
      onSaved?.(updated);
      toast.success('Policy updated', 'Its checks, tasks and assignments are unchanged.');
    } catch (e) { toast.error('Could not update policy', e.message); }
    finally { setSavingEdit(false); }
  };

  return (
    <Drawer open onClose={onClose} width="max-w-[720px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-10 w-10 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="policy" size={19} /></div>
        <div className="flex-1 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{policy.name}</h2><div className="text-[12px] text-muted truncate">{policy.description || 'No description'}</div></div>
        {can('policies.manage') && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing({ name: policy.name, description: policy.description || '' })}>Edit</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'checks', label: 'Checks', count: checks.length }, { value: 'tasks', label: 'Tasks', count: tasks.length }, { value: 'assign', label: 'Assignments', count: asg.length }]} />
      <div className="flex-1 overflow-y-auto scroll-area p-5">
        {tab === 'checks' && (
          <div className="space-y-3">
            {canChecks && <div className="flex justify-end"><Button size="sm" variant="ghost" icon="plus" onClick={() => setEditCheck('new')}>Add check</Button></div>}
            {checks.length ? <div className="space-y-2">{checks.map((c) => { const t = CHECK_TYPES[c.type]; return (
              <div key={c.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3">
                <Icon name={t.icon} size={15} className="text-muted" />
                <div className="flex-1 min-w-0"><div className="text-[13px] text-ink font-medium truncate">{c.name}</div><div className="text-[11px] text-faint">{t.label} · {c.warn_threshold != null ? `warn ${c.warn_threshold}/err ${c.err_threshold}` : ''}</div></div>
                <StatusPill status={c.latest?.status || 'passing'} size="sm" />
                {canChecks && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditCheck(c)}>Edit</Button>}
              </div>); })}</div>
              : <EmptyState icon="check" title="No checks in this policy" msg={canChecks ? 'Add one to run it on every agent this policy covers.' : undefined} />}
          </div>
        )}
        {tab === 'tasks' && (
          <div className="space-y-3">
            {canTasks && <div className="flex justify-end"><Button size="sm" variant="ghost" icon="plus" onClick={() => setEditTask('new')}>Add task</Button></div>}
            {tasks.length ? <div className="space-y-2">{tasks.map((t) => (
              <div key={t.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3">
                <Icon name="tasks" size={15} className="text-muted" />
                <div className="flex-1 min-w-0"><div className="text-[13px] text-ink font-medium truncate">{t.name}</div><div className="text-[11px] text-faint">{t.schedule_kind === 'daily' ? 'Daily at ' + minuteToHHMM(t.run_at_minute) : 'Every ' + Math.round(t.interval_sec / 60) + 'm'}</div></div>
                <Toggle checked={t.enabled} onChange={() => setConfirmToggleTask(t)} disabled={!canTasks} />
                {canTasks && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditTask(t)}>Edit</Button>}
              </div>))}</div>
              : <EmptyState icon="tasks" title="No tasks in this policy" msg={canTasks ? 'Add one to run a script on every agent this policy covers.' : undefined} />}
          </div>
        )}
        {tab === 'assign' && (
          <div className="space-y-4">
            <div className="flex items-start gap-2.5 rounded-xl border border-brand/25 bg-brandsoft px-3.5 py-3"><Icon name="info" size={16} className="text-brandhi mt-0.5 shrink-0" /><div className="text-[12.5px] text-ink leading-relaxed">Assigning to a <b>client</b> or <b>site</b> pushes every check and task in this policy to <b>all agents in that scope</b>. Agent-level assignments target a single device.</div></div>
            {can('policies.manage') && (
              <div className="flex items-center gap-2">
                <Combobox value={addScope} onChange={setAddScope} className="flex-1" placeholder="Add an assignment…"
                  options={[
                    ...clients.map((c) => ({ value: 'client:' + c.id, label: c.name + ' (all sites)', group: 'Clients' })),
                    ...sites.map((s) => ({ value: 'site:' + s.id, label: s.name, group: 'Sites' })),
                    ...Object.values(devMap).map((d) => ({ value: 'agent:' + d.id, label: d.hostname, group: 'Agents' })),
                  ]} />
                <Button variant="primary" icon="plus" onClick={addAssignment} disabled={!addScope}>Assign</Button>
              </div>
            )}
            <div className="space-y-2">
              {asg.map((a) => (
                <div key={a.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-2.5">
                  <span className={cx('inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase', a.scope_type === 'agent' ? 'text-run bg-runsoft border-run/25' : a.scope_type === 'site' ? 'text-warn bg-warnsoft border-warn/25' : 'text-brandhi bg-brandsoft border-brand/25')}>{a.scope_type}</span>
                  <span className="flex-1 text-[13px] text-ink">{scopeName(a) || a.scope_id}</span>
                  {can('policies.manage') && <button onClick={() => removeAssignment(a.id)} className="text-faint hover:text-bad p-1"><Icon name="trash" size={15} /></button>}
                </div>
              ))}
              {asg.length === 0 && <div className="text-center text-[12.5px] text-faint py-4">Not assigned anywhere yet.</div>}
            </div>
          </div>
        )}
      </div>
      {can('policies.manage') && (
        <div className="px-5 py-3.5 border-t border-line flex justify-end">
          <Button variant="danger" icon="trash" onClick={() => setConfirmDel(true)}>Delete policy</Button>
        </div>
      )}
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={del} danger title="Delete policy?" body={<>This removes <b>{policy.name}</b> with its checks, tasks and assignments. This cannot be undone.</>} confirmLabel="Delete policy" />
      {editCheck && <CheckEditor check={editCheck === 'new' ? null : editCheck} onClose={() => setEditCheck(null)}
        devMap={devMap} policies={policiesQ.data?.policies || []} defaultPolicyId={policy.id} />}
      {editTask && <TaskEditor task={editTask === 'new' ? null : editTask} onClose={() => setEditTask(null)}
        devMap={devMap} policies={policiesQ.data?.policies || []} scripts={scriptsQ.data?.scripts || []} defaultPolicyId={policy.id} />}
      <ConfirmDialog open={!!confirmToggleTask} onClose={() => setConfirmToggleTask(null)} onConfirm={() => toggleTask(confirmToggleTask)}
        title={confirmToggleTask?.enabled ? 'Disable task?' : 'Enable task?'}
        confirmLabel={confirmToggleTask?.enabled ? 'Disable' : 'Enable'} danger={!!confirmToggleTask?.enabled}
        body={confirmToggleTask && (confirmToggleTask.enabled
          ? <>Disable <b>{confirmToggleTask.name}</b>? It stops running on every agent this policy covers until it is enabled again.</>
          : <>Enable <b>{confirmToggleTask.name}</b>? It resumes on its schedule for every agent this policy covers.</>)} />
      <Modal open={!!editing} onClose={() => { if (!savingEdit) setEditing(null); }} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">Edit policy</div>
          <div className="text-[12px] text-muted mb-4">Renaming changes only the label. The checks, tasks and assignments stay exactly as they are.</div>
          <div className="space-y-3">
            <Field label="Name">
              <Input value={editing?.name || ''} autoFocus onChange={(e) => setEditing((s) => ({ ...s, name: e.target.value }))}
                onKeyDown={(e) => { if (e.key === 'Enter') saveEdit(); }} />
            </Field>
            <Field label="Description" hint="Optional. Shown on the policy card and here.">
              <Input value={editing?.description || ''} placeholder="Baseline checks for every production server"
                onChange={(e) => setEditing((s) => ({ ...s, description: e.target.value }))}
                onKeyDown={(e) => { if (e.key === 'Enter') saveEdit(); }} />
            </Field>
          </div>
          <div className="flex justify-end gap-2 mt-5">
            <Button variant="ghost" onClick={() => setEditing(null)} disabled={savingEdit}>Cancel</Button>
            <Button variant="primary" loading={savingEdit} disabled={!editing?.name.trim()} onClick={saveEdit}>Save changes</Button>
          </div>
        </div>
      </Modal>
    </Drawer>
  );
}

/* ============================ CLIENTS & SITES ============================= */
function ClientsScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const clientsQ = useQuery(['clients'], () => api.listClients(), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['sites'], () => api.listSites(), { invalidateOn: ['org'] });
  // Poll so newly-enrolled devices (a server-side event, not a UI mutation) show
  // up without a manual page refresh.
  const devicesQ = useQuery(['cl-dev'], () => can('devices.view') ? api.listDevices() : Promise.resolve({ devices: [] }), { invalidateOn: ['devices'], poll: can('devices.view') ? 8000 : 0 });
  const groupsQ = useQuery(['groups'], () => api.listGroups(), { invalidateOn: ['groups'], poll: 12000 });
  const groups = groupsQ.data?.groups || [];
  const [sel, setSel] = useState(null); // site id
  const [selDept, setSelDept] = useState(null); // department id within the selected site
  const [creatingClient, setCreatingClient] = useState(false);
  const [name, setName] = useState('');
  const [q, setQ] = useState('');

  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [], devices = devicesQ.data?.devices || [];
  const siteCount = (siteId) => devices.filter((d) => d.site_id === siteId).length;
  const clientCount = (cid) => devices.filter((d) => sites.find((s) => s.id === d.site_id && s.client_id === cid)).length;
  const selSite = sites.find((s) => s.id === sel);

  const ql = q.trim().toLowerCase();
  const devMatch = (d) => !ql || (d.hostname || '').toLowerCase().includes(ql) || (d.description || '').toLowerCase().includes(ql);
  const siteMatch = (s) => !ql || s.name.toLowerCase().includes(ql) || devices.some((d) => d.site_id === s.id && devMatch(d));
  const sitesFor = (c) => {
    const all = sites.filter((s) => s.client_id === c.id);
    return !ql || c.name.toLowerCase().includes(ql) ? all : all.filter(siteMatch);
  };
  const visibleClients = clients.filter((c) => !ql || c.name.toLowerCase().includes(ql) || sitesFor(c).length > 0);
  // Devices shown in the selected site: all of them, unless the query only matched
  // a device (then narrow to the matching ones); a client/site name hit shows all.
  const selClient = selSite && clients.find((c) => c.id === selSite.client_id);
  const nameHit = !!ql && ((selSite && selSite.name.toLowerCase().includes(ql)) || (selClient && selClient.name.toLowerCase().includes(ql)));
  const selDevices = devices.filter((d) => d.site_id === sel && (!ql || nameHit || devMatch(d)));
  // When searching by device, auto-focus the match: keep the current site if it
  // already has a match, otherwise jump to the first matching device's site/dept.
  useEffect(() => {
    if (!ql) return;
    if (sel && devices.some((d) => d.site_id === sel && devMatch(d))) return;
    const hit = devices.find((d) => d.site_id && devMatch(d));
    if (hit) { setSel(hit.site_id); setSelDept(hit.group_id || null); }
  }, [ql]);

  const addClient = async () => { await api.createClient(name); setName(''); setCreatingClient(false); toast.success('Client created'); };

  return (
      <Page wide>
        <PageHeader title="Clients & Sites" subtitle="Organize your fleet into clients and sites. Enroll agents per site."
        actions={can('clients.manage') && <Button variant="primary" icon="plus" onClick={() => setCreatingClient(true)}>New client</Button>} />
      <div className="grid lg:grid-cols-[360px_1fr] gap-5">
        <div className="space-y-3">
          <div className="relative">
            <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search client, site or device…" className="pl-9 h-9" />
          </div>
          {visibleClients.map((c) => <ClientNode key={c.id} client={c} sites={sitesFor(c)} groups={groups} devices={devices} sel={sel} setSel={setSel} selDept={selDept} setSelDept={setSelDept} siteCount={siteCount} clientCount={clientCount} />)}
          {visibleClients.length === 0 && <Card><EmptyState icon="building" title={ql ? 'No matches' : 'No clients'} /></Card>}
        </div>
        <div>{selSite ? <SiteDetail site={selSite} client={selClient} devices={selDevices} clients={clients} sites={sites} query={ql} focusDept={selDept} /> : <Card className="h-full"><EmptyState icon="pin" title="Select a site" msg="Pick a site to see its devices and enrollment command." /></Card>}</div>
      </div>
      <Modal open={creatingClient} onClose={() => setCreatingClient(false)} width="max-w-md"><div className="p-5"><div className="text-[15px] font-semibold text-ink mb-4">New client</div><Field label="Client name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Northwind Trading" autoFocus /></Field><div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setCreatingClient(false)}>Cancel</Button><Button variant="primary" onClick={addClient} disabled={!name}>Create</Button></div></div></Modal>
    </Page>
  );
}

function ClientNode({ client, sites, groups, devices, sel, setSel, selDept, setSelDept, siteCount, clientCount }) {
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
        {can('clients.manage') && !renaming && (
          <Dropdown align="right" width="w-48" trigger={<button className="text-faint hover:text-ink p-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={16} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(client.name); setRenaming(true); }}>Rename client</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDel(true)}>Delete client</MenuItem>
          </Dropdown>
        )}
      </div>
      {open && (
        <div className="border-t border-line">
          {sites.map((s) => {
            const siteDepts = (groups || []).filter((g) => g.site_id === s.id);
            return (
              <div key={s.id} className="border-b border-line/60 last:border-0">
                <button onClick={() => { setSel(s.id); setSelDept && setSelDept(null); }} className={cx('w-full flex items-center gap-2.5 pl-10 pr-3.5 py-2.5 transition text-left', sel === s.id && !selDept ? 'bg-brandsoft' : 'hover:bg-elevated/40')}>
                  <Icon name="pin" size={13} className={sel === s.id && !selDept ? 'text-brandhi' : 'text-faint'} />
                  <span className={cx('flex-1 text-[12.5px]', sel === s.id && !selDept ? 'text-ink font-medium' : 'text-muted')}>{s.name}</span>
                  <span className="text-[11px] text-faint tabular">{siteCount(s.id)}</span>
                </button>
                {siteDepts.map((g) => {
                  const active = sel === s.id && selDept === g.id;
                  return (
                    <button key={g.id} onClick={() => { setSel(s.id); setSelDept && setSelDept(g.id); }} className={cx('w-full flex items-center gap-2 pl-[3.4rem] pr-3.5 py-1.5 transition text-left', active ? 'bg-brandsoft' : 'hover:bg-elevated/30')}>
                      <Icon name="building" size={11} className={active ? 'text-brandhi' : 'text-faint/70'} />
                      <span className={cx('flex-1 text-[11.5px] truncate', active ? 'text-ink font-medium' : 'text-faint')}>{g.name}</span>
                      <span className="text-[10.5px] text-faint/70 tabular">{(devices || []).filter((d) => d.group_id === g.id).length}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
          {can('clients.manage') && (adding ? (
            <div className="flex items-center gap-2 pl-10 pr-3 py-2"><Input value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addSite(); if (e.key === 'Escape') { setAdding(false); setName(''); } }} onBlur={() => { setAdding(false); setName(''); }} placeholder="Site name" className="h-8" autoFocus /><Button size="sm" variant="primary" onMouseDown={(e) => e.preventDefault()} onClick={addSite} disabled={!name}>Add</Button></div>
          ) : <button onClick={() => setAdding(true)} className="w-full flex items-center gap-2 pl-10 pr-3.5 py-2 text-[12px] text-faint hover:text-muted transition"><Icon name="plus" size={13} />Add site</button>)}
        </div>
      )}
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={delClient} danger title="Delete client?" body={<>This removes <b>{client.name}</b> with all its sites and departments. Its devices become unassigned. This cannot be undone.</>} confirmLabel="Delete client" />
    </Card>
  );
}

// Cross-platform install commands for an enrollment token. Linux & macOS share
// the bash install.sh (it auto-detects via uname); Windows uses install.ps1.
function shellQuote(value) {
  return "'" + String(value).replace(/'/g, "'\"'\"'") + "'";
}
function EnrollCommands({ token, site }) {
  const [os, setOs] = useState('linux');
  const configQ = useQuery(['config'], () => api.getConfig(), {});
  // Enrollment, updates, and the agent-side relay live on the agents origin in
  // split ingress, not on the console origin serving this screen.
  if (configQ.loading) return <div className="text-[12px] text-faint py-2">Loading the agents install URL...</div>;
  const base = configQ.data?.server?.base_url;
  if (!base) return <div className="text-[12px] text-bad py-2">Could not load the agents install URL. Refresh the page before copying an enrollment command.</div>;
  const agentsBase = base.replace(/\/$/, '');
  const siteArg = site ? ` \\\n  --site ${shellQuote(site.name)}` : '';
  const nixCmd = `curl -fsSL ${agentsBase}/install.sh | sudo bash -s -- \\\n  --server ${agentsBase} \\\n  --token ${token}${siteArg}`;
  const winCmd = `# Run PowerShell as Administrator\n& ([scriptblock]::Create((iwr -useb ${agentsBase}/install.ps1))) -Server ${agentsBase} -Token ${token}`;
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
      {/* Wrapped: the Windows one-liner carries the server URL and the token on a
          single line and ran off the right edge, so it had to be scrolled to be
          read — and this is a command people run as Administrator. */}
      <CodeBlock code={cmd} shell={isWin ? 'powershell' : 'bash'} label="install agent" showCopy={false} maxHeight={180} wrap />
      {isWin && <div className="text-[11.5px] text-faint mt-2">Run in an <span className="text-ink font-medium">elevated PowerShell</span> (Run as Administrator).</div>}
      {os === 'macos' && <div className="text-[11.5px] text-faint mt-2">Same installer as Linux — it auto-detects macOS (Intel &amp; Apple Silicon).</div>}
    </div>
  );
}

function SiteDetail({ site, client, devices, clients, sites, query, focusDept }) {
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
  const [renamingDept, setRenamingDept] = useState(null); // { id, value } while renaming
  const [enrollDept, setEnrollDept] = useState('');       // department the token binds to, '' = the site
  const [moveDevice, setMoveDevice] = useState(null);
  const [confirmDelDevice, setConfirmDelDevice] = useState(null);
  const [selected, setSelected] = useState(null); // device id -> open the full detail drawer (same as the Devices tab)
  useEffect(() => { setRenaming(false); setRename(site.name); setCreatingDept(false); setNewDept(''); setRenamingDept(null); setEnrollDept(''); }, [site.id]);
  // Clear a generated enrollment command when the selected site/department changes.
  useEffect(() => { setEnrollToken(null); }, [site.id, focusDept]);

  const saveRename = async () => { const n = rename.trim(); if (!n || n === site.name) { setRenaming(false); return; } try { await api.updateSite(site.id, n); window.dataBus.emit('org'); toast.success('Site renamed'); setRenaming(false); } catch (e) { toast.error('Failed', e.message); } };
  const addDept = async () => { if (!newDept.trim()) return; try { await api.createGroup({ name: newDept.trim(), site_id: site.id }); setNewDept(''); setCreatingDept(false); window.dataBus.emit('groups'); toast.success('Department created'); } catch (e) { toast.error('Failed', e.message); } };
  const renameDept = async () => {
    const d = renamingDept; if (!d) return;
    const name = d.value.trim();
    if (!name || name === d.name) { setRenamingDept(null); return; }
    try { await api.renameGroup(d.id, name); setRenamingDept(null); window.dataBus.emit('groups'); toast.success('Department renamed'); }
    catch (e) { toast.error('Failed', e.message); }
  };
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
  const genEnroll = async () => {
    setBusy(true);
    const dept = depts.find((g) => g.id === enrollDept);
    try {
      const t = await api.createEnrollToken({
        description: 'Enroll for ' + site.name + (dept ? ' / ' + dept.name : ''),
        site_id: site.id, group_id: enrollDept || undefined, uses: 1, ttl_hours: 24,
      });
      setEnrollToken(t.token);
      toast.success('Enrollment token created', dept ? 'Agents land in ' + dept.name : 'Agents land in ' + site.name);
    } catch (e) { toast.error('Failed', e.message); }
    finally { setBusy(false); }
  };

  // Group the site's devices by department; ungrouped devices fall under "No department".
  const deptIds = new Set(depts.map((g) => g.id));
  const unassigned = devices.filter((d) => !d.group_id || !deptIds.has(d.group_id));
  const renderDevice = (d) => (
    <div key={d.id} onClick={() => setSelected(d.id)} title="Open device options"
      className="grid grid-cols-[auto_minmax(0,1fr)_minmax(0,1fr)_auto_auto] items-center gap-2.5 rounded-lg border border-line bg-card px-3 py-2 cursor-pointer hover:border-line2 hover:bg-elevated/40 transition">
      <span className={cx('h-2 w-2 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
      <div className="min-w-0 flex items-center gap-2"><OsIcon os={d.os} size={14} className="text-muted shrink-0" /><span className="text-[12.5px] text-ink truncate">{d.hostname}</span></div>
      <div className="text-[12px] text-muted truncate">{d.description || <span className="text-faint">No description</span>}</div>
      <span className="text-[11px] text-faint whitespace-nowrap">{timeAgo(d.last_seen)}</span>
      {(can('clients.manage') || can('devices.delete')) && (
        <span onClick={(e) => e.stopPropagation()}>
          <Dropdown align="right" width="w-44" trigger={<button className="text-faint hover:text-ink p-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={15} fill /></button>}>
            {can('clients.manage') && <MenuItem icon="pin" onClick={() => setMoveDevice(d)}>Move</MenuItem>}
            {can('clients.manage') && can('devices.delete') && <div className="my-1 border-t border-line" />}
            {can('devices.delete') && <MenuItem icon="trash" onClick={() => setConfirmDelDevice(d)}>Delete device</MenuItem>}
          </Dropdown>
        </span>
      )}
    </div>
  );
  return (
    <>
    <Card className="p-5">
      <div className="flex items-start gap-3 mb-4">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="pin" size={19} /></div>
        <div className="flex-1 min-w-0">
          {renaming
            ? <div className="flex items-center gap-2"><Input value={rename} onChange={(e) => setRename(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') saveRename(); if (e.key === 'Escape') setRenaming(false); }} className="h-8" autoFocus /><Button size="sm" variant="primary" onClick={saveRename}>Save</Button><Button size="sm" variant="ghost" onClick={() => setRenaming(false)}>Cancel</Button></div>
            : <h2 className="text-[16px] font-semibold text-ink truncate">{site.name}</h2>}
          <div className="text-[12px] text-muted">{client?.name} · {devices.length} devices</div>
        </div>
        {can('clients.manage') && !renaming && (
          <Dropdown align="right" width="w-52" trigger={<button className="text-faint hover:text-ink p-1.5 -mr-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={18} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(site.name); setRenaming(true); }}>Rename site</MenuItem>
            <MenuItem icon="building" onClick={() => setCreatingDept(true)}>Create department</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDelSite(true)}>Delete site</MenuItem>
          </Dropdown>
        )}
      </div>
      <div className="mb-5">
        <div className="flex items-center justify-between mb-2">
          <div className="text-[12px] font-semibold text-muted">Devices <span className="text-faint font-normal">· {devices.length}</span></div>
          {can('clients.manage') && !creatingDept && <button onClick={() => setCreatingDept(true)} className="text-[11.5px] text-faint hover:text-ink flex items-center gap-1"><Icon name="plus" size={12} />Department</button>}
        </div>
        {creatingDept && (
          <div className="flex items-center gap-2 mb-2.5">
            <Input value={newDept} onChange={(e) => setNewDept(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addDept(); if (e.key === 'Escape') { setCreatingDept(false); setNewDept(''); } }} placeholder="Department name (e.g. Finance)" className="h-8" autoFocus />
            <Button size="sm" variant="primary" onClick={addDept} disabled={!newDept.trim()}>Add</Button>
            <Button size="sm" variant="ghost" onClick={() => { setCreatingDept(false); setNewDept(''); }}>Cancel</Button>
          </div>
        )}
        {(() => {
          const filtering = !!query;
          // Focus a single department when one is clicked in the tree; otherwise
          // show all. When filtering by device, hide departments with no match.
          const groupsToShow = focusDept ? depts.filter((g) => g.id === focusDept) : depts;
          const visibleDepts = groupsToShow.filter((g) => focusDept || !filtering || devices.some((d) => d.group_id === g.id));
          const showUnassigned = !focusDept && (!filtering || unassigned.length > 0);
          if (!showUnassigned && visibleDepts.length === 0) {
            return <div className="text-[12.5px] text-faint py-3 text-center">{filtering || focusDept ? 'No devices here.' : 'No devices yet.'}</div>;
          }
          return (
            <div className="space-y-3.5">
              {showUnassigned && (
                <div className="space-y-1.5">
                  <div className="text-[11px] uppercase tracking-wide text-faint font-semibold px-0.5">No department <span className="font-normal text-faint/70">· {unassigned.length}</span></div>
                  {unassigned.length ? unassigned.map(renderDevice) : <div className="text-[12px] text-faint px-0.5 py-1">—</div>}
                </div>
              )}
              {visibleDepts.map((g) => {
                const items = devices.filter((d) => d.group_id === g.id);
                return (
                  <div key={g.id} className="space-y-1.5">
                    <div className="flex items-center gap-1.5 px-0.5">
                      <Icon name="building" size={12} className="text-faint" />
                      {renamingDept?.id === g.id ? (
                        <Input value={renamingDept.value} autoFocus className="h-7 flex-1 max-w-[260px]"
                          onChange={(e) => setRenamingDept({ ...renamingDept, value: e.target.value })}
                          onKeyDown={(e) => { if (e.key === 'Enter') renameDept(); if (e.key === 'Escape') setRenamingDept(null); }}
                          onBlur={renameDept} />
                      ) : (
                        <span className="text-[11px] uppercase tracking-wide text-faint font-semibold flex-1">{g.name} <span className="font-normal text-faint/70">· {items.length}</span></span>
                      )}
                      {/* Renaming used to mean deleting and rebuilding, which unfiles
                          every device under it. Only the label moves now. */}
                      {can('clients.manage') && renamingDept?.id !== g.id && <button onClick={() => setRenamingDept({ id: g.id, name: g.name, value: g.name })} title={'Rename ' + g.name} className="text-faint hover:text-ink p-0.5"><Icon name="edit" size={12} /></button>}
                      {can('clients.manage') && <button onClick={() => setConfirmDelDept(g)} title="Delete department" className="text-faint hover:text-bad p-0.5"><Icon name="trash" size={12} /></button>}
                    </div>
                    {items.length ? items.map(renderDevice) : <div className="text-[12px] text-faint px-0.5 py-1">No devices in this department.</div>}
                  </div>
                );
              })}
            </div>
          );
        })()}
      </div>
      {can('enrollment.manage') && (
        <div>
          <div className="flex items-center justify-between mb-2">
            <div className="text-[12px] font-semibold text-muted">Enroll a new agent</div>
            {enrollToken && <button onClick={() => setEnrollToken(null)} className="text-[11.5px] text-faint hover:text-ink inline-flex items-center gap-1"><Icon name="close" size={12} />Hide</button>}
          </div>
          {enrollToken ? <EnrollCommands token={enrollToken} site={site} /> : (
            <div className="flex items-center gap-2 flex-wrap">
              {/* The command does not describe the placement — the token does — so
                  the department has to be chosen before the token is minted. */}
              {depts.length > 0 && (
                <Select value={enrollDept} onChange={(e) => setEnrollDept(e.target.value)} className="h-9 w-auto min-w-[190px]">
                  <option value="">Land in {site.name}</option>
                  {depts.map((g) => <option key={g.id} value={g.id}>Land in {g.name}</option>)}
                </Select>
              )}
              <Button variant="secondary" icon="key" loading={busy} onClick={genEnroll}>Generate enrollment command</Button>
            </div>
          )}
        </div>
      )}
      <ConfirmDialog open={confirmDelSite} onClose={() => setConfirmDelSite(false)} onConfirm={delSite} danger title="Delete site?" body={<>This removes <b>{site.name}</b> and its departments. Its devices become unassigned. This cannot be undone.</>} confirmLabel="Delete site" />
      <ConfirmDialog open={!!confirmDelDept} onClose={() => setConfirmDelDept(null)} onConfirm={delDept} danger title="Delete department?" body={<>This removes the department <b>{confirmDelDept?.name}</b>. Its devices stay, but their department is cleared.</>} confirmLabel="Delete department" />
      <DeviceMoveModal open={!!moveDevice} onClose={() => setMoveDevice(null)} device={moveDevice} clients={clients} sites={sites} groups={groupsQ.data?.groups || []} onSave={saveDeviceMove} />
      <ConfirmDialog open={!!confirmDelDevice} onClose={() => setConfirmDelDevice(null)} onConfirm={delDevice} danger title="Delete device?" body={<>This permanently removes <b>{confirmDelDevice?.hostname}</b> from OpenRMM. This cannot be undone.</>} confirmLabel="Delete device" />
    </Card>
    <DeviceDetail id={selected} onClose={() => setSelected(null)} sites={sites} clients={clients} />
    </>
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

// APITokensPanel manages bearer tokens for integrations and MCP connectors
// (Claude, ChatGPT…). Gated by api_tokens.manage. The secret is returned by the
// backend only once, so it is shown in the create modal for a one-time copy.
function APITokensPanel() {
  const toast = useToast();
  const tokensQ = useQuery(['apitokens'], () => api.listAPITokens(), { invalidateOn: ['apitokens'] });
  const rolesQ = useQuery(['roles'], () => api.listRoles(), { invalidateOn: ['roles'] });
  const usersQ = useQuery(['users'], () => api.listUsers(), { invalidateOn: ['users'] });
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState('tech');
  const [userId, setUserId] = useState('');
  const [expiresDays, setExpiresDays] = useState('0'); // '0' = never
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState(null); // { token, name, role } — shown once
  const [confirmDel, setConfirmDel] = useState(null);

  const tokens = tokensQ.data?.tokens || [];
  const roles = rolesQ.data?.roles || [{ key: 'tech', name: 'Technician' }, { key: 'admin', name: 'Administrator' }];
  const users = usersQ.data?.users || [];
  const roleLabel = (r) => (r.name ? `${r.key} — ${r.name}` : r.key);
  const expLabel = (t) => {
    if (!t.expires_at) return <span className="text-faint">Never</span>;
    if (new Date(t.expires_at) <= new Date()) return <span className="text-bad font-medium">Expired</span>;
    return <span className="text-muted tabular">{fmtTime(t.expires_at)}</span>;
  };
  const EXPIRY_OPTIONS = [['0', 'Never'], ['30', '30 days'], ['90', '90 days'], ['365', '1 year']];
  const selRole = roles.find((r) => r.key === role);
  const mcpMissing = selRole && Array.isArray(selRole.permissions) && !selRole.permissions.includes('mcp.use');

  const openCreate = () => { setName(''); setRole('tech'); setUserId(''); setExpiresDays('0'); setCreated(null); setCreating(true); };
  const closeModal = () => { if (!busy) { setCreating(false); setCreated(null); } };
  const create = async () => {
    setBusy(true);
    try {
      const r = await api.createAPIToken({ name: name.trim(), role, user_id: userId || undefined, expires_in_days: Number(expiresDays) || 0 });
      setCreated(r); // token shown once
      tokensQ.refetch();
      toast.success('API token created', 'Copy it now — it is shown once.');
    } catch (e) { toast.error('Failed to create token', e.message); }
    finally { setBusy(false); }
  };
  const del = async (t) => { try { await api.deleteAPIToken(t.id); toast.info('Token revoked', t.name); tokensQ.refetch(); } catch (e) { toast.error('Failed', e.message); } };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">API tokens <span className="text-faint font-normal">({tokens.length})</span></div>
        <Button size="sm" variant="primary" icon="plus" onClick={openCreate}>New token</Button>
      </div>
      <div className="px-4 py-2.5 text-[12px] text-muted bg-elevated/30 border-b border-line flex items-start gap-1.5">
        <Icon name="info" size={13} className="mt-0.5 shrink-0" />
        <span>Bearer tokens for integrations and <b className="text-ink">MCP connectors</b> (Claude, ChatGPT…). A connector needs a role with <span className="mono">mcp.use</span>. Bind a token to a user to inherit that user's access scope. The secret is shown once — never reuse the admin token.</span>
      </div>
      {tokens.length === 0
        ? <div className="px-4 py-10 text-center text-[12.5px] text-faint">No API tokens yet.</div>
        : (
          <table className="w-full text-left">
            <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line"><th className="font-semibold px-4 py-2.5">Name</th><th className="font-semibold px-3 py-2.5">Role</th><th className="font-semibold px-3 py-2.5">Created</th><th className="font-semibold px-3 py-2.5">Expires</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th></tr></thead>
            <tbody className="divide-y divide-line">
              {tokens.map((t) => (
                <tr key={t.id} className="hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><div className="flex items-center gap-2.5"><div className="h-7 w-7 rounded-lg bg-elevated border border-line flex items-center justify-center text-faint"><Icon name="key" size={14} /></div><span className="text-[13px] text-ink font-medium">{t.name || <span className="text-faint">unnamed</span>}</span></div></td>
                  <td className="px-3 py-3"><RoleBadge role={t.role} /></td>
                  <td className="px-3 py-3 text-[12px] text-muted tabular">{fmtTime(t.created_at)}</td>
                  <td className="px-3 py-3 text-[12px]">{expLabel(t)}</td>
                  <td className="px-4 py-3 text-right"><Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(t)}>Revoke</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      <Modal open={creating} onClose={closeModal} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-4">{created ? 'API token created' : 'New API token'}</div>
          {created ? (
            <div className="space-y-3">
              <div className="text-[12.5px] text-muted">Copy this token now — it will <b className="text-ink">not be shown again</b>. Store it in your MCP client / integration.</div>
              <div className="flex items-center gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2"><Icon name="key" size={14} className="text-warn shrink-0" /><span className="text-[12.5px] mono text-ink flex-1 break-all">{created.token}</span><CopyButton value={created.token} /></div>
              <div className="text-[11.5px] text-faint">Name: <b className="text-ink">{created.name || 'unnamed'}</b> · Role: <span className="mono">{created.role}</span> · Expires: <span className="text-ink">{created.expires_at ? fmtTime(created.expires_at) : 'never'}</span></div>
              <div className="flex justify-end mt-2"><Button variant="primary" onClick={closeModal}>Done</Button></div>
            </div>
          ) : (
            <div className="space-y-3">
              <Field label="Name" hint="A label to recognize this connector in the list and the audit log."><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="claude-desktop" autoFocus /></Field>
              <Field label="Role"><Select value={role} onChange={(e) => setRole(e.target.value)}>{roles.map((r) => <option key={r.key} value={r.key}>{roleLabel(r)}</option>)}</Select></Field>
              {mcpMissing && <div className="text-[11.5px] text-warn flex items-start gap-1.5"><Icon name="alert" size={13} className="mt-0.5 shrink-0" />This role has no <span className="mono">mcp.use</span> permission — it can't connect over MCP (still usable for REST integrations).</div>}
              <Field label="Bind to user" hint="Optional. The token inherits this user's access scope (least privilege). Leave as None for the role's full scope.">
                <Select value={userId} onChange={(e) => setUserId(e.target.value)}>
                  <option value="">None (full role scope)</option>
                  {users.map((u) => <option key={u.id} value={u.id}>{u.username}</option>)}
                </Select>
              </Field>
              <Field label="Expires" hint="A short-lived token limits the damage if the secret leaks — recommended for public/cloud MCP connectors.">
                <Select value={expiresDays} onChange={(e) => setExpiresDays(e.target.value)}>
                  {EXPIRY_OPTIONS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </Select>
              </Field>
              <div className="flex justify-end gap-2 mt-2"><Button variant="ghost" onClick={closeModal} disabled={busy}>Cancel</Button><Button variant="primary" loading={busy} disabled={!name.trim()} onClick={create}>Create token</Button></div>
            </div>
          )}
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => { del(confirmDel); setConfirmDel(null); }} danger title="Revoke API token?" body={<>This immediately revokes <b>{confirmDel?.name || 'this token'}</b>. Any MCP connector or integration using it stops working. This cannot be undone.</>} confirmLabel="Revoke token" />
    </Card>
  );
}

function SettingsScreen() {
  const { can } = useAuth();
  const [tab, setTab] = useState('users');
  const tabs = [
    { value: 'users', label: 'Users' },
    ...(can('roles.manage') ? [{ value: 'roles', label: 'Roles' }] : []),
    ...(can('api_tokens.manage') ? [{ value: 'tokens', label: 'API tokens' }] : []),
    ...(can('settings.manage') ? [{ value: 'config', label: 'Configuration' }] : []),
    ...(can('settings.manage') ? [{ value: 'email', label: 'Email' }] : []),
    ...(can('enrollment.manage') ? [{ value: 'enroll', label: 'Enrollment' }] : []),
  ];
  return (
    <Page>
      <PageHeader title="Users & Settings" subtitle="Manage access and review your instance configuration." />
      <Tabs className="mb-5" value={tab} onChange={setTab} tabs={tabs} />
      {tab === 'users' && <><LanguagePicker /><ChangePasswordCard /><TwoFactorCard /><NotificationsCard />{can('users.manage') && <UsersPanel canAdmin />}</>}
      {tab === 'roles' && can('roles.manage') && <RolesPanel />}
      {tab === 'tokens' && can('api_tokens.manage') && <APITokensPanel />}
      {tab === 'config' && can('settings.manage') && <ConfigPanel />}
      {tab === 'email' && can('settings.manage') && <SMTPPanel />}
      {tab === 'enroll' && can('enrollment.manage') && <EnrollPanel canAdmin={can('enrollment.manage')} />}
    </Page>
  );
}

// LanguagePicker sits on the personal tab, next to the password and 2FA cards,
// because language is a property of the person reading — not of the instance.
// The choice lives in this browser's localStorage: it follows the operator to
// every tenant they administer, and does not change what a colleague sees.
function LanguagePicker() {
  const current = getLocale();
  const chosen = LOCALES.find((l) => l.code === current);
  return (
    <Card className="p-4 mb-4">
      <div className="flex items-center gap-2 mb-3">
        <Icon name="settings" size={15} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">{t('lang.label')}</div>
      </div>
      <Field hint={t('lang.hint')}>
        <Select value={current} onChange={(e) => setLocale(e.target.value)}>
          {LOCALES.map((l) => (
            <option key={l.code} value={l.code}>
              {l.nativeName}{l.complete ? '' : ` — ${t('lang.draft')}`}
            </option>
          ))}
        </Select>
      </Field>
      {chosen && !chosen.complete && (
        <div className="mt-2 text-[11.5px] text-faint flex items-start gap-1.5">
          <Icon name="info" size={13} className="mt-0.5 shrink-0" />
          {t('lang.draftHint')}
        </div>
      )}
    </Card>
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
  const tooShort = next.length > 0 && next.length < 12;
  const mismatch = confirm.length > 0 && next !== confirm;
  const canSubmit = cur && next.length >= 12 && next === confirm && !busy;

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
      <div className="text-[12.5px] text-muted mb-3">Set a new password for your account. Minimum 12 characters; other sessions are signed out.</div>
      <div className="space-y-2.5 max-w-md">
        <Field label="Current password"><Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
        <Field label="New password" hint={tooShort ? 'At least 12 characters' : undefined}>
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
  const [pw, setPw] = useState(''); // current password — required to enable or disable 2FA
  const [busy, setBusy] = useState(false);
  const [recovery, setRecovery] = useState(null); // string[] shown once after enable/regen
  const qrRef = useRef(null);

  useEffect(() => {
    if (setup && qrRef.current && window.QRCode) {
      qrRef.current.innerHTML = '';
      try { new window.QRCode(qrRef.current, { text: setup.uri, width: 168, height: 168, correctLevel: window.QRCode.CorrectLevel.M }); } catch (e) {}
    }
  }, [setup]);

  const start = async () => { setBusy(true); try { setSetup(await api.setup2FA(pw)); setPw(''); setCode(''); } catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); } };
  const activate = async () => { setBusy(true); try { const r = await api.enable2FA(code.trim()); setSetup(null); setCode(''); meQ.refetch(); setRecovery(r.recovery_codes || []); toast.success('Two-factor enabled'); } catch (e) { toast.error('Could not enable', e.message); } finally { setBusy(false); } };
  const disable = async () => { setBusy(true); try { await api.disable2FA(pw, code.trim()); setPw(''); setCode(''); meQ.refetch(); toast.info('Two-factor disabled'); } catch (e) { toast.error('Could not disable', e.message); } finally { setBusy(false); } };
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
          </div>
          <div className="flex items-end gap-2">
            <Field label="Account password" className="flex-1"><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
            <Button variant="danger" loading={busy} disabled={code.trim().length !== 6 || !pw} onClick={disable}>Disable 2FA</Button>
          </div>
          <div className="text-[11px] text-faint">A current code regenerates your recovery codes. Disabling 2FA also requires your account password.</div>
        </div>
      )}

      {!enabled && !setup && (
        <div className="flex items-end gap-2 max-w-md">
          <Field label="Account password" className="flex-1"><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
          <Button variant="primary" icon="shield" loading={busy} disabled={!pw} onClick={start}>Enable 2FA</Button>
        </div>
      )}

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

// Event labels shared by the self-service card and the admin panel, so a rename
// lands in both places at once.
const NOTIFY_LABELS = {
  check_failed: { title: 'Check failing', msg: 'A monitoring check starts failing or enters warning.' },
  check_recovered: { title: 'Recovered', msg: 'A failing check passes again, or an offline device reports back.' },
  agent_offline: { title: 'Device offline', msg: 'A device stops reporting in.' },
  job_failed: { title: 'Job failed', msg: 'A job returns a non-zero exit. Off by default — a bad script fans out across the fleet.' },
};

// NotificationsCard manages what the CURRENT user is notified about. The address
// is shown but not editable: it is also the address a password reset resolves,
// so changing it takes users.email.manage. Which events you receive carries no
// such risk and stays self-service for any role.
function NotificationsCard() {
  const toast = useToast();
  const prefsQ = useQuery(['my-notifications'], () => api.getMyNotifications(), { invalidateOn: ['my-notifications'] });
  const [subs, setSubs] = useState(null);   // null until loaded: event_type -> enabled
  const [busy, setBusy] = useState(false);
  const data = prefsQ.data;
  const email = data?.email || '';

  useEffect(() => {
    if (!data) return;
    const next = {};
    (data.subscriptions || []).forEach((s) => { next[s.event_type] = s.enabled; });
    setSubs(next);
  }, [data]);

  const save = async () => {
    setBusy(true);
    try {
      await api.updateMyNotifications({ subscriptions: subs });
      prefsQ.refetch();
      toast.success('Notification settings saved', email ? 'Alerts go to ' + email : 'No address set — email alerts are off.');
    } catch (e) { toast.error('Could not save', e.message); }
    finally { setBusy(false); }
  };

  if (!data || subs === null) return <Skeleton className="h-56 w-full max-w-2xl rounded-2xl mb-5" />;
  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="bell" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">Email notifications</div>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md ml-auto', email ? 'bg-oksoft text-ok' : 'bg-elevated text-faint')}>{email ? 'On' : 'No address'}</span>
      </div>
      <div className="text-[12.5px] text-muted mb-3">You only receive alerts for devices inside your access scope.</div>

      {/* Preferences are pointless while nothing can be delivered, so say so up front. */}
      {!data.delivery_configured && (
        <div className="mb-3 flex items-start gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2">
          <Icon name="info" size={14} className="text-warn mt-0.5 shrink-0" />
          <div className="text-[11.5px] text-muted">No mail server is configured yet, so nothing is being delivered. An administrator sets this up under <b>Email</b>.</div>
        </div>
      )}

      <div className="space-y-3 max-w-md">
        <Field label="Your address" hint="Set by an administrator. It is also where a password reset link would be sent, which is why you cannot change it here.">
          <div className="flex items-center gap-2 rounded-lg border border-line bg-app px-3 py-2">
            <Icon name="send" size={14} className="text-faint shrink-0" />
            <span className={cx('text-[12.5px] truncate', email ? 'text-ink mono' : 'text-faint')}>{email || 'No address set — email alerts are off.'}</span>
          </div>
        </Field>
      </div>

      <div className="mt-4 space-y-1.5">
        {(data.subscriptions || []).map((s) => (
          <div key={s.event_type} className="flex items-start gap-3 rounded-lg border border-line bg-app px-3 py-2.5">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] font-medium text-ink">{NOTIFY_LABELS[s.event_type]?.title || s.event_type}</div>
              <div className="text-[11.5px] text-muted">{NOTIFY_LABELS[s.event_type]?.msg || ''}</div>
            </div>
            <div className="pt-0.5"><Toggle checked={!!subs[s.event_type]} disabled={!email} onChange={(v) => setSubs((p) => ({ ...p, [s.event_type]: v }))} /></div>
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between gap-3 mt-4">
        <div className="text-[11px] text-faint">Recovery notices are on by default — knowing it is back matters more than the original failure.</div>
        <Button variant="primary" icon="check" loading={busy} onClick={save}>Save</Button>
      </div>
    </Card>
  );
}

// Mirrors passwordProblem in internal/api/api_auth.go. The server is the
// authority; this exists so the form can say what is missing instead of leaving
// a grey button with no explanation, which is what operators hit.
function passwordProblem(pw) {
  if (!pw) return 'Enter a password';
  if ([...pw].length < 12) return 'At least 12 characters';
  if (!/[A-ZÀ-Þ]/.test(pw)) return 'Needs an uppercase letter';
  // A symbol is anything that is not a letter, a digit or a space — defined by
  // exclusion so accented and non-Latin keyboards are accepted.
  if (!/[^\p{L}\p{N}\s]/u.test(pw)) return 'Needs a symbol';
  return '';
}
function passwordHint(pw) {
  const problem = passwordProblem(pw);
  if (!pw) return '12+ characters, with an uppercase letter and a symbol.';
  return problem || 'Meets the requirements.';
}

function UsersPanel({ canAdmin }) {
  const toast = useToast();
  const { can } = useAuth();
  // Setting an address is its own permission on top of users.manage: it is where
  // a password reset lands, so it can hand over an account that has no 2FA.
  const canEmail = can('users.email.manage');
  const usersQ = useQuery(['users'], () => api.listUsers(), { invalidateOn: ['users'] });
  const rolesQ = useQuery(['roles'], () => api.listRoles(), { invalidateOn: ['roles'] });
  const [creating, setCreating] = useState(false);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [newRole, setNewRole] = useState('tech');
  const [scopes, setScopes] = useState([]); // [] = full access
  const [scopeMode, setScopeMode] = useState('full');     // new user's picker mode
  const [editScopeMode, setEditScopeMode] = useState('full'); // edited user's picker mode
  const [confirmDel, setConfirmDel] = useState(null);
  const [confirmReset, setConfirmReset] = useState(null);
  const [editUser, setEditUser] = useState(null);   // user whose scope is being edited
  const [editScopes, setEditScopes] = useState([]);
  const [editLoading, setEditLoading] = useState(false);
  const [editSaving, setEditSaving] = useState(false);
  const [roleBusy, setRoleBusy] = useState('');
  const [pendingRole, setPendingRole] = useState(null);
  const [confirmScopeSave, setConfirmScopeSave] = useState(false);
  const [newEmail, setNewEmail] = useState('');
  const [editEmail, setEditEmail] = useState(null); // { user, value } while editing an address
  const [emailBusy, setEmailBusy] = useState(false);
  const [confirmDisable, setConfirmDisable] = useState(null);
  const users = usersQ.data?.users || [];
  const roles = rolesQ.data?.roles || [{ key: 'readonly', name: 'Read-only' }, { key: 'tech', name: 'Technician' }, { key: 'admin', name: 'Administrator' }];
  const roleLabel = (r) => r.name ? `${r.key} — ${r.name}` : r.key;

  const reset = () => { setUsername(''); setPassword(''); setNewRole('tech'); setScopes([]); setScopeMode('full'); setNewEmail(''); setCreating(false); };
  // "Restricted to…" with nothing selected sends the same empty list as full
  // access, so saving it would quietly grant the fleet. Both forms refuse it.
  const scopeIncomplete = (mode, list) => mode === 'restricted' && list.length === 0;
  const create = async () => {
    try {
      await api.createUser({ username, password, role: newRole, email: newEmail.trim(), scopes: scopes.map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })) });
      reset(); toast.success('User created', scopes.length ? scopes.length + ' scope(s)' : 'full access');
    } catch (e) { toast.error('Failed', e.message); }
  };
  // Setting someone else's address is an onboarding convenience: a technician
  // should hear about their devices before their first sign-in.
  const saveEmail = async () => {
    if (!editEmail) return;
    setEmailBusy(true);
    try {
      await api.setUserEmail(editEmail.user.id, editEmail.value.trim());
      setEditEmail(null); usersQ.refetch();
      toast.success('Address updated', editEmail.value.trim() ? editEmail.user.username + ' now receives alerts' : 'Email alerts are off for ' + editEmail.user.username);
    } catch (e) { toast.error('Could not update address', e.message); }
    finally { setEmailBusy(false); }
  };
  const toggleDisabled = async (u) => {
    try {
      await api.setUserDisabled(u.id, !u.disabled_at);
      setConfirmDisable(null);
      usersQ.refetch();
      toast.success(u.disabled_at ? 'User enabled' : 'User disabled',
        u.disabled_at ? u.username + ' can sign in again' : u.username + ' is signed out and cannot sign in');
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
    setEditUser(u); setEditLoading(true); setEditScopes([]); setEditScopeMode('full');
    try {
      const r = await api.getUserScopes(u.id);
      const loaded = (r.scopes || []).map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id }));
      setEditScopes(loaded);
      setEditScopeMode(loaded.length ? 'restricted' : 'full');
    } catch (e) {
      // Opening the picker on a failed load would show an empty selection that
      // looks like "full access", and saving it would grant exactly that.
      toast.error('Failed to load scope', e.message);
      setEditUser(null);
    }
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
      {!canAdmin && <div className="px-4 py-2.5 text-[12px] text-faint bg-elevated/30 border-b border-line flex items-center gap-1.5"><Icon name="info" size={13} />User management permission is required.</div>}
      <table className="w-full text-left">
        <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line"><th className="font-semibold px-4 py-2.5">User</th><th className="font-semibold px-3 py-2.5">Alerts to</th><th className="font-semibold px-3 py-2.5">Role</th><th className="font-semibold px-3 py-2.5">Created</th><th className="font-semibold px-4 py-2.5 text-right">Actions</th></tr></thead>
        <tbody className="divide-y divide-line">
          {users.map((u) => (
            <tr key={u.id} className="hover:bg-elevated/40 transition">
              <td className="px-4 py-3"><div className="flex items-center gap-2.5">
                <Avatar name={u.username} size={28} />
                <span className={cx('text-[13px] font-medium', u.disabled_at ? 'text-faint line-through' : 'text-ink')}>{u.username}</span>
                {u.disabled_at && <span className="text-[10.5px] px-1.5 py-0.5 rounded bg-elevated text-faint" title={'Disabled ' + fmtTime(u.disabled_at)}>disabled</span>}
              </div></td>
              <td className="px-3 py-3 text-[12px] mono">
                {u.email ? <span className="text-muted">{u.email}</span> : <span className="text-faint">— no alerts</span>}
              </td>
              <td className="px-3 py-3">
                {canAdmin && u.username !== 'admin'
                  ? <Select value={u.role} disabled={roleBusy === u.id} onChange={(e) => setPendingRole({ user: u, role: e.target.value })} className="h-8 min-w-28">{roles.map((r) => <option key={r.key} value={r.key}>{r.key}</option>)}</Select>
                  : <RoleBadge role={u.role} />}
              </td>
              <td className="px-3 py-3 text-[12px] text-muted tabular">{fmtTime(u.created_at)}</td>
              <td className="px-4 py-3 text-right">
                {/* One kebab per row rather than a row of buttons: the actions
                    are per-user and mostly rare, and Remove sat one slip away
                    from Disable. Same Dropdown/MenuItem the device and site rows
                    use. The built-in admin keeps only the actions it may have,
                    so the menu is omitted entirely when nothing is left. */}
                {canAdmin && (canEmail || u.role !== 'admin' || u.totp_enabled || u.username !== 'admin') && (
                  <div className="flex items-center justify-end">
                    <Dropdown align="right" width="w-56" trigger={
                      <button className="text-faint hover:text-ink p-1.5 -mr-1 rounded-lg hover:bg-elevated" title={'Actions for ' + u.username} aria-label={'Actions for ' + u.username}>
                        <Icon name="dots" size={16} fill />
                      </button>}>
                      {canEmail && <MenuItem icon="send" onClick={() => setEditEmail({ user: u, value: u.email || '' })} hint={u.email ? undefined : 'unset'}>Email address</MenuItem>}
                      {u.role !== 'admin' && <MenuItem icon="shield" onClick={() => openEdit(u)}>Access scope</MenuItem>}
                      {u.totp_enabled && <MenuItem icon="key" onClick={() => setConfirmReset(u)}>Reset 2FA</MenuItem>}
                      {/* Withdrawing and deleting access are separated from the
                          rest: they are the two that are painful to undo. */}
                      {u.username !== 'admin' && <div className="my-1 border-t border-line" />}
                      {u.username !== 'admin' && (u.disabled_at
                        ? <MenuItem icon="check" onClick={() => toggleDisabled(u)}>Enable sign-in</MenuItem>
                        : <MenuItem icon="lock" onClick={() => setConfirmDisable(u)}>Disable sign-in</MenuItem>)}
                      {u.username !== 'admin' && <MenuItem icon="trash" danger onClick={() => setConfirmDel(u)}>Remove user</MenuItem>}
                    </Dropdown>
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
            <Field label="Password" hint={passwordHint(password)}>
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
                placeholder="12+ characters, an uppercase letter and a symbol"
                className={password.length > 0 && passwordProblem(password) ? 'border-bad' : ''} />
            </Field>
            {/* Offered only to operators who may set it — the backend rejects an
                address from anyone without users.email.manage. */}
            {canEmail && <Field label="Email address" hint="Optional. Where their alerts go, and where a password reset link would be sent."><Input type="email" value={newEmail} onChange={(e) => setNewEmail(e.target.value)} placeholder="tech@company.com" autoComplete="off" /></Field>}
            <Field label="Role"><Select value={newRole} onChange={(e) => setNewRole(e.target.value)}>{roles.map((r) => <option key={r.key} value={r.key}>{roleLabel(r)}</option>)}</Select></Field>
            {newRole === 'admin'
              ? <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Admins always have full access (scope does not apply).</div>
              : <Field label="Access scope"><ScopePicker scopes={scopes} setScopes={setScopes} mode={scopeMode} setMode={setScopeMode} /></Field>}
          </div>
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={reset}>Cancel</Button><Button variant="primary" onClick={create}
            disabled={!username || !!passwordProblem(password) || (newRole !== 'admin' && scopeIncomplete(scopeMode, scopes))}
            title={!username ? 'Enter a username' : (passwordProblem(password) || (newRole !== 'admin' && scopeIncomplete(scopeMode, scopes) ? 'Select at least one scope, or choose Full access' : undefined))}>Create user</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => del(confirmDel.id)} danger title="Remove user?" body={<>This revokes <b>{confirmDel?.username}</b>'s access immediately.</>} confirmLabel="Remove user" />
      <Modal open={!!editEmail} onClose={() => { if (!emailBusy) setEditEmail(null); }} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">Email address</div>
          <div className="text-[12px] text-muted mb-4">Where alerts for <b className="text-ink">{editEmail?.user?.username}</b> are sent. They still only receive devices inside their access scope. Leave blank to turn their email off.</div>
          <Field label="Email address" hint="This is also where a password reset link for this account is delivered.">
            <Input type="email" value={editEmail?.value || ''} autoFocus autoComplete="off" placeholder="tech@company.com"
              onChange={(e) => setEditEmail((s) => ({ ...s, value: e.target.value }))}
              onKeyDown={(e) => { if (e.key === 'Enter') saveEmail(); }} />
          </Field>
          {/* The reset destination is an authentication factor, so a change to an
              address that already exists is worth a beat of hesitation. */}
          {editEmail && (editEmail.value || '').trim() !== (editEmail.user?.email || '') && (editEmail.user?.email || '') !== '' && (
            <div className="mt-3 flex items-start gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2">
              <Icon name="info" size={14} className="text-warn mt-0.5 shrink-0" />
              <div className="text-[11.5px] text-muted">This also moves where this account's password reset link is delivered.</div>
            </div>
          )}
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setEditEmail(null)} disabled={emailBusy}>Cancel</Button><Button variant="primary" loading={emailBusy} onClick={saveEmail}>Save address</Button></div>
        </div>
      </Modal>
      <Modal open={!!editUser} onClose={() => { if (!editSaving) { setConfirmScopeSave(false); setEditUser(null); } }} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">Access scope</div>
          <div className="text-[12px] text-muted mb-4">Define which Companies, Sites, Departments or machines <b className="text-ink">{editUser?.username}</b> can access. Empty = full access.</div>
          {editLoading
            ? <div className="py-8 flex items-center justify-center gap-2 text-faint text-[12.5px]"><Spinner size={14} />Loading current scope…</div>
            : <ScopePicker key={editUser?.id} scopes={editScopes} setScopes={setEditScopes} mode={editScopeMode} setMode={setEditScopeMode} />}
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setEditUser(null)} disabled={editSaving}>Cancel</Button><Button variant="primary" loading={editSaving}
            disabled={editLoading || scopeIncomplete(editScopeMode, editScopes)}
            title={scopeIncomplete(editScopeMode, editScopes) ? 'Select at least one scope, or choose Full access' : undefined}
            onClick={() => setConfirmScopeSave(true)}>Save scope</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmReset} onClose={() => setConfirmReset(null)} onConfirm={() => reset2fa(confirmReset)} danger title="Reset two-factor?" body={<>This turns off 2FA for <b>{confirmReset?.username}</b> and invalidates their recovery codes. They'll sign in with just their password until they re-enroll. Use only to recover a locked-out user.</>} confirmLabel="Reset 2FA" />
      <ConfirmDialog open={!!pendingRole} onClose={() => setPendingRole(null)} onConfirm={changeRole} title="Change user role?" body={<>Change <b>{pendingRole?.user?.username}</b> from <b>{pendingRole?.user?.role}</b> to <b>{pendingRole?.role}</b>? This changes what they can access and manage.</>} confirmLabel="Change role" />
      <ConfirmDialog open={confirmScopeSave} onClose={() => setConfirmScopeSave(false)} onConfirm={saveEdit} title="Save access scope?" body={<>Update access for <b>{editUser?.username}</b> to {editScopes.length ? <b>{editScopes.length} selected scope(s)</b> : <b>full access</b>}?</>} confirmLabel="Save scope" />
      <ConfirmDialog open={!!confirmDisable} onClose={() => setConfirmDisable(null)} onConfirm={() => toggleDisabled(confirmDisable)}
        title="Disable this user?" confirmLabel="Disable user"
        body={<><b>{confirmDisable?.username}</b> is signed out immediately and cannot sign in again until re-enabled. API tokens bound to them stop working, and they stop receiving alert email. The account and its audit trail are kept — use <b>Remove</b> to delete it.</>} />
    </Card>
  );
}

function RolesPanel() {
  const toast = useToast();
  const rolesQ = useQuery(['roles'], () => api.listRoles(), { invalidateOn: ['roles'] });
  const permsQ = useQuery(['permissions'], () => api.listPermissions(), { invalidateOn: ['roles'] });
  const [editing, setEditing] = useState(null);
  const [confirmDel, setConfirmDel] = useState(null);
  const roles = rolesQ.data?.roles || [];
  const permissions = permsQ.data?.permissions || [];

  const cloneRole = (r) => setEditing({ key: '', name: r.name + ' copy', description: r.description || '', permissions: r.permissions || [], system: false, _new: true });
  const del = async () => {
    if (!confirmDel) return;
    try { await api.deleteRole(confirmDel.key); window.dataBus.emit('roles'); toast.info('Role deleted'); }
    catch (e) { toast.error('Delete failed', e.message); }
    finally { setConfirmDel(null); }
  };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">Roles <span className="text-faint font-normal">({roles.length})</span></div>
        <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing({ key: '', name: '', description: '', permissions: [], _new: true })}>New role</Button>
      </div>
      <div className="divide-y divide-line">
        {roles.map((r) => (
          <div key={r.key} className="px-4 py-3 flex items-center gap-3 hover:bg-elevated/30 transition">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2"><RoleBadge role={r.key} /><span className="text-[13px] font-semibold text-ink truncate">{r.name}</span>{r.system && <span className="text-[10.5px] uppercase text-faint border border-line2 rounded px-1.5 py-0.5">system</span>}</div>
              <div className="text-[12px] text-muted mt-1 truncate">{r.description || 'No description'}</div>
            </div>
            <div className="text-[11.5px] text-faint tabular hidden sm:block">{(r.permissions || []).length} permission(s)</div>
            <Button size="sm" variant="ghost" icon="eye" onClick={() => setEditing(r)}>{r.system ? 'View' : 'Edit'}</Button>
            <Button size="sm" variant="ghost" icon="copy" onClick={() => cloneRole(r)}>Clone</Button>
            {!r.system && <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(r)} />}
          </div>
        ))}
        {!rolesQ.loading && roles.length === 0 && <EmptyState icon="shield" title="No roles" msg="Create a role to assign custom permissions." />}
      </div>
      {editing && <RoleEditor role={editing} roles={roles} permissions={permissions} onClose={() => setEditing(null)} />}
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={del} danger title="Delete role?" body={<>Delete <b>{confirmDel?.key}</b>? Users or API tokens assigned to it must be moved first.</>} confirmLabel="Delete role" />
    </Card>
  );
}

function RoleEditor({ role, roles, permissions, onClose }) {
  const toast = useToast();
  const isNew = !!role._new;
  const readOnly = role.system && !isNew;
  const [name, setName] = useState(role.name || '');
  const [description, setDescription] = useState(role.description || '');
  const [selected, setSelected] = useState(new Set(role.permissions || []));
  const [busy, setBusy] = useState(false);
  const groups = permissions.reduce((acc, p) => { (acc[p.group] = acc[p.group] || []).push(p); return acc; }, {});
  const toggle = (p) => setSelected((prev) => { const next = new Set(prev); next.has(p) ? next.delete(p) : next.add(p); return next; });
  const slug = (v) => (v || '')
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9_.-]+/g, '-')
    .replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, '')
    .slice(0, 64)
    .replace(/[^a-z0-9]+$/g, '');
  const existingKeys = new Set((roles || []).map((r) => r.key).filter((k) => !(!isNew && k === role.key)));
  const makeKey = () => {
    const base = slug(name) || 'role';
    let key = base.length >= 2 ? base : base + '-role';
    let i = 2;
    while (existingKeys.has(key)) {
      const suffix = '-' + i++;
      key = base.slice(0, 64 - suffix.length) + suffix;
    }
    return key;
  };
  const generatedKey = isNew ? makeKey() : role.key;
  const save = async () => {
    setBusy(true);
    const body = { name, description, permissions: Array.from(selected).sort() };
    if (isNew) body.key = generatedKey;
    try {
      if (isNew) await api.createRole(body); else await api.updateRole(role.key, body);
      window.dataBus.emit('roles'); toast.success(isNew ? 'Role created' : 'Role saved'); onClose();
    } catch (e) { toast.error('Save failed', e.message); }
    finally { setBusy(false); }
  };

  return (
    <Drawer open onClose={onClose} width="max-w-[760px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-9 w-9 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={17} /></div>
        <div className="flex-1"><div className="text-[15px] font-semibold text-ink">{isNew ? 'New role' : role.name}</div><div className="text-[12px] text-faint">{readOnly ? 'System role' : 'Custom role'}</div></div>
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <Field label="Name" hint={isNew ? <>Technical key will be generated as <span className="mono">{generatedKey}</span>.</> : <>Technical key: <span className="mono">{role.key}</span>.</>}><Input value={name} onChange={(e) => setName(e.target.value)} disabled={readOnly} placeholder="Helpdesk" /></Field>
        <Field label="Description"><Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={readOnly} placeholder="What this role can do" /></Field>
        {Object.entries(groups).map(([group, items]) => (
          <div key={group} className="rounded-xl border border-line bg-card overflow-hidden">
            <div className="px-3.5 py-2.5 border-b border-line text-[12.5px] font-semibold text-ink">{group}</div>
            <div className="grid sm:grid-cols-2 gap-0 divide-y sm:divide-y-0 sm:divide-x divide-line">
              {items.map((p) => (
                <label key={p.key} className={cx('flex gap-2.5 px-3.5 py-3 cursor-pointer hover:bg-elevated/30', readOnly && 'cursor-default opacity-75')}>
                  <input type="checkbox" checked={selected.has(p.key)} disabled={readOnly} onChange={() => toggle(p.key)} className="mt-0.5 h-4 w-4 accent-brand" />
                  <span className="min-w-0"><span className="block text-[12.5px] text-ink font-medium">{p.label}</span><span className="block text-[11.5px] text-faint leading-snug">{p.description}</span><span className="block text-[10.5px] text-faint mono mt-1">{p.key}</span></span>
                </label>
              ))}
            </div>
          </div>
        ))}
      </div>
      {!readOnly && <div className="px-5 py-3.5 border-t border-line flex justify-end gap-2"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" icon="check" loading={busy} onClick={save} disabled={!slug(name) || !name}>Save role</Button></div>}
    </Drawer>
  );
}

// ScopePicker builds a user's access scope as a lazy-loaded, searchable tree
// (Company -> Site -> Department -> Machine). Scales to thousands of machines:
// sites load when a company is expanded, machines when a site is expanded.
// Empty = full access. Machines without a department stay directly under the
// site. scopes is [{scope_type, scope_id}]; selection keys are "type:id".
// mode/setMode are owned by the caller: "Restricted to…" with nothing selected
// is indistinguishable from full access in the payload (both send []), so the
// caller has to see the mode to refuse that save rather than silently widening
// the user's access. See the callers' scopeIncomplete.
function ScopePicker({ scopes, setScopes, mode, setMode }) {
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
  // picked; the caller refuses to save that state rather than reading it as full
  // access.
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
    <div className={cx('flex items-center gap-1.5 py-1 hover:bg-elevated rounded', `tree-depth-${depth}`)}>
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
                      const siteDevices = devBySite[s.id] || [];
                      const siteGroups = (groupBySite[s.id] || []).filter((g) => m(g.name) || siteDevices.some((d) => d.group_id === g.id));
                      const ungroupedDevices = siteDevices.filter((d) => !d.group_id || !siteGroups.some((g) => g.id === d.group_id));
                      return (
                        <div key={s.id}>
                          <Row depth={1} icon="pin" name={s.name} checked={siteCov} disabled={clientCov} onCheck={() => toggle('site', s.id)} />
                          {siteGroups.map((g) => {
                            const groupDevices = siteDevices.filter((d) => d.group_id === g.id);
                            return <div key={g.id}><Row depth={2} icon="building" name={g.name} hint="department" checked={siteCov || sel.has('group:' + g.id)} disabled={siteCov} onCheck={() => toggle('group', g.id)} />{groupDevices.map((d) => { const cov = deviceCovered(siteCov, d); return <Row key={d.id} depth={3} icon="devices" name={d.hostname} hint={d.status} checked={cov || sel.has('agent:' + d.id)} disabled={cov} onCheck={() => toggle('agent', d.id)} />; })}</div>;
                          })}
                          {ungroupedDevices.map((d) => { const cov = deviceCovered(siteCov, d); return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={(d.status || '') + ' · no department'} checked={cov || sel.has('agent:' + d.id)} disabled={cov} onCheck={() => toggle('agent', d.id)} />; })}
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
                  const siteDevices = (devsBy[st.id] || []).filter((d) => match(d.hostname));
                  const siteGroups = groupsForSite(st.id).filter((g) => match(g.name) || siteDevices.some((d) => d.group_id === g.id));
                  const ungroupedDevices = siteDevices.filter((d) => !d.group_id || !siteGroups.some((g) => g.id === d.group_id));
                  return (
                    <div key={st.id}>
                      <Row depth={1} icon="pin" name={st.name} checked={siteCovered} disabled={sel.has('client:' + c.id)} onCheck={() => toggle('site', st.id)}
                        expandable expanded={!!expS[st.id]} onExpand={() => { setExpS((m) => ({ ...m, [st.id]: !m[st.id] })); loadDevs(st.id); }} />
                      {expS[st.id] && siteGroups.map((g) => {
                        const groupDevices = siteDevices.filter((d) => d.group_id === g.id);
                        return <div key={g.id}><Row depth={2} icon="building" name={g.name} hint="department" checked={siteCovered || sel.has('group:' + g.id)} disabled={siteCovered} onCheck={() => toggle('group', g.id)} />{groupDevices.map((d) => { const covered = deviceCovered(siteCovered, d); return <Row key={d.id} depth={3} icon="devices" name={d.hostname} hint={d.status} checked={covered || sel.has('agent:' + d.id)} disabled={covered} onCheck={() => toggle('agent', d.id)} />; })}</div>;
                      })}
                      {expS[st.id] && ungroupedDevices.map((d) => {
                        const covered = deviceCovered(siteCovered, d);
                        return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={(d.status || '') + ' · no department'} checked={covered || sel.has('agent:' + d.id)} disabled={covered} onCheck={() => toggle('agent', d.id)} />;
                      })}
                      {expS[st.id] && siteGroups.length === 0 && siteDevices.length === 0 && <div className="tree-empty-depth-2 text-[11px] text-faint py-1">no departments or devices</div>}
                    </div>
                  );
                })}
                {expC[c.id] && (sitesBy[c.id] || []).length === 0 && <div className="tree-empty-depth-1 text-[11px] text-faint py-1">no sites</div>}
              </div>
            ))}
            </>}
          </div>
        </div>
      )}
      {restricted && scopes.length === 0
        ? <div className="text-[11px] text-bad flex items-center gap-1.5"><Icon name="alert" size={12} />Select at least one item, or choose <b>Full access</b> — a restricted user with nothing selected cannot be saved.</div>
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
          <Field label="Provider"><Input value={form.ai_provider} disabled={!can('settings.manage')} onChange={(e) => set('ai_provider', e.target.value)} placeholder="openai-compatible" /></Field>
          <Field label="Base URL"><Input className="mono" value={form.ai_base_url} disabled={!can('settings.manage')} onChange={(e) => set('ai_base_url', e.target.value)} placeholder="https://api.openai.com/v1" /></Field>
          <Field label="Model"><Input className="mono" value={form.ai_model} disabled={!can('settings.manage')} onChange={(e) => set('ai_model', e.target.value)} placeholder="gpt-4o-mini" /></Field>
          <Field label="API key" hint={c.ai.api_key_set ? 'Already set. Leave blank to keep the current key.' : 'Write-only. It will never be displayed again.'}><Input type="password" value={form.ai_api_key} disabled={!can('settings.manage')} onChange={(e) => set('ai_api_key', e.target.value)} placeholder={c.ai.api_key_set ? '••••••••••••••••' : 'sk-...'} autoComplete="new-password" /></Field>
        </div>
      </Card>
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="remote" size={15} /></div><div className="text-[13px] font-semibold text-ink">Remote access</div></div>
        <div className="space-y-3">
          <Field label="Default provider"><Select value={form.remote_default} disabled={!can('settings.manage')} onChange={(e) => set('remote_default', e.target.value)}>{c.remote.providers.map((p) => <option key={p} value={p}>{p}</option>)}</Select></Field>
          <Field label="MeshCentral" hint="When disabled, the MeshCentral option is hidden from the device Remote menu."><Select value={form.meshcentral_enabled ? 'on' : 'off'} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_enabled', e.target.value === 'on')}><option value="on">Enabled</option><option value="off">Disabled</option></Select></Field>
          <Field label="MeshCentral URL"><Input className="mono" value={form.meshcentral_url} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_url', e.target.value)} placeholder="https://mesh.example.com" /></Field>
          <Field label="MeshCentral user"><Input value={form.meshcentral_user} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_user', e.target.value)} placeholder="admin" /></Field>
          <Field label="MeshCentral token key" hint={c.remote.meshcentral_token_set ? 'Already set. Leave blank to keep it.' : 'Optional write-only secret for login-token embedding.'}><Input type="password" value={form.meshcentral_token_key} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_token_key', e.target.value)} placeholder={c.remote.meshcentral_token_set ? '••••••••••••••••' : 'hex loginTokenKey'} autoComplete="new-password" /></Field>
          <Field label="RustDesk" hint="Shows a RustDesk option in the device Remote menu; reads the device id on demand and opens rustdesk://<id>. The RustDesk client is installed out-of-band."><Select value={form.rustdesk_enabled ? 'on' : 'off'} disabled={!can('settings.manage')} onChange={(e) => set('rustdesk_enabled', e.target.value === 'on')}><option value="off">Disabled</option><option value="on">Enabled</option></Select></Field>
        </div>
      </Card>
      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={15} /></div><div className="text-[13px] font-semibold text-ink">Server</div></div>
        <ConfigItem label="Version" value={c.server.version} mono /><ConfigItem label="Agents public URL" value={c.server.base_url} mono />
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Secrets are write-only and encrypted in the database; blank secret fields preserve current values.</div>
          {can('settings.manage') && <Button variant="primary" icon="check" loading={saving} onClick={save}>Save configuration</Button>}
        </div>
      </Card>
    </div>
  );
}

// SMTPPanel configures the alert mail transport. Separate from the Configuration
// tab because it carries its own save and a test send: proving delivery works is
// the whole point, and it should not wait on an unrelated form.
//
// Only one transport is editable here. Account mail — password resets — is set
// with RMM_SMTP_* in the server environment and has no form, because on a hosted
// instance the reset sender belongs to the operator's domain and a tenant admin
// must not be able to repoint it. Alerts are the tenant's own choice, so they
// stay editable; leaving the host blank sends them through the account transport.
function SMTPPanel() {
  return <SMTPProfilePanel />;
}

function SMTPProfilePanel() {
  const toast = useToast();
  const cfgQ = useQuery(['smtp', 'alerts'], () => api.getAlertSMTPConfig(), { invalidateOn: ['smtp'] });
  const c = cfgQ.data;
  const [form, setForm] = useState(null);
  const [testTo, setTestTo] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    if (!c) return;
    setForm({
      host: c.host || '', port: c.port || 587, username: c.username || '', password: '',
      from: c.from || '', from_name: c.from_name || '', tls_mode: c.tls_mode || 'starttls',
      skip_verify: !!c.skip_verify,
    });
  }, [c]);
  const set = (k, v) => setForm((f) => ({ ...f, [k]: v }));
  // The alerts profile with no host of its own rides the account transport, so
  // the connection fields below are inert and say so rather than lying.
  const inherits = !(form?.host || '').trim();
  const effective = c?.effective || {};

  const save = async () => {
    setSaving(true);
    try {
      const body = { ...form, port: Number(form.port) || 587 };
      await api.updateAlertSMTPConfig(body);
      set('password', ''); cfgQ.refetch();
      toast.success('Mail settings saved',
        body.host ? 'Alerts now leave through their own relay.' : 'Alerts ride the account transport.');
    } catch (e) { toast.error('Could not save mail settings', e.message); }
    finally { setSaving(false); }
  };
  // The test sends with the STORED settings, so unsaved edits are not covered.
  const test = async () => {
    setTesting(true);
    try {
      const r = await api.testAlertSMTP(testTo.trim());
      toast.success('Test message sent', 'Delivered to ' + r.to + '.');
    } catch (e) { toast.error('Test failed', e.message); }
    finally { setTesting(false); }
  };

  if (!c || !form) return <div className="grid md:grid-cols-2 gap-4">{Array.from({ length: 2 }).map((_, i) => <Skeleton key={i} className="h-72 w-full rounded-2xl" />)}</div>;
  const dirty = form.host !== (c.host || '') || form.from !== (c.from || '') || form.tls_mode !== (c.tls_mode || 'starttls')
    || Number(form.port) !== c.port || form.username !== (c.username || '') || form.password !== ''
    || form.from_name !== (c.from_name || 'OpenRMM') || form.skip_verify !== !!c.skip_verify;

  return (
    <div className="grid md:grid-cols-2 gap-4">
      {
        <div className="md:col-span-2 flex items-start gap-2 rounded-lg border border-line bg-elevated/40 px-3 py-2.5">
          <Icon name="info" size={14} className="text-muted mt-0.5 shrink-0" />
          <div className="text-[11.5px] text-muted">
            {inherits
              ? <>Alerts leave through the <b className="text-ink">account transport</b>. Fill in a From address to send them under a different sender on the same relay, or a host to move them to a separate relay entirely.</>
              : <>Alerts leave through this relay, independently of account mail. If it fails, alerts retry here and are never rerouted through the account transport.</>}
            {effective.from && <> Currently sending as <b className="text-ink mono">{effective.from}</b>.</>}
          </div>
        </div>
      }
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="send" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">Mail server</div>
          <span className="ml-auto"><StatusPill status={effective.enabled ? 'online' : 'warning'} label={effective.enabled ? (inherits ? 'Inherited' : 'Configured') : 'Not configured'} size="sm" /></span>
        </div>
        <div className="space-y-3">
          <Field label="Host" hint="Leave blank to send alerts through the account transport. Setting a host makes this a separate relay with its own credentials.">
            <Input className="mono" value={form.host} onChange={(e) => set('host', e.target.value)} placeholder="blank = use the account server" />
          </Field>
          <Field label="Port"><Input className="mono" type="number" value={form.port} onChange={(e) => set('port', e.target.value)} placeholder="587" disabled={inherits} /></Field>
          <Field label="Encryption" hint="STARTTLS on 587 is the usual choice; TLS means implicit TLS, normally on 465.">
            <Select value={form.tls_mode} onChange={(e) => set('tls_mode', e.target.value)} disabled={inherits}>
              <option value="starttls">STARTTLS</option>
              <option value="tls">TLS (implicit)</option>
              <option value="none">None (no encryption)</option>
            </Select>
          </Field>
          <Field label="Certificate check" hint="Only disable for a private CA on a trusted network.">
            <Select value={form.skip_verify ? 'skip' : 'verify'} onChange={(e) => set('skip_verify', e.target.value === 'skip')} disabled={inherits}>
              <option value="verify">Verify certificate</option>
              <option value="skip">Skip verification</option>
            </Select>
          </Field>
        </div>
      </Card>

      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="user" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">Sender &amp; credentials</div>
        </div>
        <div className="space-y-3">
          <Field label="From address" hint="Optional. Blank sends alerts under the account sender.">
            <Input className="mono" type="email" value={form.from} onChange={(e) => set('from', e.target.value)} placeholder="alerts@example.com" />
          </Field>
          <Field label="From name"><Input value={form.from_name} onChange={(e) => set('from_name', e.target.value)} placeholder="OpenRMM Alerts" /></Field>
          {/* Credentials belong to a host. An alerts profile that names none must not
              keep a password, so the fields go away instead of quietly doing nothing. */}
          {!inherits && <>
            <Field label="Username" hint="Leave blank for a relay that does not authenticate."><Input className="mono" value={form.username} onChange={(e) => set('username', e.target.value)} placeholder="rmm@example.com" autoComplete="off" /></Field>
            <Field label="Password" hint={c.password_set ? 'Already set. Leave blank to keep the current password.' : 'Write-only. It will never be displayed again.'}>
              <Input type="password" value={form.password} onChange={(e) => set('password', e.target.value)} placeholder={c.password_set ? '••••••••••••••••' : ''} autoComplete="new-password" />
            </Field>
          </>}
          {inherits && <div className="text-[11.5px] text-faint flex items-start gap-1.5"><Icon name="info" size={13} className="mt-0.5 shrink-0" />Credentials come from the account transport. Set a host above to give alerts their own.</div>}
        </div>
      </Card>

      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="bell" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">Test delivery</div>
        </div>
        <div className="flex items-end gap-2 flex-wrap">
          <Field label="Send a test message to" className="flex-1 min-w-[240px]" hint="Blank sends to your own address. This tests the transport on this tab only.">
            <Input type="email" value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="you@example.com" />
          </Field>
          <Button variant="secondary" icon="send" loading={testing} disabled={!effective.enabled || dirty} onClick={test}
            title={dirty ? 'Save your changes first — the test uses the stored settings' : undefined}>Send test</Button>
        </div>
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Alerts queue in the database and are delivered by a background worker, so a slow mail server never delays monitoring.</div>
          <Button variant="primary" icon="check" loading={saving} onClick={save}>Save mail settings</Button>
        </div>
      </Card>
    </div>
  );
}

function EnrollPanel({ canAdmin }) {
  const toast = useToast();
  const [token, setToken] = useState(null);
  const [busy, setBusy] = useState(false);
  const gen = async () => { setBusy(true); try { const t = await api.createEnrollToken({ description: 'Manual enrollment', uses: 1, ttl_hours: 24 }); setToken(t); toast.success('Enrollment token created', 'Shown once — copy it now.'); } finally { setBusy(false); } };
  if (!canAdmin) return <Card><EmptyState icon="shield" title="Access denied" msg="Enrollment management permission is required." /></Card>;
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
