import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('../src/ui.jsx', import.meta.url), 'utf8');
const code = source.slice(source.indexOf('function csvEscape('), source.indexOf('// downloadText saves'));
const sandbox = vm.createContext({});
vm.runInContext(code, sandbox);
// Decode one quoted field; quoting alone must not count as neutralization.
const fieldValue = (field) => field.startsWith('"') ? field.slice(1, -1).replace(/""/g, '"') : field;

test('CSV text cells neutralize formula prefixes including whitespace and Unicode variants', () => {
  for (const value of ['=1+1', '+1+1', '-1+1', '@SUM(A1)', '\t=1', '\r=1', '\n=1', '  =1', '\uFEFF=1', '\u200B+1', '\u0000@SUM(A1)', '\u0085-1', '＝1+1', '＋1', '－1', '＠SUM(A1)', '\tordinary']) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), "'" + value, JSON.stringify(value));
  }
});

test('CSV controls preserve normal labels, numbers, delimiters, quotes and line breaks', () => {
  for (const value of ['WINDOWS-TEST', 'São Paulo', 'a=b', "'=1", 'a,b', 'a;b', 'a"b', 'a\nb', 'a\rb', ' plain text']) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), value);
  }
  assert.equal(fieldValue(sandbox.csvEscape(-12.5)), '-12.5', 'typed numbers stay numeric');
  assert.equal(sandbox.csvEscape(null), '');
  assert.equal(sandbox.csvEscape(undefined), '');
  assert.equal(sandbox.csvEscape('a\rb'), '"a\rb"');
  assert.equal(sandbox.toCSV(['Target', 'Target ID'], [['=1+1', 'original-id']], true), '"Target","Target ID"\r\n"\'=1+1","original-id"');
});
