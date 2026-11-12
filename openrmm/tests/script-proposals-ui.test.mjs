import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// What AI Chat or MCP proposes is reviewed in the console: a badge, an Approve
// button, and nothing that would use the proposal before it is approved.
const src = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const slice = (from, to) => src.slice(src.indexOf(from), src.indexOf(to));
const variables = [
  { id: 'v1', name: 'VENDOR_KEY', description: 'Proposed by the assistant', secret: true, pending: true, proposed_by: 'mcp:claude', values: [] },
  { id: 'v2', name: 'SITE_CODE', description: 'Approved', secret: false, pending: false, values: [{ scope_type: 'global', scope_id: '', value: 'HQ', set: true }] },
];

function harness(code, component, props) {
  const states = []; const calls = []; let cursor = 0; let tree;
  const sandbox = {
    React: { createElement(type, p, ...children) { return typeof type === 'function' ? type({ ...p, children }) : { type, props: p || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useEffect() {}, useToast: () => ({ success() {}, error() {}, info() {} }), useAuth: () => ({ can: () => true }), useDeviceMap: () => ({}),
    useQuery: (key) => ({ data: key[0] === 'script-variables' ? { variables } : {}, loading: false }),
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key), cx: (...v) => v.filter(Boolean).join(' '), timeAgo: (v) => v,
    api: new Proxy({}, { get: (_, name) => async (...args) => { calls.push([name, ...args]); } }),
    ...Object.fromEntries(['Card', 'Modal', 'Drawer', 'Field', 'Input', 'Button', 'Select', 'Combobox', 'ConfirmDialog', 'Icon', 'CodeEditor', 'CodeBlock', 'ScriptParamsEditor', 'AIReleasePanel', 'RunOnDevice', 'ScriptVariableDefinition', 'ScriptVariableValueEditor', 'ReviewModal', 'ReviewRow', 'ReviewSection'].map((n) => [n, n])),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const render = () => { cursor = 0; tree = sandbox[component](props); return tree; };
  const walk = (pred, node, out) => { if (!node || typeof node !== 'object') return out; if (pred(node)) out.push(node); (node.children || []).flat(Infinity).forEach((c) => walk(pred, c, out)); return out; };
  render();
  return { render, calls, find: (pred, ...within) => walk(pred, within.length ? within[0] : tree, []), text: () => JSON.stringify(tree) };
}
const button = (ui, label) => ui.find((n) => n.type === 'Button' && n.children.flat(Infinity).includes(label));

test('a proposed variable shows its review state, approves, and takes no value before approval', async () => {
  const ui = harness(slice('function ScriptVariablesPanel(', 'function ScriptVariableDefinition('), 'ScriptVariablesPanel', {});
  assert.ok(ui.text().includes('proposal.awaiting'));
  assert.equal(button(ui, 'proposal.approve').length, 1, 'only the pending variable can be approved');
  // Opening the proposal shows why it takes no value yet, not "Set a value".
  const rows = () => ui.find((n) => n.type === 'button' && 'aria-expanded' in n.props);
  rows()[0].props.onClick(); ui.render();
  assert.ok(ui.text().includes('vars.pendingHint'));
  assert.equal(button(ui, 'vars.setValue').length, 0);
  rows()[1].props.onClick(); ui.render();
  assert.equal(button(ui, 'vars.setValue').length, 1, 'an approved variable still takes values');
  // Approve opens the review; nothing is approved until it is confirmed.
  await button(ui, 'proposal.approve')[0].props.onClick(); ui.render();
  assert.deepEqual(ui.calls, []);
  const review = ui.find((n) => n.type === 'ReviewModal' && n.props.open)[0];
  assert.ok(review, 'the review opens');
  assert.ok(JSON.stringify(review).includes('mcp:claude'), 'the review names who proposed it');
  await review.props.onConfirm();
  assert.deepEqual(ui.calls, [['approveScriptVariable', 'v1']]);
  // The review filter.
  ui.find((n) => n.type === 'Select' && n.props['aria-label'] === 'vars.filter')[0].props.onChange({ target: { value: 'pending' } }); ui.render();
  assert.equal(rows().length, 1);
});

test('a proposed variable cannot be bound in the script editor', () => {
  const ui = harness(slice('function ScriptEditor(', 'function RunOnDevice('), 'ScriptEditor', { script: { id: 's1', name: 'Install', shell: 'powershell', body: 'x', variables: [] }, onClose() {} });
  const boxes = ui.find((n) => n.type === 'input' && n.props.type === 'checkbox', ui.find((n) => n.props?.id === 'script-variable-picker')[0]);
  assert.equal(boxes[0].props.disabled, true, 'pending VENDOR_KEY');
  assert.equal(boxes[1].props.disabled, false, 'approved SITE_CODE');
});

test('a proposed library script is approved from the editor and cannot run or be released before', async () => {
  const code = slice('function ScriptEditor(', 'function RunOnDevice(');
  const pending = harness(code, 'ScriptEditor', { script: { id: 's9', name: 'From the AI', shell: 'bash', body: 'echo hi', variables: [], pending: true, proposed_by: 'ai:dana' }, onClose() {} });
  assert.equal(button(pending, 'script.runOnDevice').length, 0);
  assert.equal(pending.find((n) => n.type === 'AIReleasePanel').length, 0);
  assert.ok(pending.find((n) => n.props?.role === 'note').length === 1 && pending.text().includes('script.pendingHint'));
  // Approve opens the review with the stored script; only confirming approves.
  await button(pending, 'proposal.approve')[0].props.onClick(); pending.render();
  assert.deepEqual(pending.calls, []);
  const review = pending.find((n) => n.type === 'ReviewModal' && n.props.open)[0];
  assert.ok(review && JSON.stringify(review).includes('echo hi'), 'the review shows the script as it will run');
  await review.props.onConfirm();
  assert.deepEqual(pending.calls, [['approveScript', 's9']]);
  const approved = harness(code, 'ScriptEditor', { script: { id: 's8', name: 'Reviewed', shell: 'bash', body: 'echo hi', variables: [], pending: false }, onClose() {} });
  assert.equal(button(approved, 'proposal.approve').length, 0);
  assert.equal(button(approved, 'script.runOnDevice').length, 1);
  assert.equal(approved.find((n) => n.type === 'AIReleasePanel').length, 1);
});
