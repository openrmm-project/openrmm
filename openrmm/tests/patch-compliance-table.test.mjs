import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const source = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
const treeSource = await readFile(new URL('../src/fleet-tree.jsx', import.meta.url), 'utf8');
const code = source.slice(source.indexOf('function complianceRows('), source.indexOf('function CatalogTab('));
function harness(rows) {
  const states = [], calls = []; let cursor = 0, tree;
  const sandbox = {
    React: { createElement(type, props, ...children) { return { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useAuth: () => ({ can: () => true }), useQuery: () => ({ data: { rows } }),
    useToast: () => ({ info() {}, error() {} }),
    t: (key) => key, timeAgo: (v) => v, cx: (...values) => values.filter(Boolean).join(' '),
    window: { dataBus: { emit() {} } },
    api: Object.fromEntries(['scanDevicePatches', 'installDevicePatches', 'rebootDevice'].map((name) => [name, async (...args) => calls.push({ name, args })])),
    ...Object.fromEntries(['Card', 'Icon', 'Input', 'Button', 'Skeleton', 'EmptyState', 'OsIcon', 'ComplianceBadge', 'ConfirmDialog'].map((v) => [v, v])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(treeSource, { loader: 'jsx' }).code, sandbox);
  vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const walk = (node, predicate) => !node || typeof node !== 'object' ? [] : [...(predicate(node) ? [node] : []), ...(node.children || []).flat(Infinity).flatMap((n) => walk(n, predicate))];
  const render = () => { cursor = 0; tree = sandbox.ComplianceTab(); };
  const find = (predicate) => walk(tree, predicate);
  const header = (label) => find((n) => n.type === 'th' && walk(n, (c) => c.type === 'button' && c.children.includes(label)).length)[0];
  render();
  return { calls, render, find, header,
    sort: (label) => { header(label).children.flat(Infinity)[0].props.onClick(); render(); },
    search: (value) => { find((n) => n.type === 'Input')[0].props.onChange({ target: { value } }); render(); },
    rows: () => find((n) => n.type === 'tr' && n.props['data-agent-id'] != null),
    sorted: (key, dir) => [...sandbox.complianceRows(rows, '', { key, dir })].map((r) => r.agent_id),
  };
}
const rows = [
  { agent_id: 'ten', hostname: 'Server 10', description: 'Room 10', os: 'windows', status: 'critical', missing_total: 10, missing_security: 10, approved: 10, failed: 10, reboot_pending: true, last_scan: '2026-01-01T00:00:00Z' },
  { agent_id: 'two', hostname: 'server 2', description: 'Room 2', os: 'linux', status: 'compliant', missing_total: 2, missing_security: 2, approved: 2, failed: 2, reboot_pending: false, last_scan: '2026-01-01T01:00:00+02:00' },
  { agent_id: 'empty', hostname: 'Unknown', description: '', last_scan: null },
  { agent_id: 'tie', hostname: 'SERVER 2', description: 'room 2', os: 'Linux', status: 'compliant', missing_total: 2, missing_security: 2, approved: 2, failed: 2, reboot_pending: false, last_scan: '2025-12-31T23:00:00Z' },
];

test('all data headers sort with natural text, numeric counts, actual time and stable ties; missing values stay last', () => {
  const before = JSON.stringify(rows), ui = harness(rows);
  assert.equal(ui.find((n) => n.type === 'th' && n.props['aria-sort']).length, 10);
  for (const key of ['description', 'os', 'status', 'missing_total', 'missing_security', 'approved', 'failed', 'reboot_pending', 'last_scan']) {
    assert.deepEqual(ui.sorted(key, 'asc'), ['two', 'tie', 'ten', 'empty'], key);
    assert.deepEqual(ui.sorted(key, 'desc'), ['ten', 'two', 'tie', 'empty'], key);
  }
  assert.deepEqual(ui.sorted('hostname', 'asc'), ['two', 'tie', 'ten', 'empty']);
  assert.equal(JSON.stringify(rows), before, 'query cache is not mutated');
  ui.sort('patches.col.missing');
  assert.equal(ui.header('patches.col.missing').props['aria-sort'], 'ascending');
  ui.sort('patches.col.missing');
  assert.equal(ui.header('patches.col.missing').props['aria-sort'], 'descending');
  assert.equal(ui.header('patches.col.host').props['aria-sort'], 'none');
});

test('descriptions render with tooltip and fallback; search matches hostname or description', () => {
  const ui = harness(rows);
  assert.ok(ui.find((n) => n.type === 'td' && n.props.title === 'Room 10' && n.props.className.includes('truncate')).length);
  const emptyRow = ui.rows().find((r) => r.props['data-agent-id'] === 'empty');
  assert.equal(emptyRow.children.flat(Infinity)[1].children[0].children[0], '—');
  ui.search('ROOM 10'); assert.deepEqual(ui.rows().map((r) => r.props['data-agent-id']), ['ten']);
  ui.search('SERVER 2'); assert.deepEqual(ui.rows().map((r) => r.props['data-agent-id']), ['two', 'tie']);
  ui.search('not present'); assert.equal(ui.find((n) => n.type === 'EmptyState').length, 1);
});

test('sorted and filtered rows keep scan/install/reboot and bulk scan targets attached', async () => {
  const ui = harness(rows);
  ui.sort('patches.col.missing'); ui.sort('patches.col.missing');
  assert.equal(ui.rows()[0].props['data-agent-id'], 'ten');
  ui.search('ROOM 10');
  await ui.find((n) => n.type === 'Button' && n.children.includes('dpatch.scan'))[0].props.onClick();
  await ui.find((n) => n.type === 'Button' && n.children.includes('patches.installN'))[0].props.onClick();
  ui.find((n) => n.type === 'Button' && n.children.includes('reboot.button'))[0].props.onClick(); ui.render();
  await ui.find((n) => n.type === 'ConfirmDialog')[0].props.onConfirm();
  await ui.find((n) => n.type === 'Button' && n.children.includes('patches.scanAll'))[0].props.onClick();
  assert.deepEqual(JSON.parse(JSON.stringify(ui.calls)), [
    { name: 'scanDevicePatches', args: ['ten'] }, { name: 'installDevicePatches', args: ['ten', []] },
    { name: 'rebootDevice', args: ['ten'] }, { name: 'scanDevicePatches', args: ['ten'] },
  ]);
});

test('zero is a value while whitespace, invalid dates, null and undefined are missing', () => {
  const ui = harness([{ agent_id: 'a', description: ' ', last_scan: 'invalid' }, { agent_id: 'b', description: null }, { agent_id: 'c', description: 'Actual', missing_total: 0, last_scan: '2020-01-01' }]);
  for (const dir of ['asc', 'desc']) for (const key of ['description', 'missing_total', 'last_scan']) assert.deepEqual(ui.sorted(key, dir), ['c', 'a', 'b']);
});
