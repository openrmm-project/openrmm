import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';
const source = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const panel = source.slice(source.indexOf('function AlertTemplatePanel()'), source.indexOf('function SMTPProfilePanel()'));
const data = (x) => JSON.parse(JSON.stringify(x));
function harness(extra = {}) {
  const states = [], effects = [], refs = [];
  let cursor = 0;
  const sandbox = {
    React: { createElement(type, props, ...children) {
      if (typeof type === 'function') return type({ ...props, children });
      return { type, props: props || {}, children };
    } },
    useState(initial) {
      const i = cursor++;
      if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial;
      return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }];
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (!refs[i] || deps.some((d, j) => d !== refs[i][j])) { effects.push(fn); refs[i] = deps; }
    },
    useRef(initial) { const i = cursor++; if (!(i in refs)) refs[i] = { current: initial }; return refs[i]; },
    inputCls: 'input',
    useToast: () => ({ success() {}, error(...args) { throw new Error(args.join(':')); }, info() {} }),
    useAuth: () => ({ can: () => true }), t: (key) => key,
    cx: (...args) => args.filter(Boolean).join(' '), notifyLabel: (key) => ({ title: key, msg: key }),
    ...Object.fromEntries(['Modal', 'Field', 'Select', 'Input', 'Button', 'Icon', 'Combobox', 'Toggle', 'CodeEditor', 'ConfirmDialog', 'Skeleton', 'Card'].map((k) => [k, k])),
    ...extra,
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(transformSync(panel, { loader: 'jsx' }).code, sandbox);
  return {
    render(name, props = {}) { cursor = 0; const tree = sandbox[name](props); effects.splice(0).forEach((fn) => fn()); return tree; },
    find(node, predicate, out = []) {
      if (!node || typeof node !== 'object') return out;
      if (predicate(node)) out.push(node);
      (node.children || []).flat(Infinity).forEach((child) => this.find(child, predicate, out));
      return out;
    },
  };
}


const defaults = { device_name: 'description', alert: { subject: '{{device}}', body: '{{details}}' }, recovery: { subject: 'OK {{device}}', body: '{{details}}' } };
const configView = () => ({ config: data(defaults), defaults: data(defaults), variables: ['device', 'hostname', 'details'] });
const button = (ui, tree, key) => ui.find(tree, (n) => n.type === 'Button' && n.children.includes(key))[0];
const input = (ui, tree, key) => ui.find(tree, (n) => n.props['aria-label'] === key)[0];

test('draft preview, save and test mail use the intended settings', async () => {
  const calls = [];
  let view = configView();
  const ui = harness({ useQuery: () => ({ data: view }), api: {
    previewAlertTemplates: async (config, event) => { calls.push(['preview', data(config), event]); return { subject: 'Example', body: 'Example body' }; },
    updateAlertTemplates: async (config) => { calls.push(['save', data(config)]); return data(config); },
    testAlertSMTP: async (...args) => { calls.push(['send', ...args]); return { to: 'me@example.test' }; },
  }});
  ui.render('AlertTemplatePanel');
  let tree = ui.render('AlertTemplatePanel');
  input(ui, tree, 'mailtpl.subject').props.onChange({ target: { value: 'Custom {{device}}' } });
  tree = ui.render('AlertTemplatePanel');
  assert.equal(button(ui, tree, 'mailtpl.send').props.disabled, true);
  await button(ui, tree, 'mailtpl.preview').props.onClick();
  tree = ui.render('AlertTemplatePanel');
  assert.equal(calls.length, 1);
  assert.equal(calls[0][1].alert.subject, 'Custom {{device}}');
  assert.equal(calls[0][2], 'alert');
  assert.equal(ui.find(tree, (n) => n.type === 'pre')[0].children[0], 'Example body');
  // An unrelated query refresh must not discard unsaved template edits.
  view = configView(); ui.render('AlertTemplatePanel'); tree = ui.render('AlertTemplatePanel');
  assert.equal(input(ui, tree, 'mailtpl.subject').props.value, 'Custom {{device}}');
  await button(ui, tree, 'mail.save').props.onClick();
  tree = ui.render('AlertTemplatePanel');
  assert.equal(button(ui, tree, 'mailtpl.send').props.disabled, false);
  await button(ui, tree, 'mailtpl.send').props.onClick();
  assert.deepEqual(calls[2], ['send', '', 'alert']);
  input(ui, tree, 'mailtpl.kind').props.onChange({ target: { value: 'recovery' } });
  tree = ui.render('AlertTemplatePanel');
  assert.equal(input(ui, tree, 'mailtpl.subject').props.value, defaults.recovery.subject);
  assert.equal(ui.find(tree, (n) => n.type === 'pre').length, 0);
  await button(ui, tree, 'mailtpl.reset').props.onClick();
  tree = ui.render('AlertTemplatePanel');
  assert.equal(button(ui, tree, 'mailtpl.send').props.disabled, true);
  assert.equal(calls.length, 3, 'reset must not save or send');
});

test('variables insert into the selected body field and clear obsolete previews', async () => {
  const view = configView();
  const ui = harness({ useQuery: () => ({ data: view }), api: {} });
  ui.render('AlertTemplatePanel'); let tree = ui.render('AlertTemplatePanel');
  input(ui, tree, 'mailtpl.body').props.onFocus();
  tree = ui.render('AlertTemplatePanel');
  button(ui, tree, '{{hostname}}').props.onClick();
  tree = ui.render('AlertTemplatePanel');
  assert.equal(input(ui, tree, 'mailtpl.body').props.value, '{{details}}{{hostname}}');
  assert.equal(input(ui, tree, 'mailtpl.subject').props.value, '{{device}}');
});
