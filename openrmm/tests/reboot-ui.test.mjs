import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';
const source = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
const helpers = source.slice(source.indexOf("// The Devices grid's optional columns"), source.indexOf('function DevicesScreen('));
const sandbox = { React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props, children }; } }, t: (key, vars) => key + (vars ? JSON.stringify(vars) : ''), timeAgo: String, cx: (...c) => c.filter(Boolean).join(' ') };
vm.createContext(sandbox);
vm.runInContext(transformSync(helpers, { loader: 'jsx' }).code + '\nthis.present = rebootPresentation; this.matches = matchesRebootFilter; this.details = RebootDetails;', sandbox);
const now = Date.now();
const fresh = { status: 'online', reboot_status: 'not_required', reboot_checked_at: new Date(now).toISOString() };
test('reboot filters distinguish current, stale, legacy and retained positive observations', () => {
  assert.equal(sandbox.present(fresh, now).state, 'not_required');
  for (const d of [{}, { ...fresh, status: 'offline' }, { ...fresh, status: 'compliant', agent_status: 'offline' }, { ...fresh, reboot_checked_at: new Date(now - 121000).toISOString() }, { ...fresh, reboot_status: 'unknown' }]) {
    assert.equal(sandbox.present(d, now).state, 'unknown');
    assert.equal(sandbox.matches(d, 'not_required', now), false);
    assert.equal(sandbox.matches(d, 'unknown', now), true);
  }
  const retained = { ...fresh, reboot_pending: true, reboot_status: 'unknown' };
  assert.equal(sandbox.present(retained, now).lastKnown, true);
  assert.equal(sandbox.matches(retained, 'required', now), true);
  assert.equal(sandbox.matches(retained, 'unknown', now), true);
});
test('detail exposes reason, first detection, checked time and distinct request/observed boot', () => {
  const text = JSON.stringify(sandbox.details({ device: { ...fresh, reboot_pending: true, reboot_reason: '<kernel>', reboot_pending_since: 'first', reboot_requested_at: 'requested', reboot_observed_at: 'confirmed' } }));
  for (const value of ['<kernel>', 'reboot.checked', 'reboot.detectedSince', 'reboot.lastRequest', 'reboot.observed']) assert.ok(text.includes(value), value);
  const legacy = JSON.stringify(sandbox.details({ device: {} }));
  assert.ok(legacy.includes('reboot.neverChecked'));
  assert.ok(!legacy.includes('reboot.observed'));
});

test('an observed boot is not relabeled as confirmation of a request', () => {
  const text = JSON.stringify(sandbox.details({ device: { ...fresh, reboot_observed_at: 'before-request', reboot_requested_at: 'after-boot' } }));
  assert.ok(text.includes('reboot.observed'));
  assert.ok(!text.includes('reboot.confirmed'));
  assert.ok(text.includes('before-request'));
});
