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
              <th className="font-semibold px-3 py-2.5">Description</th>
              <th className="font-semibold px-3 py-2.5">OS</th>
              <th className="font-semibold px-3 py-2.5 hidden md:table-cell">Site</th>
              <th className="font-semibold px-3 py-2.5 hidden lg:table-cell">Tags</th>
              <th className="font-semibold px-3 py-2.5">Status</th>
              <th className="font-semibold px-4 py-2.5 text-right">Last seen</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {devicesQ.loading && Array.from({ length: 6 }).map((_, i) => (
              <tr key={i}><td className="px-4 py-3" colSpan={7}><Skeleton className="h-5 w-full" /></td></tr>
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
  const [remote, setRemote] = useState(null); // { deviceId, hostname } for the native viewer
  const [term, setTerm] = useState(null);     // { deviceId, hostname } for the terminal viewer
  const startRemote = async (provider) => {
    if (provider === 'native') { setRemote({ deviceId: id, hostname: d.hostname, os: d.os }); return; } // viewer brokers + reconnects
    setRemoteBusy(true);
    try {
      const s = await api.startRemote(id, provider);
      window.open(s.connect_url, '_blank'); toast.success('Remote session ready', provider + ' opened in a new tab');
    } catch (e) { toast.error('Remote failed', e.message); } finally { setRemoteBusy(false); }
  };
  // Native remote in a standalone browser tab (auth is shared via localStorage).
  const startRemoteNewTab = () => window.open(`${location.origin}/?dev=${encodeURIComponent(id)}&host=${encodeURIComponent(d.hostname)}&os=${encodeURIComponent(d.os || '')}`, '_blank');
  const linkRemote = async (field, label) => {
    const v = window.prompt('Enter the ' + label + ' for this device:');
    if (!v || !v.trim()) return;
    try { await api.setRemoteIds(id, { [field]: v.trim() }); toast.success(label + ' linked'); }
    catch (e) { toast.error('Failed', e.message); }
  };

  const setSite = async (siteId) => {
    try { await api.setDeviceSite(id, siteId || null); toast.success('Site updated'); devQ.refetch(); }
    catch (e) { toast.error('Failed to set site', e.message); }
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
                {can('tech') && <MenuItem icon="terminal" onClick={() => setTerm({ deviceId: id, hostname: d.hostname })} hint="elevated">Terminal</MenuItem>}
                {can('tech') && <MenuItem icon="devices" onClick={() => setRemote({ deviceId: id, hostname: d.hostname, os: d.os, filesOnly: true })}>File transfer</MenuItem>}
                <MenuItem icon="external" onClick={() => startRemote('meshcentral')} disabled={!d.mesh_id} hint={d.mesh_id ? '' : 'no node id'}>MeshCentral</MenuItem>
                {can('admin') && <>
                  <div className="my-1 border-t border-line" />
                  <MenuItem icon="external" onClick={() => linkRemote('mesh_id', 'MeshCentral node id')}>Link MeshCentral node…</MenuItem>
                </>}
              </Dropdown>
              {can('tech') && <Button variant="secondary" icon="terminal" onClick={() => setTab('run')}>Run script</Button>}
              <div className="flex-1" />
              <Gate min="admin">
                <div className="flex items-center gap-2">
                  <span className="text-[11.5px] text-faint flex items-center gap-1"><Icon name="pin" size={13} />Site</span>
                  <Select value={d.site_id || ''} onChange={(e) => setSite(e.target.value)} className="w-auto min-w-[160px] h-8">
                    <option value="">— Unassigned —</option>
                    {clients.map((c) => <optgroup key={c.id} label={c.name}>{sites.filter((s) => s.client_id === c.id).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</optgroup>)}
                  </Select>
                </div>
              </Gate>
            </div>
          </div>

          <Tabs className="px-5" value={tab} onChange={setTab} tabs={[{ value: 'inventory', label: 'Inventory' }, { value: 'jobs', label: 'Jobs' }, { value: 'run', label: 'Run script' }, { value: 'notes', label: 'Notes' }]} />

          <div className="flex-1 overflow-y-auto scroll-area p-5">
            {tab === 'inventory' && <InventoryView id={id} />}
            {tab === 'jobs' && <DeviceJobs id={id} navigate={navigate} onClose={onClose} />}
            {tab === 'run' && <RunScriptForm device={d} onCreated={() => setTab('jobs')} />}
            {tab === 'notes' && <DeviceNotes device={d} />}
          </div>
        </>
      )}
      {!d && <div className="flex-1 flex items-center justify-center"><Spinner size={22} /></div>}
      {remote && <RemoteViewer deviceId={remote.deviceId} hostname={remote.hostname} os={remote.os} filesOnly={remote.filesOnly} onClose={() => setRemote(null)} />}
      {term && <TerminalViewer deviceId={term.deviceId} hostname={term.hostname} onClose={() => setTerm(null)} />}
    </Drawer>
  );
}

// DeviceNotes edits the device's identification fields: Description (shown in the
// device list — e.g. the user's name) and Note (a private observation, only here).
function DeviceNotes({ device }) {
  const { can } = useAuth();
  const toast = useToast();
  const editable = can('tech');
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
        : <div className="text-[12px] text-faint flex items-center gap-1.5"><Icon name="info" size={13} />Tech role needed to edit.</div>}
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
function RemoteViewer({ deviceId, hostname, os, filesOnly, onClose }) {
  const toast = useToast();
  const isWin = os ? /^win/i.test(os) : true; // unknown host => Windows-style (legacy)
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
  const [ver, setVer] = useState(null);  // running sidecar version (staleness check)
  const [notice, setNotice] = useState(null); // host-side notice (e.g. Wayland can't be captured)
  const [chatOpen, setChatOpen] = useState(true); // side chat panel visible
  const [unread, setUnread] = useState(0);
  const openRef = useRef(true);
  useEffect(() => { openRef.current = chatOpen; if (chatOpen) setUnread(0); }, [chatOpen]);

  const nextId = () => (tidRef.current = (tidRef.current + 1) & 0xff);
  const sendJSON = (o) => { const ws = wsRef.current; if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };

  useEffect(() => {
    const cv = canvasRef.current;
    let stopped = false, retries = 0;

    const onMsg = (ev) => {
      if (typeof ev.data === 'string') {
        const m = JSON.parse(ev.data);
        if (m.t === 'hello') setVer(m.v);
        else if (m.t === 'notice') { if (m.text) { setNotice(m.text); toast.info('Remote notice', m.text); } else setNotice(null); }
        else if (m.t === 'chat') { setChat((c) => [...c, { from: m.from, text: m.text }]); if (m.from === 'user' && !openRef.current) setUnread((u) => u + 1); }
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
        drawVideo(ev.data);
      } else if (bytes[0] === CH_FILE) {
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
        if (cv2.width !== fw || cv2.height !== fh) { cv2.width = fw; cv2.height = fh; }
        const ctx = cv2.getContext('2d');
        for (const t of tiles) { if (t) { ctx.drawImage(t.bmp, t.x, t.y); if (t.bmp.close) t.bmp.close(); } }
      }).catch(() => {});
    };

    // Broker a session and connect. On drop (e.g. user logoff tears the session
    // down) re-broker and reconnect — the agent re-spawns the SYSTEM sidecar into
    // the now-active session (login screen included).
    const connect = async () => {
      if (stopped) return;
      setStatus(retries ? 'reconnecting' : 'connecting');
      let wsUrl;
      try {
        const s = await api.startRemote(deviceId, filesOnly ? 'files' : 'native');
        const tok = new URL(s.connect_url, location.origin).searchParams.get('token');
        wsUrl = location.origin.replace(/^http/, 'ws') + '/ws/remote?token=' + encodeURIComponent(tok) + '&role=viewer';
      } catch (e) {
        if (!stopped && retries++ < 40) setTimeout(connect, 3000);
        else setStatus('closed');
        return;
      }
      if (stopped) return;
      const ws = new WebSocket(wsUrl);
      wsRef.current = ws;
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        retries = 0; setStatus(filesOnly ? 'connected' : 'streaming'); setNotice(null);
        // File-transfer-only sessions never stream the screen, so don't ask for a
        // keyframe; jump straight into the browser. Desktop sessions request a frame.
        if (filesOnly) {
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
    const typing = () => { const a = document.activeElement; return a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA'); };
    let lastMove = 0;
    const onMove = (e) => { const t = Date.now(); if (t - lastMove < 33) return; lastMove = t; const p = norm(e); send({ t: 'm', x: p.x, y: p.y }); };
    const onDown = (e) => { e.preventDefault(); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button, down: true }); };
    const onUp = (e) => { e.preventDefault(); const p = norm(e); send({ t: 'b', x: p.x, y: p.y, button: e.button, down: false }); };
    const onWheel = (e) => { e.preventDefault(); send({ t: 'w', dy: e.deltaY }); };
    const onCtx = (e) => e.preventDefault();
    const kd = (e) => { if (typing()) return; e.preventDefault(); send({ t: 'k', code: e.keyCode, down: true }); };
    const ku = (e) => { if (typing()) return; e.preventDefault(); send({ t: 'k', code: e.keyCode, down: false }); };
    // No screen/input wiring in file-transfer-only mode (the canvas isn't shown).
    if (!filesOnly && cv) {
      cv.addEventListener('mousemove', onMove);
      cv.addEventListener('mousedown', onDown);
      cv.addEventListener('mouseup', onUp);
      cv.addEventListener('wheel', onWheel, { passive: false });
      cv.addEventListener('contextmenu', onCtx);
      window.addEventListener('keydown', kd);
      window.addEventListener('keyup', ku);
    }
    connect();
    return () => {
      stopped = true;
      if (wsRef.current) wsRef.current.close();
      window.removeEventListener('keydown', kd);
      window.removeEventListener('keyup', ku);
    };
  }, [deviceId]);

  const sendChat = () => { const t = draft.trim(); if (!t) return; sendJSON({ t: 'chat', from: 'tech', text: t }); setChat((c) => [...c, { from: 'tech', text: t }]); setDraft(''); };
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
  const browse = (path) => { const p = path == null ? rootPath : path; setFb({ path: p, entries: null }); setFbSel([]); sendJSON({ t: 'fls', id: nextId(), path: p }); };
  const downloadEntry = (name) => sendJSON({ t: 'fget', id: nextId(), path: joinPath(fb.path, name) });
  const toggleSel = (name) => setFbSel((s) => s.includes(name) ? s.filter((x) => x !== name) : [...s, name]);
  const doMkdir = () => { const n = window.prompt('New folder name:'); if (n) sendJSON({ t: 'fmkdir', id: nextId(), path: joinPath(fb.path, n) }); };
  const doDelete = () => { if (fbSel.length && window.confirm('Delete ' + fbSel.length + ' item(s) on ' + hostname + '?')) { fbSel.forEach((n) => sendJSON({ t: 'fdel', id: nextId(), path: joinPath(fb.path, n) })); setFbSel([]); } };
  const doRename = () => { if (fbSel.length !== 1) return; const old = fbSel[0]; const n = window.prompt('Rename to:', old); if (n && n !== old) { sendJSON({ t: 'frename', id: nextId(), src: joinPath(fb.path, old), dst: joinPath(fb.path, n) }); setFbSel([]); } };
  const doClip = (op) => { if (fbSel.length) setFbClip({ op, base: fb.path, names: [...fbSel] }); };
  const doPaste = () => { if (!fbClip) return; fbClip.names.forEach((n) => sendJSON({ t: fbClip.op === 'cut' ? 'fmove' : 'fcopy', id: nextId(), src: joinPath(fbClip.base, n), dst: joinPath(fb.path, n) })); if (fbClip.op === 'cut') setFbClip(null); };
  const doDownloadSel = () => fbSel.forEach((n) => { const en = (fb.entries || []).find((e) => e.name === n); if (en && !en.dir) downloadEntry(n); });
  const uploadFile = async (file) => {
    if (!file || !fb) return;
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
  return (
    <div className="fixed inset-0 z-[80] bg-black flex flex-col">
      <div className="flex items-center gap-3 px-4 h-11 bg-elevated border-b border-line shrink-0">
        <Icon name={filesOnly ? 'devices' : 'remote'} size={15} className="text-muted" />
        <span className="text-[13px] font-semibold text-ink">{filesOnly ? `File transfer · ${hostname}` : hostname}</span>
        <span className={cx('text-[11px] px-2 py-0.5 rounded-md', statusCls)}>{status}</span>
        {ver && <span className="text-[10px] text-faint">sidecar v{ver}</span>}
        <div className="ml-auto flex items-center gap-2">
          <input ref={fileInputRef} type="file" className="hidden" onChange={(e) => { uploadFile(e.target.files[0]); e.target.value = ''; }} />
          {!filesOnly && <>
            <Button variant={uac ? 'primary' : 'ghost'} size="sm" icon="shield" onClick={() => sendJSON({ t: 'uac', enable: !uac })} title="Move UAC prompts to the controllable desktop">UAC</Button>
            <Button variant="ghost" size="sm" icon="link" onClick={() => browse(rootPath)}>Files</Button>
            <Button variant={chatOpen ? 'primary' : 'ghost'} size="sm" icon="chat" onClick={() => setChatOpen((o) => !o)} title="Toggle chat panel">Chat{unread ? ` (${unread})` : ''}</Button>
          </>}
          <button onClick={onClose} className="text-faint hover:text-ink p-1.5"><Icon name="close" size={18} /></button>
        </div>
      </div>
      {fb && (
        <div className="absolute inset-0 z-[5] bg-black/55 flex items-center justify-center" onClick={() => filesOnly ? onClose() : setFb(null)}>
          <div className="w-[760px] h-[80vh] bg-card border border-line rounded-xl flex flex-col overflow-hidden shadow-2xl" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2 px-3 h-11 border-b border-line">
              <Icon name="devices" size={15} className="text-muted" />
              <span className="text-[12.5px] font-semibold text-ink">File transfer · {hostname}</span>
              <button onClick={() => filesOnly ? onClose() : setFb(null)} className="ml-auto text-faint hover:text-ink p-1"><Icon name="close" size={16} /></button>
            </div>
            {/* toolbar */}
            <div className="flex items-center gap-0.5 px-2 h-10 border-b border-line bg-elevated/40 flex-wrap">
              <Button variant="ghost" size="sm" icon="chevleft" title="Up" disabled={parentPath(fb.path) === null} onClick={() => browse(parentPath(fb.path))} />
              <Button variant="ghost" size="sm" icon="refresh" title="Refresh" onClick={() => browse(fb.path)} />
              <Button variant="ghost" size="sm" icon="plus" title="New folder" disabled={!fb.path} onClick={doMkdir} />
              <div className="w-px h-5 bg-line mx-1" />
              <Button variant="ghost" size="sm" icon="external" title="Upload here" disabled={!fb.path} onClick={() => fileInputRef.current?.click()} />
              <Button variant="ghost" size="sm" icon="link" title="Download" disabled={!fbSel.length} onClick={doDownloadSel} />
              <Button variant="ghost" size="sm" icon="copy" title="Copy" disabled={!fbSel.length} onClick={() => doClip('copy')} />
              <Button variant="ghost" size="sm" icon="edit" title="Cut" disabled={!fbSel.length} onClick={() => doClip('cut')} />
              <Button variant="ghost" size="sm" icon="script" title="Rename" disabled={fbSel.length !== 1} onClick={doRename} />
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
                    <Icon name={en.dir ? 'pin' : 'script'} size={14} className={en.dir ? 'text-brand' : 'text-faint'} />
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
      <div className="flex-1 flex overflow-hidden">
        {filesOnly ? (
          <div className="flex-1 flex items-center justify-center text-faint text-[12.5px]">
            {fb ? 'File transfer session' : 'Opening file browser…'}
          </div>
        ) : (<>
        <div className="relative flex-1 flex items-center justify-center overflow-hidden p-2">
          <canvas ref={canvasRef} tabIndex={0} className="max-w-full max-h-full outline-none" style={{ objectFit: 'contain', cursor: 'default' }} />
          {notice && (
            <div className="absolute top-3 left-1/2 -translate-x-1/2 max-w-[80%] rounded-lg border border-warn/40 bg-warn/[0.12] backdrop-blur px-3.5 py-2 text-[12px] text-ink flex items-start gap-2 shadow-lg">
              <Icon name="info" size={14} className="text-warn shrink-0 mt-0.5" />
              <span>{notice}</span>
              <button onClick={() => setNotice(null)} className="text-faint hover:text-ink shrink-0"><Icon name="close" size={13} /></button>
            </div>
          )}
        </div>
        {chatOpen && <div className="w-72 shrink-0 border-l border-line bg-bg flex flex-col">
          <div className="px-3 h-9 flex items-center text-[11.5px] font-semibold text-muted border-b border-line">Chat<button onClick={() => setChatOpen(false)} className="ml-auto text-faint hover:text-ink" title="Collapse chat"><Icon name="chevright" size={16} /></button></div>
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
        </div>}
        </>)}
      </div>
    </div>
  );
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
      let wsUrl;
      try {
        const s = await api.startRemote(deviceId, 'terminal');
        const tok = new URL(s.connect_url, location.origin).searchParams.get('token');
        wsUrl = location.origin.replace(/^http/, 'ws') + '/ws/remote?token=' + encodeURIComponent(tok) + '&role=viewer';
      } catch (e) { setStatus('closed'); toast.error('Terminal failed', e.message); return; }
      if (stopped) return;
      const ws = new WebSocket(wsUrl);
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
      <div className="flex items-center gap-3 px-4 h-11 bg-elevated border-b border-line shrink-0">
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
