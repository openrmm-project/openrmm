import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const source = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const code = source.slice(source.indexOf('function ConfigItem('), source.indexOf('// SMTPPanel configures'));
const config = {
  ai: { provider: 'openai-compatible', base_url: 'https://ai.example.invalid/v1', model: 'test', configured: true, api_key_set: true },
  remote: { default: 'native', providers: ['native', 'meshcentral', 'rustdesk'], meshcentral_enabled: true, meshcentral_url: 'https://mesh.example.invalid', meshcentral_user: 'shared-admin', meshcentral_token_set: true, meshcentral_shared_account: true, meshcentral_match_hostname: false, rustdesk_enabled: false, guest_access: false },
  jobs: { allow_unapproved: false }, server: { version: 'test', base_url: 'https://agents.example.invalid' },
};

function harness(canManage = true, failSave = false) {
  const states = [], deps = [], effects = [], calls = [], errors = [];
  let cursor = 0;
  const sandbox = {
    React: { createElement(type, props, ...children) {
      return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children };
    } },
    useState(initial) {
      const i = cursor++;
      if (!(i in states)) states[i] = initial;
      return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }];
    },
    useEffect(fn, values) {
      const i = cursor++;
      if (!deps[i] || values.some((v, j) => v !== deps[i][j])) { effects.push(fn); deps[i] = values; }
    },
    useQuery: () => ({ data: config }), useAuth: () => ({ can: () => canManage }),
    useToast: () => ({ success() {}, error(...args) { errors.push(args); } }),
    api: { async updateConfig(body) { calls.push(JSON.parse(JSON.stringify(body))); if (failSave) throw new Error('save failed'); } },
    t: (key) => key, cx: (...v) => v.filter(Boolean).join(' '),
    ...Object.fromEntries(['Card', 'Field', 'Select', 'Input', 'Button', 'Icon', 'StatusPill', 'Skeleton'].map((k) => [k, k])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const find = (node, fn, out = []) => {
    if (!node || typeof node !== 'object') return out;
    if (fn(node)) out.push(node);
    (node.children || []).flat(Infinity).forEach((child) => find(child, fn, out));
    return out;
  };
  let tree;
  const render = () => { cursor = 0; tree = sandbox.ConfigPanel(); effects.splice(0).forEach((fn) => fn()); return tree; };
  render(); render();
  return {
    calls, errors, render, find,
    tabs: () => find(tree, (n) => n.props.role === 'tab'),
    panel: () => find(tree, (n) => n.props.role === 'tabpanel' && !n.props.hidden)[0],
    selectTab(key) { find(tree, (n) => n.props.id === 'remote-config-tab-' + key)[0].props.onClick(); render(); },
    change(label, value) {
      const field = find(this.panel(), (n) => n.type === 'Field' && n.props.label === label)[0];
      assert.ok(field, 'field is in the active provider panel: ' + label);
      find(field, (n) => n.type === 'Input' || n.type === 'Select')[0].props.onChange({ target: { value } });
      render();
    },
    save: async () => { await find(tree, (n) => n.type === 'Button' && n.children.includes('cfg.save'))[0].props.onClick(); render(); },
  };
}

test('remote settings separate provider fields and preserve unsaved values when switching tabs', async () => {
  const ui = harness();
  assert.deepEqual(ui.tabs().map((n) => n.children[0]), ['cfg.remoteNative', 'MeshCentral', 'RustDesk', 'cfg.defaultProvider']);
  const labels = () => ui.find(ui.panel(), (n) => n.type === 'Field').map((n) => n.props.label);
  assert.deepEqual(labels(), ['cfg.guestAccess']);
  ui.change('cfg.guestAccess', 'on');
  ui.selectTab('meshcentral');
  assert.deepEqual(labels(), ['MeshCentral', 'cfg.meshUrl', 'cfg.meshUser', 'cfg.meshToken', 'cfg.meshSharedAccount', 'cfg.meshMatchHostname']);
  const secret = ui.find(ui.panel(), (n) => n.type === 'Input' && n.props.type === 'password')[0];
  assert.equal(secret.props.value, '', 'stored tokens are never returned to the form');
  assert.equal(secret.props.autoComplete, 'new-password');
  ui.change('cfg.meshUrl', 'https://new-mesh.example.invalid');
  ui.change('cfg.meshToken', 'test-token-not-a-real-secret');
  ui.change('cfg.meshSharedAccount', 'off');
  ui.selectTab('rustdesk');
  assert.deepEqual(labels(), ['RustDesk']);
  ui.change('RustDesk', 'on');
  ui.selectTab('default');
  assert.deepEqual(labels(), ['cfg.defaultProvider']);
  ui.change('cfg.defaultProvider', 'rustdesk');
  ui.selectTab('meshcentral');
  assert.equal(ui.find(ui.panel(), (n) => n.type === 'Input' && n.props.type === 'password')[0].props.value, 'test-token-not-a-real-secret');
  assert.equal(ui.calls.length, 0, 'switching tabs makes no write request');
  await ui.save();
  assert.deepEqual(ui.calls[0].remote, { ...Object.fromEntries(Object.entries(config.remote).filter(([k]) => !['providers', 'meshcentral_token_set'].includes(k))), default: 'rustdesk', meshcentral_url: 'https://new-mesh.example.invalid', meshcentral_token_key: 'test-token-not-a-real-secret', meshcentral_shared_account: false, rustdesk_enabled: true, guest_access: true });
  assert.equal(ui.find(ui.panel(), (n) => n.type === 'Input' && n.props.type === 'password')[0].props.value, '');
  assert.equal(ui.calls[0].ai.api_key, '');
  assert.equal(ui.calls[0].jobs.allow_unapproved, false);
});

test('remote settings retain tokens on failed save and leave existing token untouched on an ordinary save', async () => {
  const ui = harness();
  await ui.save();
  assert.equal(ui.calls[0].remote.meshcentral_token_key, '');
  const failing = harness(true, true);
  failing.selectTab('meshcentral');
  failing.change('cfg.meshToken', 'retry-token');
  await failing.save();
  assert.equal(failing.errors.length, 1);
  assert.equal(failing.find(failing.panel(), (n) => n.type === 'Input' && n.props.type === 'password')[0].props.value, 'retry-token');
});

test('read-only users can inspect every tab but cannot edit or save', () => {
  const ui = harness(false);
  for (const tab of ['native', 'meshcentral', 'rustdesk', 'default']) {
    ui.selectTab(tab);
    const inputs = ui.find(ui.panel(), (n) => n.type === 'Input' || n.type === 'Select');
    assert.ok(inputs.length > 0);
    assert.ok(inputs.every((n) => n.props.disabled));
  }
  assert.equal(ui.find(ui.render(), (n) => n.type === 'Button' && n.children.includes('cfg.save')).length, 0);
  assert.equal(ui.calls.length, 0);
});

test('provider tabs support keyboard navigation and expose consistent accessible relationships', () => {
  const ui = harness();
  let focused = -1;
  const elements = [0, 1, 2, 3].map((i) => ({ focus() { focused = i; } }));
  const currentTarget = { parentElement: { querySelectorAll: () => elements } };
  for (const [key, expected] of [['ArrowLeft', 3], ['ArrowRight', 0], ['End', 3], ['Home', 0], ['ArrowRight', 1]]) {
    let prevented = false;
    ui.tabs().find((n) => n.props['aria-selected']).props.onKeyDown({ key, currentTarget, preventDefault() { prevented = true; } });
    ui.render();
    assert.equal(prevented, true);
    assert.equal(focused, expected);
    const selected = ui.tabs()[expected];
    assert.equal(selected.props.tabIndex, 0);
    assert.equal(ui.tabs().filter((n) => n.props.tabIndex === 0).length, 1);
    assert.equal(selected.props['aria-controls'], ui.panel().props.id);
    assert.equal(ui.panel().props['aria-labelledby'], selected.props.id);
  }
});
