import assert from 'node:assert/strict';
import { readdir, readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const src = new URL('../src/', import.meta.url);

// The console asks in its own dialogs: a browser prompt or confirm opens on top
// of the tab, outside the console's look, language and full-screen viewer.
test('the console never opens the browser\'s prompt, confirm or alert', async () => {
  const offenders = [];
  for (const name of await readdir(src)) {
    if (!name.endsWith('.jsx')) continue;
    const text = await readFile(new URL(name, src), 'utf8');
    text.split('\n').forEach((line, i) => {
      if (/\bwindow\.(prompt|confirm|alert)\s*\(/.test(line)) offenders.push(`${name}:${i + 1}`);
    });
  }
  assert.deepEqual(offenders, []);
});

// FormDialog: the values typed reach onSubmit, and validate keeps the dialog
// from submitting a bad value.
async function formDialog(props) {
  const source = await readFile(new URL('ui.jsx', src), 'utf8');
  const start = source.indexOf('function FormDialog(');
  const end = source.indexOf('\nfunction ConfirmDialog(', start);
  const { code } = transformSync(source.slice(start, end), { loader: 'jsx' });
  const states = [];
  let cursor = 0;
  const sandbox = {
    React: { createElement: (type, p, ...children) => ({ type, props: p || {}, children }) },
    useState(initial) {
      const i = cursor++;
      if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial;
      return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }];
    },
    useEffect() {},
    t: (k) => k,
    Modal: 'Modal', Field: 'Field', Input: 'Input', Textarea: 'Textarea', Button: 'Button',
  };
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox);
  const render = () => { cursor = 0; return sandbox.FormDialog(props); };
  const find = (node, pred, out = []) => {
    if (!node || typeof node !== 'object') return out;
    if (pred(node)) out.push(node);
    (node.children || []).flat(Infinity).forEach((c) => find(c, pred, out));
    return out;
  };
  return { render, find };
}

test('FormDialog submits what was typed and refuses what validate refuses', async () => {
  const submitted = [];
  const { render, find } = await formDialog({
    open: true, onClose() {}, title: 'Suppress',
    fields: [{ name: 'minutes', type: 'number', initial: '60', label: 'Minutes:' }, { name: 'note', type: 'text', label: 'Reason:' }],
    validate: (v) => (Number(v.minutes) > 0 ? '' : 'positive minutes'),
    onSubmit: (v) => { submitted.push(v); },
  });
  let tree = render();
  const inputs = find(tree, (n) => n.type === 'Input');
  assert.equal(inputs.length, 2);
  assert.equal(inputs[0].props.value, '60');
  // Labels lose the trailing colon of the old prompt text.
  assert.deepEqual(find(tree, (n) => n.type === 'Field').map((f) => f.props.label), ['Minutes', 'Reason']);
  inputs[0].props.onChange({ target: { value: '0' } });
  tree = render();
  const form = find(tree, (n) => n.type === 'form')[0];
  await form.props.onSubmit({ preventDefault() {} });
  tree = render();
  assert.equal(submitted.length, 0, 'an invalid value was submitted');
  assert.ok(JSON.stringify(tree).includes('positive minutes'));
  find(tree, (n) => n.type === 'Input')[0].props.onChange({ target: { value: '15' } });
  find(tree, (n) => n.type === 'Input')[1].props.onChange({ target: { value: 'planned' } });
  tree = render();
  await find(tree, (n) => n.type === 'form')[0].props.onSubmit({ preventDefault() {} });
  // Built inside the VM, so compare the data, not the prototype.
  assert.deepEqual(JSON.parse(JSON.stringify(submitted)), [{ minutes: '15', note: 'planned' }]);
});
