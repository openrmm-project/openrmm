import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// Category was a plain text field with no list behind it, so nothing offered
// what already existed and "Networking" and "Netwoking" both came to be.
test('a script is filed by picking a category, not by retyping one', async () => {
  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const editor = source.slice(source.indexOf('function ScriptEditor('), source.indexOf('/* ================================ POLICIES'));

  assert.doesNotMatch(editor, /label="Category"><Input/, 'the category field must not be free text');
  assert.match(editor, /<Combobox value=\{category\}[\s\S]{0,200}creatable/,
    'the category field must be a creatable picker');
  // The decision to offer "Create X" compares what was typed against the option
  // labels, so a label carrying anything but the name (a count, say) would offer
  // to create a category that already exists — the very duplicate being avoided.
  assert.match(editor, /options=\{categories\.map\(\(c\) => \(\{ value: c\.name, label: c\.name \}\)\)\}/,
    'option labels must be the bare category name');
});

test('the picker only offers to create a name that is not already taken', async () => {
  const ui = await readFile(path.join(root, 'src/ui.jsx'), 'utf8');
  const combobox = ui.slice(ui.indexOf('function Combobox('), ui.indexOf('function Toggle('));

  assert.match(combobox, /creatable/, 'Combobox must support creation');
  assert.match(combobox, /!options\.some\(\(o\) => o\.label\.toLowerCase\(\) === typed\.toLowerCase\(\)\)/,
    'an exact match must suppress the create row, case-insensitively');
  // Off unless asked for: every other call site is a picker over a fixed list.
  assert.match(combobox, /const canCreate = creatable &&/);
  // A value that is not in the list has to render as itself, or a newly created
  // category would show as the placeholder.
  assert.match(combobox, /const shown = selected \? selected\.label : \(creatable && value \? value : placeholder\)/);
});

// Duplicates that already exist can only be repaired by rewriting every script
// filed under the old name — which is a merge when the target name is in use.
test('categories can be renamed, merged and emptied', async () => {
  const api = await readFile(path.join(root, 'src/mockapi.jsx'), 'utf8');
  assert.match(api, /listScriptCategories\(\) \{ return GET\('\/api\/script-categories'\); \}/);
  // Names contain spaces and punctuation; they have to survive the URL.
  assert.match(api, /renameScriptCategory\(name, newName\) \{ return PUT\('\/api\/script-categories\/' \+ encodeURIComponent\(name\), \{ name: newName \}\); \}/);

  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const manager = source.slice(source.indexOf('function CategoryManager('), source.indexOf('function ScriptEditor('));
  assert.ok(manager.length > 0, 'CategoryManager must exist');
  assert.match(manager, /api\.renameScriptCategory\(confirm\.from, confirm\.to\)/);
  // Renaming onto an existing name is a merge and has to say so — afterwards the
  // two are not separable again.
  assert.match(manager, /categories\.some\(\(x\) => x\.name !== c\.name && x\.name\.toLowerCase\(\) === to\.toLowerCase\(\)\)/);
  assert.match(manager, /kind: merging \? 'merge' : 'rename'/);
  assert.match(manager, /Merge the two categories\?/);
  // Removing keeps the scripts; only the filing goes.
  assert.match(manager, /kind: 'clear', from: c\.name, to: ''/);
  assert.match(manager, /api\.deleteScriptCategory\(confirm\.from\)/);
  assert.match(manager, /The scripts themselves are kept/);

  // A category can be named before anything is filed under it — the whole point
  // of it being registered rather than inferred from the scripts.
  assert.match(api, /createScriptCategory\(name\) \{ return POST\('\/api\/script-categories', \{ name \}\); \}/);
  assert.match(api, /deleteScriptCategory\(name\) \{ return DELETE\('\/api\/script-categories\/' \+ encodeURIComponent\(name\)\); \}/);
  assert.match(manager, /api\.createScriptCategory\(name\)/);
  assert.match(manager, />Add category</, 'the manager needs an Add affordance');

  // And the library filters by category rather than only matching text.
  const screen = source.slice(source.indexOf('function ScriptsScreen('), source.indexOf('function CategoryManager('));
  assert.match(screen, /\.filter\(\(s\) => !cat \|\| s\.category === cat\)/);
});
