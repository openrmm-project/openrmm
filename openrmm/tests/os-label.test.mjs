import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// src/icons.jsx is JSX, so the two helpers are read out of it by source rather
// than executed: they are plain functions with no JSX inside, and running them
// is worth more than asserting on the text of the file.
async function loadHelpers() {
  const source = await readFile(path.join(root, 'src', 'icons.jsx'), 'utf8');
  const start = source.indexOf('function osFamily');
  const end = source.indexOf('function OsIcon');
  assert.ok(start > 0 && end > start, 'osFamily and osLabel must stay above OsIcon in icons.jsx');
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(source.slice(start, end) + '\nthis.osFamily = osFamily; this.osLabel = osLabel;', sandbox);
  return sandbox;
}

test('the agent reports GOOS, and darwin is not Windows', async () => {
  const { osFamily } = await loadHelpers();
  // The bug this guards: "darwin" contains "win", so a substring test in the
  // wrong order gave every Mac a Windows icon, a Windows label, and no match
  // under the macOS filter.
  assert.equal(osFamily('darwin'), 'macOS');
  assert.equal(osFamily('windows'), 'Windows');
  assert.equal(osFamily('linux'), 'Linux');
});

test('families are recognised from what endpoints actually report', async () => {
  const { osFamily } = await loadHelpers();
  assert.equal(osFamily('macOS 15.8'), 'macOS');
  assert.equal(osFamily('Mac OS X'), 'macOS');
  assert.equal(osFamily('Microsoft Windows 11 Pro'), 'Windows');
  assert.equal(osFamily('Ubuntu 24.04.1 LTS'), 'Linux');
  assert.equal(osFamily('Debian GNU/Linux 12'), 'Linux');
  assert.equal(osFamily(''), 'Unknown');
  assert.equal(osFamily(null), 'Unknown');
});

test('the label reads as a product, not a kernel', async () => {
  const { osLabel } = await loadHelpers();
  assert.equal(osLabel('darwin'), 'macOS', 'the kernel name means nothing to an operator');
  assert.equal(osLabel('windows'), 'Windows');
  assert.equal(osLabel('linux'), 'Linux');
});

test('a more specific report survives instead of being flattened', async () => {
  const { osLabel } = await loadHelpers();
  assert.equal(osLabel('Ubuntu 24.04.1 LTS'), 'Ubuntu 24.04.1 LTS');
  assert.equal(osLabel('macOS 15.8'), 'macOS 15.8');
  assert.equal(osLabel(''), '—');
});
