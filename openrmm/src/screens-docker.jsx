/* Docker snapshots are observations, never container controls. Errors preserve
   the last successful server snapshot, visibly marked stale. */
function dockerMetricBytes(value) {
  if (value == null) return t('docker.noSample');
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0; let n = value;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 }).format(n) + ' ' + units[i];
}

// Update checks run less often than inventory collection. Old or partial reports
// must never imply that the container has been checked and is current.
function dockerUpdateInfo(container, hostStale = false, now = Date.now()) {
  const update = container.update;
  if (!update) return { filter: 'unknown', missing: true, stale: false };
  const checked = Date.parse(update.checked_at);
  const observed = Number.isFinite(checked) && checked - now <= 5 * 60 * 1000;
  const stale = !!(hostStale || container.stale || (observed && now - checked > 26 * 60 * 60 * 1000));
  const fresh = observed && !stale;
  const sameTag = observed && update.status === 'update_available';
  const newerVersion = observed && update.version_status === 'available' && !!update.latest_tag;
  const current = update.status === 'up_to_date' && ['not_found', 'unsupported'].includes(update.version_status);
  return { filter: !fresh ? 'unknown' : sameTag || newerVersion ? 'available' : current ? 'current' : 'unknown',
    fresh, stale, sameTag, newerVersion, major: newerVersion && update.version_kind === 'major',
    checkedAt: observed ? update.checked_at : null };
}

function dockerUpdateErrorHint(code) {
  if (['authentication_required', 'authentication_failed'].includes(code)) return 'docker.update.error.access';
  if (code === 'registry_unsupported') return 'docker.update.error.registry';
  if (code === 'tag_format_unsupported') return 'docker.update.error.tagFormat';
  if (code === 'tag_search_incomplete') return 'docker.update.error.incomplete';
  return null;
}

function dockerMatches(container, query, state, health, update = '', hostStale = false, now = Date.now()) {
  const needle = query.trim().toLowerCase();
  return (!needle || [container.name, container.image, container.compose_project, container.compose_service].some((v) => (v || '').toLowerCase().includes(needle)))
    && (!state || container.state === state) && (!health || container.health === health)
    && (!update || dockerUpdateInfo(container, hostStale, now).filter === update);
}

// Missing observations are not evidence of an empty Docker engine.
function dockerEmptyState(hosts = [], filtered = false) {
  if (filtered) return ['docker.empty', 'docker.emptyHint'];
  if (hosts.length && hosts.every((h) => !h.last_success_at && h.status === 'unknown'))
    return ['docker.awaitingData', 'docker.awaitingDataHint'];
  if (hosts.length && hosts.every((h) => !h.last_success_at && h.status !== 'available'))
    return ['docker.collectionUnavailable', 'docker.collectionUnavailableHint'];
  return ['docker.noContainers', 'docker.noContainersHint'];
}

// Filter changes and overlapping polls must not let an older response replace
// the selected view. Failed HTTP access hides prior data; collection errors are
// normal, authorized 200 responses with last-success metadata from the server.
function useDockerQuery(key, fetcher) {
  const ref = useRef(fetcher); ref.current = fetcher;
  const [state, setState] = useState({ key, loading: true });
  useEffect(() => {
    let closed = false; let serial = 0;
    const load = async () => {
      const current = ++serial;
      try { const data = await ref.current(); if (!closed && current === serial) setState({ key, data, loading: false }); }
      catch (error) { if (!closed && current === serial) setState({ key, error, loading: false }); }
    };
    load(); const timer = setInterval(load, 15000);
    return () => { closed = true; clearInterval(timer); };
  }, [key]);
  return state.key === key ? state : { key, loading: true };
}

function DockerCollection({ host }) {
  return <div className="rounded-lg border border-line px-3 py-2 text-[12px] space-y-1" data-docker-collection={host.status}>
    <div className="flex flex-wrap gap-2 items-center"><strong>{host.description || host.hostname}</strong><span className={host.stale ? 'text-warn' : 'text-ok'}>{t('docker.collection.' + host.status)}</span>{host.stale && host.last_success_at && <span className="text-warn">{t('docker.stale')}</span>}</div>
    {host.error_code && <p className="text-muted">{t('docker.error.' + host.error_code)}</p>}
    <p className="text-faint">{host.last_success_at ? t('docker.lastSuccess', { when: timeAgo(host.last_success_at) }) : t('docker.neverReceived')}{host.last_attempt_at ? ' · ' + t('docker.lastAttempt', { when: timeAgo(host.last_attempt_at) }) : ''}</p>
  </div>;
}

function DockerUpdate({ container, stale }) {
  const info = dockerUpdateInfo(container, stale);
  if (info.missing) return <div className="text-faint max-w-[240px]" data-docker-update="unknown"><span>{t('docker.update.unknown')}</span><p className="text-[10px] mt-1">{t('docker.update.awaiting')}</p></div>;
  const update = container.update;
  const status = ['up_to_date', 'update_available', 'unknown', 'pinned', 'unsupported'].includes(update.status) && (info.checkedAt || ['pinned', 'unsupported'].includes(update.status)) ? update.status : 'unknown';
  const errorHints = [...new Set([update.error_code, update.version_error_code].map(dockerUpdateErrorHint).filter(Boolean))];
  const versionStatus = ['available', 'not_found', 'unknown', 'unsupported'].includes(update.version_status) && (info.checkedAt || update.version_status === 'unsupported') ? update.version_status : 'unknown';
  return <div className="min-w-[180px] max-w-[260px] space-y-1" data-docker-update={info.filter} data-update-stale={info.stale ? 'true' : 'false'}>
    {info.stale && <p className="text-warn text-[10px]">{t('docker.update.stale')}</p>}
    <p className={info.stale ? 'text-muted' : info.sameTag ? 'text-brandhi font-medium' : status === 'up_to_date' ? 'text-ok' : 'text-muted'}>{t('docker.update.status.' + status)}</p>
    {info.newerVersion ? <>
      <p className={info.stale ? 'text-muted' : 'text-brandhi font-medium'}>{t('docker.update.newerVersion')}: <span className="mono break-all">{update.latest_tag}</span></p>
      {info.major && <p className={info.stale ? 'text-muted text-[10px]' : 'text-warn text-[10px]'}>{t('docker.update.major')}</p>}
    </> : <p className="text-faint text-[10px]">{t('docker.update.version.' + (versionStatus === 'available' ? 'unknown' : versionStatus))}</p>}
    {['unknown', 'unsupported', 'pinned'].includes(status) && <p className="text-faint text-[10px]">{t('docker.update.hint.' + status)}</p>}
    {errorHints.map((key) => <p key={key} className="text-faint text-[10px]">{t(key)}</p>)}
    <p className="text-faint text-[10px]">{info.checkedAt ? t('docker.update.checked', { when: timeAgo(info.checkedAt) }) : t('docker.update.neverChecked')}</p>
  </div>;
}

function DockerFilters({ query, setQuery, state, setState, health, setHealth, update, setUpdate }) {
  return <div className="flex flex-wrap gap-2 mb-3">
    <Input aria-label={t('docker.search')} placeholder={t('docker.search')} value={query} onChange={(e) => setQuery(e.target.value)} maxLength={256} className="min-w-0 flex-1 min-w-[180px]" />
    <Select aria-label={t('docker.state')} value={state} onChange={(e) => setState(e.target.value)} className="w-auto"><option value="">{t('docker.allStates')}</option>{['running', 'restarting', 'paused', 'exited', 'created', 'removing', 'dead'].map((v) => <option key={v} value={v}>{t('docker.state.' + v)}</option>)}</Select>
    <Select aria-label={t('docker.health')} value={health} onChange={(e) => setHealth(e.target.value)} className="w-auto"><option value="">{t('docker.allHealth')}</option>{['healthy', 'unhealthy', 'starting', 'none'].map((v) => <option key={v} value={v}>{t('docker.health.' + v)}</option>)}</Select>
    <Select aria-label={t('docker.updates')} value={update} onChange={(e) => setUpdate(e.target.value)} className="w-auto"><option value="">{t('docker.update.all')}</option>{['available', 'current', 'unknown'].map((v) => <option key={v} value={v}>{t('docker.update.filter.' + v)}</option>)}</Select>
  </div>;
}

function DockerContainers({ rows, fleet = false, stale = false, navigate, hosts = [], filtered = false }) {
  if (!rows.length) { const [title, hint] = dockerEmptyState(hosts, filtered); return <EmptyState icon="devices" title={t(title)} msg={t(hint)} />; }
  const columns = [...(fleet ? ['host'] : []), 'container', 'image', 'updates', 'state', 'health', 'cpu', 'memory', 'restarts', 'compose', 'started'];
  return <div className="max-w-full overflow-x-auto rounded-lg border border-line">
    <table className="w-full text-[12px] min-w-[1150px]" aria-label={t('docker.containers')}>
      <thead><tr className="text-left text-faint bg-elevated/50">{columns.map((v) => <th key={v} className="px-3 py-2 font-medium">{t('docker.' + v)}</th>)}</tr></thead>
      <tbody className="divide-y divide-line">{rows.map((c) => <tr key={(c.agent_id || '') + c.id} className={stale || c.stale ? 'text-muted' : 'text-ink'} data-docker-name={c.name} data-stale={stale || c.stale ? 'true' : 'false'}>
        {fleet && <td className="px-3 py-3 max-w-[180px] truncate"><button className="hover:text-brandhi" onClick={() => navigate?.('devices', { id: c.agent_id })}>{c.description || c.hostname}</button></td>}
        <td className="px-3 py-3 max-w-[180px]"><span className="font-medium" title={c.id}>{c.name}</span>{(stale || c.stale) && <div className="text-warn text-[10px]">{t('docker.stale')}</div>}</td>
        <td className="px-3 py-3 max-w-[230px] truncate mono" title={c.image}>{c.image}</td>
        <td className="px-3 py-3"><DockerUpdate container={c} stale={stale || c.stale} /></td>
        <td className="px-3 py-3"><span className={stale || c.stale ? '' : c.state === 'running' ? 'text-ok' : 'text-warn'}>{t('docker.state.' + c.state)}</span></td>
        <td className="px-3 py-3"><span className={!stale && !c.stale && c.health === 'unhealthy' ? 'text-bad' : ''}>{t('docker.health.' + c.health)}</span></td>
        <td className="px-3 py-3 tabular whitespace-nowrap">{c.cpu_percent == null ? t('docker.noSample') : new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 }).format(c.cpu_percent) + '%'}</td>
        <td className="px-3 py-3 tabular whitespace-nowrap">{dockerMetricBytes(c.memory_used_bytes)} / {dockerMetricBytes(c.memory_limit_bytes)}</td>
        <td className="px-3 py-3 tabular">{c.restart_count}</td>
        <td className="px-3 py-3 max-w-[180px] truncate" title={[c.compose_project, c.compose_service].filter(Boolean).join(' / ')}>{[c.compose_project, c.compose_service].filter(Boolean).join(' / ') || t('docker.none')}</td>
        <td className="px-3 py-3 whitespace-nowrap">{c.started_at ? timeAgo(c.started_at) : t('docker.neverStarted')}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

function DeviceDocker({ id }) {
  const [query, setQuery] = useState(''); const [state, setState] = useState(''); const [health, setHealth] = useState(''); const [update, setUpdate] = useState('');
  const result = useDockerQuery('device-docker:' + id, () => api.getDeviceDocker(id));
  if (result.loading) return <Spinner />;
  if (result.error) return <EmptyState title={t('docker.loadFailed')} msg={result.error.message} />;
  const host = result.data;
  return <div className="min-w-0 space-y-3"><DockerCollection host={host} /><p className="text-[12px] text-faint">{t('docker.samplesHint')} {t('docker.update.readOnlyHint')}</p><DockerFilters {...{ query, setQuery, state, setState, health, setHealth, update, setUpdate }} /><DockerContainers rows={(host.containers || []).filter((c) => dockerMatches(c, query, state, health, update, host.stale))} stale={host.stale} hosts={[host]} filtered={!!(query.trim() || state || health || update)} /></div>;
}

function DockerScreen({ navigate }) {
  const [query, setQueryRaw] = useState(''); const [state, setStateRaw] = useState(''); const [health, setHealthRaw] = useState(''); const [update, setUpdateRaw] = useState(''); const [offset, setOffset] = useState(0);
  const setQuery = (v) => { setQueryRaw(v); setOffset(0); }; const setState = (v) => { setStateRaw(v); setOffset(0); }; const setHealth = (v) => { setHealthRaw(v); setOffset(0); };
  const setUpdate = (v) => { setUpdateRaw(v); setOffset(0); };
  const key = JSON.stringify({ query, state, health, update, offset });
  const result = useDockerQuery(key, () => api.listDocker({ q: query, state, health, update, limit: 100, offset }));
  const data = result.data;
  return <div className="p-3 sm:p-5 max-w-[1600px] mx-auto min-w-0">
    <p className="text-muted text-[13px] mb-4">{t('docker.samplesHint')} {t('docker.update.readOnlyHint')}</p>
    <DockerFilters {...{ query, setQuery, state, setState, health, setHealth, update, setUpdate }} />
    {result.loading ? <Spinner /> : result.error ? <EmptyState title={t('docker.loadFailed')} msg={result.error.message} /> : <>
      <div className="flex items-center justify-between gap-2 mb-3 text-[12px] text-muted"><span>{t('docker.total', { count: data.total })}</span><div className="flex gap-2"><Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 100))}>{t('docker.previous')}</Button><Button size="sm" disabled={offset + data.containers.length >= data.total} onClick={() => setOffset(offset + 100)}>{t('docker.next')}</Button></div></div>
      <DockerContainers rows={data.containers} fleet navigate={navigate} hosts={data.hosts} filtered={!!(query.trim() || state || health || update || offset)} />
      <details className="mt-4 rounded-lg border border-line p-3"><summary className="cursor-pointer text-[13px] font-medium">{t('docker.collectionTitle', { count: data.hosts.length })}</summary><div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-2 mt-3">{data.hosts.map((host) => <DockerCollection key={host.agent_id} host={host} />)}</div></details>
    </>}
  </div>;
}

Object.assign(window, { DockerScreen, DeviceDocker });
