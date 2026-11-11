import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';
const src = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const code = src.slice(src.indexOf('function ScriptVariablesPanel('), src.indexOf('// Creating, renaming and removing categories.'));
function harness(component, props, fail = false, source = code) {
  const states = [], calls = [], errors = []; let cursor = 0;
  const sandbox = {
    React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useEffect() {}, useToast: () => ({ success() {}, error(...args) { errors.push(args); } }), t: (key) => key, statusLabel: (v) => v,
    api: Object.fromEntries(['createScriptVariable', 'updateScriptVariable', 'setScriptVariableValue', 'runLibraryScript', 'runScript'].map((name) => [name, async (...args) => { calls.push({ name, args: JSON.parse(JSON.stringify(args)) }); if (fail) throw new Error('test failure'); }])),
    ...Object.fromEntries(['Modal', 'Field', 'Input', 'Button', 'Select', 'Combobox'].map((name) => [name, name])),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(source, { loader: 'jsx' }).code, sandbox);
  let tree;
  const render = () => { cursor = 0; tree = sandbox[component](props); return tree; };
  const find = (node, predicate, out = []) => { if (!node || typeof node !== 'object') return out; if (predicate(node)) out.push(node); (node.children || []).flat(Infinity).forEach((child) => find(child, predicate, out)); return out; };
  render(); return { calls, errors, render, find: (predicate) => find(tree, predicate), input: (key) => find(tree, (n) => n.type === 'Input' && n.props['aria-label'] === key)[0], save: () => find(tree, (n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.onClick() };
}
test('secret input starts empty and clears only after successful write', async () => {
  let closed = 0;
  const props = { variable: { id: 'v1', name: 'CUSTOM_KEY', secret: true }, value: { scope_type: 'client', scope_id: 'c1', value: 'must-never-prefill' }, onClose: () => closed++ };
  const ui = harness('ScriptVariableValueEditor', props);
  assert.equal(ui.input('vars.value').props.type, 'password'); assert.equal(ui.input('vars.value').props.value, '');
  assert.equal(ui.input('vars.value').props.autoComplete, 'new-password');
  ui.input('vars.value').props.onChange({ target: { value: 'synthetic-customer-key' } }); ui.render(); await ui.save(); ui.render();
  assert.deepEqual(ui.calls, [{ name: 'setScriptVariableValue', args: ['v1', { scope_type: 'client', scope_id: 'c1', value: 'synthetic-customer-key' }] }]);
  assert.equal(ui.input('vars.value').props.value, ''); assert.equal(closed, 1);
  const retry = harness('ScriptVariableValueEditor', { ...props, onClose() { throw new Error('closed after failure'); } }, true);
  retry.input('vars.value').props.onChange({ target: { value: 'synthetic-customer-key' } }); retry.render(); await retry.save(); retry.render();
  assert.equal(retry.input('vars.value').props.value, 'synthetic-customer-key'); assert.equal(retry.errors.length, 1);
});
test('public values remain editable and scope changes reset the previous target', async () => {
  const ui = harness('ScriptVariableValueEditor', { variable: { id: 'v1', secret: false }, onClose() {} });
  ui.find((n) => n.type === 'Select')[0].props.onChange({ target: { value: 'client' } }); ui.render();
  ui.find((n) => n.type === 'Combobox')[0].props.onChange('client-a'); ui.render();
  ui.find((n) => n.type === 'Select')[0].props.onChange({ target: { value: 'site' } }); ui.render();
  assert.equal(ui.find((n) => n.type === 'Combobox')[0].props.value, '');
  ui.find((n) => n.type === 'Select')[0].props.onChange({ target: { value: 'global' } }); ui.render();
  ui.input('vars.value').props.onChange({ target: { value: 'public-id' } }); ui.render(); await ui.save();
  assert.equal(ui.calls[0].args[1].scope_id, ''); assert.equal(ui.input('vars.value').props.type, 'text');
});
test('editing a secret definition cannot unmask it or submit previous values', async () => {
  const ui = harness('ScriptVariableDefinition', { variable: { id: 'v1', name: 'CUSTOM_KEY', secret: true, values: [{ value: 'must-not-submit' }] }, onClose() {} });
  const toggle = ui.find((n) => n.type === 'input' && n.props.type === 'checkbox')[0];
  assert.equal(toggle.props.checked, true); assert.equal(toggle.props.disabled, true); await ui.save();
  assert.deepEqual(ui.calls, [{ name: 'updateScriptVariable', args: ['v1', { description: '', secret: true }] }]);
});
test('new variable names preserve case without a field length cap', async () => {
  for (const name of ['customerKey', '_myKey', 'customerKey_' + 'Ab'.repeat(4096)]) {
    const ui = harness('ScriptVariableDefinition', { variable: {}, onClose() {} });
    const input = ui.input('checks.name');
    assert.equal(input.props.maxLength, undefined);
    input.props.onChange({ target: { value: name } }); ui.render();
    assert.equal(ui.input('checks.name').props.value, name);
    assert.equal(ui.find((n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.disabled, false);
    await ui.save();
    assert.deepEqual(ui.calls, [{ name: 'createScriptVariable', args: [{ name, description: '', secret: false }] }]);
  }
  const invalid = harness('ScriptVariableDefinition', { variable: {}, onClose() {} });
  for (const name of ['', '1key', 'has space', 'key=value']) {
    invalid.input('checks.name').props.onChange({ target: { value: name } }); invalid.render();
    assert.equal(invalid.find((n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.disabled, true);
  }
});
test('short secrets and long values can be saved without prefill or length caps', async () => {
  for (const secret of [false, true]) {
    for (const value of ['x', 'v'.repeat(65536)]) {
      const ui = harness('ScriptVariableValueEditor', { variable: { id: 'v1', name: 'customerKey', secret }, onClose() {} });
      assert.equal(ui.input('vars.value').props.maxLength, undefined);
      assert.equal(ui.find((n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.disabled, true);
      ui.input('vars.value').props.onChange({ target: { value } }); ui.render();
      assert.equal(ui.find((n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.disabled, false);
      await ui.save(); ui.render();
      assert.deepEqual(ui.calls, [{ name: 'setScriptVariableValue', args: ['v1', { scope_type: 'global', scope_id: '', value }] }]);
      assert.equal(ui.input('vars.value').props.value, '');
      assert.equal(ui.input('vars.value').props.type, secret ? 'password' : 'text');
    }
  }
});
test('variable-only library scripts run by stored ID, with no secret values in the request', async () => {
  const runner = src.slice(src.indexOf('function RunOnDevice('), src.indexOf('// The values a script accepts,'));
  const ui = harness('RunOnDevice', { script: { id: 's1', name: 'installer', variables: ['CUSTOM_KEY'], params: [] }, devMap: { a1: { hostname: 'PC', status: 'online' } }, onClose() {}, onDone() {} }, false, runner);
  ui.find((n) => n.type === 'Select')[0].props.onChange({ target: { value: 'a1' } }); ui.render();
  await ui.find((n) => n.type === 'Button' && n.props.icon === 'plus')[0].props.onClick();
  assert.deepEqual(ui.calls, [{ name: 'runLibraryScript', args: ['a1', 's1', {}] }]);
});
