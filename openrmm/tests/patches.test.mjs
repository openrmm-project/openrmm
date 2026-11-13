import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const source = await readFile(path.join(root, 'src/screens-patches.jsx'), 'utf8');
const catalog = source.slice(source.indexOf('function CatalogTab('), source.indexOf('function PoliciesTab('));

// The lists are plain JS inside a JSX file, so they are cut out and run on
// their own rather than through a browser.
function loadLists() {
  const start = source.indexOf('const n0 = ');
  const end = source.indexOf('};', source.indexOf('const PATCH_LISTS = {')) + 2;
  assert.ok(start > 0 && end > start, 'PATCH_LISTS moved; update this test');
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(source.slice(start, end) + '\nthis.PATCH_LISTS = PATCH_LISTS;', sandbox);
  return sandbox.PATCH_LISTS;
}

// Same rule as matchesApprovalFilter in internal/store/store_patches.go: a
// patch is in a list when at least one of its devices is in that state.
test('a patch is listed under each state its devices are in', () => {
  const L = loadLists();
  assert.deepEqual(Object.keys(L), ['all', 'pending', 'approved', 'declined']);
  const lists = (row) => Object.keys(L).filter((k) => L[k](row));
  assert.deepEqual(lists({ pending_count: 2 }), ['all', 'pending']);
  // Approved stays approved through the install: this is the patch that used
  // to read as unapproved the moment its install was claimed or failed.
  for (const field of ['approved_count', 'installing_count', 'failed_count', 'installed_count']) {
    assert.deepEqual(lists({ [field]: 1 }), ['all', 'approved'], field);
  }
  assert.deepEqual(lists({ declined_count: 3 }), ['all', 'declined']);
  assert.deepEqual(lists({ pending_count: 1, failed_count: 1, declined_count: 1 }), ['all', 'pending', 'approved', 'declined']);
  // A row from a server without the counts is in no list but All, not an error.
  assert.deepEqual(lists({ missing_count: 4 }), ['all']);
});

test('the catalog shows the approval the server worked out, and offers what it lacks', () => {
  assert.match(catalog, /<ApprovalBadge a=\{p\.approval\} \/>/);
  // The old test compared counts that dropped every row being installed.
  assert.doesNotMatch(catalog, /approved_count >= p\.missing_count/);
  assert.match(catalog, /p\.approval !== 'approved'\s*\? <Button[^>]*onClick=\{\(\) => decide\(p, 'approve'\)\}/,
    'anything not approved everywhere can be approved');
  assert.match(catalog, /n0\(p\.failed_count\) > 0 && <Button[^>]*onClick=\{\(\) => decide\(p, 'approve'\)\}>\{t\('patches\.retry'\)\}/,
    'an approved patch whose install failed can be retried');
  assert.match(catalog, /p\.approval !== 'declined' && <Button[^>]*onClick=\{\(\) => decide\(p, 'decline'\)\}/,
    'a declined patch is not offered the decline again');
  assert.match(catalog, /aria-pressed=\{list === key\}/, 'the list buttons say which one is showing');
});

test('the devices list has a Department column', async () => {
  const devices = await readFile(path.join(root, 'src/screens-devices.jsx'), 'utf8');
  const screen = devices.slice(devices.indexOf('function DevicesScreen('), devices.indexOf('function PendingDeviceOperations('));
  assert.match(screen, /<th[^>]*>\{t\('devices\.col\.department'\)\}<\/th>/);
  assert.match(screen, /<td[^>]*>\{departmentName\(d\.group_id\)\}<\/td>/);
  // The loading rows span the whole table.
  const columns = (screen.match(/<th className=/g) || []).length;
  assert.match(screen, new RegExp(`colSpan=\\{${columns}\\}`), `the skeleton row must span ${columns} columns`);
});
