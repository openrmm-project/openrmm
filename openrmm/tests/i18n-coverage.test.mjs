import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// Every word the console shows goes through t(). A literal slipped into a
// screen is English in every language, and nothing on screen says so: the
// Portuguese console opened a device and showed "Inventory", "Last seen" and
// "Run script" for months. This reads the screens the way a reviewer would and
// fails on text a person would see that is not a translation lookup.
//
// It is a scanner, not a parser, so it looks where visible text lives:
//   - text between JSX tags            <span>Last seen</span>
//   - text-bearing attributes          title="Delete" placeholder="Search…"
//   - toast / prompt / confirm literals toast.error('Failed', …)
// Names that are the same in every language (product names, protocols, units)
// are allowed below; anything else belongs in src/locales.
import { SCREENS, findLiterals, stripComments, lineOf } from './i18n-scan.mjs';

for (const file of SCREENS) {
  test(`${file} shows no untranslated text`, async () => {
    const hits = findLiterals(await readFile(path.join(root, file), 'utf8'));
    assert.deepEqual(hits, [], `${hits.length} literal(s) outside t():\n  ${hits.slice(0, 25).join('\n  ')}`);
  });
}

// A local variable named t shadows the translation function for its whole
// scope, and every t('…') inside then calls the wrong thing — a crash, or worse,
// a silently wrong value. The screens name their locals anything else.
for (const file of SCREENS) {
  test(`${file} never shadows t()`, async () => {
    const src = stripComments(await readFile(path.join(root, file), 'utf8'));
    const shadows = [...src.matchAll(/\b(?:const|let|var)\s+t\b\s*[=;,]|\(\s*t\s*[,)]\s*=>|\bfor\s*\(\s*(?:const|let)\s+t\s+of\b|\bfunction\s*\w*\s*\(\s*t\s*[,)]/g)]
      .map((m) => lineOf(src, m.index));
    assert.deepEqual(shadows, [], `t is shadowed at line(s) ${shadows.join(', ')}`);
  });
}

// The scanner itself: if it stopped finding literals, every test above would
// pass for the wrong reason.
test('the scanner finds the literals it is meant to find', () => {
  const sample = [
    '<span>Last seen</span>',
    '<Button title="Delete">x</Button>',
    "toast.error('Failed', e.message);",
    "window.prompt('New folder name:');",
    "<span>{t('ok')}</span>",
    '<span>OpenRMM</span>',
    '<option>50%</option>',
    "<Tip label={dark ? 'Light mode' : 'Dark mode'} />",
    "<div className={cx(ok ? 'text-ok bg-oksoft' : 'text-bad')} />",
    "{name || 'Unassigned'}",
    "{kind === 'x' ? 'bash' : 'powershell'}",
  ].join('\n');
  const hits = findLiterals(sample);
  assert.equal(hits.length, 7, hits.join(' | '));
});
