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
      <PageHeader title={t('title.scripts')} subtitle={t('scripts.subtitle')}
        actions={can('scripts.manage') && <div className="flex items-center gap-2">
          <Button variant="ghost" icon="grid" onClick={() => setManaging(true)}>{t('scripts.categories')}</Button>
          <Button variant="primary" icon="plus" onClick={() => setEditing('new')}>{t('scripts.new')}</Button>
        </div>} />
      <Card className="mb-4"><div className="flex items-center gap-2 p-2.5">
        <div className="relative flex-1"><Icon name="search" size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('scripts.search')} className={cx(inputCls, 'pl-9')} /></div>
        <Select value={cat} onChange={(e) => setCat(e.target.value)} className="w-auto min-w-[170px]">
          <option value="">{t('scripts.everyCategory')}</option>
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
      {!scriptsQ.loading && scripts.length === 0 && <Card><EmptyState icon="script" title={t('scripts.none')} msg={t('scripts.noneMsg')} /></Card>}
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
      toast.success(t('cats.created'), t('cats.createdMsg'));
    } catch (e) { toast.error(t('cats.createFailed'), e.message); }
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
      toast.success(confirm.kind === 'clear' ? t('cats.removed') : confirm.kind === 'merge' ? t('cats.merged') : t('cats.renamed'),
        tn('cats.scriptsUpdated', scripts));
    } catch (e) { toast.error(t('common.failed'), e.message); }
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
          <div className="text-[15px] font-semibold text-ink">{t('cats.title')}</div>
          {!adding && <Button size="sm" variant="ghost" icon="plus" onClick={() => setAdding('')}>{t('cats.add')}</Button>}
        </div>
        <div className="text-[12px] text-muted mb-4">{t('cats.help')}</div>
        {adding !== null && (
          <div className="flex items-center gap-2 rounded-xl border border-brand/30 bg-brandsoft px-3 py-2 mb-2">
            <Icon name="grid" size={14} className="text-brandhi shrink-0" />
            <Input value={adding} autoFocus placeholder={t('cats.placeholder')} className="h-8 flex-1"
              onChange={(e) => setAdding(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') create(); if (e.key === 'Escape') setAdding(null); }} />
            <Button size="sm" variant="ghost" onClick={() => setAdding(null)} disabled={busy}>{t('action.cancel')}</Button>
            <Button size="sm" variant="primary" loading={busy} disabled={!adding.trim()} onClick={create}>{t('action.create')}</Button>
          </div>
        )}
        {categories.length === 0
          ? <EmptyState icon="grid" title={t('cats.none')} msg={t('cats.noneMsg')} />
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
                    <span className="text-[11.5px] text-faint tabular">{tn('cats.scriptCount', c.count)}</span></>
                )}
                {editing?.name === c.name
                  ? <><Button size="sm" variant="ghost" onClick={() => setEditing(null)}>{t('action.cancel')}</Button><Button size="sm" variant="primary" onClick={() => askRename(c)}>{t('cats.rename')}</Button></>
                  : <><Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing({ name: c.name, value: c.name })} title={t('cats.renameNamed', { name: c.name })} />
                    <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirm({ kind: 'clear', from: c.name, to: '', count: c.count })} title={t('cats.removeNamed', { name: c.name })} /></>}
              </div>
            ))}
          </div>}
        <div className="flex justify-end mt-5"><Button variant="ghost" onClick={onClose} disabled={busy}>{t('cats.done')}</Button></div>
      </div>
      <ConfirmDialog open={!!confirm} onClose={() => setConfirm(null)} onConfirm={apply} danger={confirm?.kind !== 'rename'}
        title={confirm?.kind === 'clear' ? t('cats.removeTitle') : confirm?.kind === 'merge' ? t('cats.mergeTitle') : t('cats.renameTitle')}
        confirmLabel={confirm?.kind === 'clear' ? t('cats.removeConfirm') : confirm?.kind === 'merge' ? t('cats.merge') : t('cats.rename')}
        body={confirm && (confirm.kind === 'clear'
          ? (confirm.count > 0 ? t('cats.removeBodyUsed', { name: confirm.from, scripts: tn('cats.scriptCount', confirm.count) }) : t('cats.removeBodyEmpty', { name: confirm.from }))
          : confirm.kind === 'merge'
            ? t('cats.mergeBody', { from: confirm.from, to: confirm.to, scripts: tn('cats.scriptCount', confirm.count) })
            : t('cats.renameBody', { from: confirm.from, to: confirm.to, scripts: tn('cats.scriptCount', confirm.count) }))} />
    </Modal>
  );
}

function ScriptEditor({ script, onClose, categories = [] }) {
  const { can } = useAuth();
  const toast = useToast();
  const devMap = useDeviceMap();
  const [name, setName] = useState(script?.name || '');
  const [desc, setDesc] = useState(script?.description || '');
  const [category, setCategory] = useState(script?.category || t('script.defaultCategory'));
  const [shell, setShell] = useState(script?.shell || 'bash');
  const [body, setBody] = useState(script?.body || '');
  const [busy, setBusy] = useState(false);
  const [runOpen, setRunOpen] = useState(false);
  const readOnly = !can('scripts.manage');

  const save = async () => {
    setBusy(true);
    try { const b = { name, description: desc, category, shell, body }; if (script) await api.updateScript(script.id, b); else await api.createScript(b); toast.success(script ? t('script.saved') : t('script.created')); onClose(); }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  const del = async () => { await api.deleteScript(script.id); toast.info(t('script.deleted')); onClose(); };

  return (
    <Drawer open onClose={onClose} width="max-w-[760px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-9 w-9 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="script" size={17} /></div>
        <div className="flex-1"><div className="text-[15px] font-semibold text-ink">{script ? script.name : t('scripts.new')}</div><div className="text-[12px] text-faint">{readOnly ? t('script.readOnly') : t('script.editing')}</div></div>
        {can('jobs.create') && script && <Button variant="secondary" icon="play" onClick={() => setRunOpen(true)}>{t('script.runOnDevice')}</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('checks.name')}><Input value={name} onChange={(e) => setName(e.target.value)} disabled={readOnly} placeholder={t('script.namePlaceholder')} /></Field>
          {/* Pick an existing category or name a new one. A plain text field was
              how "Networking" and "Netwoking" both came to exist. */}
          <Field label={t('script.category')} hint={readOnly ? undefined : t('script.categoryHint')}>
            {/* The label is the bare name: `creatable` decides whether to offer
                "Create X" by comparing what was typed against these labels, so a
                decorated label would offer to create a category that exists. */}
            <Combobox value={category} onChange={setCategory} disabled={readOnly} creatable placeholder={t('script.uncategorized')}
              options={categories.map((c) => ({ value: c.name, label: c.name }))} />
          </Field>
        </div>
        <Field label={t('common.description')}><Input value={desc} onChange={(e) => setDesc(e.target.value)} disabled={readOnly} /></Field>
        <Field label={t('deploy.shell')} className="w-44"><Select value={shell} onChange={(e) => setShell(e.target.value)} disabled={readOnly}><option value="bash">bash</option><option value="sh">sh</option><option value="powershell">powershell</option><option value="cmd">cmd</option><option value="python">python</option></Select></Field>
        <Field label={t('script.body')}>{readOnly ? <CodeBlock code={body} shell={shell} maxHeight={420} /> : <CodeEditor value={body} onChange={setBody} shell={shell} minHeight={300} />}</Field>
      </div>
      {!readOnly && (
        <div className="px-5 py-3.5 border-t border-line flex justify-between gap-2">
          {script ? <Button variant="danger" icon="trash" onClick={del}>{t('action.delete')}</Button> : <span />}
          <div className="flex gap-2"><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} onClick={save} disabled={!name || !body}>{script ? t('script.save') : t('script.create')}</Button></div>
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
    try { await api.runScript(dev, { shell: script.shell, script: script.body, description: t('script.runDesc', { name: script.name }), name: script.name }); toast.success(t('run.created'), t('script.queuedOn', { host: devMap[dev].hostname })); onClose(); onDone(); }
    catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={onClose} width="max-w-md">
      <div className="p-5">
        <div className="text-[15px] font-semibold text-ink mb-1">{t('script.runTitle', { name: script.name })}</div>
        <div className="text-[12.5px] text-muted mb-4">{t('script.runHint')}</div>
        <Field label={t('script.targetDevice')}><Select value={dev} onChange={(e) => setDev(e.target.value)}><option value="">{t('script.chooseDevice')}</option>{devices.map((d) => <option key={d.id} value={d.id}>{t('script.deviceOption', { host: d.hostname, status: statusLabel(d.status) })}</option>)}</Select></Field>
        <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" icon="plus" loading={busy} disabled={!dev} onClick={run}>{t('script.createJob')}</Button></div>
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
      resetCreate(); toast.success(t('pol.created')); setDetail(p);
    } catch (e) { toast.error(t('pol.createFailed'), e.message); }
  };
  // Any non-GET already invalidates every query, so the card list refreshes
  // itself; what it cannot do is update the drawer's own copy of the policy, so
  // a rename has to be handed back or the header keeps the old name.
  const onPolicySaved = (p) => setDetail(p);

  return (
    <Page wide>
      <PageHeader title={t('title.policies')} subtitle={t('pol.subtitle')}
        actions={can('policies.manage') && <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>{t('pol.new')}</Button>} />
      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {policies.map((p) => <PolicyCard key={p.id} policy={p} onClick={() => setDetail(p)} />)}
      </div>
      {detail && <PolicyDetail policy={detail} onClose={() => setDetail(null)} onSaved={onPolicySaved} />}
      <Modal open={creating} onClose={resetCreate} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-4">{t('pol.new')}</div>
          <div className="space-y-3">
            <Field label={t('checks.name')}><Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder={t('pol.namePlaceholder')} autoFocus /></Field>
            <Field label={t('common.description')} hint={t('pol.descHint')}>
              <Input value={newDesc} onChange={(e) => setNewDesc(e.target.value)} placeholder={t('pol.descPlaceholder')} />
            </Field>
          </div>
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={resetCreate}>{t('action.cancel')}</Button><Button variant="primary" onClick={create} disabled={!newName.trim()}>{t('action.create')}</Button></div>
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
      <div className="text-[12px] text-muted line-clamp-2 leading-snug min-h-[32px]">{policy.description || t('pol.noDescription')}</div>
      <div className="flex items-center gap-3 mt-3 text-[11.5px] text-faint">
        <span className="flex items-center gap-1"><Icon name="check" size={12} />{t('pol.checks', { n: (checksQ.data?.checks || []).length })}</span>
        <span className="flex items-center gap-1"><Icon name="tasks" size={12} />{t('pol.tasks', { n: (tasksQ.data?.tasks || []).length })}</span>
        <span className="flex items-center gap-1"><Icon name="link" size={12} />{t('pol.assigned', { n: (asgQ.data?.assignments || []).length })}</span>
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
  const toggleTask = async (task) => {
    try {
      await api.updateTask(task.id, { ...task, enabled: !task.enabled });
      toast.success(task.enabled ? t('tasks.disabled') : t('tasks.enabled'), task.name);
    } catch (e) { toast.error(t('tasks.updateFailed'), e.message); }
    finally { setConfirmToggleTask(null); }
  };

  const checks = checksQ.data?.checks || [], tasks = tasksQ.data?.tasks || [], asg = asgQ.data?.assignments || [];
  const clients = clientsQ.data?.clients || [], sites = sitesQ.data?.sites || [];
  const scopeName = (a) => a.scope_type === 'agent' ? devMap[a.scope_id]?.hostname : a.scope_type === 'site' ? sites.find((s) => s.id === a.scope_id)?.name : clients.find((c) => c.id === a.scope_id)?.name;

  const addAssignment = async () => { if (!addScope) return; const [type, id] = addScope.split(':'); await api.createAssignment(policy.id, { scope_type: type, scope_id: id }); setAddScope(''); toast.success(t('pol.assignedToast'), t('pol.assignedToastMsg')); };
  const removeAssignment = async (aid) => { await api.deleteAssignment(aid); toast.info(t('pol.assignmentRemoved')); };
  const [confirmDel, setConfirmDel] = useState(false);
  const del = async () => { try { await api.deletePolicy(policy.id); toast.info(t('pol.deleted')); onClose(); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const saveEdit = async () => {
    if (!editing?.name.trim()) return;
    setSavingEdit(true);
    try {
      const updated = await api.updatePolicy(policy.id, { name: editing.name.trim(), description: editing.description.trim() });
      setEditing(null);
      onSaved?.(updated);
      toast.success(t('pol.updated'), t('pol.updatedMsg'));
    } catch (e) { toast.error(t('pol.updateFailed'), e.message); }
    finally { setSavingEdit(false); }
  };

  return (
    <Drawer open onClose={onClose} width="max-w-[720px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-10 w-10 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="policy" size={19} /></div>
        <div className="flex-1 min-w-0"><h2 className="text-[16px] font-semibold text-ink truncate">{policy.name}</h2><div className="text-[12px] text-muted truncate">{policy.description || t('pol.noDescription')}</div></div>
        {can('policies.manage') && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditing({ name: policy.name, description: policy.description || '' })}>{t('action.edit')}</Button>}
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'checks', label: t('pol.tab.checks'), count: checks.length }, { value: 'tasks', label: t('pol.tab.tasks'), count: tasks.length }, { value: 'assign', label: t('pol.tab.assign'), count: asg.length }]} />
      <div className="flex-1 overflow-y-auto scroll-area p-5">
        {tab === 'checks' && (
          <div className="space-y-3">
            {canChecks && <div className="flex justify-end"><Button size="sm" variant="ghost" icon="plus" onClick={() => setEditCheck('new')}>{t('pol.addCheck')}</Button></div>}
            {checks.length ? <div className="space-y-2">{checks.map((c) => { const ct = checkType(c.type); return (
              <div key={c.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3">
                <Icon name={ct.icon} size={15} className="text-muted" />
                <div className="flex-1 min-w-0"><div className="text-[13px] text-ink font-medium truncate">{c.name}</div><div className="text-[11px] text-faint">{ct.label} · {c.warn_threshold != null ? t('pol.thresholdsShort', { warn: c.warn_threshold, err: c.err_threshold }) : ''}</div></div>
                <StatusPill status={c.latest?.status || 'passing'} size="sm" />
                {canChecks && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditCheck(c)}>{t('action.edit')}</Button>}
              </div>); })}</div>
              : <EmptyState icon="check" title={t('pol.noChecks')} msg={canChecks ? t('pol.noChecksMsg') : undefined} />}
          </div>
        )}
        {tab === 'tasks' && (
          <div className="space-y-3">
            {canTasks && <div className="flex justify-end"><Button size="sm" variant="ghost" icon="plus" onClick={() => setEditTask('new')}>{t('pol.addTask')}</Button></div>}
            {tasks.length ? <div className="space-y-2">{tasks.map((task) => (
              <div key={task.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-3">
                <Icon name="tasks" size={15} className="text-muted" />
                <div className="flex-1 min-w-0"><div className="text-[13px] text-ink font-medium truncate">{task.name}</div><div className="text-[11px] text-faint">{task.schedule_kind === 'daily' ? t('tasks.dailyAt', { time: minuteToHHMM(task.run_at_minute) }) : t('tasks.every', { interval: fmtInterval(task.interval_sec) })}</div></div>
                <Toggle checked={task.enabled} onChange={() => setConfirmToggleTask(task)} disabled={!canTasks} />
                {canTasks && <Button size="sm" variant="ghost" icon="edit" onClick={() => setEditTask(task)}>{t('action.edit')}</Button>}
              </div>))}</div>
              : <EmptyState icon="tasks" title={t('pol.noTasks')} msg={canTasks ? t('pol.noTasksMsg') : undefined} />}
          </div>
        )}
        {tab === 'assign' && (
          <div className="space-y-4">
            <div className="flex items-start gap-2.5 rounded-xl border border-brand/25 bg-brandsoft px-3.5 py-3"><Icon name="info" size={16} className="text-brandhi mt-0.5 shrink-0" /><div className="text-[12.5px] text-ink leading-relaxed">{t('pol.assignInfo')}</div></div>
            {can('policies.manage') && (
              <div className="flex items-center gap-2">
                <Combobox value={addScope} onChange={setAddScope} className="flex-1" placeholder={t('pol.addAssignment')}
                  options={[
                    ...clients.map((c) => ({ value: 'client:' + c.id, label: t('pol.clientAllSites', { name: c.name }), group: t('pol.group.clients') })),
                    ...sites.map((s) => ({ value: 'site:' + s.id, label: s.name, group: t('pol.group.sites') })),
                    ...Object.values(devMap).map((d) => ({ value: 'agent:' + d.id, label: d.hostname, group: t('pol.group.agents') })),
                  ]} />
                <Button variant="primary" icon="plus" onClick={addAssignment} disabled={!addScope}>{t('pol.assign')}</Button>
              </div>
            )}
            <div className="space-y-2">
              {asg.map((a) => (
                <div key={a.id} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-2.5">
                  <span className={cx('inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase', a.scope_type === 'agent' ? 'text-run bg-runsoft border-run/25' : a.scope_type === 'site' ? 'text-warn bg-warnsoft border-warn/25' : 'text-brandhi bg-brandsoft border-brand/25')}>{tEnum('pol.scope', a.scope_type)}</span>
                  <span className="flex-1 text-[13px] text-ink">{scopeName(a) || a.scope_id}</span>
                  {can('policies.manage') && <button onClick={() => removeAssignment(a.id)} className="text-faint hover:text-bad p-1"><Icon name="trash" size={15} /></button>}
                </div>
              ))}
              {asg.length === 0 && <div className="text-center text-[12.5px] text-faint py-4">{t('pol.notAssigned')}</div>}
            </div>
          </div>
        )}
      </div>
      {can('policies.manage') && (
        <div className="px-5 py-3.5 border-t border-line flex justify-end">
          <Button variant="danger" icon="trash" onClick={() => setConfirmDel(true)}>{t('pol.delete')}</Button>
        </div>
      )}
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={del} danger title={t('pol.deleteTitle')} body={t('pol.deleteBody', { name: policy.name })} confirmLabel={t('pol.delete')} />
      {editCheck && <CheckEditor check={editCheck === 'new' ? null : editCheck} onClose={() => setEditCheck(null)}
        devMap={devMap} policies={policiesQ.data?.policies || []} defaultPolicyId={policy.id} />}
      {editTask && <TaskEditor task={editTask === 'new' ? null : editTask} onClose={() => setEditTask(null)}
        devMap={devMap} policies={policiesQ.data?.policies || []} scripts={scriptsQ.data?.scripts || []} defaultPolicyId={policy.id} />}
      <ConfirmDialog open={!!confirmToggleTask} onClose={() => setConfirmToggleTask(null)} onConfirm={() => toggleTask(confirmToggleTask)}
        title={confirmToggleTask?.enabled ? t('tasks.disableTitle') : t('tasks.enableTitle')}
        confirmLabel={confirmToggleTask?.enabled ? t('action.disable') : t('action.enable')} danger={!!confirmToggleTask?.enabled}
        body={confirmToggleTask && (confirmToggleTask.enabled
          ? t('pol.taskDisableBody', { name: confirmToggleTask.name })
          : t('pol.taskEnableBody', { name: confirmToggleTask.name }))} />
      <Modal open={!!editing} onClose={() => { if (!savingEdit) setEditing(null); }} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">{t('pol.editTitle')}</div>
          <div className="text-[12px] text-muted mb-4">{t('pol.editHint')}</div>
          <div className="space-y-3">
            <Field label={t('checks.name')}>
              <Input value={editing?.name || ''} autoFocus onChange={(e) => setEditing((s) => ({ ...s, name: e.target.value }))}
                onKeyDown={(e) => { if (e.key === 'Enter') saveEdit(); }} />
            </Field>
            <Field label={t('common.description')} hint={t('pol.editDescHint')}>
              <Input value={editing?.description || ''} placeholder={t('pol.descPlaceholder')}
                onChange={(e) => setEditing((s) => ({ ...s, description: e.target.value }))}
                onKeyDown={(e) => { if (e.key === 'Enter') saveEdit(); }} />
            </Field>
          </div>
          <div className="flex justify-end gap-2 mt-5">
            <Button variant="ghost" onClick={() => setEditing(null)} disabled={savingEdit}>{t('action.cancel')}</Button>
            <Button variant="primary" loading={savingEdit} disabled={!editing?.name.trim()} onClick={saveEdit}>{t('pol.saveChanges')}</Button>
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

  const addClient = async () => { await api.createClient(name); setName(''); setCreatingClient(false); toast.success(t('clients.created')); };

  return (
      <Page wide>
        <PageHeader title={t('title.clients')} subtitle={t('clients.subtitle')}
        actions={can('clients.manage') && <Button variant="primary" icon="plus" onClick={() => setCreatingClient(true)}>{t('clients.new')}</Button>} />
      <div className="grid lg:grid-cols-[360px_1fr] gap-5">
        <div className="space-y-3">
          <div className="relative">
            <Icon name="search" size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('clients.search')} className="pl-9 h-9" />
          </div>
          {visibleClients.map((c) => <ClientNode key={c.id} client={c} sites={sitesFor(c)} groups={groups} devices={devices} sel={sel} setSel={setSel} selDept={selDept} setSelDept={setSelDept} siteCount={siteCount} clientCount={clientCount} />)}
          {visibleClients.length === 0 && <Card><EmptyState icon="building" title={ql ? t('clients.noMatches') : t('clients.none')} /></Card>}
        </div>
        <div>{selSite ? <SiteDetail site={selSite} client={selClient} devices={selDevices} siteDeviceTotal={window.DeviceBulk.siteDeviceCount(devices, sel)} clients={clients} sites={sites} query={ql} focusDept={selDept} /> : <Card className="h-full"><EmptyState icon="pin" title={t('clients.selectSite')} msg={t('clients.selectSiteMsg')} /></Card>}</div>
      </div>
      <Modal open={creatingClient} onClose={() => setCreatingClient(false)} width="max-w-md"><div className="p-5"><div className="text-[15px] font-semibold text-ink mb-4">{t('clients.new')}</div><Field label={t('clients.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('clients.namePlaceholder')} autoFocus /></Field><div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setCreatingClient(false)}>{t('action.cancel')}</Button><Button variant="primary" onClick={addClient} disabled={!name}>{t('action.create')}</Button></div></div></Modal>
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
  const addSite = async () => { await api.createSite(client.id, name); setName(''); setAdding(false); toast.success(t('clients.siteCreated')); };
  const saveRename = async () => { const n = rename.trim(); if (!n || n === client.name) { setRenaming(false); return; } try { await api.updateClient(client.id, n); window.dataBus.emit('org'); toast.success(t('clients.renamed')); setRenaming(false); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const delClient = async () => { try { await api.deleteClient(client.id); window.dataBus.emit('org'); window.dataBus.emit('devices'); window.dataBus.emit('groups'); toast.info(t('clients.deleted')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 px-3.5 py-3">
        <button onClick={() => setOpen((o) => !o)} className="text-faint hover:text-ink"><Icon name={open ? 'chevdown' : 'chevright'} size={15} /></button>
        <Icon name="building" size={16} className="text-muted" />
        <div className="flex-1 min-w-0">
          {renaming
            ? <div className="flex items-center gap-1.5"><Input value={rename} onChange={(e) => setRename(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') saveRename(); if (e.key === 'Escape') setRenaming(false); }} className="h-7" autoFocus /><Button size="sm" variant="primary" onClick={saveRename}>{t('action.save')}</Button><Button size="sm" variant="ghost" onClick={() => setRenaming(false)}>{t('action.cancel')}</Button></div>
            : <div className="text-[13.5px] font-semibold text-ink truncate">{client.name}</div>}
        </div>
        {!renaming && <span className="text-[11px] text-faint">{t('clients.devices', { n: clientCount(client.id) })}</span>}
        {can('clients.manage') && !renaming && (
          <Dropdown align="right" width="w-48" trigger={<button className="text-faint hover:text-ink p-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={16} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(client.name); setRenaming(true); }}>{t('clients.rename')}</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDel(true)}>{t('clients.delete')}</MenuItem>
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
            <div className="flex items-center gap-2 pl-10 pr-3 py-2"><Input value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addSite(); if (e.key === 'Escape') { setAdding(false); setName(''); } }} onBlur={() => { setAdding(false); setName(''); }} placeholder={t('clients.siteName')} className="h-8" autoFocus /><Button size="sm" variant="primary" onMouseDown={(e) => e.preventDefault()} onClick={addSite} disabled={!name}>{t('action.add')}</Button></div>
          ) : <button onClick={() => setAdding(true)} className="w-full flex items-center gap-2 pl-10 pr-3.5 py-2 text-[12px] text-faint hover:text-muted transition"><Icon name="plus" size={13} />{t('clients.addSite')}</button>)}
        </div>
      )}
      <ConfirmDialog open={confirmDel} onClose={() => setConfirmDel(false)} onConfirm={delClient} danger title={t('clients.deleteTitle')} body={t('clients.deleteBody', { name: client.name })} confirmLabel={t('clients.delete')} />
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
  // Which remote-desktop bundle a Mac installs. Signed keeps privacy grants
  // across updates; ad-hoc depends on no certificate but asks again after each
  // one. Signed is the installer's own default, so it adds nothing to the
  // command — only the opt-out is spelled out.
  const [macBundle, setMacBundle] = useState('signed');
  const configQ = useQuery(['config'], () => api.getConfig(), {});
  // Enrollment, updates, and the agent-side relay live on the agents origin in
  // split ingress, not on the console origin serving this screen.
  if (configQ.loading) return <div className="text-[12px] text-faint py-2">{t('enroll.loadingUrl')}</div>;
  const base = configQ.data?.server?.base_url;
  if (!base) return <div className="text-[12px] text-bad py-2">{t('enroll.urlFailed')}</div>;
  const agentsBase = base.replace(/\/$/, '');
  // The installers refuse an HTTPS install without an update key to pin, so a
  // command without one fails on every endpoint it is pasted into. The server
  // publishes its keys (RMM_UPDATE_PUBLIC_KEY); a rotation carries two.
  const keyList = (configQ.data?.server?.update_public_keys || []).join(',');
  const missingKey = /^https:\/\//i.test(agentsBase) && !keyList;
  const keyArg = keyList ? ` \\\n  --update-public-key ${shellQuote(keyList)}` : '';
  const winKeyArg = keyList ? ` -UpdatePublicKey '${keyList}'` : '';
  const siteArg = site ? ` \\\n  --site ${shellQuote(site.name)}` : '';
  const bundleArg = os === 'macos' && macBundle === 'adhoc' ? ' \\\n  --macos-bundle adhoc' : '';
  const nixCmd = `curl -fsSL ${agentsBase}/install.sh | sudo bash -s -- \\\n  --server ${agentsBase} \\\n  --token ${token}${keyArg}${siteArg}${bundleArg}`;
  const winCmd = `${t('enroll.psComment')}\n& ([scriptblock]::Create((iwr -useb ${agentsBase}/install.ps1))) -Server ${agentsBase} -Token ${token}${winKeyArg}`;
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
        <CopyButton value={cmd} label={t('enroll.copyCommand')} size="sm" variant="ghost" />
      </div>
      {/* Wrapped: the Windows one-liner carries the server URL and the token on a
          single line and ran off the right edge, so it had to be scrolled to be
          read — and this is a command people run as Administrator. */}
      <CodeBlock code={cmd} shell={isWin ? 'powershell' : 'bash'} label={t('enroll.installAgent')} showCopy={false} maxHeight={180} wrap />
      {missingKey && (
        <div className="text-[11.5px] text-bad mt-2">{t('enroll.missingKey')}</div>
      )}
      {isWin && <div className="text-[11.5px] text-faint mt-2">{t('enroll.runElevated')}</div>}
      {os === 'macos' && <div className="text-[11.5px] text-faint mt-2">{t('enroll.macSame')}</div>}
      {os === 'macos' && (
        <div className="mt-3">
          <div className="text-[11.5px] text-muted mb-1.5">{t('enroll.bundle')}</div>
          <div className="inline-flex rounded-lg border border-line bg-elevated p-0.5">
            {[{ v: 'signed', l: t('enroll.signed') }, { v: 'adhoc', l: t('enroll.adhoc') }].map((o) => (
              <button key={o.v} onClick={() => setMacBundle(o.v)}
                className={cx('px-3 h-7 rounded-md text-[12px] font-medium transition-colors', macBundle === o.v ? 'bg-card text-ink shadow-sm' : 'text-faint hover:text-muted')}>{o.l}</button>
            ))}
          </div>
          <div className="text-[11.5px] text-faint mt-1.5">
            {macBundle === 'signed' ? t('enroll.signedHint') : t('enroll.adhocHint')}
            {' '}{t('enroll.choiceStays')}
          </div>
        </div>
      )}
    </div>
  );
}

// siteDeviceTotal counts every device in the site; `devices` is narrowed by the
// search box and must not decide whether the site can be deleted.
function SiteDetail({ site, client, devices, siteDeviceTotal, clients, sites, query, focusDept }) {
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
  const [selected, setSelected] = useState(null); // device id -> open the full detail drawer (same as the Devices tab)
  useEffect(() => { setRenaming(false); setRename(site.name); setCreatingDept(false); setNewDept(''); setRenamingDept(null); setEnrollDept(''); }, [site.id]);
  // Clear a generated enrollment command when the selected site/department changes.
  useEffect(() => { setEnrollToken(null); }, [site.id, focusDept]);
  // Move / delete of this site's devices, one or many: tick them, and the two
  // buttons appear. The checkboxes replace the per-device menu that did the
  // same for a single device.
  const canMoveDevices = can('clients.manage');
  const canDeleteDevices = can('devices.delete');
  const canBulk = canMoveDevices || canDeleteDevices;
  const [picked, setPicked] = useState([]);
  const [bulkMoveOpen, setBulkMoveOpen] = useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  useEffect(() => { setPicked([]); }, [site.id]);
  useEffect(() => { setPicked((cur) => window.DeviceBulk.prune(cur, devices.map((d) => d.id))); }, [devices]);
  const visibleIds = (focusDept ? devices.filter((d) => d.group_id === focusDept) : devices).map((d) => d.id);
  const pickedDevices = devices.filter((d) => picked.includes(d.id));
  const tooMany = picked.length > window.DeviceBulk.LIMIT;
  const moveSelected = async (target) => {
    try {
      const r = await api.bulkMoveDevices(picked, target.siteId, target.groupId || null);
      const dest = sites.find((x) => x.id === target.siteId)?.name || t('site.theSite');
      toast.success(t('site.moved'), t('site.nowIn', { devices: window.DeviceBulk.countLabel(r.moved), dest }));
      setBulkMoveOpen(false); setPicked([]);
      window.dataBus.emit('devices'); window.dataBus.emit('org');
    } catch (e) { toast.error(t('site.nothingMoved'), e.message); }
  };
  const deleteSelected = async () => {
    try {
      const r = await api.bulkDeleteDevices(picked, picked.length);
      if (r.failed && r.failed.length) toast.error(t('site.notDeleted'), formatList(r.failed.map((f) => f.hostname || f.agent_id)));
      else toast.success(t('site.devicesDeleted'), window.DeviceBulk.countLabel(r.deleted));
      setPicked([]); window.dataBus.emit('devices'); window.dataBus.emit('org');
    } catch (e) { toast.error(t('site.nothingDeleted'), e.message); }
  };

  const saveRename = async () => { const n = rename.trim(); if (!n || n === site.name) { setRenaming(false); return; } try { await api.updateSite(site.id, n); window.dataBus.emit('org'); toast.success(t('site.renamed')); setRenaming(false); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const addDept = async () => { if (!newDept.trim()) return; try { await api.createGroup({ name: newDept.trim(), site_id: site.id }); setNewDept(''); setCreatingDept(false); window.dataBus.emit('groups'); toast.success(t('site.deptCreated')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const renameDept = async () => {
    const d = renamingDept; if (!d) return;
    const name = d.value.trim();
    if (!name || name === d.name) { setRenamingDept(null); return; }
    try { await api.renameGroup(d.id, name); setRenamingDept(null); window.dataBus.emit('groups'); toast.success(t('site.deptRenamed')); }
    catch (e) { toast.error(t('common.failed'), e.message); }
  };
  const delDept = async () => { const g = confirmDelDept; if (!g) return; try { await api.deleteGroup(g.id); window.dataBus.emit('groups'); window.dataBus.emit('devices'); toast.info(t('site.deptDeleted')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const delSite = async () => { try { await api.deleteSite(site.id); window.dataBus.emit('org'); window.dataBus.emit('devices'); window.dataBus.emit('groups'); toast.info(t('site.deleted')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const genEnroll = async () => {
    setBusy(true);
    const dept = depts.find((g) => g.id === enrollDept);
    try {
      const minted = await api.createEnrollToken({
        description: t('site.enrollFor', { place: site.name + (dept ? ' / ' + dept.name : '') }),
        site_id: site.id, group_id: enrollDept || undefined, uses: 1, ttl_hours: 24,
      });
      setEnrollToken(minted.token);
      toast.success(t('site.tokenCreated'), t('site.agentsLand', { place: dept ? dept.name : site.name }));
    } catch (e) { toast.error(t('common.failed'), e.message); }
    finally { setBusy(false); }
  };

  // Group the site's devices by department; ungrouped devices fall under "No department".
  const deptIds = new Set(depts.map((g) => g.id));
  const unassigned = devices.filter((d) => !d.group_id || !deptIds.has(d.group_id));
  const renderDevice = (d) => (
    <div key={d.id} onClick={() => setSelected(d.id)} title={t('site.openDevice')}
      className={cx(canBulk ? 'grid-cols-[auto_auto_minmax(0,1fr)_minmax(0,1fr)_auto]' : 'grid-cols-[auto_minmax(0,1fr)_minmax(0,1fr)_auto]',
        'grid items-center gap-2.5 rounded-lg border px-3 py-2 cursor-pointer hover:border-line2 hover:bg-elevated/40 transition',
        picked.includes(d.id) ? 'border-brand/40 bg-brandsoft/30' : 'border-line bg-card')}>
      {canBulk && <input type="checkbox" aria-label={t('site.selectDevice', { host: d.hostname })} className="h-4 w-4 accent-brand cursor-pointer"
        checked={picked.includes(d.id)} onClick={(e) => e.stopPropagation()} onChange={() => setPicked((cur) => window.DeviceBulk.toggle(cur, d.id))} />}
      <span className={cx('h-2 w-2 rounded-full', d.status === 'online' ? 'bg-ok' : 'bg-bad')} />
      <div className="min-w-0 flex items-center gap-2"><OsIcon os={d.os} size={14} className="text-muted shrink-0" /><span className="text-[12.5px] text-ink truncate">{d.hostname}</span></div>
      <div className="text-[12px] text-muted truncate">{d.description || <span className="text-faint">{t('site.noDescription')}</span>}</div>
      <span className="text-[11px] text-faint whitespace-nowrap">{timeAgo(d.last_seen)}</span>
    </div>
  );
  return (
    <>
    <Card className="p-5">
      <div className="flex items-start gap-3 mb-4">
        <div className="h-10 w-10 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="pin" size={19} /></div>
        <div className="flex-1 min-w-0">
          {renaming
            ? <div className="flex items-center gap-2"><Input value={rename} onChange={(e) => setRename(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') saveRename(); if (e.key === 'Escape') setRenaming(false); }} className="h-8" autoFocus /><Button size="sm" variant="primary" onClick={saveRename}>{t('action.save')}</Button><Button size="sm" variant="ghost" onClick={() => setRenaming(false)}>{t('action.cancel')}</Button></div>
            : <h2 className="text-[16px] font-semibold text-ink truncate">{site.name}</h2>}
          <div className="text-[12px] text-muted">{t('site.summary', { client: client?.name || '—', n: devices.length })}</div>
        </div>
        {can('clients.manage') && !renaming && (
          <Dropdown align="right" width="w-52" trigger={<button className="text-faint hover:text-ink p-1.5 -mr-1 rounded-lg hover:bg-elevated"><Icon name="dots" size={18} fill /></button>}>
            <MenuItem icon="edit" onClick={() => { setRename(site.name); setRenaming(true); }}>{t('site.rename')}</MenuItem>
            <MenuItem icon="building" onClick={() => setCreatingDept(true)}>{t('site.createDept')}</MenuItem>
            <div className="my-1 border-t border-line" />
            <MenuItem icon="trash" onClick={() => setConfirmDelSite(true)}>{t('site.delete')}</MenuItem>
          </Dropdown>
        )}
      </div>
      <div className="mb-5">
        <div className="flex flex-wrap items-center gap-2 mb-2">
          {canBulk && visibleIds.length > 0 && <input type="checkbox" aria-label={t('site.selectAllShown')} title={t('site.selectAllShown')} className="h-4 w-4 accent-brand cursor-pointer"
            checked={window.DeviceBulk.allVisibleSelected(picked, visibleIds)} onChange={() => setPicked((cur) => window.DeviceBulk.toggleAllVisible(cur, visibleIds))} />}
          <div className="text-[12px] font-semibold text-muted">{t('site.devices')} <span className="text-faint font-normal">· {devices.length}</span>
            {canBulk && picked.length > 0 && <span className="text-ink font-normal"> · {t('site.selected', { n: picked.length })} <button onClick={() => setPicked([])} className="text-faint hover:text-ink underline-offset-2 hover:underline">{t('site.clear')}</button></span>}
          </div>
          <div className="flex-1" />
          {canMoveDevices && picked.length > 0 && <Button size="sm" icon="pin" disabled={tooMany} onClick={() => setBulkMoveOpen(true)}>{t('site.moveN', { n: picked.length })}</Button>}
          {canDeleteDevices && picked.length > 0 && <Button size="sm" variant="danger" icon="trash" disabled={tooMany} onClick={() => setBulkDeleteOpen(true)}>{t('site.deleteN', { n: picked.length })}</Button>}
          {can('clients.manage') && !creatingDept && <button onClick={() => setCreatingDept(true)} className="text-[11.5px] text-faint hover:text-ink flex items-center gap-1"><Icon name="plus" size={12} />{t('site.department')}</button>}
        </div>
        {tooMany && <div className="text-[12px] text-bad mb-2">{t('site.tooMany', { limit: window.DeviceBulk.LIMIT })}</div>}
        {creatingDept && (
          <div className="flex items-center gap-2 mb-2.5">
            <Input value={newDept} onChange={(e) => setNewDept(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addDept(); if (e.key === 'Escape') { setCreatingDept(false); setNewDept(''); } }} placeholder={t('site.deptPlaceholder')} className="h-8" autoFocus />
            <Button size="sm" variant="primary" onClick={addDept} disabled={!newDept.trim()}>{t('action.add')}</Button>
            <Button size="sm" variant="ghost" onClick={() => { setCreatingDept(false); setNewDept(''); }}>{t('action.cancel')}</Button>
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
            return <div className="text-[12.5px] text-faint py-3 text-center">{filtering || focusDept ? t('site.noDevicesHere') : t('site.noDevicesYet')}</div>;
          }
          return (
            <div className="space-y-3.5">
              {showUnassigned && (
                <div className="space-y-1.5">
                  <div className="text-[11px] uppercase tracking-wide text-faint font-semibold px-0.5">{t('site.noDepartment')} <span className="font-normal text-faint/70">· {unassigned.length}</span></div>
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
                      {can('clients.manage') && renamingDept?.id !== g.id && <button onClick={() => setRenamingDept({ id: g.id, name: g.name, value: g.name })} title={t('site.renameNamed', { name: g.name })} className="text-faint hover:text-ink p-0.5"><Icon name="edit" size={12} /></button>}
                      {can('clients.manage') && <button onClick={() => setConfirmDelDept(g)} title={t('site.deleteDept')} className="text-faint hover:text-bad p-0.5"><Icon name="trash" size={12} /></button>}
                    </div>
                    {items.length ? items.map(renderDevice) : <div className="text-[12px] text-faint px-0.5 py-1">{t('site.noDevicesDept')}</div>}
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
            <div className="text-[12px] font-semibold text-muted">{t('site.enrollTitle')}</div>
            {enrollToken && <button onClick={() => setEnrollToken(null)} className="text-[11.5px] text-faint hover:text-ink inline-flex items-center gap-1"><Icon name="close" size={12} />{t('site.hide')}</button>}
          </div>
          {enrollToken ? <EnrollCommands token={enrollToken} site={site} /> : (
            <div className="flex items-center gap-2 flex-wrap">
              {/* The command does not describe the placement — the token does — so
                  the department has to be chosen before the token is minted. */}
              {depts.length > 0 && (
                <Select value={enrollDept} onChange={(e) => setEnrollDept(e.target.value)} className="h-9 w-auto min-w-[190px]">
                  <option value="">{t('site.landIn', { place: site.name })}</option>
                  {depts.map((g) => <option key={g.id} value={g.id}>{t('site.landIn', { place: g.name })}</option>)}
                </Select>
              )}
              <Button variant="secondary" icon="key" loading={busy} onClick={genEnroll}>{t('site.generate')}</Button>
            </div>
          )}
        </div>
      )}
      {/* A site with devices is refused by the server; say so here, before the
          click, instead of letting it come back as an error. */}
      <ConfirmDialog open={confirmDelSite} onClose={() => setConfirmDelSite(false)} onConfirm={delSite} danger title={t('site.deleteTitle')}
        blocked={siteDeviceTotal > 0}
        body={siteDeviceTotal > 0
          ? t(siteDeviceTotal === 1 ? 'site.stillHasOne' : 'site.stillHasMany', { site: site.name, devices: window.DeviceBulk.countLabel(siteDeviceTotal) })
          : t('site.deleteBody', { site: site.name })}
        confirmLabel={t('site.delete')} />
      <ConfirmDialog open={!!confirmDelDept} onClose={() => setConfirmDelDept(null)} onConfirm={delDept} danger title={t('site.deleteDeptTitle')} body={t('site.deleteDeptBody', { name: confirmDelDept?.name || '' })} confirmLabel={t('site.deleteDept')} />
      <DeviceMoveModal open={bulkMoveOpen} onClose={() => setBulkMoveOpen(false)} bulkCount={picked.length}
        device={{ id: 'bulk', hostname: window.DeviceBulk.countLabel(picked.length), site_id: '', group_id: '' }}
        clients={clients} sites={sites} groups={groupsQ.data?.groups || []} onSave={moveSelected} />
      <ConfirmDialog open={bulkDeleteOpen} onClose={() => setBulkDeleteOpen(false)} onConfirm={deleteSelected} danger
        title={t('site.bulkDeleteTitle', { devices: window.DeviceBulk.countLabel(picked.length) })}
        body={<>
          <span>{pickedDevices.length > 8 ? t('site.andMore', { list: pickedDevices.slice(0, 8).map((d) => d.hostname).join(', '), n: pickedDevices.length - 8 }) : formatList(pickedDevices.map((d) => d.hostname))}.</span>
          <span className="block mt-2">{t('site.bulkDeleteWarning')}</span>
        </>}
        requireType={String(picked.length)} confirmLabel={t('site.deleteDevicesConfirm', { devices: window.DeviceBulk.countLabel(picked.length) })} />
    </Card>
    <DeviceDetail id={selected} onClose={() => setSelected(null)} sites={sites} clients={clients} />
    </>
  );
}

// bulkCount > 0 moves several devices at once: there is no current placement to
// compare with, and "Unassigned" is not offered — a bulk move names a site.
function DeviceMoveModal({ open, onClose, device, clients, sites, groups, onSave, bulkCount = 0 }) {
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
  const changed = bulkCount > 0 ? !!siteId : (siteId || '') !== (device.site_id || '') || (groupId || '') !== (device.group_id || '');
  const save = async () => { setBusy(true); try { await onSave({ siteId, groupId }); } finally { setBusy(false); } };

  return (
    <Modal open={open} onClose={onClose} width="max-w-2xl">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="pin" size={18} /></div>
          <div><div className="text-[15px] font-semibold text-ink">{bulkCount > 0 ? t('move.moveBulk', { devices: device.hostname }) : t('move.moveDevice')}</div><div className="text-[12.5px] text-muted">{bulkCount > 0 ? t('move.bulkHint', { devices: device.hostname }) : t('move.hint', { host: device.hostname })}</div></div>
        </div>
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('org.search')} autoFocus />
        <div className="rounded-xl border border-line max-h-80 overflow-y-auto scroll-area p-1">
          {bulkCount === 0 && <button onClick={() => chooseSite('')} className={cx('w-full flex items-center gap-2 rounded-lg px-3 py-2 text-left text-[12.5px] transition', !siteId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="close" size={13} />{t('devices.unassignedTitle')}</button>}
          {visibleClients.map((c) => {
            const clientSites = sites.filter((s) => s.client_id === c.id && (matches(c.name) || matches(s.name) || groupsForSite(s.id).some((g) => matches(g.name))));
            return <div key={c.id} className="mt-1"><div className="px-3 py-1.5 text-[10.5px] uppercase tracking-wide text-faint font-semibold flex items-center gap-1.5"><Icon name="building" size={12} />{c.name}</div>{clientSites.map((s) => {
              const siteGroups = groupsForSite(s.id).filter((g) => matches(c.name) || matches(s.name) || matches(g.name));
              return <div key={s.id}><button onClick={() => chooseSite(s.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-6 pr-3 py-2 text-left text-[12.5px] transition', siteId === s.id && !groupId ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="pin" size={13} /><span className="flex-1 truncate">{s.name}</span><span className="text-[10.5px] text-faint">{t('org.site')}</span></button>{siteGroups.map((g) => <button key={g.id} onClick={() => chooseGroup(s.id, g.id)} className={cx('w-full flex items-center gap-2 rounded-lg pl-11 pr-3 py-1.5 text-left text-[12.5px] transition', groupId === g.id ? 'bg-brandsoft text-ink' : 'text-muted hover:bg-elevated/50')}><Icon name="building" size={12} /><span className="flex-1 truncate">{g.name}</span><span className="text-[10.5px] text-faint">{t('org.department')}</span></button>)}</div>;
            })}</div>;
          })}
          {visibleClients.length === 0 && <div className="px-3 py-8 text-center text-[12px] text-faint">{t('org.noMatches')}</div>}
        </div>
        <div className="flex items-center justify-end gap-2"><Button variant="ghost" onClick={onClose} disabled={busy}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} disabled={!changed} onClick={save}>{bulkCount > 0 ? t('move.moveBulk', { devices: device.hostname }) : t('move.moveDevice')}</Button></div>
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
  const roles = rolesQ.data?.roles || [{ key: 'tech', name: t('tok.technician') }, { key: 'admin', name: t('tok.administrator') }];
  const users = usersQ.data?.users || [];
  const roleLabel = (r) => (r.name ? `${r.key} — ${r.name}` : r.key);
  const expLabel = (tok) => {
    if (!tok.expires_at) return <span className="text-faint">{t('tok.never')}</span>;
    if (new Date(tok.expires_at) <= new Date()) return <span className="text-bad font-medium">{t('tok.expired')}</span>;
    return <span className="text-muted tabular">{fmtTime(tok.expires_at)}</span>;
  };
  const EXPIRY_OPTIONS = [['0', t('tok.never')], ['30', t('tok.days30')], ['90', t('tok.days90')], ['365', t('tok.year1')]];
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
      toast.success(t('tok.created'), t('tok.createdMsg'));
    } catch (e) { toast.error(t('tok.createFailed'), e.message); }
    finally { setBusy(false); }
  };
  const del = async (tok) => { try { await api.deleteAPIToken(tok.id); toast.info(t('tok.revoked'), tok.name); tokensQ.refetch(); } catch (e) { toast.error(t('common.failed'), e.message); } };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">{t('tok.title')} <span className="text-faint font-normal">({tokens.length})</span></div>
        <Button size="sm" variant="primary" icon="plus" onClick={openCreate}>{t('tok.new')}</Button>
      </div>
      <div className="px-4 py-2.5 text-[12px] text-muted bg-elevated/30 border-b border-line flex items-start gap-1.5">
        <Icon name="info" size={13} className="mt-0.5 shrink-0" />
        <span>{t('tok.info')}</span>
      </div>
      {tokens.length === 0
        ? <div className="px-4 py-10 text-center text-[12.5px] text-faint">{t('tok.none')}</div>
        : (
          <table className="w-full text-left">
            <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line"><th className="font-semibold px-4 py-2.5">{t('tok.col.name')}</th><th className="font-semibold px-3 py-2.5">{t('tok.col.role')}</th><th className="font-semibold px-3 py-2.5">{t('tok.col.created')}</th><th className="font-semibold px-3 py-2.5">{t('tok.col.expires')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('alerts.col.actions')}</th></tr></thead>
            <tbody className="divide-y divide-line">
              {tokens.map((tok) => (
                <tr key={tok.id} className="hover:bg-elevated/40 transition">
                  <td className="px-4 py-3"><div className="flex items-center gap-2.5"><div className="h-7 w-7 rounded-lg bg-elevated border border-line flex items-center justify-center text-faint"><Icon name="key" size={14} /></div><span className="text-[13px] text-ink font-medium">{tok.name || <span className="text-faint">{t('tok.unnamed')}</span>}</span></div></td>
                  <td className="px-3 py-3"><RoleBadge role={tok.role} /></td>
                  <td className="px-3 py-3 text-[12px] text-muted tabular">{fmtTime(tok.created_at)}</td>
                  <td className="px-3 py-3 text-[12px]">{expLabel(tok)}</td>
                  <td className="px-4 py-3 text-right"><Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(tok)}>{t('tok.revoke')}</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      <Modal open={creating} onClose={closeModal} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-4">{created ? t('tok.created') : t('tok.newTitle')}</div>
          {created ? (
            <div className="space-y-3">
              <div className="text-[12.5px] text-muted">{t('tok.copyNow')}</div>
              <div className="flex items-center gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2"><Icon name="key" size={14} className="text-warn shrink-0" /><span className="text-[12.5px] mono text-ink flex-1 break-all">{created.token}</span><CopyButton value={created.token} /></div>
              <div className="text-[11.5px] text-faint">{t('tok.createdSummary', { name: created.name || t('tok.unnamed'), role: created.role, expires: created.expires_at ? fmtTime(created.expires_at) : t('tok.neverLower') })}</div>
              <div className="flex justify-end mt-2"><Button variant="primary" onClick={closeModal}>{t('cats.done')}</Button></div>
            </div>
          ) : (
            <div className="space-y-3">
              <Field label={t('tok.col.name')} hint={t('tok.nameHint')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="claude-desktop" autoFocus /></Field>{/* i18n-ignore: an example identifier */}
              <Field label={t('tok.col.role')}><Select value={role} onChange={(e) => setRole(e.target.value)}>{roles.map((r) => <option key={r.key} value={r.key}>{roleLabel(r)}</option>)}</Select></Field>
              {mcpMissing && <div className="text-[11.5px] text-warn flex items-start gap-1.5"><Icon name="alert" size={13} className="mt-0.5 shrink-0" />{t('tok.noMcp')}</div>}
              <Field label={t('tok.bindUser')} hint={t('tok.bindHint')}>
                <Select value={userId} onChange={(e) => setUserId(e.target.value)}>
                  <option value="">{t('tok.none2')}</option>
                  {users.map((u) => <option key={u.id} value={u.id}>{u.username}</option>)}
                </Select>
              </Field>
              <Field label={t('tok.expires')} hint={t('tok.expiresHint')}>
                <Select value={expiresDays} onChange={(e) => setExpiresDays(e.target.value)}>
                  {EXPIRY_OPTIONS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </Select>
              </Field>
              <div className="flex justify-end gap-2 mt-2"><Button variant="ghost" onClick={closeModal} disabled={busy}>{t('action.cancel')}</Button><Button variant="primary" loading={busy} disabled={!name.trim()} onClick={create}>{t('tok.create')}</Button></div>
            </div>
          )}
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => { del(confirmDel); setConfirmDel(null); }} danger title={t('tok.revokeTitle')} body={t('tok.revokeBody', { name: confirmDel?.name || t('tok.thisToken') })} confirmLabel={t('tok.revokeConfirm')} />
    </Card>
  );
}

function SettingsScreen() {
  const { can } = useAuth();
  const [tab, setTab] = useState('users');
  const tabs = [
    { value: 'users', label: t('settings.tab.users') },
    ...(can('roles.manage') ? [{ value: 'roles', label: t('settings.tab.roles') }] : []),
    ...(can('api_tokens.manage') ? [{ value: 'tokens', label: t('settings.tab.tokens') }] : []),
    ...(can('settings.manage') ? [{ value: 'config', label: t('settings.tab.config') }] : []),
    ...(can('settings.manage') ? [{ value: 'email', label: t('settings.tab.email') }] : []),
    ...(can('enrollment.manage') ? [{ value: 'enroll', label: t('settings.tab.enroll') }] : []),
  ];
  return (
    <Page>
      <PageHeader title={t('title.settings')} subtitle={t('settings.subtitle')} />
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
              {l.nativeName}
            </option>
          ))}
        </Select>
      </Field>
      {chosen && !chosen.nativeReviewed && (
        <div className="mt-2 text-[11.5px] text-faint flex items-start gap-1.5">
          <Icon name="info" size={13} className="mt-0.5 shrink-0" />
          {t('lang.reviewHint')}
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
      toast.success(t('pwd.changed'), t('pwd.otherSessions'));
    } catch (e) { toast.error(t('pwd.failed'), e.message); }
    finally { setBusy(false); }
  };

  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="key" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">{t('pwd.title')}</div>
      </div>
      <div className="text-[12.5px] text-muted mb-3">{t('pwd.hint')}</div>
      <div className="space-y-2.5 max-w-md">
        <Field label={t('pwd.current')}><Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
        <Field label={t('auth.new.password')} hint={tooShort ? t('auth.new.tooShort') : undefined}>
          <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" placeholder="••••••••" className={tooShort ? 'border-bad' : ''} />
        </Field>
        <Field label={t('auth.new.confirm')} hint={mismatch ? t('pwd.mismatch') : undefined}>
          <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" placeholder="••••••••"
            onKeyDown={(e) => { if (e.key === 'Enter') submit(); }} className={mismatch ? 'border-bad' : ''} />
        </Field>
        <Button variant="primary" icon="key" loading={busy} disabled={!canSubmit} onClick={submit}>{t('pwd.title')}</Button>
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

  const start = async () => { setBusy(true); try { setSetup(await api.setup2FA(pw)); setPw(''); setCode(''); } catch (e) { toast.error(t('common.failed'), e.message); } finally { setBusy(false); } };
  const activate = async () => { setBusy(true); try { const r = await api.enable2FA(code.trim()); setSetup(null); setCode(''); meQ.refetch(); setRecovery(r.recovery_codes || []); toast.success(t('tfa.enabledToast')); } catch (e) { toast.error(t('tfa.enableFailed'), e.message); } finally { setBusy(false); } };
  const disable = async () => { setBusy(true); try { await api.disable2FA(pw, code.trim()); setPw(''); setCode(''); meQ.refetch(); toast.info(t('tfa.disabledToast')); } catch (e) { toast.error(t('tfa.disableFailed'), e.message); } finally { setBusy(false); } };
  const regen = async () => { setBusy(true); try { const r = await api.regenRecoveryCodes(code.trim()); setCode(''); setRecovery(r.recovery_codes || []); toast.success(t('tfa.regenerated')); } catch (e) { toast.error(t('tfa.regenFailed'), e.message); } finally { setBusy(false); } };

  // downloadCodes saves the recovery codes as a text file (client-side).
  const downloadCodes = () => {
    const text = t('tfa.fileHeader') + '\n' + t('tfa.fileNote') + '\n\n' + (recovery || []).join('\n') + '\n';
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const a = document.createElement('a'); a.href = url; a.download = 'rmm-ai-recovery-codes.txt';
    document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="shield" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">{t('tfa.title')}</div>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md ml-auto', enabled ? 'bg-oksoft text-ok' : 'bg-elevated text-faint')}>{enabled ? t('tfa.on') : t('tfa.off')}</span>
      </div>
      <div className="text-[12.5px] text-muted mb-3">{t('tfa.intro')}</div>

      {recovery && (
        <div className="mb-4 rounded-xl border border-warn/40 bg-warn/[0.04] p-3.5 animate-scale-in">
          <div className="flex items-center gap-2 mb-1.5"><Icon name="key" size={14} className="text-warn" /><span className="text-[12.5px] font-semibold text-ink">{t('tfa.recoveryCodes')}</span></div>
          <div className="text-[11.5px] text-muted mb-2.5">{t('tfa.saveNow')}</div>
          <div className="grid grid-cols-2 gap-x-6 gap-y-1 rounded-lg border border-line bg-app px-3 py-2.5 mono text-[12.5px] text-ink">
            {recovery.map((c, i) => <div key={i} className="tracking-wide">{c}</div>)}
          </div>
          <div className="flex items-center gap-2 mt-3">
            <Button size="sm" variant="secondary" icon="download" onClick={downloadCodes}>{t('action.download')}</Button>
            <CopyButton value={recovery.join('\n')} size="sm" variant="ghost" />
            <Button size="sm" variant="primary" className="ml-auto" onClick={() => setRecovery(null)}>{t('tfa.savedThem')}</Button>
          </div>
        </div>
      )}

      {enabled && !setup && (
        <div className="space-y-2">
          <div className="flex items-end gap-2">
            <Field label={t('tfa.currentCode')} className="flex-1"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" inputMode="numeric" className="mono tracking-widest" /></Field>
            <Button variant="secondary" icon="refresh" loading={busy} disabled={code.trim().length !== 6} onClick={regen} title={t('tfa.newCodesHint')}>{t('tfa.newCodes')}</Button>
          </div>
          <div className="flex items-end gap-2">
            <Field label={t('tfa.accountPassword')} className="flex-1"><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
            <Button variant="danger" loading={busy} disabled={code.trim().length !== 6 || !pw} onClick={disable}>{t('tfa.disable')}</Button>
          </div>
          <div className="text-[11px] text-faint">{t('tfa.enabledHelp')}</div>
        </div>
      )}

      {!enabled && !setup && (
        <div className="flex items-end gap-2 max-w-md">
          <Field label={t('tfa.accountPassword')} className="flex-1"><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" placeholder="••••••••" /></Field>
          <Button variant="primary" icon="shield" loading={busy} disabled={!pw} onClick={start}>{t('tfa.enable')}</Button>
        </div>
      )}

      {setup && (
        <div className="space-y-3 animate-fade-in">
          <div className="flex gap-4 flex-wrap">
            <div className="rounded-xl border border-line bg-white p-2 shrink-0" ref={qrRef} />
            <div className="min-w-0 flex-1">
              <div className="text-[12px] text-muted mb-1">{t('tfa.step1')}</div>
              <div className="flex items-center gap-2 rounded-lg border border-line bg-elevated px-2.5 py-1.5"><span className="text-[12.5px] mono text-ink break-all flex-1">{setup.secret}</span><CopyButton value={setup.secret} size="sm" variant="ghost" /></div>
              <div className="text-[11px] text-faint mt-1.5">{t('tfa.issuer')}</div>
            </div>
          </div>
          <div className="flex items-end gap-2">
            <Field label={t('tfa.step2')} className="flex-1"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" inputMode="numeric" autoFocus className="mono tracking-widest" /></Field>
            <Button variant="ghost" onClick={() => { setSetup(null); setCode(''); }}>{t('action.cancel')}</Button>
            <Button variant="primary" loading={busy} disabled={code.trim().length !== 6} onClick={activate}>{t('tfa.activate')}</Button>
          </div>
        </div>
      )}
    </Card>
  );
}

// Event labels shared by the self-service card and the admin panel, so a rename
// lands in both places at once.
// Labels are t('notify.<event>.title|msg'); an event this build does not know
// shows its raw name and no description.
function notifyLabel(eventType) {
  const title = tEnum('notify', eventType + '.title');
  const known = title !== eventType + '.title';
  return { title: known ? title : eventType, msg: known ? t('notify.' + eventType + '.msg') : '' };
}

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
      toast.success(t('notify.saved'), email ? t('notify.goesTo', { email }) : t('notify.noAddress'));
    } catch (e) { toast.error(t('notify.saveFailed'), e.message); }
    finally { setBusy(false); }
  };

  if (!data || subs === null) return <Skeleton className="h-56 w-full max-w-2xl rounded-2xl mb-5" />;
  return (
    <Card className="p-5 mb-5 max-w-2xl">
      <div className="flex items-center gap-2.5 mb-1">
        <Icon name="bell" size={16} className="text-muted" />
        <div className="text-[14px] font-semibold text-ink">{t('notify.title')}</div>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md ml-auto', email ? 'bg-oksoft text-ok' : 'bg-elevated text-faint')}>{email ? t('notify.on') : t('notify.noAddressBadge')}</span>
      </div>
      <div className="text-[12.5px] text-muted mb-3">{t('notify.scopeNote')}</div>

      {/* Preferences are pointless while nothing can be delivered, so say so up front. */}
      {!data.delivery_configured && (
        <div className="mb-3 flex items-start gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2">
          <Icon name="info" size={14} className="text-warn mt-0.5 shrink-0" />
          <div className="text-[11.5px] text-muted">{t('notify.noMailServer')}</div>
        </div>
      )}

      <div className="space-y-3 max-w-md">
        <Field label={t('notify.yourAddress')} hint={t('notify.yourAddressHint')}>
          <div className="flex items-center gap-2 rounded-lg border border-line bg-app px-3 py-2">
            <Icon name="send" size={14} className="text-faint shrink-0" />
            <span className={cx('text-[12.5px] truncate', email ? 'text-ink mono' : 'text-faint')}>{email || t('notify.noAddress')}</span>
          </div>
        </Field>
      </div>

      <div className="mt-4 space-y-1.5">
        {(data.subscriptions || []).map((s) => (
          <div key={s.event_type} className="flex items-start gap-3 rounded-lg border border-line bg-app px-3 py-2.5">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] font-medium text-ink">{notifyLabel(s.event_type).title}</div>
              <div className="text-[11.5px] text-muted">{notifyLabel(s.event_type).msg}</div>
            </div>
            <div className="pt-0.5"><Toggle checked={!!subs[s.event_type]} disabled={!email} onChange={(v) => setSubs((p) => ({ ...p, [s.event_type]: v }))} /></div>
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between gap-3 mt-4">
        <div className="text-[11px] text-faint">{t('notify.recoveryNote')}</div>
        <Button variant="primary" icon="check" loading={busy} onClick={save}>{t('action.save')}</Button>
      </div>
    </Card>
  );
}

// Mirrors passwordProblem in internal/api/api_auth.go. The server is the
// authority; this exists so the form can say what is missing instead of leaving
// a grey button with no explanation, which is what operators hit.
function passwordProblem(pw) {
  if (!pw) return t('pw.enter');
  if ([...pw].length < 12) return t('pw.min');
  if (!/[A-ZÀ-Þ]/.test(pw)) return t('pw.upper');
  // A symbol is anything that is not a letter, a digit or a space — defined by
  // exclusion so accented and non-Latin keyboards are accepted.
  if (!/[^\p{L}\p{N}\s]/u.test(pw)) return t('pw.symbol');
  return '';
}
function passwordHint(pw) {
  const problem = passwordProblem(pw);
  if (!pw) return t('pw.rule');
  return problem || t('pw.ok');
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
  const roles = rolesQ.data?.roles || [{ key: 'readonly', name: t('users.roleReadonly') }, { key: 'tech', name: t('tok.technician') }, { key: 'admin', name: t('tok.administrator') }];
  const roleLabel = (r) => r.name ? `${r.key} — ${r.name}` : r.key;

  const reset = () => { setUsername(''); setPassword(''); setNewRole('tech'); setScopes([]); setScopeMode('full'); setNewEmail(''); setCreating(false); };
  // "Restricted to…" with nothing selected sends the same empty list as full
  // access, so saving it would quietly grant the fleet. Both forms refuse it.
  const scopeIncomplete = (mode, list) => mode === 'restricted' && list.length === 0;
  const create = async () => {
    try {
      await api.createUser({ username, password, role: newRole, email: newEmail.trim(), scopes: scopes.map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })) });
      reset(); toast.success(t('users.created'), scopes.length ? t('users.scopes', { n: scopes.length }) : t('users.fullAccess'));
    } catch (e) { toast.error(t('common.failed'), e.message); }
  };
  // Setting someone else's address is an onboarding convenience: a technician
  // should hear about their devices before their first sign-in.
  const saveEmail = async () => {
    if (!editEmail) return;
    setEmailBusy(true);
    try {
      await api.setUserEmail(editEmail.user.id, editEmail.value.trim());
      setEditEmail(null); usersQ.refetch();
      toast.success(t('users.addressUpdated'), editEmail.value.trim() ? t('users.nowReceives', { user: editEmail.user.username }) : t('users.alertsOff', { user: editEmail.user.username }));
    } catch (e) { toast.error(t('users.addressFailed'), e.message); }
    finally { setEmailBusy(false); }
  };
  const toggleDisabled = async (u) => {
    try {
      await api.setUserDisabled(u.id, !u.disabled_at);
      setConfirmDisable(null);
      usersQ.refetch();
      toast.success(u.disabled_at ? t('users.enabled') : t('users.disabled'),
        u.disabled_at ? t('users.canSignIn', { user: u.username }) : t('users.signedOut', { user: u.username }));
    } catch (e) { toast.error(t('common.failed'), e.message); }
  };
  const del = async (id) => { try { await api.deleteUser(id); toast.info(t('users.removed')); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const reset2fa = async (u) => { try { await api.reset2FA(u.id); toast.info(t('users.tfaReset'), t('users.tfaResetMsg', { user: u.username })); usersQ.refetch(); } catch (e) { toast.error(t('common.failed'), e.message); } };
  const changeRole = async () => {
    const u = pendingRole?.user, role = pendingRole?.role;
    if (!u || !role || role === u.role) { setPendingRole(null); return; }
    setRoleBusy(u.id);
    try {
      await api.updateUserRole(u.id, role);
      toast.success(t('users.roleUpdated'), t('users.isNow', { user: u.username, role }));
      setPendingRole(null);
      usersQ.refetch();
    } catch (e) { toast.error(t('users.roleFailed'), e.message); }
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
      toast.error(t('users.scopeLoadFailed'), e.message);
      setEditUser(null);
    }
    finally { setEditLoading(false); }
  };
  const saveEdit = async () => {
    setEditSaving(true);
    try {
      await api.setUserScopes(editUser.id, editScopes.map((s) => ({ scope_type: s.scope_type, scope_id: s.scope_id })));
      toast.success(t('users.scopeUpdated'), editScopes.length ? t('users.scopes', { n: editScopes.length }) : t('users.fullAccess'));
      setConfirmScopeSave(false);
      setEditUser(null);
    } catch (e) { toast.error(t('common.failed'), e.message); }
    finally { setConfirmScopeSave(false); setEditSaving(false); }
  };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">{t('users.title')} <span className="text-faint font-normal">({users.length})</span></div>
        {canAdmin && <Button size="sm" variant="primary" icon="plus" onClick={() => setCreating(true)}>{t('users.add')}</Button>}
      </div>
      {!canAdmin && <div className="px-4 py-2.5 text-[12px] text-faint bg-elevated/30 border-b border-line flex items-center gap-1.5"><Icon name="info" size={13} />{t('users.needPerm')}</div>}
      <table className="w-full text-left">
        <thead><tr className="text-[11px] uppercase tracking-wide text-faint border-b border-line"><th className="font-semibold px-4 py-2.5">{t('users.col.user')}</th><th className="font-semibold px-3 py-2.5">{t('users.col.alertsTo')}</th><th className="font-semibold px-3 py-2.5">{t('users.col.role')}</th><th className="font-semibold px-3 py-2.5">{t('users.col.created')}</th><th className="font-semibold px-4 py-2.5 text-right">{t('alerts.col.actions')}</th></tr></thead>
        <tbody className="divide-y divide-line">
          {users.map((u) => (
            <tr key={u.id} className="hover:bg-elevated/40 transition">
              <td className="px-4 py-3"><div className="flex items-center gap-2.5">
                <Avatar name={u.username} size={28} />
                <span className={cx('text-[13px] font-medium', u.disabled_at ? 'text-faint line-through' : 'text-ink')}>{u.username}</span>
                {u.disabled_at && <span className="text-[10.5px] px-1.5 py-0.5 rounded bg-elevated text-faint" title={t('users.disabledAt', { when: fmtTime(u.disabled_at) })}>{t('users.disabledBadge')}</span>}
              </div></td>
              <td className="px-3 py-3 text-[12px] mono">
                {u.email ? <span className="text-muted">{u.email}</span> : <span className="text-faint">{t('users.noAlerts')}</span>}
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
                      <button className="text-faint hover:text-ink p-1.5 -mr-1 rounded-lg hover:bg-elevated" title={t('users.actionsFor', { user: u.username })} aria-label={t('users.actionsFor', { user: u.username })}>
                        <Icon name="dots" size={16} fill />
                      </button>}>
                      {canEmail && <MenuItem icon="send" onClick={() => setEditEmail({ user: u, value: u.email || '' })} hint={u.email ? undefined : t('users.unset')}>{t('users.emailAddress')}</MenuItem>}
                      {u.role !== 'admin' && <MenuItem icon="shield" onClick={() => openEdit(u)}>{t('users.accessScope')}</MenuItem>}
                      {u.totp_enabled && <MenuItem icon="key" onClick={() => setConfirmReset(u)}>{t('users.reset2fa')}</MenuItem>}
                      {/* Withdrawing and deleting access are separated from the
                          rest: they are the two that are painful to undo. */}
                      {u.username !== 'admin' && <div className="my-1 border-t border-line" />}
                      {u.username !== 'admin' && (u.disabled_at
                        ? <MenuItem icon="check" onClick={() => toggleDisabled(u)}>{t('users.enableSignIn')}</MenuItem>
                        : <MenuItem icon="lock" onClick={() => setConfirmDisable(u)}>{t('users.disableSignIn')}</MenuItem>)}
                      {u.username !== 'admin' && <MenuItem icon="trash" danger onClick={() => setConfirmDel(u)}>{t('users.remove')}</MenuItem>}
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
          <div className="text-[15px] font-semibold text-ink mb-4">{t('users.add')}</div>
          <div className="space-y-3">
            <Field label={t('users.username')}><Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="tech@company" autoFocus /></Field>{/* i18n-ignore: an example identifier */}
            <Field label={t('users.password')} hint={passwordHint(password)}>
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
                placeholder={t('users.passwordPlaceholder')}
                className={password.length > 0 && passwordProblem(password) ? 'border-bad' : ''} />
            </Field>
            {/* Offered only to operators who may set it — the backend rejects an
                address from anyone without users.email.manage. */}
            {canEmail && <Field label={t('users.emailAddress')} hint={t('users.emailHint')}><Input type="email" value={newEmail} onChange={(e) => setNewEmail(e.target.value)} placeholder="tech@company.com" autoComplete="off" /></Field>}
            <Field label={t('users.col.role')}><Select value={newRole} onChange={(e) => setNewRole(e.target.value)}>{roles.map((r) => <option key={r.key} value={r.key}>{roleLabel(r)}</option>)}</Select></Field>
            {newRole === 'admin'
              ? <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />{t('users.adminsFull')}</div>
              : <Field label={t('users.accessScope')}><ScopePicker scopes={scopes} setScopes={setScopes} mode={scopeMode} setMode={setScopeMode} /></Field>}
          </div>
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={reset}>{t('action.cancel')}</Button><Button variant="primary" onClick={create}
            disabled={!username || !!passwordProblem(password) || (newRole !== 'admin' && scopeIncomplete(scopeMode, scopes))}
            title={!username ? t('users.enterUsername') : (passwordProblem(password) || (newRole !== 'admin' && scopeIncomplete(scopeMode, scopes) ? t('users.selectScope') : undefined))}>{t('users.create')}</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => del(confirmDel.id)} danger title={t('users.removeTitle')} body={t('users.removeBody', { user: confirmDel?.username || '' })} confirmLabel={t('users.remove')} />
      <Modal open={!!editEmail} onClose={() => { if (!emailBusy) setEditEmail(null); }} width="max-w-md">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">{t('users.emailAddress')}</div>
          <div className="text-[12px] text-muted mb-4">{t('users.emailFor', { user: editEmail?.user?.username || '' })}</div>
          <Field label={t('users.emailAddress')} hint={t('users.emailResetHint')}>
            <Input type="email" value={editEmail?.value || ''} autoFocus autoComplete="off" placeholder="tech@company.com"
              onChange={(e) => setEditEmail((s) => ({ ...s, value: e.target.value }))}
              onKeyDown={(e) => { if (e.key === 'Enter') saveEmail(); }} />
          </Field>
          {/* The reset destination is an authentication factor, so a change to an
              address that already exists is worth a beat of hesitation. */}
          {editEmail && (editEmail.value || '').trim() !== (editEmail.user?.email || '') && (editEmail.user?.email || '') !== '' && (
            <div className="mt-3 flex items-start gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2">
              <Icon name="info" size={14} className="text-warn mt-0.5 shrink-0" />
              <div className="text-[11.5px] text-muted">{t('users.emailMoveWarning')}</div>
            </div>
          )}
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setEditEmail(null)} disabled={emailBusy}>{t('action.cancel')}</Button><Button variant="primary" loading={emailBusy} onClick={saveEmail}>{t('users.saveAddress')}</Button></div>
        </div>
      </Modal>
      <Modal open={!!editUser} onClose={() => { if (!editSaving) { setConfirmScopeSave(false); setEditUser(null); } }} width="max-w-lg">
        <div className="p-5">
          <div className="text-[15px] font-semibold text-ink mb-1">{t('users.accessScope')}</div>
          <div className="text-[12px] text-muted mb-4">{t('users.scopeHint', { user: editUser?.username || '' })}</div>
          {editLoading
            ? <div className="py-8 flex items-center justify-center gap-2 text-faint text-[12.5px]"><Spinner size={14} />{t('users.loadingScope')}</div>
            : <ScopePicker key={editUser?.id} scopes={editScopes} setScopes={setEditScopes} mode={editScopeMode} setMode={setEditScopeMode} />}
          <div className="flex justify-end gap-2 mt-5"><Button variant="ghost" onClick={() => setEditUser(null)} disabled={editSaving}>{t('action.cancel')}</Button><Button variant="primary" loading={editSaving}
            disabled={editLoading || scopeIncomplete(editScopeMode, editScopes)}
            title={scopeIncomplete(editScopeMode, editScopes) ? t('users.selectScope') : undefined}
            onClick={() => setConfirmScopeSave(true)}>{t('users.saveScope')}</Button></div>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirmReset} onClose={() => setConfirmReset(null)} onConfirm={() => reset2fa(confirmReset)} danger title={t('users.reset2faTitle')} body={t('users.reset2faBody', { user: confirmReset?.username || '' })} confirmLabel={t('users.reset2fa')} />
      <ConfirmDialog open={!!pendingRole} onClose={() => setPendingRole(null)} onConfirm={changeRole} title={t('users.changeRoleTitle')} body={t('users.changeRoleBody', { user: pendingRole?.user?.username || '', from: pendingRole?.user?.role || '', to: pendingRole?.role || '' })} confirmLabel={t('users.changeRole')} />
      <ConfirmDialog open={confirmScopeSave} onClose={() => setConfirmScopeSave(false)} onConfirm={saveEdit} title={t('users.saveScopeTitle')} body={t('users.saveScopeBody', { user: editUser?.username || '', scope: editScopes.length ? t('users.selectedScopes', { n: editScopes.length }) : t('users.fullAccess') })} confirmLabel={t('users.saveScope')} />
      <ConfirmDialog open={!!confirmDisable} onClose={() => setConfirmDisable(null)} onConfirm={() => toggleDisabled(confirmDisable)}
        title={t('users.disableTitle')} confirmLabel={t('users.disableConfirm')}
        body={t('users.disableBody', { user: confirmDisable?.username || '' })} />
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

  const cloneRole = (r) => setEditing({ key: '', name: t('roles.copyName', { name: r.name }), description: r.description || '', permissions: r.permissions || [], system: false, _new: true });
  const del = async () => {
    if (!confirmDel) return;
    try { await api.deleteRole(confirmDel.key); window.dataBus.emit('roles'); toast.info(t('roles.deleted')); }
    catch (e) { toast.error(t('roles.deleteFailed'), e.message); }
    finally { setConfirmDel(null); }
  };

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between px-4 h-12 border-b border-line">
        <div className="text-[13px] font-semibold text-ink">{t('roles.title')} <span className="text-faint font-normal">({roles.length})</span></div>
        <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing({ key: '', name: '', description: '', permissions: [], _new: true })}>{t('roles.new')}</Button>
      </div>
      <div className="divide-y divide-line">
        {roles.map((r) => (
          <div key={r.key} className="px-4 py-3 flex items-center gap-3 hover:bg-elevated/30 transition">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2"><RoleBadge role={r.key} /><span className="text-[13px] font-semibold text-ink truncate">{r.name}</span>{r.system && <span className="text-[10.5px] uppercase text-faint border border-line2 rounded px-1.5 py-0.5">{t('roles.system')}</span>}</div>
              <div className="text-[12px] text-muted mt-1 truncate">{r.description || t('pol.noDescription')}</div>
            </div>
            <div className="text-[11.5px] text-faint tabular hidden sm:block">{t('roles.permCount', { n: (r.permissions || []).length })}</div>
            <Button size="sm" variant="ghost" icon="eye" onClick={() => setEditing(r)}>{r.system ? t('roles.view') : t('action.edit')}</Button>
            <Button size="sm" variant="ghost" icon="copy" onClick={() => cloneRole(r)}>{t('roles.clone')}</Button>
            {!r.system && <Button size="sm" variant="ghost" icon="trash" onClick={() => setConfirmDel(r)} />}
          </div>
        ))}
        {!rolesQ.loading && roles.length === 0 && <EmptyState icon="shield" title={t('roles.none')} msg={t('roles.noneMsg')} />}
      </div>
      {editing && <RoleEditor role={editing} roles={roles} permissions={permissions} onClose={() => setEditing(null)} />}
      <ConfirmDialog open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={del} danger title={t('roles.deleteTitle')} body={t('roles.deleteBody', { role: confirmDel?.key || '' })} confirmLabel={t('roles.deleteConfirm')} />
    </Card>
  );
}

// A permission's label and description arrive from the server in English; the
// console shows its own translation when it has one for that permission key, and
// the server's text for a permission this build does not know yet.
function permText(p, field) {
  const key = 'perm.' + p.key + '.' + field;
  const s = t(key);
  return s === key ? (field === 'label' ? p.label : p.description) : s;
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
      window.dataBus.emit('roles'); toast.success(isNew ? t('roles.created') : t('roles.saved')); onClose();
    } catch (e) { toast.error(t('roles.saveFailed'), e.message); }
    finally { setBusy(false); }
  };

  return (
    <Drawer open onClose={onClose} width="max-w-[760px]">
      <div className="px-5 py-4 border-b border-line flex items-center gap-3">
        <div className="h-9 w-9 rounded-xl bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={17} /></div>
        <div className="flex-1"><div className="text-[15px] font-semibold text-ink">{isNew ? t('roles.new') : role.name}</div><div className="text-[12px] text-faint">{readOnly ? t('roles.systemRole') : t('roles.customRole')}</div></div>
        <button onClick={onClose} className="text-faint hover:text-ink p-1"><Icon name="close" size={18} /></button>
      </div>
      <div className="flex-1 overflow-y-auto scroll-area p-5 space-y-4">
        <Field label={t('checks.name')} hint={isNew ? t('roles.keyGenerated', { key: generatedKey }) : t('roles.key', { key: role.key })}><Input value={name} onChange={(e) => setName(e.target.value)} disabled={readOnly} placeholder={t('roles.namePlaceholder')} /></Field>
        <Field label={t('common.description')}><Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={readOnly} placeholder={t('roles.descPlaceholder')} /></Field>
        {Object.entries(groups).map(([group, items]) => (
          <div key={group} className="rounded-xl border border-line bg-card overflow-hidden">
            <div className="px-3.5 py-2.5 border-b border-line text-[12.5px] font-semibold text-ink">{tEnum('perm.group', group)}</div>
            <div className="grid sm:grid-cols-2 gap-0 divide-y sm:divide-y-0 sm:divide-x divide-line">
              {items.map((p) => (
                <label key={p.key} className={cx('flex gap-2.5 px-3.5 py-3 cursor-pointer hover:bg-elevated/30', readOnly && 'cursor-default opacity-75')}>
                  <input type="checkbox" checked={selected.has(p.key)} disabled={readOnly} onChange={() => toggle(p.key)} className="mt-0.5 h-4 w-4 accent-brand" />
                  <span className="min-w-0"><span className="block text-[12.5px] text-ink font-medium">{permText(p, 'label')}</span><span className="block text-[11.5px] text-faint leading-snug">{permText(p, 'desc')}</span><span className="block text-[10.5px] text-faint mono mt-1">{p.key}</span></span>
                </label>
              ))}
            </div>
          </div>
        ))}
      </div>
      {!readOnly && <div className="px-5 py-3.5 border-t border-line flex justify-end gap-2"><Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button><Button variant="primary" icon="check" loading={busy} onClick={save} disabled={!slug(name) || !name}>{t('roles.save')}</Button></div>}
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
    const timer = setTimeout(() => { api.listDevices({ q: q.trim() }).then((r) => setFound(r.devices || [])).catch(() => setFound([])); }, 250);
    return () => clearTimeout(timer);
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
        <label className="flex items-center gap-1.5 cursor-pointer"><input type="radio" checked={!restricted} onChange={() => { setMode('full'); setScopes([]); }} className="accent-brand" />{t('scope.full')}</label>
        <label className="flex items-center gap-1.5 cursor-pointer"><input type="radio" checked={restricted} onChange={() => { setMode('restricted'); if (clients === null) loadClients(); if (groups === null) loadGroups(); }} className="accent-brand" />{t('scope.restricted')}</label>
        {restricted && <span className="ml-auto text-[11px] text-faint">{t('scope.selected', { n: scopes.length })}</span>}
      </div>
      {restricted && (
        <div className="rounded-xl border border-line overflow-hidden">
          <div className="p-2 border-b border-line"><Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('scope.search')} className="text-[12px] h-8" /></div>
          <div className="max-h-72 overflow-y-auto p-1">
            {searching ? (() => {
              if (allSites === null || clients === null || groups === null) return <div className="p-3 text-center text-faint text-[12px]">{t('scope.searching')}</div>;
              const m = (n) => (n || '').toLowerCase().includes(q.trim().toLowerCase());
              const siteObj = {}; allSites.forEach((s) => { siteObj[s.id] = s; });
              const clientObj = {}; clients.forEach((c) => { clientObj[c.id] = c; });
              const devBySite = {}; found.forEach((d) => { (devBySite[d.site_id] = devBySite[d.site_id] || []).push(d); });
              const groupBySite = {}; groups.forEach((g) => { (groupBySite[g.site_id] = groupBySite[g.site_id] || []).push(g); });
              const siteIds = new Set([...Object.keys(devBySite), ...allSites.filter((s) => m(s.name)).map((s) => s.id), ...groups.filter((g) => m(g.name)).map((g) => g.site_id)]);
              const clientIds = new Set(clients.filter((c) => m(c.name)).map((c) => c.id));
              siteIds.forEach((sid) => { const s = siteObj[sid]; if (s) clientIds.add(s.client_id); });
              if (clientIds.size === 0) return <div className="p-3 text-center text-faint text-[12px]">{t('scope.noResults', { q })}</div>;
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
                            return <div key={g.id}><Row depth={2} icon="building" name={g.name} hint={t('org.department')} checked={siteCov || sel.has('group:' + g.id)} disabled={siteCov} onCheck={() => toggle('group', g.id)} />{groupDevices.map((d) => { const cov = deviceCovered(siteCov, d); return <Row key={d.id} depth={3} icon="devices" name={d.hostname} hint={statusLabel(d.status)} checked={cov || sel.has('agent:' + d.id)} disabled={cov} onCheck={() => toggle('agent', d.id)} />; })}</div>;
                          })}
                          {ungroupedDevices.map((d) => { const cov = deviceCovered(siteCov, d); return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={statusLabel(d.status || '') + ' · ' + t('scope.noDepartment')} checked={cov || sel.has('agent:' + d.id)} disabled={cov} onCheck={() => toggle('agent', d.id)} />; })}
                        </div>
                      );
                    })}
                  </div>
                );
              });
            })() : <>
            {clients === null && <div className="p-3 text-center text-faint text-[12px]">{t('common.loading')}</div>}
            {clients && clients.length === 0 && <div className="p-3 text-center text-faint text-[12px]">{t('scope.noCompanies')}</div>}
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
                        return <div key={g.id}><Row depth={2} icon="building" name={g.name} hint={t('org.department')} checked={siteCovered || sel.has('group:' + g.id)} disabled={siteCovered} onCheck={() => toggle('group', g.id)} />{groupDevices.map((d) => { const covered = deviceCovered(siteCovered, d); return <Row key={d.id} depth={3} icon="devices" name={d.hostname} hint={statusLabel(d.status)} checked={covered || sel.has('agent:' + d.id)} disabled={covered} onCheck={() => toggle('agent', d.id)} />; })}</div>;
                      })}
                      {expS[st.id] && ungroupedDevices.map((d) => {
                        const covered = deviceCovered(siteCovered, d);
                        return <Row key={d.id} depth={2} icon="devices" name={d.hostname} hint={statusLabel(d.status || '') + ' · ' + t('scope.noDepartment')} checked={covered || sel.has('agent:' + d.id)} disabled={covered} onCheck={() => toggle('agent', d.id)} />;
                      })}
                      {expS[st.id] && siteGroups.length === 0 && siteDevices.length === 0 && <div className="tree-empty-depth-2 text-[11px] text-faint py-1">{t('scope.nothingUnder')}</div>}
                    </div>
                  );
                })}
                {expC[c.id] && (sitesBy[c.id] || []).length === 0 && <div className="tree-empty-depth-1 text-[11px] text-faint py-1">{t('scope.noSites')}</div>}
              </div>
            ))}
            </>}
          </div>
        </div>
      )}
      {restricted && scopes.length === 0
        ? <div className="text-[11px] text-bad flex items-center gap-1.5"><Icon name="alert" size={12} />{t('scope.mustSelect')}</div>
        : <div className="text-[11px] text-faint">{t('scope.union')}</div>}
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
      toast.success(t('cfg.saved'), t('cfg.savedMsg'));
    } catch (e) { toast.error(t('cfg.saveFailed'), e.message); }
    finally { setSaving(false); }
  };
  if (!c) return <div className="space-y-3">{Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-2xl" />)}</div>;
  if (!form) return null;
  return (
    <div className="grid md:grid-cols-2 gap-4">
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-brandsoft border border-brand/25 flex items-center justify-center text-brandhi"><Icon name="sparkle" size={15} /></div><div className="text-[13px] font-semibold text-ink">{t('cfg.aiProvider')}</div><span className="ml-auto"><StatusPill status={c.ai.configured ? 'online' : 'warning'} label={c.ai.configured ? t('cfg.configured') : t('cfg.missingKey')} size="sm" /></span></div>
        <div className="space-y-3">
          <Field label={t('cfg.provider')}><Input value={form.ai_provider} disabled={!can('settings.manage')} onChange={(e) => set('ai_provider', e.target.value)} placeholder="openai-compatible" /></Field>{/* i18n-ignore: a provider identifier */}
          <Field label={t('cfg.baseUrl')}><Input className="mono" value={form.ai_base_url} disabled={!can('settings.manage')} onChange={(e) => set('ai_base_url', e.target.value)} placeholder="https://api.openai.com/v1" /></Field>
          <Field label={t('cfg.model')}><Input className="mono" value={form.ai_model} disabled={!can('settings.manage')} onChange={(e) => set('ai_model', e.target.value)} placeholder="gpt-4o-mini" /></Field>{/* i18n-ignore: a model identifier */}
          <Field label={t('cfg.apiKey')} hint={c.ai.api_key_set ? t('cfg.keySet') : t('cfg.writeOnly')}><Input type="password" value={form.ai_api_key} disabled={!can('settings.manage')} onChange={(e) => set('ai_api_key', e.target.value)} placeholder={c.ai.api_key_set ? '••••••••••••••••' : 'sk-...'} autoComplete="new-password" /></Field>
        </div>
      </Card>
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="remote" size={15} /></div><div className="text-[13px] font-semibold text-ink">{t('cfg.remote')}</div></div>
        <div className="space-y-3">
          <Field label={t('cfg.defaultProvider')}><Select value={form.remote_default} disabled={!can('settings.manage')} onChange={(e) => set('remote_default', e.target.value)}>{c.remote.providers.map((p) => <option key={p} value={p}>{p}</option>)}</Select></Field>
          <Field label="MeshCentral" hint={t('cfg.meshHint')}><Select value={form.meshcentral_enabled ? 'on' : 'off'} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_enabled', e.target.value === 'on')}><option value="on">{t('cfg.enabled')}</option><option value="off">{t('cfg.disabled')}</option></Select></Field>
          <Field label={t('cfg.meshUrl')}><Input className="mono" value={form.meshcentral_url} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_url', e.target.value)} placeholder="https://mesh.example.com" /></Field>
          <Field label={t('cfg.meshUser')}><Input value={form.meshcentral_user} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_user', e.target.value)} placeholder="admin" /></Field>{/* i18n-ignore: an account name */}
          <Field label={t('cfg.meshToken')} hint={c.remote.meshcentral_token_set ? t('cfg.meshTokenSet') : t('cfg.meshTokenHint')}><Input type="password" value={form.meshcentral_token_key} disabled={!can('settings.manage')} onChange={(e) => set('meshcentral_token_key', e.target.value)} placeholder={c.remote.meshcentral_token_set ? '••••••••••••••••' : 'hex loginTokenKey'} autoComplete="new-password" /></Field>{/* i18n-ignore: MeshCentral's own field name */}
          <Field label="RustDesk" hint={t('cfg.rustdeskHint')}><Select value={form.rustdesk_enabled ? 'on' : 'off'} disabled={!can('settings.manage')} onChange={(e) => set('rustdesk_enabled', e.target.value === 'on')}><option value="off">{t('cfg.disabled')}</option><option value="on">{t('cfg.enabled')}</option></Select></Field>
        </div>
      </Card>
      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3"><div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="shield" size={15} /></div><div className="text-[13px] font-semibold text-ink">{t('cfg.server')}</div></div>
        <ConfigItem label={t('cfg.version')} value={c.server.version} mono /><ConfigItem label={t('cfg.agentsUrl')} value={c.server.base_url} mono />
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />{t('cfg.secretsNote')}</div>
          {can('settings.manage') && <Button variant="primary" icon="check" loading={saving} onClick={save}>{t('cfg.save')}</Button>}
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
// with RMM_SMTP_* in the server environment and has no form, so a settings
// administrator cannot repoint authentication mail. Alerts are the tenant's own:
// they leave only through the server set here, never the account transport, and
// with no server alert mail is off.
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

  const save = async () => {
    setSaving(true);
    try {
      const body = { ...form, port: Number(form.port) || 587 };
      await api.updateAlertSMTPConfig(body);
      set('password', ''); cfgQ.refetch();
      toast.success(t('mail.saved'), body.host ? t('mail.ownRelay') : t('mail.alertsOffSaved'));
    } catch (e) { toast.error(t('mail.saveFailed'), e.code === 'alerts_need_own_server' ? t('mail.needOwnServer') : e.message); }
    finally { setSaving(false); }
  };
  // The test sends with the STORED settings, so unsaved edits are not covered.
  const test = async () => {
    setTesting(true);
    try {
      const r = await api.testAlertSMTP(testTo.trim());
      toast.success(t('mail.testSent'), t('mail.deliveredTo', { to: r.to }));
    } catch (e) { toast.error(t('mail.testFailed'), e.message); }
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
            {t('mail.ownInfo')}
            {c.enabled ? <> {t('mail.sendingAs', { from: c.from })}</> : <> {t('mail.alertsOff')}</>}
          </div>
        </div>
      }
      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="send" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">{t('mail.server')}</div>
          <span className="ml-auto"><StatusPill status={c.enabled ? 'online' : 'warning'} label={c.enabled ? t('cfg.configured') : t('mail.notConfigured')} size="sm" /></span>
        </div>
        <div className="space-y-3">
          <Field label={t('mail.host')} hint={t('mail.hostHint')}>
            <Input className="mono" value={form.host} onChange={(e) => set('host', e.target.value)} placeholder={t('mail.hostPlaceholder')} />
          </Field>
          <Field label={t('mail.port')}><Input className="mono" type="number" value={form.port} onChange={(e) => set('port', e.target.value)} placeholder="587" /></Field>
          <Field label={t('mail.encryption')} hint={t('mail.encryptionHint')}>
            <Select value={form.tls_mode} onChange={(e) => set('tls_mode', e.target.value)}>
              <option value="starttls">STARTTLS</option>
              <option value="tls">{t('mail.tlsImplicit')}</option>
              <option value="none">{t('mail.none')}</option>
            </Select>
          </Field>
          <Field label={t('mail.certCheck')} hint={t('mail.certHint')}>
            <Select value={form.skip_verify ? 'skip' : 'verify'} onChange={(e) => set('skip_verify', e.target.value === 'skip')}>
              <option value="verify">{t('mail.verify')}</option>
              <option value="skip">{t('mail.skip')}</option>
            </Select>
          </Field>
        </div>
      </Card>

      <Card className="p-4">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="user" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">{t('mail.sender')}</div>
        </div>
        <div className="space-y-3">
          <Field label={t('mail.from')} hint={t('mail.fromHint')}>
            <Input className="mono" type="email" value={form.from} onChange={(e) => set('from', e.target.value)} placeholder="alerts@example.com" />
          </Field>
          <Field label={t('mail.fromName')}><Input value={form.from_name} onChange={(e) => set('from_name', e.target.value)} placeholder={t('mail.fromNamePlaceholder')} /></Field>
          <Field label={t('mail.username')} hint={t('mail.usernameHint')}><Input className="mono" value={form.username} onChange={(e) => set('username', e.target.value)} placeholder="rmm@example.com" autoComplete="off" /></Field>
          <Field label={t('mail.password')} hint={c.password_set ? t('mail.passwordSet') : t('cfg.writeOnly')}>
            <Input type="password" value={form.password} onChange={(e) => set('password', e.target.value)} placeholder={c.password_set ? '••••••••••••••••' : ''} autoComplete="new-password" />
          </Field>
        </div>
      </Card>

      <Card className="p-4 md:col-span-2">
        <div className="flex items-center gap-2 mb-3">
          <div className="h-8 w-8 rounded-lg bg-elevated border border-line flex items-center justify-center text-muted"><Icon name="bell" size={15} /></div>
          <div className="text-[13px] font-semibold text-ink">{t('mail.test')}</div>
        </div>
        <div className="flex items-end gap-2 flex-wrap">
          <Field label={t('mail.testTo')} className="flex-1 min-w-[240px]" hint={t('mail.testToHint')}>
            <Input type="email" value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="you@example.com" />
          </Field>
          <Button variant="secondary" icon="send" loading={testing} disabled={!c.enabled || dirty} onClick={test}
            title={dirty ? t('mail.saveFirst') : undefined}>{t('mail.sendTest')}</Button>
        </div>
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />{t('mail.queueNote')}</div>
          <Button variant="primary" icon="check" loading={saving} onClick={save}>{t('mail.save')}</Button>
        </div>
      </Card>
    </div>
  );
}

function EnrollPanel({ canAdmin }) {
  const toast = useToast();
  const [token, setToken] = useState(null);
  const [busy, setBusy] = useState(false);
  const gen = async () => { setBusy(true); try { const minted = await api.createEnrollToken({ description: t('enrollp.manual'), uses: 1, ttl_hours: 24 }); setToken(minted); toast.success(t('site.tokenCreated'), t('enrollp.shownOnce')); } finally { setBusy(false); } };
  if (!canAdmin) return <Card><EmptyState icon="shield" title={t('app.denied')} msg={t('enrollp.needPerm')} /></Card>;
  return (
    <Card className="p-5 max-w-2xl">
      <div className="text-[14px] font-semibold text-ink mb-1">{t('enrollp.title')}</div>
      <div className="text-[12.5px] text-muted mb-4">{t('enrollp.intro')}</div>
      <Button variant="primary" icon="key" loading={busy} onClick={gen}>{t('enrollp.create')}</Button>
      {token && (
        <div className="mt-4 space-y-3 animate-fade-in">
          <div className="flex items-center gap-2 rounded-lg border border-warn/25 bg-warnsoft px-3 py-2"><Icon name="key" size={14} className="text-warn" /><span className="text-[12.5px] mono text-ink flex-1 truncate">{token.token}</span><CopyButton value={token.token} /></div>
          <div><div className="text-[12px] font-semibold text-muted mb-2">{t('enrollp.installCommand')}</div>
            <EnrollCommands token={token.token} />
          </div>
          <div className="text-[11.5px] text-faint">{t('enrollp.validity', { hours: token.ttl_hours, uses: token.uses })}</div>
        </div>
      )}
    </Card>
  );
}

window.SettingsScreen = SettingsScreen;
