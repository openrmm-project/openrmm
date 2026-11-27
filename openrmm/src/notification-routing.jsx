/* Shared Client/Site recipient editor. Only managers with complete target access
   can read it; endpoint errors never leave previous account labels on screen. */
function NotificationRoutingPanel({ scopeType, scopeId }) {
  const { can } = useAuth();
  const [snapshot, setSnapshot] = useState(null);
  const [mode, setMode] = useState('inherit');
  const [picked, setPicked] = useState([]);
  const [options, setOptions] = useState([]);
  const [known, setKnown] = useState([]);
  const [search, setSearch] = useState('');
  const [more, setMore] = useState(false);
  const [error, setError] = useState('');
  const [forbidden, setForbidden] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const saving = useRef(false);
  const alive = useRef(true);
  const permitted = can('clients.manage');
  const inaccessible = (e) => e?.status === 401 || e?.status === 403 || e?.status === 404;
  const reject = (e) => {
    if (inaccessible(e)) { setForbidden(true); setSnapshot(null); setOptions([]); setKnown([]); setPicked([]); }
    setError(e?.status === 409 ? t('routing.conflict') : t('routing.failed'));
  };
  const accept = (data) => { setSnapshot(data); setMode(data.local.mode); setPicked(data.local.user_ids || []); setConflict(false); setError(''); };
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    if (!permitted) return;
    let active = true;
    setSnapshot(null); setOptions([]); setKnown([]); setPicked([]); setError(''); setForbidden(false);
    api.getNotificationRouting(scopeType, scopeId).then((data) => { if (active) accept(data); }).catch((e) => { if (active) reject(e); });
    return () => { active = false; };
  }, [scopeType, scopeId, reload, permitted]);
  useEffect(() => {
    if (!permitted || forbidden) return;
    let active = true;
    api.notificationRouteRecipients(scopeType, scopeId, search).then((data) => {
      if (active) { setOptions(data.recipients || []); setKnown((old) => [...new Map([...old, ...(data.recipients || [])].map((u) => [u.id, u])).values()]); setMore(!!data.has_more); }
    }).catch((e) => { if (active) reject(e); });
    return () => { active = false; };
  }, [scopeType, scopeId, search, reload, permitted, forbidden]);
  const changed = snapshot && ((mode === 'selected' && snapshot.local.unavailable_count > 0) || mode !== snapshot.local.mode || [...picked].sort().join(',') !== [...(snapshot.local.user_ids || [])].sort().join(','));
  const valid = mode === 'inherit' || (picked.length > 0 && picked.length <= 200);
  const save = async () => {
    if (!snapshot || forbidden || !permitted || !changed || !valid || saving.current) return;
    saving.current = true; setBusy(true); setError('');
    try {
      const data = await api.updateNotificationRouting(scopeType, scopeId, { expected_version: snapshot.local.version, mode, user_ids: mode === 'selected' ? picked : [] });
      if (alive.current) { accept(data); window.dataBus.emit('notification-routing'); }
    } catch (e) { if (alive.current) { setConflict(e?.status === 409); reject(e); } }
    finally { saving.current = false; if (alive.current) setBusy(false); }
  };
  if (!permitted || forbidden) return <p role="alert" className="text-sm text-muted">{t('routing.denied')}</p>;
  if (!snapshot) return <div className="space-y-3">{error ? <p role="alert">{error}</p> : <Skeleton className="h-24 w-full" />}<Button variant="ghost" onClick={() => setReload((v) => v + 1)}>{t('routing.reload')}</Button></div>;
  const labels = new Map([...(snapshot.local.recipients || []), ...known, ...options].map((u) => [u.id, u]));
  const visible = [...labels.values()].filter((u) => picked.includes(u.id) || options.some((o) => o.id === u.id));
  const effective = snapshot.effective;
  return <div className="space-y-4" data-notification-routing={scopeType + ':' + scopeId}>
    <p className="text-[12px] text-muted">{t('routing.incidents')}</p>
    <div data-routing-effective className="rounded-lg border border-line p-3 text-[12px] space-y-1">
      <div className="font-medium">{t('routing.effective')}</div>
      <p>{effective.mode === 'automatic' ? t('routing.automatic') : t('routing.origin', { name: effective.source_name })}</p>
      {effective.recipients?.length > 0 && <p>{formatList(effective.recipients.map((u) => u.username + ' (' + u.email + ')'))}</p>}
      {effective.mode === 'selected' && !effective.recipients?.length && <p>{t('routing.noEffective')}</p>}
      {effective.unavailable_count > 0 && <p>{t('routing.unavailable', { n: effective.unavailable_count })}</p>}
    </div>
    <Field label={t('routing.mode')}><Select aria-label={t('routing.mode')} value={mode} disabled={busy} onChange={(e) => setMode(e.target.value)}>
      <option value="inherit">{t('routing.inherit')}</option><option value="selected">{t('routing.selected')}</option>
    </Select></Field>
    {mode === 'selected' && <div className="space-y-2">
      <Input aria-label={t('routing.search')} placeholder={t('routing.search')} value={search} maxLength={200} onChange={(e) => setSearch(e.target.value)} />
      <div className="max-h-64 overflow-y-auto rounded-lg border border-line p-2 space-y-1">
        {visible.map((u) => <label key={u.id} className="flex items-start gap-2 p-2 text-[12px] cursor-pointer"><input type="checkbox" checked={picked.includes(u.id)} disabled={busy || (!picked.includes(u.id) && picked.length >= 200)} onChange={(e) => setPicked((old) => e.target.checked ? [...new Set([...old, u.id])] : old.filter((id) => id !== u.id))} /><span className="min-w-0 break-words">{u.username} <span className="text-muted">{u.email}</span></span></label>)}
        {!visible.length && <p className="text-muted text-[12px] p-2">{t('routing.none')}</p>}
      </div>
      <p className="text-[11px] text-faint">{t('routing.limit', { n: picked.length })}</p>
      {more && <p className="text-[12px] text-muted">{t('routing.more')}</p>}
      {snapshot.local.unavailable_count > 0 && <p role="note" className="text-[12px] text-warn">{t('routing.removeUnavailable', { n: snapshot.local.unavailable_count })}</p>}
    </div>}
    <p className="text-[11px] text-faint">{t('routing.eligibility')}</p>
    {error && <p role="alert" className="text-sm text-bad">{error}</p>}
    <div className="flex flex-wrap gap-2">
      <Button variant="primary" disabled={!changed || !valid || busy || conflict} loading={busy} onClick={save}>{t('action.save')}</Button>
      <Button variant="ghost" disabled={busy} onClick={() => setReload((v) => v + 1)}>{t('routing.reload')}</Button>
    </div>
  </div>;
}
window.NotificationRoutingPanel = NotificationRoutingPanel;
