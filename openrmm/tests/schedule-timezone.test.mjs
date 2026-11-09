import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// A schedule's times are read in its own zone on the server. The console must
// start a new schedule in the browser's zone, keep an existing one's, and
// never show a time without the zone it is in: a bare "11:40" read as local
// time while the server ran it at 11:40 UTC.
async function load(file, from, to, sandbox) {
  const source = await readFile(new URL(`../src/${file}`, import.meta.url), 'utf8');
  const start = source.indexOf(from);
  // A closing brace marker ends the slice after the brace itself.
  const found = source.indexOf(to, start + from.length);
  const end = to === '\n}\n' ? found + 2 : found;
  assert.ok(start >= 0 && found > start, `${from} not found in ${file}`);
  const { code } = transformSync(source.slice(start, end), { loader: 'jsx' });
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox);
  return sandbox;
}

const uiSandbox = (intl) => ({
  Intl: intl,
  React: { createElement: (type, props, ...children) => ({ type, props, children }) },
  useMemo: (fn) => fn(),
  Select: 'Select',
});

test('a new schedule starts in the browser zone, and in UTC when the browser has none', async () => {
  const ny = await load('ui.jsx', 'function browserTimezone(', '/* --------------------------- query', uiSandbox({
    DateTimeFormat: () => ({ resolvedOptions: () => ({ timeZone: 'America/New_York' }) }),
  }));
  assert.equal(ny.browserTimezone(), 'America/New_York');
  const broken = await load('ui.jsx', 'function browserTimezone(', '/* --------------------------- query', uiSandbox({
    DateTimeFormat: () => { throw new Error('no Intl'); },
  }));
  assert.equal(broken.browserTimezone(), 'UTC');
  assert.equal(ny.scheduleZone(''), 'UTC');
  assert.equal(ny.scheduleZone(undefined), 'UTC');
  assert.equal(ny.scheduleZone('Asia/Tokyo'), 'Asia/Tokyo');
});

test('the zone picker lists UTC first and keeps a zone the browser does not list', async () => {
  const sb = await load('ui.jsx', 'function TimezoneSelect(', '// Combobox is a searchable', uiSandbox({
    supportedValuesOf: (kind) => (kind === 'timeZone' ? ['America/New_York', 'Europe/Lisbon'] : []),
  }));
  const el = sb.TimezoneSelect({ value: 'Mars/Olympus', onChange() {} });
  const values = el.children.flat().map((o) => o.props.value);
  assert.equal(values[0], 'UTC');
  assert.ok(values.includes('America/New_York') && values.includes('Mars/Olympus'));
  // An old browser without supportedValuesOf still offers UTC and the value.
  const old = await load('ui.jsx', 'function TimezoneSelect(', '// Combobox is a searchable', uiSandbox({}));
  const oldValues = old.TimezoneSelect({ value: 'America/Chicago', onChange() {} }).children.flat().map((o) => o.props.value);
  assert.deepEqual(oldValues, ['UTC', 'America/Chicago']);
});

test('a task schedule names the zone its times are in', async () => {
  const sb = await load('screens-rmm.jsx', 'function taskSchedule(', '\n}\n', {
    t: (key, params) => `${key} ${JSON.stringify(params || {})}`,
    minuteToHHMM: (m) => `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`,
    weekdayShort: (i) => ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][i],
    WEEKDAY_BITS: [1, 2, 3, 4, 5, 6, 0],
    fmtInterval: (s) => `${s}s`,
    scheduleZone: (tz) => tz || 'UTC',
  });
  assert.match(sb.taskSchedule({ schedule_kind: 'daily', run_at_minute: 700, timezone: 'America/New_York' }), /"time":"11:40 America\/New_York"/);
  assert.match(sb.taskSchedule({ schedule_kind: 'daily', run_at_minute: 700 }), /"time":"11:40 UTC"/);
  assert.match(sb.taskSchedule({ schedule_kind: 'weekly', days_of_week: 2, run_at_minutes: [540], timezone: 'Asia/Tokyo' }), /"times":"09:00 Asia\/Tokyo"/);
  assert.doesNotMatch(sb.taskSchedule({ schedule_kind: 'interval', interval_sec: 300, timezone: 'Asia/Tokyo' }), /Asia/);
});

test('the task and patch policy editors save the zone', async () => {
  const rmm = await readFile(new URL('../src/screens-rmm.jsx', import.meta.url), 'utf8');
  assert.match(rmm, /run_at_minutes: kind === 'weekly' \? times : \[\], timezone, enabled/);
  assert.match(rmm, /useState\(task \? scheduleZone\(task\.timezone\) : browserTimezone\(\)\)/);
  const patches = await readFile(new URL('../src/screens-patches.jsx', import.meta.url), 'utf8');
  assert.match(patches, /window_end_min: toMin\(end\), timezone, reboot/);
  assert.match(patches, /useState\(isNew \? browserTimezone\(\) : scheduleZone\(policy\.timezone\)\)/);
});
