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
  const [remote, setRemote] = useState(null); // { wsUrl, hostname } for the native viewer
  const startRemote = async (provider) => {
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, provider);
      if (provider === 'native') {
        // Build the viewer WS from the browser origin (not the server's public
        // URL, which may be localhost) and the session token.
        const tok = new URL(s.connect_url, location.origin).searchParams.get('token');
        const wsUrl = location.origin.replace(/^http/, 'ws') + '/ws/remote?token=' + encodeURIComponent(tok) + '&role=viewer';
        setRemote({ wsUrl, hostname: d.hostname });
      } else if (provider === 'rustdesk') { toast.info('RustDesk session', 'Triggering ' + s.connect_url); }
      else { window.open(s.connect_url, '_blank'); toast.success('Remote session ready', provider + ' opened in a new tab'); }
    } catch (e) { toast.error('Remote failed', e.message); } finally { setRemoteBusy(false); }
  };
  // Native remote in a standalone browser tab (auth is shared via localStorage;
  // the relay authenticates by the session token in the URL).
  const startRemoteNewTab = async () => {
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, 'native');
      const tok = new URL(s.connect_url, location.origin).searchParams.get('token');
      window.open(`${location.origin}/?remote=${encodeURIComponent(tok)}&host=${encodeURIComponent(d.hostname)}`, '_blank');
    } catch (e) { toast.error('Remote failed', e.message); } finally { setRemoteBusy(false); }
  };
  const linkRemote = async (field, label) => {
    const v = window.prompt('Enter the ' + label + ' for this device:');
    if (!v || !v.trim()) return;
    try { await api.setRemoteIds(id, { [field]: v.trim() }); toast.success(label + ' linked'); }
    catch (e) { toast.error('Failed', e.message); }
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
                <MenuItem icon="remote" onClick={() => startRemote('native')} hint="beta">Native</MenuItem>
                <MenuItem icon="external" onClick={startRemoteNewTab} hint="new tab">Native — new tab</MenuItem>
                <MenuItem icon="external" onClick={() => startRemote('meshcentral')} disabled={!d.mesh_id} hint={d.mesh_id ? '' : 'no node id'}>MeshCentral</MenuItem>
                <MenuItem icon="link" onClick={() => startRemote('rustdesk')} disabled={!d.rustdesk_id} hint={d.rustdesk_id || 'no id'}>RustDesk</MenuItem>
                {can('admin') && <>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="external" onClick={() => linkRemote('mesh_id', 'MeshCentral node id')}>Link MeshCentral node…</MenuItem>
                  <MenuItem icon="link" onClick={() => linkRemote('rustdesk_id', 'RustDesk id')}>Link RustDesk id…</MenuItem>
                </>}
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
      {remote && <RemoteViewer wsUrl={remote.wsUrl} hostname={remote.hostname} onClose={() => setRemote(null)} />}
    </Drawer>
  );
}

// RemoteViewer is the native remote-desktop viewer: it connects to the relay as
// the viewer peer and multiplexes video (JPEG frames), input, chat and file
// transfer over the one socket. Pairs with cmd/rmm-remote.
//   binary [0x01][JPEG]      screen frame   (agent -> viewer)
//   binary [0x02][id][bytes] file chunk     (both directions)
//   text   {"t":...}         input / chat / file control
const CH_VIDEO = 0x01, CH_FILE = 0x02;
function RemoteViewer({ wsUrl, hostname, onClose }) {
  const toast = useToast();
  const canvasRef = useRef(null);
  const wsRef = useRef(null);
  const dlRef = useRef({});   // active downloads: id -> { name, chunks: [] }
  const tidRef = useRef(0);
  const fileInputRef = useRef(null);
  const [status, setStatus] = useState('connecting');
  const [chat, setChat] = useState([]); // { from: 'tech'|'user', text }
  const [draft, setDraft] = useState('');
  const [fb, setFb] = useState(null); // file browser: null=closed; else { path, entries|null }
  const [uac, setUac] = useState(false); // UAC prompts moved to the interactive desktop

  const nextId = () => (tidRef.current = (tidRef.current + 1) & 0xff);
  const sendJSON = (o) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };

  useEffect(() => {
    const cv = canvasRef.current;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    ws.binaryType = 'arraybuffer';
    let closed = false;
    ws.onopen = () => setStatus('streaming');
    ws.onclose = () => { if (!closed) setStatus('closed'); };
    ws.onerror = () => setStatus('error');
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        const m = JSON.parse(ev.data);
        if (m.t === 'chat') setChat((c) => [...c, { from: m.from, text: m.text }]);
        else if (m.t === 'flist') setFb({ path: m.path, entries: m.entries || [] });
        else if (m.t === 'uac') { setUac(!!m.enabled); if (m.err) toast.error('UAC toggle failed', m.err); }
        else if (m.t === 'fmeta') dlRef.current[m.id] = { name: m.name, chunks: [] };
        else if (m.t === 'fend') {
          const d = dlRef.current[m.id];
          if (d && d.chunks) { saveBlob(new Blob(d.chunks), d.name); toast.success('Downloaded', d.name); delete dlRef.current[m.id]; }
        } else if (m.t === 'ferr') toast.error('Transfer failed', m.err || '');
        return;
      }
      const bytes = new Uint8Array(ev.data);
      if (bytes[0] === CH_VIDEO) {
        const url = URL.createObjectURL(new Blob([ev.data.slice(1)], { type: 'image/jpeg' }));
        const img = new Image();
        img.onload = () => {
          if (canvasRef.current) {
            if (cv.width !== img.naturalWidth) { cv.width = img.naturalWidth; cv.height = img.naturalHeight; }
            cv.getContext('2d').drawImage(img, 0, 0);
          }
          URL.revokeObjectURL(url);
        };
        img.src = url;
      } else if (bytes[0] === CH_FILE) {
        const d = dlRef.current[bytes[1]];
        if (d) d.chunks.push(ev.data.slice(2));
      }
    };
    const send = (o) => { if (ws.readyState === 1) ws.send(JSON.stringify(o)); };
    const norm = (e) => { const r = cv.getBoundingClientRect(); return { x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)) }; };
    const typing = () => { const a = document.activeElement; return a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA'); };
    let lastMove = 0;
    const onMove = (e) => { const t = Date.now(); if (t - lastMove < 33) return; lastMove = t; const p = norm(e); send({ t: 'm', x: p.x, y: p.y }); };
    const onDown = (e) => { e.preventDefault(); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button, down: true }); };
    const onUp = (e) => { e.preventDefault(); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button, down: false }); };
    const onWheel = (e) => { e.preventDefault(); send({ t: 'w', dy: e.deltaY }); };
    const onCtx = (e) => e.preventDefault();
    const kd = (e) => { if (typing()) return; e.preventDefault(); send({ t: 'k', code: e.keyCode, down: true }); };
    const ku = (e) => { if (typing()) return; e.preventDefault(); send({ t: 'k', code: e.keyCode, down: false }); };
    cv.addEventListener('mousemove', onMove);
    cv.addEventListener('mousedown', onDown);
    cv.addEventListener('mouseup', onUp);
    cv.addEventListener('wheel', onWheel, { passive: false });
    cv.addEventListener('contextmenu', onCtx);
    window.addEventListener('keydown', kd);
    window.addEventListener('keyup', ku);
    return () => {
      closed = true; ws.close();
      window.removeEventListener('keydown', kd);
      window.removeEventListener('keyup', ku);
    };
  }, [wsUrl]);

  const sendChat = () => { const t = draft.trim(); if (!t) return; sendJSON({ t: 'chat', from: 'tech', text: t }); setChat((c) => [...c, { from: 'tech', text: t }]); setDraft(''); };
  // Remote file browser (RustDesk-style): navigate folders, download a file, or
  // upload into the current folder — no path typing.
  const winJoin = (base, name) => (!base ? name : base.endsWith('\\') ? base + name : base + '\\' + name);
  const parentPath = (p) => {
    if (!p) return null;            // already at "This PC" (drive list)
    const t = p.replace(/\\+$/, '');
    if (/^[A-Za-z]:$/.test(t)) return '';        // drive root -> drive list
    const i = t.lastIndexOf('\\');
    return i <= 2 ? t.slice(0, 2) + '\\' : t.slice(0, i);
  };
  const browse = (path) => { const p = path == null ? '' : path; setFb({ path: p, entries: null }); sendJSON({ t: 'fls', id: nextId(), path: p }); };
  const downloadEntry = (name) => sendJSON({ t: 'fget', id: nextId(), path: winJoin(fb.path, name) });
  const uploadFile = async (file) => {
    if (!file || !fb) return;
    const ws = wsRef.current; if (!ws || ws.readyState !== 1) return;
    const dest = winJoin(fb.path, file.name);
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
  return (
    <div className="fixed inset-0 z-[80] bg-black flex flex-col">
      <div className="flex items-center gap-3 px-4 h-11 bg-elevated border-b border-line shrink-0">
        <Icon name="remote" size={15} className="text-muted" />
        <span className="text-[13px] font-semibold text-ink">{hostname}</span>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{status}</span>
        <div className="ml-auto flex items-center gap-2">
          <input ref={fileInputRef} type="file" className="hidden" onChange={(e) => { uploadFile(e.target.files[0]); e.target.value = ''; }} />
          <Button variant={uac ? 'primary' : 'ghost'} size="sm" icon="shield" onClick={() => sendJSON({ t: 'uac', enable: !uac })} title="Move UAC prompts to the controllable desktop">UAC</Button>
          <Button variant="ghost" size="sm" icon="link" onClick={() => browse('')}>Files</Button>
          <button onClick={onClose} className="text-faint hover:text-ink p-1.5"><Icon name="close" size={18} /></button>
        </div>
      </div>
      {fb && (
        <div className="absolute inset-0 z-[5] bg-black/50 flex items-center justify-center" onClick={() => setFb(null)}>
          <div className="w-[560px] max-h-[72vh] bg-card border border-line rounded-xl flex flex-col overflow-hidden shadow-2xl" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2 px-3 h-11 border-b border-line">
              <Icon name="devices" size={15} className="text-muted" />
              <span className="text-[12.5px] font-semibold text-ink">Files on {hostname}</span>
              <Button variant="ghost" size="sm" icon="external" className="ml-auto" onClick={() => fileInputRef.current?.click()}>Upload here</Button>
              <button onClick={() => setFb(null)} className="text-faint hover:text-ink p-1"><Icon name="close" size={16} /></button>
            </div>
            <div className="flex items-center gap-2 px-3 h-9 border-b border-line bg-elevated/40">
              <button disabled={parentPath(fb.path) === null} onClick={() => browse(parentPath(fb.path))} className="text-muted hover:text-ink disabled:opacity-40"><Icon name="chevleft" size={16} /></button>
              <span className="text-[11.5px] mono text-muted truncate">{fb.path || 'This PC'}</span>
            </div>
            <div className="flex-1 overflow-y-auto min-h-[200px]">
              {fb.entries === null && <div className="p-6 text-center text-faint text-[12px]">Loading…</div>}
              {fb.entries && fb.entries.length === 0 && <div className="p-6 text-center text-faint text-[12px]">Empty folder.</div>}
              {fb.entries && fb.entries.map((en, i) => (
                <button key={i} onClick={() => en.dir ? browse(winJoin(fb.path, en.name)) : downloadEntry(en.name)}
                  className="w-full flex items-center gap-2.5 px-3 py-2 hover:bg-elevated text-left border-b border-line/40">
                  <Icon name={en.dir ? 'pin' : 'script'} size={14} className={en.dir ? 'text-brand' : 'text-faint'} />
                  <span className="text-[12.5px] text-ink flex-1 truncate">{en.name}</span>
                  {!en.dir && <span className="text-[11px] text-faint">{Math.max(1, (en.size / 1024) | 0).toLocaleString()} KB</span>}
                  {!en.dir && <Icon name="link" size={13} className="text-faint" />}
                </button>
              ))}
            </div>
            <div className="px-3 py-2 border-t border-line text-[11px] text-faint">Click a folder to open · a file to download · "Upload here" to send into this folder.</div>
          </div>
        </div>
      )}
      <div className="flex-1 flex overflow-hidden">
        <div className="flex-1 flex items-center justify-center overflow-hidden p-2">
          <canvas ref={canvasRef} tabIndex={0} className="max-w-full max-h-full outline-none" style={{ objectFit: 'contain', cursor: 'default' }} />
        </div>
        <div className="w-72 shrink-0 border-l border-line bg-bg flex flex-col">
          <div className="px-3 h-9 flex items-center text-[11.5px] font-semibold text-muted border-b border-line">Chat</div>
          <div className="flex-1 overflow-y-auto p-3 space-y-2">
            {chat.length === 0 && <div className="text-[11.5px] text-faint">No messages. A chat window opens on {hostname}.</div>}
            {chat.map((m, i) => (
              <div key={i} className={cx('text-[12px] max-w-[85%] rounded-lg px-2.5 py-1.5', m.from === 'tech' ? 'ml-auto bg-brand/15 text-ink' : 'bg-elevated text-ink')}>
                <div className="text-[10px] text-faint mb-0.5">{m.from === 'tech' ? 'You' : hostname}</div>{m.text}
              </div>
            ))}
          </div>
          <div className="p-2 border-t border-line flex gap-1.5">
            <Input value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') sendChat(); }} placeholder="Message…" className="text-[12px]" />
            <Button variant="primary" size="sm" icon="send" onClick={sendChat} disabled={!draft.trim()} />
          </div>
        </div>
      </div>
    </div>
  );
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
            {(net.interfaces || []).length === 0 && <div className="text-[12.5px] text-faint">No network interfaces reported yet.</div>}
          </InventorySection>
          <Card className="p-4 md:col-span-2">
            <div className="flex items-center gap-2 mb-3 text-[12.5px] font-semibold text-ink"><Icon name="grid" size={14} className="text-muted" />Installed software <span className="text-faint font-normal">({sw.length})</span></div>
            <div className="grid sm:grid-cols-2 gap-x-6 gap-y-1.5">
              {sw.map((s, i) => <div key={i} className="flex items-center justify-between border-b border-line/60 pb-1.5"><span className="text-[12.5px] text-ink">{s.name}</span><span className="text-[11.5px] text-faint mono">{s.version}</span></div>)}
            </div>
            {sw.length === 0 && <div className="text-[12.5px] text-faint">No software inventory reported yet.</div>}
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
  const [runNow, setRunNow] = useState(false);
  const scriptsQ = useQuery(['scripts-pick'], () => api.listScripts(), { invalidateOn: ['scripts'] });
  const cfgQ = useQuery(['config'], () => api.getConfig(), {});
  const canDirect = can('admin') && cfgQ.data?.server?.allow_unapproved_jobs;
  const direct = canDirect && runNow;

  if (!can('tech')) return <EmptyState icon="shield" title="Read-only access" msg="Running scripts requires the tech or admin role." />;

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
          : <>This creates an <b>approval-gated job</b>. It is a dry-run preview until a tech or admin approves it — nothing executes on {device.hostname} yet.</>}</div>
      </div>
      {canDirect && (
        <label className="flex items-center gap-2.5 cursor-pointer select-none">
          <input type="checkbox" checked={runNow} onChange={(e) => setRunNow(e.target.checked)} className="h-4 w-4 accent-bad" />
          <span className="text-[12.5px] text-ink">Run immediately (skip approval)</span>
          <span className="text-[11.5px] text-faint">— admin only; enabled on this instance</span>
        </label>
      )}
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
        <Button variant="primary" icon="plus" loading={busy} onClick={submit} disabled={!script.trim()}>{direct ? 'Run now on ' + device.hostname : 'Create approval-gated job'}</Button>
      </div>
    </div>
  );
}

window.DevicesScreen = DevicesScreen;
window.DeviceDetail = DeviceDetail;
window.RemoteViewer = RemoteViewer;
