/* screens-reports.jsx — Reports: fleet inventory (software / hardware / machines)
   grouped/filtered by Client → Site → Department, with client-side CSV export.
   Read-only; the backend scope-filters every row to the caller's access scope. */

// fmtBytes renders a byte count as GB (the inventory reports RAM in bytes).
function fmtBytes(b) {
  if (!b) return '—';
  const gb = b / 1e9;
  return (gb >= 100 ? Math.round(gb) : gb.toFixed(1)) + ' GB';
}

// COLUMNS defines, per report type, each column's key (its header is
// t('rep.col.<key>'), in the CSV too — this export is for people, not scripts),
// the plain text used for CSV + search, and an optional rich cell renderer.
const REPORT_COLUMNS = {
  software: [
    { key: 'name', text: (r) => r.name, className: 'text-ink font-medium' },
    { key: 'manufacturer', text: (r) => r.manufacturer },
    { key: 'version', text: (r) => r.version, className: 'mono text-muted' },
    { key: 'hostname', text: (r) => r.hostname },
    { key: 'client', text: (r) => r.client },
    { key: 'site', text: (r) => r.site },
    { key: 'department', text: (r) => r.department },
  ],
  hardware: [
    { key: 'hostname', text: (r) => r.hostname, className: 'text-ink font-medium' },
    { key: 'os', text: (r) => r.os, cell: (r) => <span className="inline-flex items-center gap-1.5"><OsIcon os={r.os} />{r.os}</span> },
    { key: 'arch', text: (r) => r.arch, className: 'mono text-muted' },
    { key: 'manufacturer', text: (r) => r.manufacturer },
    { key: 'model', text: (r) => r.model },
    { key: 'serial_number', text: (r) => r.serial_number, className: 'mono text-muted' },
    { key: 'cpu_model', text: (r) => r.cpu_model },
    { key: 'cpu_cores', text: (r) => (r.cpu_cores || ''), className: 'tabular' },
    { key: 'mem', text: (r) => fmtBytes(r.mem_total_bytes), className: 'tabular' },
    { key: 'gpu', text: (r) => r.gpu },
    { key: 'storage', text: (r) => r.storage },
    { key: 'storage_size', text: (r) => fmtBytes(r.storage_bytes), className: 'tabular' },
    { key: 'bios', text: (r) => r.bios, className: 'mono text-muted' },
    { key: 'platform', text: (r) => [r.platform, r.platform_version].filter(Boolean).join(' ') },
    { key: 'kernel_version', text: (r) => r.kernel_version, className: 'mono text-muted' },
    { key: 'ip', text: (r) => r.ip, className: 'mono text-muted' },
    { key: 'mac', text: (r) => r.mac, className: 'mono text-muted' },
    { key: 'client', text: (r) => r.client },
    { key: 'site', text: (r) => r.site },
    { key: 'department', text: (r) => r.department },
  ],
  machines: [
    { key: 'hostname', text: (r) => r.hostname, className: 'text-ink font-medium' },
    { key: 'status', text: (r) => r.status, cell: (r) => <StatusPill status={r.status} size="sm" /> },
    { key: 'agent_version', text: (r) => r.agent_version, className: 'mono text-muted' },
    { key: 'client', text: (r) => r.client },
    { key: 'site', text: (r) => r.site },
    { key: 'department', text: (r) => r.department },
    { key: 'last_seen', text: (r) => fmtTime(r.last_seen), cell: (r) => timeAgo(r.last_seen) },
  ],
};

const colLabel = (c) => t('rep.col.' + c.key);

// ColumnPicker is a dropdown of checkboxes to show/hide table columns.
function ColumnPicker({ columns, hidden, onToggle }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return;
    const h = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    window.addEventListener('mousedown', h);
    return () => window.removeEventListener('mousedown', h);
  }, [open]);
  const shown = columns.length - hidden.length;
  return (
    <div className="relative" ref={ref}>
      <Button variant="secondary" icon="list" iconRight="chevdown" onClick={() => setOpen((o) => !o)}>
        {t('rep.columns')} <span className="text-faint tabular ml-0.5">{shown}/{columns.length}</span>
      </Button>
      {open && (
        <div className="absolute right-0 mt-1.5 z-30 w-60 rounded-xl border border-line bg-panel shadow-2xl p-1.5 max-h-[60vh] overflow-auto scroll-area">
          <div className="px-2 py-1.5 text-[10.5px] uppercase tracking-wide text-faint">{t('rep.showColumns')}</div>
          {columns.map((c) => {
            const vis = !hidden.includes(c.key);
            return (
              <button key={c.key} onClick={() => onToggle(c.key)}
                className="flex w-full items-center gap-2.5 px-2 py-1.5 rounded-lg hover:bg-elevated text-[13px] text-ink transition-colors">
                <span className={cx('h-4 w-4 rounded border flex items-center justify-center shrink-0', vis ? 'bg-brand border-brand text-white' : 'border-line2')}>
                  {vis && <Icon name="approve" size={12} />}
                </span>
                <span className="flex-1 text-left">{colLabel(c)}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ReportsScreen() {
  const { can } = useAuth();
  const toast = useToast();
  const [tab, setTab] = useState('software');
  const [clientId, setClientId] = useState('');
  const [siteId, setSiteId] = useState('');
  const [deptId, setDeptId] = useState('');
  const [q, setQ] = useState('');
  // A fleet report is thousands of rows; rendering every one of them at once
  // made the tab unusable and gave no way to work through it.
  const [pageSize, setPageSize] = useState(() => Number(localStorage.getItem('rmm-report-page-size')) || 100);
  const [page, setPage] = useState(0);
  useEffect(() => { try { localStorage.setItem('rmm-report-page-size', String(pageSize)); } catch (e) {} }, [pageSize]);
  // Hidden columns, persisted per report tab across sessions.
  const [hiddenCols, setHiddenCols] = useState(() => { try { return JSON.parse(localStorage.getItem('rmm-report-hidden') || '{}'); } catch (e) { return {}; } });
  useEffect(() => { try { localStorage.setItem('rmm-report-hidden', JSON.stringify(hiddenCols)); } catch (e) {} }, [hiddenCols]);

  const clientsQ = useQuery(['rep-clients'], () => can('clients.view') ? api.listClients() : Promise.resolve({ clients: [] }), { invalidateOn: ['org'] });
  const sitesQ = useQuery(['rep-sites'], () => can('clients.view') ? api.listSites() : Promise.resolve({ sites: [] }), { invalidateOn: ['org'] });
  const groupsQ = useQuery(['rep-groups'], () => can('clients.view') ? api.listGroups() : Promise.resolve({ groups: [] }), { invalidateOn: ['groups'] });
  const clients = clientsQ.data?.clients || [];
  const sites = sitesQ.data?.sites || [];
  const groups = groupsQ.data?.groups || [];

  // Selectors cascade: sites narrow to the chosen client, departments to the site.
  const siteOpts = clientId ? sites.filter((s) => s.client_id === clientId) : sites;
  const deptOpts = siteId ? groups.filter((g) => g.site_id === siteId) : [];

  // The effective filter is the most specific selection.
  const filter = deptId ? { scope: 'group', id: deptId }
    : siteId ? { scope: 'site', id: siteId }
      : clientId ? { scope: 'client', id: clientId }
        : {};

  const fetchReport = () => {
    const p = filter.scope ? { scope: filter.scope, id: filter.id } : {};
    if (tab === 'hardware') return api.reportHardware(p);
    if (tab === 'machines') return api.reportMachines(p);
    return api.reportSoftware(p);
  };
  const dataQ = useQuery([tab, filter.scope || '', filter.id || ''], fetchReport);

  const cols = REPORT_COLUMNS[tab];
  const hidden = hiddenCols[tab] || [];
  const visibleCols = cols.filter((c) => !hidden.includes(c.key));
  const toggleCol = (key) => setHiddenCols((h) => {
    const cur = h[tab] || [];
    return { ...h, [tab]: cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key] };
  });
  const rows = dataQ.data?.rows || [];
  const ql = q.trim().toLowerCase();
  const filtered = !ql ? rows : rows.filter((r) => visibleCols.some((c) => String(c.text(r) || '').toLowerCase().includes(ql)));
  // Page bounds follow the filtered set: searching while on page 12 of an
  // unfiltered report must not leave the viewer staring at an empty page.
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const current = Math.min(page, pageCount - 1);
  useEffect(() => { setPage(0); }, [tab, ql, filter.scope, filter.id, pageSize]);
  const pageRows = filtered.slice(current * pageSize, current * pageSize + pageSize);
  const firstShown = filtered.length ? current * pageSize + 1 : 0;
  const lastShown = Math.min(filtered.length, (current + 1) * pageSize);

  const onClient = (v) => { setClientId(v); setSiteId(''); setDeptId(''); };
  const onSite = (v) => { setSiteId(v); setDeptId(''); };

  const exportCSV = () => {
    if (!filtered.length) { toast?.info?.(t('rep.nothingToExport'), t('rep.noRows')); return; }
    const csv = toCSV(visibleCols.map(colLabel), filtered.map((r) => visibleCols.map((c) => c.text(r))));
    const date = new Date().toISOString().slice(0, 10);
    downloadText('report-' + tab + '-' + date + '.csv', csv, 'text/csv');
    toast?.success?.(t('rep.csvDownloaded'), tn('rep.rowsExported', filtered.length));
  };

  return (
    <div className="p-5 max-w-[1400px] mx-auto">
      <Tabs
        value={tab}
        onChange={(v) => { setTab(v); setQ(''); }}
        className="mb-4"
        tabs={[
          { value: 'software', label: t('rep.tab.software') },
          { value: 'hardware', label: t('rep.tab.hardware') },
          { value: 'machines', label: t('rep.tab.machines') },
        ]}
      />

      {/* filter bar */}
      <Card className="p-3.5 mb-4">
        <div className="flex flex-wrap items-end gap-3">
          <Field label={t('rep.col.client')} className="min-w-[180px] flex-1">
            <Select value={clientId} onChange={(e) => onClient(e.target.value)}>
              <option value="">{t('rep.allClients')}</option>
              {clients.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          </Field>
          <Field label={t('rep.col.site')} className="min-w-[180px] flex-1">
            <Select value={siteId} onChange={(e) => onSite(e.target.value)} disabled={!clientId}>
              <option value="">{clientId ? t('rep.allSites') : t('rep.chooseClient')}</option>
              {siteOpts.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
          </Field>
          <Field label={t('rep.col.department')} className="min-w-[180px] flex-1">
            <Select value={deptId} onChange={(e) => setDeptId(e.target.value)} disabled={!siteId}>
              <option value="">{siteId ? t('rep.allDepartments') : t('rep.chooseSite')}</option>
              {deptOpts.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </Select>
          </Field>
          <Field label={t('rep.search')} className="min-w-[180px] flex-1">
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('rep.filterRows')} />
          </Field>
          <ColumnPicker columns={cols} hidden={hidden} onToggle={toggleCol} />
          <Button variant="secondary" icon="download" onClick={exportCSV} disabled={dataQ.loading || !filtered.length}>
            {t('chat.downloadCsv')}
          </Button>
        </div>
      </Card>

      {/* results */}
      <Card className="overflow-hidden">
        <div className="flex items-center justify-between px-4 h-11 border-b border-line">
          <div className="text-[12.5px] text-muted">
            {dataQ.loading ? t('common.loading') : <>{t('rep.showing', { range: `${firstShown}–${lastShown}`, total: filtered.length })}{ql && rows.length !== filtered.length ? ' ' + t('rep.filteredFrom', { n: rows.length }) : ''}</>}
          </div>
          <Tip label={t('action.refresh')}><Button size="icon" variant="ghost" icon="refresh" onClick={() => dataQ.refetch()} /></Tip>
        </div>

        {dataQ.error ? (
          <EmptyState icon="alert" title={t('rep.loadFailed')} msg={dataQ.error.message} />
        ) : dataQ.loading ? (
          <div className="p-4 space-y-2">{Array.from({ length: 8 }).map((_, i) => <Skeleton key={i} className="h-7 w-full" />)}</div>
        ) : !filtered.length ? (
          <EmptyState icon="report" title={t('rep.noData')} msg={rows.length ? t('rep.noMatch') : t('rep.noInventory')} />
        ) : (
          <div className="overflow-auto scroll-area max-h-[calc(100vh-280px)]">
            <table className="w-full text-[12.5px] border-collapse">
              <thead className="sticky top-0 z-10 bg-elevated/95 backdrop-blur">
                <tr className="text-left text-faint">
                  {visibleCols.map((c) => <th key={c.key} className="font-medium px-4 py-2.5 whitespace-nowrap border-b border-line">{colLabel(c)}</th>)}
                </tr>
              </thead>
              <tbody>
                {pageRows.map((r, i) => (
                  <tr key={i} className="border-b border-line/60 hover:bg-white/[0.02]">
                    {visibleCols.map((c) => (
                      <td key={c.key} className={cx('px-4 py-2 align-top', c.className)}>
                        {c.cell ? c.cell(r) : (c.text(r) || <span className="text-faint">—</span>)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {!dataQ.loading && !dataQ.error && filtered.length > 0 && (
          <div className="flex items-center justify-between gap-3 px-4 h-11 border-t border-line">
            <div className="flex items-center gap-2 text-[12px] text-muted">
              <span>{t('rep.rowsPerPage')}</span>
              {/* Select hands over the change event, not the value: Number(event)
                  was NaN, and the table lost its paging the moment it was used. */}
              <Select value={String(pageSize)} onChange={(e) => setPageSize(Number(e.target.value) || 100)} className="h-7 w-[84px] text-[12px]">
                {[50, 100, 250, 500].map((n) => <option key={n} value={String(n)}>{n}</option>)}
              </Select>
              {/* The export covers the filtered report, not the page on screen —
                  said here because a paged table invites the opposite assumption. */}
              <span className="text-faint hidden sm:inline">{t('rep.csvAll', { n: filtered.length })}</span>
            </div>
            <div className="flex items-center gap-1.5 text-[12px] text-muted">
              <Button size="icon" variant="ghost" icon="chevleft" disabled={current === 0} onClick={() => setPage(current - 1)} title={t('rep.prevPage')} />
              <span className="tabular px-1">{t('audit.pageOf', { page: current + 1, pages: pageCount })}</span>
              <Button size="icon" variant="ghost" icon="chevright" disabled={current >= pageCount - 1} onClick={() => setPage(current + 1)} title={t('rep.nextPage')} />
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}

window.ReportsScreen = ReportsScreen;
