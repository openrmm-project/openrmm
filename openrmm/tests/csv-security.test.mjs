import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('../src/ui.jsx', import.meta.url), 'utf8');
const code = source.slice(source.indexOf('const CSV_FORMULA_PREFIX'), source.indexOf('// downloadText saves'));
const sandbox = vm.createContext({});
vm.runInContext(code, sandbox);
// Decode one quoted field; quoting alone must not count as neutralization.
const fieldValue = (field) => field.startsWith('"') ? field.slice(1, -1).replace(/""/g, '"') : field;

test('CSV text cells neutralize formula prefixes including whitespace and Unicode variants', () => {
  for (const value of ['=1+1', '+1+1', '-1+1', '@SUM(A1)', '  =1', '\uFEFF=1', '\u200B+1', '\u0000@SUM(A1)', '\u0085-1', '＝1+1', '＋1', '－1', '＠SUM(A1)', '\tordinary']) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), "'" + value, JSON.stringify(value));
  }
  // A leading tab or line break is guarded as a whole, and so is the formula after it.
  for (const [value, want] of [['\t=1', "'\t'=1"], ['\r=1', "'\r'=1"], ['\n=1', "'\n'=1"]]) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), want, JSON.stringify(value));
  }
});

// Excel opens a .csv with the regional list separator (';' in pt-BR, de-DE,
// es-ES) and honours quotes only at the start of its own cells: "x;=1+1"
// becomes a second cell that starts with the formula. Every cell start is guarded.
test('CSV guards a formula after any separator or line break, not only at the start', () => {
  for (const [value, want] of [
    ['x;=1+1', "x;'=1+1"], ['x;=1+1;y', "x;'=1+1;y"], ['x; =1+1', "x;' =1+1"], ['x;\u200B+1', "x;'\u200B+1"],
    ['x,-2+3', "x,'-2+3"], ['x\t@SUM(A1)', "x\t'@SUM(A1)"], ['x\n=1+1', "x\n'=1+1"], ['x\r\n=1+1', "x\r\n'=1+1"],
    ['x;=1;=2', "x;'=1;'=2"], [';=1', ";'=1"], ['=1;=2', "'=1;'=2"], ['x;＝1', "x;'＝1"],
  ]) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), want, JSON.stringify(value));
  }
  assert.equal(sandbox.toCSV(['Name'], [['7-Zip'], ['x;=1+1;y']]), 'Name\r\n7-Zip\r\n"x;\'=1+1;y"');
});

test('CSV controls preserve normal labels, numbers, delimiters, quotes and line breaks', () => {
  for (const value of ['WINDOWS-TEST', 'São Paulo', 'São Paulo, Ltd', 'a=b', "'=1", 'a,b', 'a;b', 'a"b', 'a\nb', 'a\rb', ' plain text', '1.0-beta', 'C:\\x; D:\\y']) {
    assert.equal(fieldValue(sandbox.csvEscape(value)), value);
  }
  assert.equal(fieldValue(sandbox.csvEscape(-12.5)), '-12.5', 'typed numbers stay numeric');
  assert.equal(sandbox.csvEscape(null), '');
  assert.equal(sandbox.csvEscape(undefined), '');
  assert.equal(sandbox.csvEscape('a\rb'), '"a\rb"');
  assert.equal(sandbox.toCSV(['Target', 'Target ID'], [['=1+1', 'original-id']], true), '"Target","Target ID"\r\n"\'=1+1","original-id"');
});
