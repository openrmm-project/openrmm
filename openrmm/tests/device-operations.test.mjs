import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const read = (name) => readFile(new URL('../' + name, import.meta.url), 'utf8');
test('operations transport uses the scoped profile resource and preserves existing inventory', async () => {
  const source = await read('src/mockapi.jsx');
  const get = source.match(/getDeviceOperationsProfile\(id\) \{([^}]+)\}/)[1];
  const put = source.match(/putDeviceOperationsProfile\(id, fields\) \{([^}]+)\}/)[1];
  const calls = [];
  const context = vm.createContext({ GET: (...args) => calls.push(['GET', ...args]), PUT: (...args) => calls.push(['PUT', ...args]) });
  vm.runInContext(`function get(id) { ${get} }; function put(id, fields) { ${put} }; get('device-a'); put('device-b', {expected_version: 3});`, context);
  assert.equal(calls[0][1], '/api/devices/device-a/operations-profile');
  assert.equal(calls[1][1], '/api/devices/device-b/operations-profile');
  assert.equal(calls[1][2].expected_version, 3);
  assert.match(source, /async getInventory\(id\) \{ return adaptInventory/);
});

test('operations translations contain every field and status in all supported languages', async () => {
  const locales = ['en', 'de', 'es', 'pt-BR', 'zh-CN', 'ja', 'ko', 'ru', 'ms'];
  let keys;
  for (const locale of locales) {
    const context = vm.createContext({ window: {} });
    vm.runInContext(await read('src/locales/' + locale + '.jsx'), context);
    const strings = context.window.LOCALE_STRINGS[locale];
    const found = Object.keys(strings).filter((k) => k.startsWith('operations.')).sort();
    keys ||= found;
    assert.deepEqual(found, keys, locale);
    assert.ok(strings['audit.action.device.operational_profile.update']);
    assert.ok(strings['operations.updated'].includes('{actor}') && strings['operations.updated'].includes('{when}'));
    assert.ok(strings['operations.limit'].includes('{n}'));
  }
});
