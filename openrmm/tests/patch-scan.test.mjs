import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const source = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
const deviceSource = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
function harness({ state, patches = [], rows = [], failure = false, outcomes = { dispatched: 1, busy: 0, offline: 0, failed: 0 } } = {}) {
  const states = [], calls = [], notices = []; let cursor = 0;
  const sandbox = {
    React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = initial; return [states[i], (v) => { states[i] = v; }]; },
    useAuth: () => ({ can: () => true }),
    useToast: () => Object.fromEntries(['error', 'info'].map((kind) => [kind, (...args) => notices.push({ kind, args })])),
    useQuery: (key) => ({ data: key[0] === 'compliance' ? { rows } : { patches, scan_state: state }, loading: false }),
    api: Object.fromEntries(['scanDevicePatches', 'scanFleetPatches'].map((name) => [name, async (...args) => { calls.push({ name, args: JSON.parse(JSON.stringify(args)) }); if (failure) throw new Error('conflict'); return outcomes; }])),
    t: (key, values) => key + (values ? JSON.stringify(values) : ''), tn: (key) => key, tEnum: (key, value) => key + '.' + value,
    cx: (...args) => args.filter(Boolean).join(' '), timeAgo: (v) => v, Date,
    window: { dataBus: { emit() {} } },
    ...Object.fromEntries(['Card', 'Button', 'Icon', 'Input', 'Skeleton', 'EmptyState', 'OsIcon', 'ConfirmDialog'].map((name) => [name, name])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(source.slice(0, source.indexOf('function CatalogTab()')), { loader: 'jsx' }).code, sandbox);
  vm.runInContext(transformSync(deviceSource.slice(deviceSource.indexOf('function DevicePatches('), deviceSource.indexOf('window.DevicesScreen')), { loader: 'jsx' }).code, sandbox);
  let tree;
  const render = (device = false) => { cursor = 0; tree = device ? sandbox.DevicePatches({ id: 'dev-1' }) : sandbox.ComplianceTab(); return tree; };
  const find = (node, predicate, results = []) => { if (!node || typeof node !== 'object') return results; if (predicate(node)) results.push(node); (node.children || []).flat(Infinity).forEach((child) => find(child, predicate, results)); return results; };
  const text = (node) => typeof node === 'string' ? node : (node?.children || []).flat(Infinity).map(text).join(' ');
  return { sandbox, calls, notices, render, find: (p) => find(tree, p), text: () => text(tree) };
}
const now = new Date().toISOString();
const verified = { status: 'succeeded', scan_mode: 'refreshed', last_success: now, last_metadata_refresh: now };

test('only the current verified/native scan can support clean compliance', () => {
  const { sandbox: ui } = harness();
  assert.equal(ui.patchComplianceStatus({ status: 'compliant' }), 'unknown');
  assert.equal(ui.patchComplianceStatus({ scan_state: verified }), 'compliant');
  assert.equal(ui.patchComplianceStatus({ status: 'unknown', scan_state: verified }), 'unknown');
  for (const scan_state of [
    { ...verified, scan_mode: 'cached' }, { ...verified, scan_mode: 'legacy' },
    { ...verified, status: 'failed' }, { ...verified, status: 'pending' },
    { ...verified, last_success: new Date(Date.now() - 25 * 3600000).toISOString() },
  ]) {
    assert.equal(ui.patchComplianceStatus({ scan_state }), 'unknown');
    assert.equal(ui.patchComplianceStatus({ missing_security: 1, scan_state }), 'critical');
  }
});

test('fleet sends refresh option and reports actual outcomes, including zero dispatch', async () => {
  for (const dispatched of [0, 1]) {
    const ui = harness({ rows: [{ agent_id: 'dev-1' }], outcomes: { dispatched, busy: 1, offline: 2, failed: 3 } });
    ui.render(); ui.find((n) => n.type === 'input' && n.props.type === 'checkbox')[0].props.onChange({ target: { checked: true } }); ui.render();
    await ui.find((n) => n.type === 'Button' && n.props.onClick?.name === 'scanAll')[0].props.onClick();
    assert.deepEqual(ui.calls[0], { name: 'scanFleetPatches', args: [['dev-1'], { refresh_metadata: true }] });
    assert.equal(ui.notices[0].kind, dispatched ? 'info' : 'error');
    assert.match(JSON.stringify(ui.notices), /offline.*2/);
    assert.match(JSON.stringify(ui.notices), /failed.*3/);
  }
});

test('device renders failure and provenance, sends refresh, and never announces success on conflict', async () => {
  const ui = harness({ state: { ...verified, status: 'failed', error_code: 'refresh_failed', last_attempt: now }, failure: true });
  ui.render(true);
  assert.match(ui.text(), /pscan.failed/); assert.match(ui.text(), /pscan.error.refresh_failed/);
  assert.match(ui.text(), /pscan.attempt/); assert.match(ui.text(), /pscan.catalog/);
  const empty = ui.find((n) => n.type === 'EmptyState')[0]; assert.equal(empty.props.msg, 'pscan.unverified');
  ui.find((n) => n.type === 'input')[0].props.onChange({ target: { checked: true } });ui.render(true);
  await ui.find((n) => n.type === 'Button' && n.children.flat().includes('dpatch.scan'))[0].props.onClick();
  assert.deepEqual(ui.calls[0], { name: 'scanDevicePatches', args: ['dev-1', { refresh_metadata: true }] });
  assert.deepEqual(ui.notices.map((n) => n.kind), ['error']);
});

test('legacy missing fields render safely and pending scans disable duplicate action', () => {
  const old = harness();old.render(true);assert.match(old.text(), /pscan.mode.legacy/);assert.match(old.text(), /pscan.unverified/);
  const active = harness({ state: { status: 'pending', deadline: new Date(Date.now() + 60000).toISOString() } });active.render(true);
  assert.equal(active.find((n) => n.type === 'Button' && n.children.flat().includes('dpatch.scan'))[0].props.disabled, true);
});
