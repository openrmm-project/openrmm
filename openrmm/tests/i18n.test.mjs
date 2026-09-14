import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
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
// src/i18n-strings.jsx is plain JS that assigns to window, so it runs in a vm
// with a stubbed window rather than being parsed.
async function loadStrings() {
  const source = await readFile(path.join(root, 'src', 'i18n-strings.jsx'), 'utf8');
  const sandbox = { window: {} };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox);
  return sandbox.window.LOCALE_STRINGS;
}

const strings = await loadStrings();
const SOURCE = 'en';
const locales = Object.keys(strings);
const englishKeys = Object.keys(strings[SOURCE]).sort();
const placeholders = (value) => (String(value).match(/\{(\w+)\}/g) || []).sort().join(',');

test('the locales declared in i18n.jsx all have a table', async () => {
  const i18n = await readFile(path.join(root, 'src', 'i18n.jsx'), 'utf8');
  const declared = Array.from(i18n.matchAll(/code:\s*'([^']+)'/g), (m) => m[1]);
  assert.deepEqual(declared.sort(), locales.slice().sort());
});

for (const locale of locales) {
  test(`${locale} carries exactly the English key set`, () => {
    const keys = Object.keys(strings[locale]);
    const missing = englishKeys.filter((k) => !(k in strings[locale]));
    const extra = keys.filter((k) => !englishKeys.includes(k));
    assert.deepEqual(missing, [], `missing: ${missing.slice(0, 5).join(', ')}`);
    assert.deepEqual(extra, [], `not in English: ${extra.slice(0, 5).join(', ')}`);
  });

  test(`${locale} keeps every placeholder`, () => {
    for (const key of englishKeys) {
      assert.equal(placeholders(strings[locale][key]), placeholders(strings[SOURCE][key]),
        `${key}: placeholders differ from English`);
    }
  });
}

// The reviewed locales, only. de, zh-CN and ja are machine-drafted and marked
// incomplete in i18n.jsx; holding them to this would be asserting a claim the
// file itself refuses to make. A handful of values are identical in every
// language on purpose — 'Patches', 'tok_…', 'OpenRMM' — so this asserts the
// bulk was translated rather than every line.
for (const locale of ['pt-BR', 'es']) {
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
      .filter(([, line]) => !/return CHECK_TYPES\[type\] \|\|/.test(line) && !/^\s*\/\//.test(line));
    assert.deepEqual(direct.map(([n]) => n), [],
      `${name}: use checkType() instead of indexing CHECK_TYPES at line(s) ${direct.map(([n]) => n).join(', ')}`);
  }
});
