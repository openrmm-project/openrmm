import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Hundreds of variables must stay workable in the Variables tab and in the
// script editor: a search, a bounded scroll area and, in the tab, values that
// open on demand. 200 synthetic variables, alternating secret and public.
const src = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const slice = (from, to) => src.slice(src.indexOf(from), src.indexOf(to));
const many = Array.from({ length: 200 }, (_, i) => ({
  id: 'v' + i, name: 'CUSTOMER_KEY_' + String(i).padStart(3, '0'), description: i === 137 ? 'Huntress account key' : 'Synthetic variable ' + i,
  secret: i % 2 === 0, values: i === 7 ? [{ scope_type: 'global', scope_id: '', value: 'public-value-7' }] : [],
}));

function harness(code, component, props, { can = () => true } = {}) {
  const states = []; let cursor = 0; let tree;
  const sandbox = {
    React: { createElement(type, p, ...children) { return typeof type === 'function' ? type({ ...p, children }) : { type, props: p || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useEffect() {}, useToast: () => ({ success() {}, error() {}, info() {} }), useAuth: () => ({ can }), useDeviceMap: () => ({}),
    useQuery: (key) => ({ data: key[0] === 'script-variables' ? { variables: many } : {}, loading: false }),
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key), cx: (...v) => v.filter(Boolean).join(' '), timeAgo: (v) => v,
    api: {},
    ...Object.fromEntries(['Card', 'Modal', 'Drawer', 'Field', 'Input', 'Button', 'Select', 'Combobox', 'ConfirmDialog', 'Icon', 'CodeEditor', 'CodeBlock', 'ScriptParamsEditor', 'AIReleasePanel', 'RunOnDevice', 'ScriptVariableDefinition', 'ScriptVariableValueEditor', 'ReviewModal', 'ReviewRow', 'ReviewSection'].map((n) => [n, n])),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const render = () => { cursor = 0; tree = sandbox[component](props); return tree; };
  const walk = (pred, node, out) => { if (!node || typeof node !== 'object') return out; if (pred(node)) out.push(node); (node.children || []).flat(Infinity).forEach((c) => walk(pred, c, out)); return out; };
  // find(pred) searches the whole tree; find(pred, node) only under node (none when node is missing).
  const find = (pred, ...within) => walk(pred, within.length ? within[0] : tree, []);
  render();
  return { render, find, text: () => JSON.stringify(tree) };
}

test('the Variables tab bounds 200 variables in a scroll area, with search, a secret filter and values on demand', () => {
  const ui = harness(slice('function ScriptVariablesPanel(', 'function ScriptVariableDefinition('), 'ScriptVariablesPanel', {});
  const list = () => ui.find((n) => n.props?.id === 'script-variables-list')[0];
  const rows = () => ui.find((n) => n.type === 'button' && 'aria-expanded' in n.props, list());
  assert.match(list().props.className, /max-h-\[28rem\]/);
  assert.match(list().props.className, /overflow-y-auto/);
  assert.equal(rows().length, 200);
  assert.ok(ui.text().includes('vars.count {\\"shown\\":200,\\"total\\":200}'));
  // Values stay closed until a variable is opened.
  assert.ok(!ui.text().includes('public-value-7'));
  rows()[7].props.onClick(); ui.render();
  assert.ok(ui.text().includes('public-value-7'));
  assert.equal(rows()[7].props['aria-expanded'], true);
  // Search by name and by description, case-insensitive.
  const search = () => ui.find((n) => n.type === 'Input' && n.props['aria-label'] === 'vars.search')[0];
  search().props.onChange({ target: { value: 'customer_key_19' } }); ui.render();
  assert.deepEqual(rows().map((r) => r.props['aria-label'].split(': ')[1]), ['CUSTOMER_KEY_190', 'CUSTOMER_KEY_191', 'CUSTOMER_KEY_192', 'CUSTOMER_KEY_193', 'CUSTOMER_KEY_194', 'CUSTOMER_KEY_195', 'CUSTOMER_KEY_196', 'CUSTOMER_KEY_197', 'CUSTOMER_KEY_198', 'CUSTOMER_KEY_199']);
  search().props.onChange({ target: { value: 'HUNTRESS' } }); ui.render();
  assert.equal(rows().length, 1);
  search().props.onChange({ target: { value: 'nothing like this' } }); ui.render();
  assert.equal(list(), undefined);
  assert.ok(ui.text().includes('vars.noMatches'));
  // The secret filter, combined with an empty search.
  search().props.onChange({ target: { value: '' } }); ui.render();
  const filter = () => ui.find((n) => n.type === 'Select' && n.props['aria-label'] === 'vars.filter')[0];
  filter().props.onChange({ target: { value: 'secret' } }); ui.render();
  assert.equal(rows().length, 100);
  filter().props.onChange({ target: { value: 'public' } }); ui.render();
  assert.equal(rows().length, 100);
  assert.ok(ui.text().includes('vars.count {\\"shown\\":100,\\"total\\":200}'));
});

test('the script editor binds from a bounded, searchable picker and keeps hidden selections', () => {
  const ui = harness(slice('function ScriptEditor(', 'function RunOnDevice('), 'ScriptEditor', { script: { id: 's1', name: 'Install', shell: 'powershell', body: 'x', variables: ['CUSTOMER_KEY_001'] }, onClose() {} });
  const picker = () => ui.find((n) => n.props?.id === 'script-variable-picker')[0];
  const boxes = () => ui.find((n) => n.type === 'input' && n.props.type === 'checkbox', picker());
  assert.match(picker().props.className, /max-h-60/);
  assert.equal(boxes().length, 200);
  assert.equal(ui.find((n) => n.type === 'label' && /h-12/.test(n.props.className || ''), picker()).length, 200, 'five 48 px rows fill the 240 px area');
  const search = () => ui.find((n) => n.type === 'Input' && n.props['aria-controls'] === 'script-variable-picker')[0];
  search().props.onChange({ target: { value: 'huntress' } }); ui.render();
  assert.equal(boxes().length, 1);
  boxes()[0].props.onChange({ target: { checked: true } }); ui.render();
  // CUSTOMER_KEY_001 is hidden by the search but stays bound, shown as a chip.
  const chips = () => ui.find((n) => n.type === 'span' && /rounded-md border/.test(n.props.className || '')).map((n) => n.children.flat(Infinity).find((c) => c?.type === 'code').children[0]);
  assert.deepEqual(chips(), ['CUSTOMER_KEY_001', 'CUSTOMER_KEY_137']);
  // Removing a chip unbinds it.
  ui.find((n) => n.type === 'button' && n.props['aria-label'] === 'action.remove: CUSTOMER_KEY_001')[0].props.onClick(); ui.render();
  assert.deepEqual(chips(), ['CUSTOMER_KEY_137']);
  search().props.onChange({ target: { value: 'no such variable' } }); ui.render();
  assert.equal(boxes().length, 0);
  assert.ok(ui.text().includes('vars.noMatches'));
});

test('the picker is read-only for CMD and without the variables permission', () => {
  const code = slice('function ScriptEditor(', 'function RunOnDevice(');
  const cmd = harness(code, 'ScriptEditor', { script: { id: 's1', name: 'Legacy', shell: 'cmd', body: 'x', variables: [] }, onClose() {} });
  assert.ok(cmd.find((n) => n.type === 'input' && n.props.type === 'checkbox').every((b) => b.props.disabled));
  const viewer = harness(code, 'ScriptEditor', { script: { id: 's1', name: 'Install', shell: 'powershell', body: 'x', variables: ['CUSTOMER_KEY_001'] }, onClose() {} }, { can: (p) => p !== 'scripts.variables' });
  assert.ok(viewer.find((n) => n.type === 'input' && n.props.type === 'checkbox').every((b) => b.props.disabled));
  assert.equal(viewer.find((n) => n.type === 'button' && /^action\.remove: /.test(n.props['aria-label'] || '')).length, 0, 'no chip can be removed');
});
