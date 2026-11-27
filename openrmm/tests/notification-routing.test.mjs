import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const source = await readFile(new URL('../src/notification-routing.jsx', import.meta.url), 'utf8');
const screens = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const users = [{ id: 'a', username: 'Alice', email: 'alice@example.test' }, { id: 'b', username: 'Bob', email: 'bob@example.test' }];
const initial = () => ({ local: { version: 0, mode: 'inherit', user_ids: [], recipients: [], unavailable_count: 0 }, effective: { mode: 'selected', source_name: 'Client A', recipients: [users[0]], unavailable_count: 1 } });
function harness({ canManage = true } = {}) {
  const states = [], effects = [], calls = []; let cursor = 0, tree, server = initial(), updateError, optionsError, pendingEffects = [];
  const sandbox = {
    React: { createElement: (type, props, ...children) => ({ type, props: props || {}, children }) },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useRef(initial) { const i = cursor++; if (!(i in states)) states[i] = { current: initial }; return states[i]; },
    useEffect(fn, deps) { const i = cursor++; const old = effects[i]; if (!old || JSON.stringify(old.deps) !== JSON.stringify(deps)) { old?.cleanup?.(); effects[i] = { deps }; pendingEffects.push(() => { effects[i].cleanup = fn(); }); } },
    useAuth: () => ({ can: () => canManage }), t: (key, args) => key + (args ? JSON.stringify(args) : ''), formatList: (a) => a.join(', '), window: { dataBus: { emit() {} } },
    api: {
      async getNotificationRouting(...args) { calls.push(['get', ...args]); return structuredClone(server); },
      async notificationRouteRecipients(...args) { calls.push(['options', ...args]); if (optionsError) throw optionsError; return { recipients: users.filter((u) => u.username.toLowerCase().includes(args[2].toLowerCase())), has_more: false }; },
      async updateNotificationRouting(...args) { calls.push(['put', ...JSON.parse(JSON.stringify(args))]); if (updateError) throw updateError; const body = args[2]; server.local = { ...server.local, ...body, version: server.local.version + 1, recipients: users.filter((u) => body.user_ids.includes(u.id)) }; return structuredClone(server); },
    },
    ...Object.fromEntries(['Button', 'Skeleton', 'Field', 'Select', 'Input'].map((s) => [s, s])),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(source, { loader: 'jsx' }).code, sandbox);
  const walk = (n, fn) => !n || typeof n !== 'object' ? [] : [...(fn(n) ? [n] : []), ...(n.children || []).flat(Infinity).flatMap((c) => walk(c, fn))];
  const render = () => { cursor = 0; tree = sandbox.NotificationRoutingPanel({ scopeType: 'site', scopeId: 'site-a' }); const next = pendingEffects; pendingEffects = []; next.forEach((fn) => fn()); };
  const flush = async () => { for (let i = 0; i < 4; i++) { await new Promise((resolve) => setImmediate(resolve)); render(); } };
  const find = (fn) => walk(tree, fn);
  const button = (text) => find((n) => n.type === 'Button' && n.children.includes(text))[0];
  render();
  return { calls, flush, render, find, button, text: () => JSON.stringify(tree),
    mode: (mode) => { find((n) => n.type === 'Select')[0].props.onChange({ target: { value: mode } }); render(); },
    search: (q) => { find((n) => n.type === 'Input')[0].props.onChange({ target: { value: q } }); render(); },
    pick: (i, checked) => { find((n) => n.type === 'input')[i].props.onChange({ target: { checked } }); render(); },
    errors: (update, options) => { updateError = update; optionsError = options; }, setServer: (data) => { server = data; },
  };
}

test('Client/Site routes select authorized accounts, preserve searched selections and explicitly return to inheritance', async () => {
  const ui = harness(); await ui.flush(); assert.match(ui.text(), /Client A/);
  ui.mode('selected'); assert.equal(ui.button('action.save').props.disabled, true);
  ui.pick(0, true); ui.search('bob'); await ui.flush(); assert.match(ui.text(), /alice@example.test/);
  ui.pick(1, true);
  await Promise.all([ui.button('action.save').props.onClick(), ui.button('action.save').props.onClick()]); await ui.flush();
  assert.equal(ui.calls.filter((c) => c[0] === 'put').length, 1, 'duplicate submit is guarded synchronously');
  assert.deepEqual(ui.calls.find((c) => c[0] === 'put')[3], { expected_version: 0, mode: 'selected', user_ids: ['a', 'b'] });
  ui.mode('inherit'); await ui.button('action.save').props.onClick(); await ui.flush();
  assert.deepEqual(ui.calls.filter((c) => c[0] === 'put').at(-1)[3], { expected_version: 1, mode: 'inherit', user_ids: [] });
});

test('conflict retains edits until explicit reload and unavailable selections have an explicit save warning', async () => {
  const ui = harness(); const data = initial(); data.local = { version: 4, mode: 'selected', user_ids: ['a'], recipients: [users[0]], unavailable_count: 1 }; ui.setServer(data); await ui.flush();
  // Initial request raced the supplied fixture; explicit reload obtains it.
  ui.button('routing.reload').props.onClick(); await ui.flush(); assert.match(ui.text(), /routing.removeUnavailable/);
  assert.equal(ui.button('action.save').props.disabled, false, 'announced cleanup can save the unchanged valid selection');
  ui.pick(1, true); ui.errors({ status: 409 }); await ui.button('action.save').props.onClick(); await ui.flush();
  assert.match(ui.text(), /routing.conflict/); assert.equal(ui.find((n) => n.type === 'input' && n.props.checked).length, 2);
  assert.equal(ui.button('action.save').props.disabled, true);
  ui.button('routing.reload').props.onClick(); await ui.flush(); assert.equal(ui.find((n) => n.type === 'input' && n.props.checked).length, 1);
});

test('viewer makes no requests and a scope denial removes prior recipient labels and controls', async () => {
  const viewer = harness({ canManage: false }); await viewer.flush(); assert.deepEqual(viewer.calls, []);
  const ui = harness(); await ui.flush(); ui.mode('selected'); ui.pick(0, true); ui.errors(undefined, { status: 403 }); ui.search('denied'); await ui.flush();
  assert.match(ui.text(), /routing.denied/); assert.doesNotMatch(ui.text(), /alice@example.test|bob@example.test|Client A/); assert.equal(ui.button('action.save'), undefined);
  assert.match(screens, /key=\{'client:' \+ client.id\}/); assert.match(screens, /key=\{'site:' \+ site.id\}/);
});
