import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Switching a policy or patch policy on, and approving an AI-proposed task, go
// through one review: the content is on screen, nothing changes until the
// person confirms, and a review that went stale is refused and reloaded.
const rmm2 = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const rmm = await readFile(new URL('../src/screens-rmm.jsx', import.meta.url), 'utf8');
const patches = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
const slice = (src, from, to) => src.slice(src.indexOf(from), src.indexOf(to));
const COMPONENTS = ['Card', 'Modal', 'Drawer', 'Field', 'Input', 'Button', 'Select', 'Toggle', 'ConfirmDialog', 'Icon', 'CodeBlock', 'Skeleton',
  'EmptyState', 'Page', 'PageHeader', 'OsIcon', 'TaskEditor', 'ReviewModal', 'ReviewSection', 'ReviewRow', 'TaskReview', 'CheckReview', 'ProposalBadge'];

function harness(code, component, props, responses, data = {}) {
  const states = []; const calls = []; const toasts = []; let cursor = 0; let effectCursor = 0; let tree;
  const effects = {}; let pending = [];
  const sandbox = {
    React: { createElement(type, p, ...children) { return typeof type === 'function' ? type({ ...p, children }) : { type, props: p || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useEffect(fn, deps) { const i = effectCursor++; const key = JSON.stringify(deps); if (effects[i] !== key) { effects[i] = key; pending.push(fn); } },
    useToast: () => ({ success: (...a) => toasts.push(['success', ...a]), error: (...a) => toasts.push(['error', ...a]), info() {} }),
    useAuth: () => ({ can: () => true }), useDeviceMap: () => ({}),
    useQuery: (key) => ({ data: data[key[0]] || {}, loading: false }),
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key), tEnum: (ns, v) => ns + '.' + v, tn: (k, n) => k + n,
    cx: (...v) => v.filter(Boolean).join(' '), timeAgo: (v) => v, taskSchedule: (task) => 'every ' + task.interval_sec,
    asList: (v) => (Array.isArray(v) ? v : []), weekdayShort: (d) => 'd' + d, minuteToHHMM: (m) => String(m),
    awaitingReview: (row) => !!row?.proposed_by && !row?.approved_by,
    window: { dataBus: { emit() {} } },
    api: new Proxy({}, { get: (_, name) => async (...args) => { calls.push([name, ...args]); const r = responses[name]; return typeof r === 'function' ? r(...args) : r; } }),
    ...Object.fromEntries(COMPONENTS.map((n) => [n, n])),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const render = () => { cursor = 0; effectCursor = 0; tree = sandbox[component](props); const run = pending; pending = []; run.forEach((fn) => fn()); return tree; };
  const settle = async () => { for (let i = 0; i < 3; i++) { await new Promise((r) => setTimeout(r, 0)); render(); } };
  const walk = (pred, node, out) => { if (!node || typeof node !== 'object') return out; if (pred(node)) out.push(node); (node.children || []).flat(Infinity).forEach((c) => walk(pred, c, out)); return out; };
  render();
  return { render, settle, calls, toasts, find: (pred) => walk(pred, tree, []), text: () => JSON.stringify(tree) };
}

const review = {
  fingerprint: 'fp-1', pending_tasks: 1, pending_checks: 1,
  policy: { id: 'p1', name: 'AI workstations', enabled: false, pending: true, proposed_by: 'mcp:claude' },
  assignments: [{ id: 'a1', scope_type: 'client', scope_id: 'c1' }],
  tasks: [{ id: 't1', name: 'Clean temp', shell: 'bash', script: 'find /tmp -mtime +7 -delete', interval_sec: 3600, enabled: false, proposed_by: 'mcp:claude' },
    { id: 't2', name: 'Mine', shell: 'sh', script: 'true', interval_sec: 60, enabled: false }],
  checks: [{ id: 'c1', name: 'Disk', type: 'diskspace', enabled: false, proposed_by: 'mcp:claude', config: { path: '/' } }],
};

test('activating a policy shows everything it puts into effect and sends the reviewed fingerprint', async () => {
  let activated = null;
  const ui = harness(slice(rmm2, 'function PolicyActivation(', 'function PolicyCard('), 'PolicyActivation',
    { policy: review.policy, scopeName: () => 'Acme', onClose() {}, onActivated: (p) => { activated = p; } },
    { reviewPolicy: review, activatePolicy: { policy: { ...review.policy, enabled: true, pending: false } } });
  await ui.settle();
  const text = ui.text();
  for (const shown of ['Acme', 'Clean temp', 'Mine', 'Disk', 'pol.reviewWillApprove', 'pol.reviewStaysOff']) assert.ok(text.includes(shown), shown + ' is shown');
  assert.equal(ui.calls.filter((c) => c[0] === 'activatePolicy').length, 0, 'nothing is activated before the confirmation');
  const modal = ui.find((n) => n.type === 'ReviewModal')[0];
  await modal.props.onConfirm();
  assert.deepEqual(ui.calls.find((c) => c[0] === 'activatePolicy'), ['activatePolicy', 'p1', 'fp-1']);
  assert.equal(activated?.enabled, true);
});

test('a stale review is refused, explained and reloaded for a second reading', async () => {
  let reviews = 0;
  const ui = harness(slice(rmm2, 'function PolicyActivation(', 'function PolicyCard('), 'PolicyActivation',
    { policy: review.policy, scopeName: () => 'Acme', onClose() {} },
    {
      reviewPolicy: () => { reviews++; return { ...review, fingerprint: 'fp-' + reviews }; },
      activatePolicy: () => { const e = new Error('conflict'); e.status = 409; throw e; },
    });
  await ui.settle();
  await ui.find((n) => n.type === 'ReviewModal')[0].props.onConfirm();
  await ui.settle();
  const modal = ui.find((n) => n.type === 'ReviewModal')[0];
  assert.equal(modal.props.error, 'review.stale');
  assert.equal(reviews, 2, 'the review was read again');
  await modal.props.onConfirm();
  assert.deepEqual(ui.calls.filter((c) => c[0] === 'activatePolicy').map((c) => c[2]), ['fp-1', 'fp-2']);
});

test('an AI-proposed task is switched on only through its review', async () => {
  const proposal = { id: 't9', name: 'Proposed', shell: 'bash', script: 'echo hi', interval_sec: 600, enabled: false, proposed_by: 'mcp:claude' };
  const own = { id: 't8', name: 'Own', shell: 'bash', script: 'true', interval_sec: 60, enabled: false };
  const ui = harness(slice(rmm, 'function TasksScreen(', '// defaultPolicyId pre-selects'), 'TasksScreen', {}, {},
    { tasks: { tasks: [proposal, own] } });
  const toggles = () => ui.find((n) => n.type === 'Toggle');
  toggles()[0].props.onChange(); ui.render();
  const modal = () => ui.find((n) => n.type === 'ReviewModal')[0];
  assert.equal(modal().props.open, true, 'the proposal opens its review');
  assert.equal(ui.find((n) => n.type === 'ConfirmDialog' && n.props.open).length, 0, 'not the bare toggle confirmation');
  assert.ok(JSON.stringify(modal()).includes('Proposed'));
  assert.equal(ui.calls.length, 0);
  await modal().props.onConfirm();
  assert.deepEqual(JSON.parse(JSON.stringify(ui.calls[0])), ['updateTask', 't9', { ...proposal, enabled: true }]);
  // A person's own task keeps the plain confirmation.
  toggles()[1].props.onChange(); ui.render();
  assert.equal(ui.find((n) => n.type === 'ConfirmDialog' && n.props.open).length, 1);
});

test('activating a patch policy reviews its settings and scopes, then sends the fingerprint', async () => {
  const policy = { id: 'pp1', name: 'AI weekend', enabled: false, pending: true, proposed_by: 'mcp:claude' };
  const ui = harness(slice(patches, 'function PatchPolicyActivation(', 'function PolicyModal('), 'PatchPolicyActivation', { policy, onClose() {} }, {
    reviewPatchPolicy: { fingerprint: 'pfp', policy: { ...policy, auto_approve: ['security'], auto_approve_age_days: 3, window_days: [0, 6], window_start_min: 120, window_end_min: 360, timezone: 'America/Sao_Paulo', reboot: 'always', exclusions: [] },
      assignments: [{ id: 'x', scope_type: 'client', scope_id: 'c1' }] },
    activatePatchPolicy: { ...policy, enabled: true },
  }, { clients: { clients: [{ id: 'c1', name: 'Acme' }] } });
  await ui.settle();
  const text = ui.text();
  for (const shown of ['Acme', 'patch.class.security', 'patchpol.reboot.always', 'America/Sao_Paulo', 'mcp:claude']) assert.ok(text.includes(shown), shown + ' is shown');
  assert.equal(ui.calls.filter((c) => c[0] === 'activatePatchPolicy').length, 0);
  await ui.find((n) => n.type === 'ReviewModal')[0].props.onConfirm();
  assert.deepEqual(ui.calls.find((c) => c[0] === 'activatePatchPolicy'), ['activatePatchPolicy', 'pp1', 'pfp']);
});

test('a pending patch policy cannot be switched on from its edit form', () => {
  const form = slice(patches, 'function PolicyModal(', 'function PolicyAssignments(');
  assert.match(form, /policy\.pending\s*\?\s*<p role="note"[^]*patchpol\.pendingHint[^]*:\s*<label[^]*<Toggle checked=\{enabled\}/);
});
