/* Shared Client > Site > device projection. Input is already authorized,
   filtered and sorted by the screen. Never build branches from fleet totals. */
function fleetTreeRows(rows, orgOf, idOf, closed = {}) {
  const clients = new Map();
  for (const row of rows) {
    const org = orgOf(row) || {};
    // A known site with no resolved client means metadata is unavailable,
    // not that the device is unassigned. Keep those branches distinct.
    const clientUnavailable = !org.client_id && !!org.site_id;
    const clientKey = 'client:' + (org.client_id || (clientUnavailable ? 'unavailable' : 'unassigned'));
    const siteKey = 'site:' + (org.site_id || 'unassigned:' + clientKey);
    if (!clients.has(clientKey)) clients.set(clientKey, {
      kind: 'client', key: clientKey, name: org.client?.trim() || t(org.client_id || clientUnavailable ? 'fleetTree.unknownClient' : 'fleetTree.noClient'), sites: new Map(), count: 0,
    });
    const client = clients.get(clientKey);
    if (!client.sites.has(siteKey)) client.sites.set(siteKey, {
      kind: 'site', key: siteKey, name: org.site?.trim() || t(org.site_id ? 'fleetTree.unknownSite' : 'fleetTree.noSite'), rows: [], count: 0,
    });
    const site = client.sites.get(siteKey);
    site.rows.push({ kind: 'agent', key: 'agent:' + idOf(row), row });
    client.count++; site.count++;
  }
  const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true }) || a.key.localeCompare(b.key);
  const out = [];
  for (const client of [...clients.values()].sort(byName)) {
    const expanded = !closed[client.key];
    out.push({ kind: client.kind, key: client.key, name: client.name, count: client.count, expanded });
    if (!expanded) continue;
    for (const site of [...client.sites.values()].sort(byName)) {
      const expanded = !closed[site.key];
      out.push({ kind: site.kind, key: site.key, name: site.name, count: site.count, expanded });
      if (expanded) out.push(...site.rows);
    }
  }
  return out;
}

function useFleetTree(screen, available, search) {
  const { user } = useAuth();
  const storageKey = 'rmm-fleet-view:' + (user?.id || user?.username || 'anonymous') + ':' + screen;
  const readMode = () => { try { return localStorage.getItem(storageKey) === 'flat' ? 'flat' : 'tree'; } catch (e) { return 'tree'; } };
  const [preference, setPreference] = useState(() => ({ key: storageKey, mode: readMode() }));
  const [manual, setManual] = useState({});
  // Search starts with all ancestors open. Its temporary toggles never overwrite
  // manual expansion, and a different query starts open again. No polling effect.
  const [searched, setSearched] = useState({ query: '', closed: {} });
  const query = search.trim().toLowerCase();
  // Clear temporary toggles on every query transition, including clearing and
  // later repeating the same search. The derived value is open immediately.
  if (searched.query !== query) setSearched({ query, closed: {} });
  const closed = query ? (searched.query === query ? searched.closed : {}) : manual;
  const mode = available ? (preference.key === storageKey ? preference.mode : readMode()) : 'flat';
  const setMode = (value) => {
    setPreference({ key: storageKey, mode: value });
    try { localStorage.setItem(storageKey, value); } catch (e) {}
  };
  const toggle = (key) => {
    if (query) setSearched((s) => ({ query, closed: { ...(s.query === query ? s.closed : {}), [key]: !closed[key] } }));
    else setManual((s) => ({ ...s, [key]: !s[key] }));
  };
  return { mode, setMode, closed, toggle, available };
}

function FleetViewChoice({ tree }) {
  if (!tree.available) return null;
  return <div className="inline-flex gap-1" role="group" aria-label={t('fleetTree.view')}>
    {['flat', 'tree'].map((mode) => <button key={mode} type="button" aria-pressed={tree.mode === mode} onClick={() => tree.setMode(mode)}
      className={cx('h-8 px-2.5 rounded-lg border text-[12px] whitespace-nowrap', tree.mode === mode ? 'border-brand bg-brandsoft text-ink' : 'border-line2 text-muted hover:text-ink')}>{t('fleetTree.' + mode)}</button>)}
  </div>;
}

function FleetGroupRow({ node, colSpan, onToggle }) {
  return <tr data-fleet-group={node.key} className="bg-elevated/50 border-b border-line">
    <td colSpan={colSpan} className={node.kind === 'site' ? 'ps-8 pe-4 py-1.5' : 'px-4 py-2'}>
      <button type="button" aria-expanded={node.expanded} onClick={(event) => { event.stopPropagation(); onToggle(node.key); }}
        className="inline-flex items-center gap-2 text-[12.5px] font-medium text-ink hover:text-brandhi">
        <Icon name={node.expanded ? 'chevdown' : 'chevright'} size={12} />
        <span className="text-faint text-[11px]">{t(node.kind === 'client' ? 'devices.col.client' : 'devices.col.site')}</span>
        <span>{node.name}</span><span className="text-faint tabular">{tn('devices.agentCount', node.count)}</span>
      </button>
    </td>
  </tr>;
}

window.FleetTree = { project: fleetTreeRows, use: useFleetTree, Choice: FleetViewChoice, GroupRow: FleetGroupRow };
