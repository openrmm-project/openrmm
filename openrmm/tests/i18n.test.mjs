import assert from 'node:assert/strict';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// Two things go wrong in a translation table and neither shows up in the
// browser you happen to have open.
//
// A key present in English and missing elsewhere renders as the key itself —
// `auth.title` on the sign-in card — which is deliberate in i18n.jsx (a blank
// label ships unnoticed; a visible key gets fixed) but only works if somebody
// opens that screen in that language. And a placeholder dropped in translation
// turns a sentence into a lie: `{email}` missing from the reset confirmation
// means the address the link went to is simply not shown.
//
// src/locales/*.jsx are plain JS that assign to window, so they run in a vm
// with a stubbed window rather than being parsed.
async function loadStrings() {
  const dir = path.join(root, 'src', 'locales');
  const sandbox = { window: {} };
  vm.createContext(sandbox);
  for (const name of (await readdir(dir)).filter((f) => f.endsWith('.jsx')).sort()) {
    vm.runInContext(await readFile(path.join(dir, name), 'utf8'), sandbox, { filename: name });
  }
  return sandbox.window.LOCALE_STRINGS;
}

const strings = await loadStrings();
const SOURCE = 'en';
const locales = Object.keys(strings);
const englishKeys = Object.keys(strings[SOURCE]).sort();
const placeholders = (value) => (String(value).match(/\{(\w+)\}/g) || []).sort().join(',');
const pluralSource = (key) => {
  const base = key.replace(/\.(zero|two|few|many)$/, '');
  return base !== key && `${base}.one` in strings[SOURCE] && `${base}.other` in strings[SOURCE]
    ? `${base}.other` : null;
};

test('the locales declared in i18n.jsx all have a table', async () => {
  const i18n = await readFile(path.join(root, 'src', 'i18n.jsx'), 'utf8');
  const declared = Array.from(i18n.matchAll(/code:\s*'([^']+)'/g), (m) => m[1]);
  assert.deepEqual(declared.sort(), locales.slice().sort());
});

for (const locale of locales) {
  test(`${locale} carries the English key set and only valid extra plural forms`, () => {
    const keys = Object.keys(strings[locale]);
    const missing = englishKeys.filter((k) => !(k in strings[locale]));
    const extra = keys.filter((k) => !englishKeys.includes(k) && !pluralSource(k));
    assert.deepEqual(missing, [], `missing: ${missing.slice(0, 5).join(', ')}`);
    assert.deepEqual(extra, [], `not in English: ${extra.slice(0, 5).join(', ')}`);
  });

  test(`${locale} keeps every placeholder`, () => {
    for (const key of englishKeys) {
      assert.equal(typeof strings[locale][key], 'string', `${key}: not a string`);
      assert.ok(strings[locale][key].trim(), `${key}: empty translation`);
      assert.equal(placeholders(strings[locale][key]), placeholders(strings[SOURCE][key]),
        `${key}: placeholders differ from English`);
    }
    for (const key of Object.keys(strings[locale]).filter((k) => !englishKeys.includes(k))) {
      assert.equal(placeholders(strings[locale][key]), placeholders(strings[SOURCE][pluralSource(key)]),
        `${key}: plural placeholders differ from English`);
    }
  });
}

// Translation coverage does not imply review by a native speaker. A handful of
// values are identical in every language on purpose — 'Patches', 'tok_…',
// 'OpenRMM' — so this asserts the
// bulk was translated rather than every line.
for (const locale of ['pt-BR', 'es', 'ru', 'ko', 'ms']) {
  test(`${locale} is translated, not copied from English`, () => {
    const identical = englishKeys.filter((k) => strings[locale][k] === strings[SOURCE][k]);
    assert.ok(identical.length < englishKeys.length * 0.15,
      `${identical.length} of ${englishKeys.length} strings are still English: ${identical.slice(0, 8).join(', ')}`);
  });
}

// The screens that greet someone who is not signed in yet. These were literal
// English in app.jsx, which meant a console opened in Portuguese asked for a
// "Username" and a "Password" — the one screen every user sees, in the one
// language they did not choose.
test('the signed-out screens are translated in every locale', () => {
  const authKeys = englishKeys.filter((k) => k.startsWith('auth.'));
  assert.ok(authKeys.length > 20, 'the auth.* keys went missing from English');
  for (const locale of locales) {
    for (const key of authKeys) {
      assert.equal(typeof strings[locale][key], 'string', `${locale} is missing ${key}`);
    }
  }
});

test('Russian plurals handle zero, teens, few, many and fractions', async () => {
  const { i18nSandbox } = await import('./i18n-sandbox.mjs');
  const sb = await i18nSandbox();
  sb.setLocale('ru');
  for (const [n, noun] of [[0, 'устройств'], [1, 'устройство'], [2, 'устройства'],
    [5, 'устройств'], [11, 'устройств'], [21, 'устройство'], [22, 'устройства'],
    [25, 'устройств'], [1.5, 'устройства']]) {
    assert.equal(sb.tn('bulk.devices', n), `${n} ${noun}`);
  }
  for (const key of englishKeys.filter((k) => k.endsWith('.one') && `${k.slice(0, -4)}.other` in strings.en)) {
    const base = key.slice(0, -4);
    for (const form of ['few', 'many']) assert.ok(strings.ru[`${base}.${form}`], `${base}.${form}`);
  }
  delete sb.window.LOCALE_STRINGS.ru['bulk.devices.few'];
  assert.equal(sb.tn('bulk.devices', 2), '2 устройства', 'a missing category uses the local other form');
  sb.setLocale('en');
  assert.equal(sb.tn('missing.count', 2), 'missing.count.other');
  sb.setLocale('pt-BR');
  assert.equal(sb.tn('bulk.devices', 0), '0 dispositivos');
  sb.setLocale('ko');
  assert.equal(sb.tn('bulk.devices', 2), '기기 2대');
  sb.setLocale('ms');
  assert.equal(sb.tn('bulk.devices', 2), '2 peranti');
});

test('new languages support regional browser tags and remember explicit choices', async () => {
  const source = await readFile(path.join(root, 'src', 'i18n.jsx'), 'utf8');
  for (const [candidate, expected] of [['ru-RU', 'ru'], ['ko-KR', 'ko'], ['ms-MY', 'ms'], ['MS-bn', 'ms']]) {
    const store = {};
    const sandbox = { window: {}, navigator: { languages: [candidate, 'en'] },
      localStorage: { getItem: (k) => store[k], setItem: (k, v) => { store[k] = v; } },
      document: { documentElement: {} }, Intl };
    vm.createContext(sandbox);
    vm.runInContext(source, sandbox);
    assert.equal(sandbox.window.getLocale(), expected);
    assert.equal(sandbox.document.documentElement.lang, expected);
    sandbox.window.setLocale('en');
    sandbox.window.setLocale(expected);
    sandbox.navigator.languages = ['en'];
    vm.runInContext(`{${source}}`, sandbox);
    assert.equal(sandbox.window.getLocale(), expected, 'saved choice wins over browser');
  }
});

// Not i18n, but the same shape of failure and the same place to catch it: a
// table lookup that assumes the server only ever sends values this build knows.
//
// CHECK_TYPES[c.type].icon threw during render for any unrecognised type, and a
// throw during render unmounts the console — the whole screen goes white. The
// fix is checkType(), which falls back; the rule is that nothing reads the table
// directly, because the next direct read brings the crash back.
test('no screen reads CHECK_TYPES without the fallback', async () => {
  const files = ['screens-rmm.jsx', 'screens-rmm2.jsx', 'screens-core.jsx', 'app.jsx'];
  for (const name of files) {
    const source = await readFile(path.join(root, 'src', name), 'utf8');
    const direct = source
      .split('\n')
      .map((line, i) => [i + 1, line])
      .filter(([, line]) => /CHECK_TYPES\s*\[/.test(line))
      // The helper itself, and the comment explaining why it exists.
      .filter(([, line]) => !/const known = CHECK_TYPES\[type\];/.test(line) && !/^\s*\/\//.test(line));
    assert.deepEqual(direct.map(([n]) => n), [],
      `${name}: use checkType() instead of indexing CHECK_TYPES at line(s) ${direct.map(([n]) => n).join(', ')}`);
  }
});

// Audit actions read in the console's language. Composed from resource and
// verb, with each locale's own format so gendered languages never have to
// agree an adjective with a noun they cannot see.
test('audit actions are labelled in the console language', async () => {
  const { i18nSandbox } = await import('./i18n-sandbox.mjs');
  const sb = await i18nSandbox('src/labels.jsx');
  const label = sb.window.auditActionLabel;
  assert.equal(label('job.approve'), 'Job approved');
  assert.equal(label('patch.policy.update'), 'Patch policy updated');
  assert.equal(label('auth.login'), 'Signed in', 'a whole-action entry wins over composition');
  assert.equal(label('brand.new_thing'), 'brand new_thing', 'an unknown action still reads');
  sb.setLocale('pt-BR');
  assert.equal(label('policy.update'), 'Política · alteração');
  assert.equal(label('job.approve'), 'Job · aprovação');
  sb.setLocale('ja');
  assert.equal(label('device.delete'), 'デバイス · 削除');
});
