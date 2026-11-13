import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const source = await readFile(new URL('../src/screens-jobs.jsx', import.meta.url), 'utf8');
const code = source.slice(source.indexOf('function AuditScreen('), source.indexOf('window.JobsScreen ='));
const uiSource = await readFile(new URL('../src/ui.jsx', import.meta.url), 'utf8');
const csvCode = uiSource.slice(uiSource.indexOf('const CSV_FORMULA_PREFIX'), uiSource.indexOf('// downloadText saves'));
// The detail column's text comes from labels.jsx; run the real one.
const labelsSource = await readFile(new URL('../src/labels.jsx', import.meta.url), 'utf8');
const detailCode = labelsSource.slice(labelsSource.indexOf('function auditDetailText('), labelsSource.indexOf('// Short weekday name'));
const id = '47e4ba8d-56e9-41cf-91bb-daa70e9371eb';
function render(entries) {
  const downloads = [];
  const sandbox = {
    React: { createElement: (type, props, ...children) => ({ type, props: props || {}, children }) },
    useState: (initial) => [initial, () => {}], useAuth: () => ({ can: () => false }),
    useToast: () => ({ success() {}, error(msg) { throw new Error(msg); } }),
    useDeviceMap: () => ({ 'live-agent': { hostname: 'LIVE-HOST' } }),
    useQuery: ([key]) => ({ data: key === 'audit' ? { entries, total: entries.length } : {} }),
    api: { listAudit: async () => ({ entries, total: entries.length }) },
    t: (k) => k, tEnum: (k, v) => v, cx: (...v) => v.filter(Boolean).join(' '), fmtTime: (v) => v,
    ACTION_META: {}, auditActionLabel: (v) => v,
    downloadText: (...args) => downloads.push(args),
    ...Object.fromEntries(['Page', 'PageHeader', 'Card', 'Field', 'Select', 'Input', 'Button', 'Icon', 'EmptyState'].map((k) => [k, k])),
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(csvCode + detailCode + transformSync(code, { loader: 'jsx' }).code, sandbox);
  const tree = sandbox.AuditScreen();
  const find = (n, fn, out = []) => {
    if (!n || typeof n !== 'object') return out;
    if (fn(n)) out.push(n);
    (n.children || []).flat(Infinity).forEach((c) => find(c, fn, out));
    return out;
  };
  return { tree, downloads, find };
}

test('acknowledged alert audit targets use snapshot or legacy label and retain the UUID', async () => {
  const entries = [
    { id: 'a', action: 'alert.ack', target: id, detail: { hostname: 'ORIGINAL-HOST' }, target_label: 'RENAMED-HOST' },
    { id: 'b', action: 'alert.ack', target: id, detail: { version: 2 }, target_label: 'LAB-WINDOWS' },
    { id: 'c', action: 'alert.ack', target: 'deleted-alert', detail: { name: 'System health alert' } },
    { id: 'd', action: 'alert.ack', target: 'unresolvable-alert', detail: {} },
    { id: 'e', action: 'device.update', target: 'live-agent', detail: {} },
  ];
  const ui = render(entries);
  const targets = ui.find(ui.tree, (n) => n.type === 'td' && entries.some((e) => n.props.title === e.target));
  assert.deepEqual(targets.map((n) => n.children[0]), ['ORIGINAL-HOST', 'LAB-WINDOWS', 'System health alert', 'unresolvable-alert', 'LIVE-HOST']);
  assert.equal(targets[0].props.title, id);
  const exportButton = ui.find(ui.tree, (n) => n.type === 'Button' && n.props.icon === 'download')[0];
  await exportButton.props.onClick();
  assert.equal(ui.downloads.length, 1);
  assert.ok(ui.downloads[0][1].includes('"LAB-WINDOWS","' + id + '"'));
  assert.ok(ui.downloads[0][1].includes('"ORIGINAL-HOST","' + id + '"'));
});


test('audit CSV guards snapshots, legacy labels and other columns without mutating the audit entry', async () => {
  const entries = [
    { id: 'snapshot', action: 'alert.ack', target: id, actor: '=1+1', detail: { hostname: '=1+1' } },
    { id: 'legacy', action: 'alert.resolve', target: id, detail: { version: 2 }, target_label: '  @SUM(A1)' },
    { id: 'ordinary', action: 'alert.suppress', target: id, detail: { hostname: 'WINDOWS-TEST' } },
  ];
  const before = JSON.stringify(entries);
  const ui = render(entries);
  await ui.find(ui.tree, (n) => n.type === 'Button' && n.props.icon === 'download')[0].props.onClick();
  const csv = ui.downloads[0][1];
  assert.ok(csv.includes('"\'=1+1","' + id + '"'));
  assert.ok(csv.includes('"\'  @SUM(A1)","' + id + '"'));
  assert.ok(csv.includes('"WINDOWS-TEST","' + id + '"'));
  assert.ok(csv.includes(',"\'=1+1",'), 'actor text is guarded too');
  assert.equal(JSON.stringify(entries), before, 'only the exported representation changes');
});
