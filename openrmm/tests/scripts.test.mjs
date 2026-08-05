import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// Python is offered wherever a shell is chosen. A picker that omits it makes the
// feature unreachable from the console; one that offers a value the backend
// rejects produces a form that 400s on submit. Counting rather than matching one
// occurrence, so a picker added later is caught too.
test('every shell picker offers python alongside the others', async () => {
  for (const file of ['screens-rmm2.jsx', 'screens-devices.jsx']) {
    const source = await readFile(path.join(root, 'src', file), 'utf8');
    const pickers = source.match(/<option value="bash">/g) || [];
    const python = source.match(/<option value="python">/g) || [];
    assert.ok(pickers.length > 0, `${file}: no shell picker found — has it moved?`);
    assert.equal(python.length, pickers.length,
      `${file}: ${pickers.length} shell pickers but ${python.length} offer python`);
  }
});

// Python is indentation-significant and its keywords are not bash's, so the
// editor highlights it as its own language rather than falling through to the
// bash rules.
test('the code viewer highlights python as its own language', async () => {
  const ui = await readFile(path.join(root, 'src', 'ui.jsx'), 'utf8');
  assert.match(ui, /shell === 'python'/, 'the tokenizer should recognise python');
  assert.match(ui, /PY_KW/, 'python keywords should have their own set');
  for (const kw of ['def', 'elif', 'lambda', 'yield']) {
    assert.ok(ui.includes(`'${kw}'`), `PY_KW should include ${kw}`);
  }
});
