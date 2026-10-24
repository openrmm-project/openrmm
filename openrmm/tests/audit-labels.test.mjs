import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const LOCALES = ['en', 'pt-BR', 'es', 'de', 'zh-CN', 'ja', 'ru', 'ko', 'ms'];

async function goFiles(dir, out = []) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) await goFiles(p, out);
    else if (p.endsWith('.go') && !p.endsWith('_test.go')) out.push(p);
  }
  return out;
}

// Every action the server writes to the audit trail must read as words in
// every console language. The label is either a whole-action entry or a
// resource plus a verb (see src/labels.jsx); without one the audit screen shows
// the raw identifier — "oauth app revoke" in the middle of a Portuguese page —
// which is how ten actions went unnoticed until this test existed.
//
// Only literal actions are checked. Ones assembled at run time ("alert." +
// verb) have no single name to look up.
test('every audit action the server records has a label in every language', async () => {
  const actions = new Set();
  for (const file of await goFiles(path.join(root, '..', 'internal'))) {
    const src = await readFile(file, 'utf8');
    for (const m of src.matchAll(/AddAudit\([^,]+,\s*[^,]+,\s*"([a-z0-9_.]+)"/g)) {
      if (!m[1].endsWith('.')) actions.add(m[1]);
    }
  }
  assert.ok(actions.size > 40, `found only ${actions.size} audit actions; the scan is broken`);

  for (const locale of LOCALES) {
    const table = await readFile(path.join(root, 'src', 'locales', `${locale}.jsx`), 'utf8');
    const has = (key) => table.includes(`'${key}':`);
    const missing = [...actions].filter((a) => {
      if (has(`audit.action.${a}`)) return false;
      const i = a.lastIndexOf('.');
      return i < 0 || !(has(`audit.res.${a.slice(0, i)}`) && has(`audit.verb.${a.slice(i + 1)}`));
    });
    assert.deepEqual(missing, [], `${locale}: audit actions without a label`);
  }
});
