import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';
const shared = await readFile(new URL('../src/fleet-tree.jsx', import.meta.url), 'utf8');
const devices = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
const patches = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
const data = () => ({
  'sites-all': { sites: [{ id: 's1', client_id: 'c1', name: 'Main' }, { id: 's2', client_id: 'c2', name: 'Main' }] },
  'clients-all': { clients: [{ id: 'c2', name: 'Acme' }, { id: 'c1', name: 'Acme' }, { id: 'empty', name: 'Empty client' }] },
  groups: { groups: [] },
  devices: { devices: [
    { id: 'ten', hostname: 'Server 10', description: 'Database', site_id: 's1', os: 'linux', tags: [], status: 'online' },
    { id: 'two', hostname: 'Server 2', description: 'Accounting', site_id: 's1', os: 'linux', tags: [], status: 'online' },
    { id: 'other', hostname: 'Other', description: 'Remote', site_id: 's2', os: 'windows', tags: [], status: 'offline' },
    { id: 'none', hostname: 'Unassigned', description: '', os: 'linux', tags: [], status: 'online' },
  ] },
  compliance: { rows: [
    { agent_id: 'ten', hostname: 'Server 10', description: 'Database', client_id: 'c1', client: 'Acme', site_id: 's1', site: 'Main', missing_total: 10, approved: 2 },
    { agent_id: 'two', hostname: 'Server 2', description: 'Accounting', client_id: 'c1', client: 'Acme', site_id: 's1', site: 'Main', missing_total: 2, approved: 1 },
    { agent_id: 'other', hostname: 'Other', description: 'Remote', client_id: 'c2', client: 'Acme', site_id: 's2', site: 'Main', missing_total: 1 },
    { agent_id: 'none', hostname: 'Unassigned', missing_total: 0 },
  ] },
});
function harness(screen = 'devices', { org = true, storage = new Map(), userId = 'viewer' } = {}) {
  const queries = data(), states = [], calls = []; let cursor = 0, tree;
  const sandbox = {
    React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (value) => { states[i] = typeof value === 'function' ? value(states[i]) : value; }]; },
    useAuth: () => ({ user: { id: userId }, can: (permission) => permission !== 'clients.view' || org }),
    useQuery: (key, fetch) => { fetch?.(); return { data: queries[key[0]] }; },
    useToast: () => ({ info() {}, error() {} }),
    t: (key) => key, tn: (key, n) => `${key} ${n}`, timeAgo: (v) => v, formatList: (v) => v.join(', '),
    cx: (...v) => v.filter(Boolean).join(' '), inputCls: '', DEVICE_ROLES: [],
    localStorage: { getItem: (key) => storage.get(key), setItem: (key, value) => storage.set(key, value) },
    window: { dataBus: { emit() {} }, ColumnPicker: 'ColumnPicker', osLabel: (v) => v, osFamily: (v) => v },
    api: new Proxy({}, { get: (_, name) => async (...args) => { calls.push({ name, args }); return {}; } }),
    ...Object.fromEntries(['Page', 'PageHeader', 'Card', 'Icon', 'Input', 'Button', 'Select', 'Skeleton', 'EmptyState', 'OsIcon', 'StatusPill', 'ComplianceBadge', 'ConfirmDialog', 'PendingDeviceOperations', 'DeviceDetail', 'BulkDeployModal', 'Tip'].map((v) => [v, v])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(shared, { loader: 'jsx' }).code, sandbox);
  const source = screen === 'devices' ? devices.slice(0, devices.indexOf('function PendingDeviceOperations(')) : patches.slice(patches.indexOf('function complianceRows('), patches.indexOf('function CatalogTab('));
  vm.runInContext(transformSync(source, { loader: 'jsx' }).code, sandbox);
  const walk = (node, predicate) => !node || typeof node !== 'object' ? [] : [...(predicate(node) ? [node] : []), ...(node.children || []).flat(Infinity).flatMap((n) => walk(n, predicate))];
  const render = () => { cursor = 0; tree = screen === 'devices' ? sandbox.DevicesScreen({}) : sandbox.ComplianceTab(); };
  const find = (predicate) => walk(tree, predicate);
  const group = (key) => find((n) => n.type === 'tr' && n.props['data-fleet-group'] === key)[0];
  const button = (label) => find((n) => ['button', 'Button'].includes(n.type) && n.children.includes(label))[0];
  render();
  return { queries, calls, storage, find, render, group, button,
    projection: (...args) => JSON.parse(JSON.stringify(sandbox.window.FleetTree.project(...args))),
    rows: () => find((n) => n.props['data-agent-id']).map((n) => n.props['data-agent-id']),
    toggle: (key) => { let stopped = false; walk(group(key), (n) => n.type === 'button')[0].props.onClick({ stopPropagation() { stopped = true; } }); assert.ok(stopped); render(); },
    search: (value) => { find((n) => (n.type === 'input' || n.type === 'Input') && n.props.onChange)[0].props.onChange({ target: { value } }); render(); },
    click: (label) => { button(label).props.onClick(); render(); },
    expanded: (key) => walk(group(key), (n) => n.type === 'button')[0].props['aria-expanded'],
    setOrg: (value) => { org = value; render(); },
  };
}

test('projection uses stable IDs for duplicate labels, counts only its inputs and preserves leaf order', () => {
  const ui = harness();
  const input = [
    { id: '10', client_id: 'b', client: 'Same', site_id: 's2', site: 'Office' },
    { id: '2', client_id: 'a', client: 'Same', site_id: 's1', site: 'Office' },
    { id: '3', client_id: 'a', client: 'Same', site_id: 's1', site: 'Office' },
    { id: 'missing', client_id: 'missing-client', site_id: 'missing-site' }, { id: 'none' },
  ];
  const result = ui.projection(input, (r) => r, (r) => r.id);
  assert.equal(result.filter((n) => n.kind === 'client').length, 4);
  assert.equal(result.find((n) => n.key === 'client:a').count, 2);
  assert.equal(result.find((n) => n.key === 'site:s1').count, 2);
  assert.ok(result.find((n) => n.key === 'client:missing-client').name.includes('unknownClient'));
  assert.ok(result.find((n) => n.key === 'site:missing-site').name.includes('unknownSite'));
  assert.deepEqual(result.filter((n) => n.kind === 'agent').map((n) => n.key), ['agent:none', 'agent:missing', 'agent:2', 'agent:3', 'agent:10']);
  assert.deepEqual(ui.projection(input.slice(1, 3), (r) => r, (r) => r.id).filter((n) => n.kind === 'client').map((n) => n.key), ['client:a']);
});
function queriesRefresh(ui, screen) {
  const key = screen === 'devices' ? 'devices' : 'compliance';
  ui.queries[key] = JSON.parse(JSON.stringify(ui.queries[key]));
}
for (const screen of ['devices', 'compliance']) {
  test(`${screen}: collapse, temporary search expansion, polling and stored flat choice`, () => {
    const ui = harness(screen);
    assert.equal(ui.expanded('client:c1'), true);
    assert.deepEqual(ui.rows().slice(0, 3), ['two', 'ten', 'other']);
    ui.toggle('site:s1'); assert.deepEqual(ui.rows(), ['other', 'none']);
    queriesRefresh(ui, screen);
    ui.render(); assert.equal(ui.expanded('site:s1'), false);
    ui.search('ACCOUNTING'); assert.deepEqual(ui.rows(), ['two']);
    assert.equal(ui.expanded('site:s1'), true);
    ui.toggle('site:s1'); assert.deepEqual(ui.rows(), [], 'search branches can still collapse');
    ui.search(''); assert.deepEqual(ui.rows(), ['other', 'none']);
    ui.search('ACCOUNTING'); assert.deepEqual(ui.rows(), ['two'], 'repeating a cleared query opens ancestors again');
    ui.search('database'); assert.deepEqual(ui.rows(), ['ten']);
    ui.search(''); assert.deepEqual(ui.rows(), ['other', 'none'], 'restore manual collapse');
    ui.search('main'); assert.equal(ui.rows().length, 3);
    ui.search('acme'); assert.equal(ui.rows().length, 3);
    ui.search(''); ui.click('fleetTree.flat');
    assert.equal(ui.rows().length, 4);
    assert.equal(ui.find((n) => n.props['data-fleet-group']).length, 0);
    const restored = harness(screen, { storage: ui.storage });
    assert.equal(restored.find((n) => n.props['data-fleet-group']).length, 0);
    const anotherViewer = harness(screen, { storage: ui.storage, userId: 'another' });
    assert.ok(anotherViewer.group('client:c1'), 'view preference belongs to the signed-in user');
  });
}
test('Devices keeps detail selection, dynamic column spans and sorting within each site', () => {
  const ui = harness();
  ui.click('common.description'); assert.deepEqual(ui.rows().slice(0, 3), ['two', 'ten', 'other']);
  ui.click('common.description'); assert.deepEqual(ui.rows().slice(0, 3), ['ten', 'two', 'other']);
  ui.find((n) => n.props['data-agent-id'] === 'two')[0].props.onClick(); ui.render();
  assert.equal(ui.find((n) => n.type === 'DeviceDetail')[0].props.id, 'two');
  ui.toggle('client:c1'); ui.render();
  assert.equal(ui.find((n) => n.type === 'DeviceDetail')[0].props.id, 'two');
  ui.find((n) => n.type === 'ColumnPicker')[0].props.onToggle('description'); ui.render();
  assert.equal(ui.group('client:c1').children[0].props.colSpan, 9);
});
test('Devices without clients.view neither requests organization endpoints nor exposes cached labels', () => {
  const ui = harness('devices', { org: false });
  assert.equal(ui.rows().length, 4);
  assert.equal(ui.find((n) => n.props['data-fleet-group']).length, 0);
  assert.ok(!ui.calls.some((c) => ['listClients', 'listSites', 'listGroups'].includes(c.name)));
  assert.equal(ui.find((n) => n.type === 'DeviceDetail')[0].props.clients.length, 0);
  ui.search('acme'); assert.equal(ui.rows().length, 0);
  ui.search('accounting'); assert.deepEqual(ui.rows(), ['two']);
  ui.setOrg(true); ui.search(''); assert.ok(ui.group('client:c1'));
  ui.setOrg(false); assert.equal(ui.rows().length, 4); assert.equal(ui.find((n) => n.props['data-fleet-group']).length, 0);
});
test('Compliance sorts counts within a site and scans all filtered rows including collapsed ones', async () => {
  const ui = harness('compliance');
  ui.click('patches.col.missing'); assert.deepEqual(ui.rows().slice(0, 3), ['two', 'ten', 'other']);
  ui.click('patches.col.missing'); assert.deepEqual(ui.rows().slice(0, 3), ['ten', 'two', 'other']);
  ui.search('acme'); ui.toggle('client:c1'); ui.toggle('client:c2'); assert.deepEqual(ui.rows(), []);
  await ui.button('patches.scanAll').props.onClick();
  assert.deepEqual(ui.calls.filter((c) => c.name === 'scanDevicePatches').map((c) => c.args[0]), ['ten', 'two', 'other']);
  assert.ok(!ui.calls.some((c) => ['listClients', 'listSites', 'listGroups'].includes(c.name)));
});


test('partial Devices metadata keeps unknown sites separate from unassigned devices', () => {
  const ui = harness();
  ui.queries.devices.devices.push(
    { id: 'lost1', hostname: 'Unknown site 1', site_id: 'lost-site-1', os: 'linux', tags: [] },
    { id: 'lost2', hostname: 'Unknown site 2', site_id: 'lost-site-2', os: 'linux', tags: [] },
  );
  ui.render();
  assert.ok(ui.group('client:unavailable'));
  assert.ok(ui.group('client:unassigned'));
  assert.ok(ui.group('site:lost-site-1'));
  assert.ok(ui.group('site:lost-site-2'));
  const labels = ui.find((n) => n.type === 'span').flatMap((n) => n.children);
  assert.ok(labels.includes('fleetTree.unknownClient'));
  assert.ok(labels.includes('fleetTree.noClient'));
  assert.equal(labels.filter((v) => v === 'fleetTree.unknownSite').length, 2);
  ui.toggle('client:unavailable');
  assert.ok(ui.rows().includes('none'), 'unassigned device remains visible');
  assert.ok(!ui.rows().includes('lost1') && !ui.rows().includes('lost2'));
});

test('a legacy compliance response with labels but no IDs remains flat', () => {
  const ui = harness('compliance');
  ui.queries.compliance.rows = ui.queries.compliance.rows.map(({ client_id, site_id, ...row }) => row);
  ui.render();
  assert.equal(ui.rows().length, 4);
  assert.equal(ui.find((n) => n.props['data-fleet-group']).length, 0);
  assert.equal(ui.button('fleetTree.tree'), undefined);
  ui.search('acme'); assert.equal(ui.rows().length, 3, 'authorized labels remain searchable');
});
