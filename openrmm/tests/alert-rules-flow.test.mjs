import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Exercise the real JSX and event handlers without starting a server/browser.
// Database-backed router E2E tests complement these console user flows in CI.
const source = await readFile(new URL('../src/screens-rmm.jsx', import.meta.url), 'utf8');
const settings = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const definitions = source.slice(source.indexOf('const CHECK_TYPES ='), source.indexOf('// A task\'s schedule'));
const editor = source.slice(source.indexOf('function CheckEditor('), source.indexOf('/* ============================ SCHEDULED TASKS'));
const notifications = settings.slice(settings.indexOf('function NotificationsCard('), settings.indexOf('// Mirrors passwordProblem'));
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
    useToast: () => ({ success() {}, error(...args) { throw new Error(args.join(':')); }, info() {} }),
    useAuth: () => ({ can: () => true }), t: (key) => key,
    cx: (...args) => args.filter(Boolean).join(' '), notifyLabel: (key) => ({ title: key, msg: key }),
    ...Object.fromEntries(['Modal', 'Field', 'Select', 'Input', 'Button', 'Icon', 'Combobox', 'Toggle', 'CodeEditor', 'ConfirmDialog', 'Skeleton', 'Card'].map((k) => [k, k])),
    ...extra,
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(transformSync(definitions + editor + notifications, { loader: 'jsx' }).code, sandbox);
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

test('console creates an enabled ignore rule and keeps agent check settings distinct', async () => {
  const calls = [];
  const ui = harness({ api: { createCheck: async (body) => calls.push(data(body)) } });
  const props = { onClose() {}, devMap: {}, policies: [{ id: 'p', name: 'Laptops' }], defaultPolicyId: 'p' };
  let tree = ui.render('CheckEditor', props);
  ui.find(tree, (n) => n.type === 'Select')[0].props.onChange({ target: { value: 'connectivity' } });
  tree = ui.render('CheckEditor', props);
  const field = ui.find(tree, (n) => n.type === 'Field' && n.props.label === 'rules.mode')[0];
  ui.find(field, (n) => n.type === 'Select')[0].props.onChange({ target: { value: 'ignore' } });
  tree = ui.render('CheckEditor', props);
  assert.equal(ui.find(tree, (n) => n.type === 'Field' && n.props.label === 'checks.intervalSec').length, 0);
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('checks.create'))[0].props.onClick();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].policy_id, 'p');
  assert.equal(calls[0].enabled, true, 'ignore rules must remain enabled to override inherited defaults');
  assert.deepEqual(calls[0].config, { mode: 'ignore', severity: 'error', after_min: 3 });
  assert.equal('warn_threshold' in calls[0], false);
  // Control: switching back to an agent check keeps its thresholds/settings.
  ui.find(tree, (n) => n.type === 'Select')[0].props.onChange({ target: { value: 'diskspace' } });
  tree = ui.render('CheckEditor', props);
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('checks.create'))[0].props.onClick();
  assert.equal(calls[1].type, 'diskspace');
  assert.equal(calls[1].warn_threshold, 20);
  assert.equal('mode' in calls[1].config, false);
});

test('each server type exposes its own config and drops unrelated fields', () => {
  const ui = harness();
  for (const [type, label] of [
    ['connectivity', 'rules.afterMin'],
    ['job_failure', 'rules.jobScope'],
    ['patch_failure', null],
    ['reboot_pending', 'rules.afterDays'],
    ['agent_outdated', 'rules.behind'],
  ]) {
    const tree = ui.render('ServerRuleFields', { type, value: { script: 'never dispatch', mode: 'alert' }, onChange() {} });
    if (label) assert.equal(ui.find(tree, (n) => n.type === 'Field' && n.props.label === label).length, 1);
    assert.equal(JSON.stringify(tree).includes('never dispatch'), false);
    assert.equal(ui.find(tree, (n) => n.type === 'CodeEditor').length, 0);
  }
});

test('fleet defaults are read-only for restricted users and editable for global managers', async () => {
  const calls = [];
  const ui = harness({ useQuery: () => ({ data: { defaults: { connectivity: { config: { mode: 'alert', after_min: 3 } } } } }), api: { updateAlertDefault: async (...args) => calls.push(data(args)) } });
  let tree = ui.render('AlertDefaultsEditor', { canEdit: false, onClose() {} });
  assert.equal(ui.find(tree, (n) => n.type === 'Button' && n.children.includes('action.save')).length, 0);
  assert.ok(ui.find(tree, (n) => n.type === 'Select' && n.props.disabled).length > 0);
  tree = ui.render('AlertDefaultsEditor', { canEdit: true, onClose() {} });
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.onClick();
  assert.deepEqual(calls, [['connectivity', { mode: 'alert', severity: 'error', after_min: 3 }]]);
});

test('email preferences save selected rules and defaults independently from event subscriptions', async () => {
  const calls = [];
  const prefs = { email: 'user@example.invalid', delivery_configured: true, subscriptions: [{ event_type: 'check_failed', enabled: true }], rules: { mode: 'all', include_defaults: true, check_ids: [] }, available_rules: [{ id: 'r1', type: 'connectivity', name: 'Laptops' }] };
  const ui = harness({ useQuery: () => ({ data: prefs, refetch() {} }), api: { updateMyNotifications: async (body) => calls.push(data(body)) } });
  ui.render('NotificationsCard'); // apply the real loading effect
  let tree = ui.render('NotificationsCard');
  ui.find(tree, (n) => n.type === 'Select')[0].props.onChange({ target: { value: 'selected' } });
  tree = ui.render('NotificationsCard');
  const toggles = ui.find(tree, (n) => n.type === 'Toggle');
  toggles[0].props.onChange(false); // fleet defaults
  toggles[1].props.onChange(true); // r1
  tree = ui.render('NotificationsCard');
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.onClick();
  assert.deepEqual(calls[0], { subscriptions: { check_failed: true }, rules: { mode: 'selected', include_defaults: false, check_ids: ['r1'] } });
  ui.find(tree, (n) => n.type === 'Select')[0].props.onChange({ target: { value: 'all' } });
  tree = ui.render('NotificationsCard');
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('action.save'))[0].props.onClick();
  assert.equal(calls[1].rules.mode, 'all');
});

test('console creates Linux and macOS service checks with platform-specific fields', async () => {
  const calls = [];
  const ui = harness({ api: { createCheck: async (body) => calls.push(data(body)) } });
  const props = { onClose() {}, devMap: {}, policies: [{ id: 'mixed-policy', name: 'Mixed fleet' }], defaultPolicyId: 'mixed-policy' };
  let tree = ui.render('CheckEditor', props);
  const changeType = (kind) => { ui.find(tree, (n) => n.type === 'Select')[0].props.onChange({ target: { value: kind } }); tree = ui.render('CheckEditor', props); };
  const changeField = (label, value) => {
    const field = ui.find(tree, (n) => n.type === 'Field' && n.props.label === label)[0];
    ui.find(field, (n) => n.type === 'Input' || n.type === 'Select')[0].props.onChange({ target: { value } });
    tree = ui.render('CheckEditor', props);
  };
  const save = async () => ui.find(tree, (n) => n.type === 'Button' && n.children.includes('checks.create'))[0].props.onClick();
  changeType('linuxservice');
  changeField('check.cfg.linuxservice', 'sshd.service');
  assert.equal(ui.find(tree, (n) => n.type === 'Field' && n.props.label === 'check.service.state').length, 0);
  await save();
  assert.deepEqual(calls[0].config, { service: 'sshd.service' });
  assert.equal(calls[0].policy_id, 'mixed-policy');
  assert.equal('warn_threshold' in calls[0], false);
  changeType('macservice');
  changeField('check.cfg.macservice', 'gui/501/com.example.worker');
  changeField('check.service.state', 'loaded');
  await save();
  assert.deepEqual(calls[1].config, { service: 'gui/501/com.example.worker', state: 'loaded' });
  assert.equal('err_threshold' in calls[1], false);
  changeType('linuxservice');
  changeField('check.cfg.linuxservice', 'nginx');
  await save();
  assert.deepEqual(calls[2].config, { service: 'nginx' }, 'macOS options do not leak into Linux');
});

test('editing a macOS service retains its loaded state', async () => {
  const calls = [];
  const ui = harness({ api: { updateCheck: async (...args) => calls.push(data(args)) } });
  const tree = ui.render('CheckEditor', { check: { id: 'mac-check', type: 'macservice', name: 'On demand', agent_id: 'mac-1', config: { service: 'system/com.example.worker', state: 'loaded' } }, onClose() {}, devMap: {}, policies: [] });
  await ui.find(tree, (n) => n.type === 'Button' && n.children.includes('checks.save'))[0].props.onClick();
  assert.deepEqual(calls[0][1].config, { service: 'system/com.example.worker', state: 'loaded' });
});
