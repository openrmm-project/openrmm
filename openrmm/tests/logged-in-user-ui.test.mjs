import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Devices ▸ Logged-in user: an optional, sortable, filterable column showing
// who is logged in interactively (DOMAIN\user, AzureAD\user), "No user logged
// in" otherwise; also on the device page and in the machines report.
const devices = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
const reports = await readFile(new URL('../src/screens-reports.jsx', import.meta.url), 'utf8');
const helpers = devices.slice(devices.indexOf("// The Devices grid's optional columns"), devices.indexOf('function DevicesScreen('));

function load() {
  const sandbox = {
    React: { createElement(type, p, ...children) { return typeof type === 'function' ? type({ ...p, children }) : { type, props: p || {}, children }; } },
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key),
    timeAgo: (when) => 'ago(' + when + ')',
    cx: (...c) => c.filter(Boolean).join(' '),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(helpers, { loader: 'jsx' }).code + '\nthis.sortDevices = sortDevices; this.LoggedInUser = LoggedInUser; this.hiddenDeviceColumns = hiddenDeviceColumns; this.cols = DEVICE_OPTIONAL_COLUMNS; this.labels = DEVICE_COLUMN_LABELS;', sandbox);
  return sandbox;
}

test('sorting by user is case-insensitive, both ways, with devices nobody is logged in to last', () => {
  const { sortDevices } = load();
  const list = () => [
    { id: 'a', logged_in_user: 'ACME\\zoe' }, { id: 'b', logged_in_user: '' },
    { id: 'c', logged_in_user: 'acme\\Ana' }, { id: 'd' }, { id: 'e', logged_in_user: 'AzureAD\\user10' }, { id: 'f', logged_in_user: 'AzureAD\\user9' },
  ];
  const ids = (l) => l.map((d) => d.id).join('');
  assert.equal(ids(sortDevices(list(), (d) => d.logged_in_user, 'asc')), 'cafebd');
  assert.equal(ids(sortDevices(list(), (d) => d.logged_in_user, 'desc')), 'efacbd', 'empties stay last when descending');
});

test('the cell names the user, says when no one is logged in, and dims a stale report', () => {
  const { LoggedInUser } = load();
  const none = LoggedInUser({ device: { status: 'online', logged_in_user: '' } });
  assert.equal(none.children.flat().join(''), 'devices.noUser');
  const online = LoggedInUser({ device: { status: 'online', logged_in_user: 'AzureAD\\JoaoSilva', logged_in_user_since: '2026-11-12T10:00:00Z' } });
  assert.equal(online.children.flat().join(''), 'AzureAD\\JoaoSilva');
  assert.match(online.props.className, /text-ink/);
  assert.match(online.props.title, /^devices\.userSince .*ago\(2026-11-12T10:00:00Z\)/);
  const offline = LoggedInUser({ device: { status: 'offline', logged_in_user: 'ACME\\maria' } });
  assert.match(offline.props.className, /text-faint/, 'an offline device shows its last report dimmed');
  assert.equal(offline.props.title, undefined);
});

test('the remembered hidden columns are read defensively', () => {
  const { hiddenDeviceColumns } = load();
  assert.deepEqual([...hiddenDeviceColumns('["user","tags"]')], ['user', 'tags']);
  for (const raw of [null, '', 'not json', '{"user":true}', '"user"', '42', 'null']) assert.deepEqual([...hiddenDeviceColumns(raw)], [], String(raw));
  assert.deepEqual([...hiddenDeviceColumns('["user","hostname","status","__proto__"]')], ['user'], 'hostname and status cannot be hidden');
});

test('the column can be shown or hidden, and the list filters and searches by user', () => {
  const { cols, labels } = load();
  assert.ok(cols.includes('user') && labels.user === 'devices.col.user', 'user is an optional column');
  assert.ok(!cols.includes('hostname') && !cols.includes('status'), 'hostname and status always show');
  assert.match(devices, /\{shows\('user'\) && sortTh\('user', t\('devices\.col\.user'\), 'px-3 hidden sm:table-cell'\)\}/);
  assert.match(devices, /\{shows\('user'\) && <td[^>]*><LoggedInUser device=\{d\} \/><\/td>\}/);
  assert.match(devices, /<window\.ColumnPicker columns=\{optionalCols\} hidden=\{hiddenCols\} onToggle=\{toggleCol\}/);
  assert.match(devices, /localStorage\.setItem\(DEVICE_COLUMNS_KEY/);
  assert.match(devices, /if \(userF === 'with' && !d\.logged_in_user\) return false;/);
  assert.match(devices, /if \(userF === 'none' && d\.logged_in_user\) return false;/);
  assert.match(devices, /\(d\.logged_in_user \|\| ''\)\.toLowerCase\(\)\.includes\(needle\)/, 'search matches the user');
  assert.match(devices, /colSpan=\{visibleCount\}/, 'the loading row spans the visible columns');
});

test('the machines report has the column and the device page shows the user', () => {
  assert.match(reports, /\{ key: 'logged_in_user', text: \(r\) => r\.logged_in_user \|\| ''/);
  assert.match(reports, /window\.ColumnPicker = ColumnPicker;/);
  const header = devices.slice(devices.indexOf("t('device.lastSeen'"));
  assert.match(header.slice(0, 400), /<LoggedInUser device=\{d\} \/>/);
});
