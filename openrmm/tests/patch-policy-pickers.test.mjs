import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Patches ▸ Policies ▸ Assigned to / exceptions: a fleet can have hundreds of
// clients, sites and devices, so both pickers are searchable (Combobox), the
// assignment picker offers every scope the API accepts for a patch policy
// (every device, client, site, device), and a device assignment reads by name.
const src = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
const code = src.slice(src.indexOf('function PolicyAssignments('), src.indexOf('window.PatchesScreen = PatchesScreen;'));

function harness() {
  const states = [], calls = []; let cursor = 0;
  const data = {
    'patch-assign': { assignments: [{ id: 'a1', scope_type: 'agent', scope_id: 'dev-1', os: '', role: '' }, { id: 'a2', scope_type: 'client', scope_id: 'c1', os: 'windows', role: 'server' }] },
    clients: { clients: [{ id: 'c1', name: 'Acme' }, { id: 'c2', name: 'Globex' }] },
    sites: { sites: [{ id: 's1', name: 'Main office', client_id: 'c1' }] },
    'patch-excl': { exclusions: [{ agent_id: 'dev-2', hostname: 'ALPHA-WS' }] },
  };
  const sandbox = {
    React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useAuth: () => ({ can: () => true }),
    useToast: () => ({ error(...args) { calls.push({ name: 'toast.error', args }); } }),
    useQuery: (key) => ({ data: data[key[0]] }),
    useDeviceMap: () => ({ 'dev-1': { id: 'dev-1', hostname: 'SMOKE-PC' }, 'dev-2': { id: 'dev-2', hostname: 'ALPHA-WS' }, 'dev-3': { id: 'dev-3', hostname: 'BETA-SRV' } }),
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key),
    window: { dataBus: { emit() {} } },
    api: Object.fromEntries(['listPatchAssignments', 'listClients', 'listSites', 'listPatchPolicyExclusions', 'addPatchAssignment', 'addPatchPolicyExclusion', 'removePatchPolicyExclusion', 'deletePatchAssignment']
      .map((name) => [name, async (...args) => { calls.push({ name, args: JSON.parse(JSON.stringify(args)) }); return {}; }])),
    ...Object.fromEntries(['Field', 'Icon', 'Button', 'Select', 'Combobox', 'TargetChips', 'TargetSelects'].map((name) => [name, name])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  let tree;
  const render = () => { cursor = 0; tree = sandbox.PolicyAssignments({ policyId: 'pp-1' }); return tree; };
  const find = (node, predicate, out = []) => { if (!node || typeof node !== 'object') return out; if (predicate(node)) out.push(node); (node.children || []).flat(Infinity).forEach((child) => find(child, predicate, out)); return out; };
  const text = (node) => (typeof node === 'string' ? node : (node?.children || []).flat(Infinity).map(text).join(''));
  render();
  return { calls, render, find: (predicate) => find(tree, predicate), text: () => text(tree) };
}

test('both patch policy pickers are searchable and offer every scope a patch policy takes', () => {
  const ui = harness();
  assert.equal(ui.find((n) => n.type === 'Select').length, 0, 'no plain select is left for scopes or devices');
  const [scopes, devices] = ui.find((n) => n.type === 'Combobox');
  assert.equal(scopes.props.placeholder, 'patchpol.addAssignment');
  assert.deepEqual([...scopes.props.options].map((o) => o.value), ['global:', 'client:c1', 'client:c2', 'site:s1', 'agent:dev-1', 'agent:dev-2', 'agent:dev-3']);
  assert.deepEqual([...new Set([...scopes.props.options].map((o) => o.group))], ['pol.group.global', 'patchpol.clients', 'patchpol.sites', 'pol.group.agents']);
  assert.ok(!scopes.props.options.some((o) => o.value.startsWith('group:')), 'departments are not a patch policy scope');
  assert.equal(devices.props.placeholder, 'pol.addExclusion');
  assert.deepEqual([...devices.props.options].map((o) => o.label), ['SMOKE-PC', 'BETA-SRV'], 'a device already left out is not offered again');
});

test('a device assignment reads by name and the pickers send what was chosen', async () => {
  const ui = harness();
  assert.match(ui.text(), /SMOKE-PC/, 'the device assignment shows its hostname');
  assert.doesNotMatch(ui.text(), /agent: dev-1/);
  let [scopes] = ui.find((n) => n.type === 'Combobox');
  scopes.props.onChange('agent:dev-3'); ui.render();
  const add =ui.find((n) => n.type === 'Button' && n.children.flat(Infinity).includes('action.add'))[0];
  await add.props.onClick();
  assert.deepEqual(ui.calls.find((c) => c.name === 'addPatchAssignment')?.args, ['pp-1', { scope_type: 'agent', scope_id: 'dev-3', os: '', role: '' }]);
  ui.render();
  [scopes] = ui.find((n) => n.type === 'Combobox');
  scopes.props.onChange('global:'); ui.render();
  await ui.find((n) => n.type === 'Button' && n.children.flat(Infinity).includes('action.add'))[0].props.onClick();
  assert.deepEqual(ui.calls.filter((c) => c.name === 'addPatchAssignment').at(-1).args, ['pp-1', { scope_type: 'global', scope_id: '', os: '', role: '' }]);
  ui.render();
  const devices = ui.find((n) => n.type === 'Combobox')[1];
  devices.props.onChange('dev-3'); ui.render();
  await ui.find((n) => n.type === 'Button' && n.children.flat(Infinity).includes('pol.exclude'))[0].props.onClick();
  assert.deepEqual(ui.calls.find((c) => c.name === 'addPatchPolicyExclusion')?.args, ['pp-1', 'dev-3']);
});
