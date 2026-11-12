import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Policy targeting in the console (RFC #258): the device page shows the role
// policy assignments filter on and lets an administrator correct it on any OS;
// assignments show their filters; the device list filters by role.
const devices = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
const rmm = await readFile(new URL('../src/screens-rmm.jsx', import.meta.url), 'utf8');
const rmm2 = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const slice = (src, from, to) => src.slice(src.indexOf(from), src.indexOf(to));

function render(code, component, props) {
  const calls = [];
  const states = []; let cursor = 0;
  const sandbox = {
    React: { createElement(type, p, ...children) { return typeof type === 'function' ? type({ ...p, children }) : { type, props: p || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = initial; return [states[i], (v) => { states[i] = v; }]; },
    useToast: () => ({ success() {}, error() {} }), t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key),
    DEVICE_ROLES: ['workstation', 'server'], window: { dataBus: { emit() {} } }, Select: 'Select',
    api: new Proxy({}, { get: (_, name) => async (...args) => { calls.push([name, ...args]); } }),
  };
  vm.createContext(sandbox); vm.runInContext(transformSync(code, { loader: 'jsx' }).code, sandbox);
  const tree = sandbox[component](props);
  const walk = (pred, node, out = []) => { if (!node || typeof node !== 'object') return out; if (pred(node)) out.push(node); (node.children || []).flat(Infinity).forEach((c) => walk(pred, c, out)); return out; };
  return { tree, calls, find: (pred) => walk(pred, tree), text: JSON.stringify(tree) };
}

test('a Mac is shown as detected and can be corrected to a server, or back to detection', async () => {
  const code = slice(devices, 'function DeviceRole(', 'function DeviceDetail(');
  const mac = { id: 'd1', os: 'darwin', os_role: 'workstation', role_override: '', role: 'workstation' };
  const ui = render(code, 'DeviceRole', { device: mac, canEdit: true });
  const select = ui.find((n) => n.type === 'Select')[0];
  assert.ok(ui.text.includes('device.roleFollow') && ui.text.includes('target.role.workstation'), 'the detected role is named');
  const options = ui.find((n) => n.type === 'option').map((n) => n.props.value);
  assert.deepEqual(options, ['', 'workstation', 'server'], 'any OS can be set to either role');
  await select.props.onChange({ target: { value: 'server' } });
  await select.props.onChange({ target: { value: '' } });
  assert.deepEqual(JSON.parse(JSON.stringify(ui.calls)), [['setDeviceRole', 'd1', 'server'], ['setDeviceRole', 'd1', '']]);
  const linux = render(code, 'DeviceRole', { device: { id: 'd2', os: 'linux', os_role: '', role_override: '', role: '' }, canEdit: false });
  assert.equal(linux.find((n) => n.type === 'Select').length, 0, 'read-only without devices.manage');
  assert.ok(linux.text.includes('device.roleUnknown'), 'Linux shows that no role is set');
});

test('assignment filters are offered and shown, and the device list filters by role', () => {
  const chips = render(slice(rmm, 'function TargetChips(', '// TaskReview shows'), 'TargetChips', { os: 'windows', role: 'server' });
  assert.ok(chips.text.includes('target.os.windows') && chips.text.includes('target.role.server'));
  assert.equal(render(slice(rmm, 'function TargetChips(', '// TaskReview shows'), 'TargetChips', { os: '', role: '' }).tree, null, 'no filter, no chips');
  const detail = rmm2.slice(rmm2.indexOf('function PolicyDetail('));
  assert.match(detail, /<TargetSelects os=\{addOs\} role=\{addRole\}/);
  assert.match(detail, /\{ value: 'global:', label: t\('pol\.globalOption'\)/);
  assert.match(detail, /value: 'exclusions', label: t\('pol\.tab\.exclusions'\)/);
  assert.match(detail, /api\.addPolicyExclusion\(policy\.id, addExcluded\)/);
  assert.match(devices, /if \(roleF !== 'all' && \(d\.role \|\| 'none'\) !== roleF\) return false;/);
});
