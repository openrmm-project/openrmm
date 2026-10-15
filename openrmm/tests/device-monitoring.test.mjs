import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const read = (name) => readFile(path.join(root, name), 'utf8');

// The device drawer had Inventory, Jobs, Patches, Run script and Notes, and no
// way to see what was monitoring or scheduled on the machine: that lived only on
// the fleet-wide Checks and Scheduled tasks screens, where a policy check shows
// its policy rather than the devices it reaches.
test('the device drawer has Checks and Tasks tabs next to Jobs, each behind its permission', async () => {
  const source = await read('src/screens-devices.jsx');
  const tabs = source.match(/<Tabs className="px-5" value=\{tab\} onChange=\{setTab\} tabs=\{\[(.*)\]\} \/>/);
  assert.ok(tabs, 'the device drawer tab list moved; update this test');
  const order = [...tabs[1].matchAll(/value: '([a-z]+)'/g)].map((m) => m[1]);
  assert.deepEqual(order.slice(0, 4), ['inventory', 'jobs', 'checks', 'tasks'], 'Checks and Tasks follow Jobs');
  assert.match(tabs[1], /can\('checks\.view'\) \? \[\{ value: 'checks'/);
  assert.match(tabs[1], /can\('tasks\.view'\) \? \[\{ value: 'tasks'/);
  assert.match(source, /tab === 'checks' && can\('checks\.view'\) && <DeviceChecks id=\{id\}/);
  assert.match(source, /tab === 'tasks' && can\('tasks\.view'\) && <DeviceTasks id=\{id\}/);
});

test('the tabs read what applies to the device, not only what is assigned to it', async () => {
  const api = await read('src/mockapi.jsx');
  // /api/checks?agent_id= lists direct assignments only; a policy check would
  // be missing from the very machine it monitors.
  assert.match(api, /listDeviceChecks\(id\) \{ return GET\('\/api\/devices\/' \+ id \+ '\/checks'\); \}/);
  assert.match(api, /listDeviceTasks\(id\) \{ return GET\('\/api\/devices\/' \+ id \+ '\/tasks'\); \}/);
  const source = await read('src/screens-devices.jsx');
  assert.match(source, /api\.listDeviceChecks\(id\)/);
  assert.match(source, /api\.listDeviceTasks\(id\)/);
});

// Each source file is its own block scope in the bundle: helpers from
// screens-rmm.jsx reach the drawer only through window.
test('the drawer describes checks and tasks with the Checks and Tasks screens\' own helpers', async () => {
  const rmm = await read('src/screens-rmm.jsx');
  assert.match(rmm, /window\.checkType = checkType;/);
  assert.match(rmm, /window\.taskSchedule = taskSchedule;/);
  assert.match(rmm, /\{taskSchedule\(task\)\}/, 'the Tasks screen uses the same schedule wording');
  const devices = await read('src/screens-devices.jsx');
  assert.match(devices, /window\.checkType\(c\.type\)/);
  assert.match(devices, /window\.taskSchedule\(task\)/);
});
