import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// src/remote-input.jsx is plain JS that assigns to window, so it runs in a
// sandbox. These are the decisions that made pasting fail in a real session:
// the viewer cancelled the browser's default action on every keystroke, which
// cancelled the paste event the clipboard text rides on.
async function loadPolicy() {
  const source = await readFile(path.join(root, 'src', 'remote-input.jsx'), 'utf8');
  const sandbox = { window: {} };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox);
  return sandbox.window.RemoteInput;
}

const key = (over) => ({ ctrlKey: false, metaKey: false, altKey: false, key: '', keyCode: 0, ...over });

test('the paste shortcut is recognised on both platforms', async () => {
  const policy = await loadPolicy();
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, key: 'v' })), true);
  assert.equal(policy.isPasteShortcut(key({ metaKey: true, key: 'v' })), true, 'Command+V on a Mac browser');
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, key: 'V' })), true, 'with Shift held the key arrives upper case');
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, key: '', keyCode: 86 })), true, 'keyup without a key name');
});

test('other keystrokes keep being forwarded', async () => {
  const policy = await loadPolicy();
  // Each of these must stay false: returning true would swallow the keystroke
  // and the endpoint would never see it.
  assert.equal(policy.isPasteShortcut(key({ key: 'v' })), false, 'plain v is typing');
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, key: 'c' })), false);
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, altKey: true, key: 'v' })), false, 'Ctrl+Alt+V is a different chord');
  assert.equal(policy.isPasteShortcut(key({ ctrlKey: true, key: 'Escape' })), false);
  assert.equal(policy.isPasteShortcut(null), false);
});

test('Control shortcuts become Command shortcuts for a Mac endpoint', async () => {
  const policy = await loadPolicy();
  assert.equal(policy.endpointKeyCode(17, true), 91, 'Control becomes Command');
  assert.equal(policy.endpointKeyCode(93, true), 91, 'right Command is normalized');
  assert.equal(policy.endpointKeyCode(17, false), 17, 'Windows and Linux keep Control');
  assert.equal(policy.endpointKeyCode(67, true), 67, 'the shortcut key itself is unchanged');
});

test('the Mac Force Quit button can retry after Accessibility is granted', async () => {
  const policy = await loadPolicy();
  assert.equal(policy.shortcutAvailable(true, false), true, 'macOS rechecks the permission when clicked');
  assert.equal(policy.shortcutAvailable(false, false), false, 'other endpoints keep their advertised capability gate');
  assert.equal(policy.shortcutAvailable(false, true), true);
});

test('the clipboard message carries the text and asks the endpoint to paste', async () => {
  const policy = await loadPolicy();
  const message = policy.clipMessage('hello');
  // Field by field: the object is built inside the vm realm, so a deep-strict
  // comparison against a literal from this one fails on the prototype alone.
  assert.equal(message.t, 'clip');
  assert.equal(message.text, 'hello');
  assert.equal(message.paste, true);
});

test('clipboard text is capped, and nothing is sent when there is none', async () => {
  const policy = await loadPolicy();
  const big = 'x'.repeat(policy.CLIP_LIMIT + 5000);
  assert.equal(policy.clipMessage(big).text.length, policy.CLIP_LIMIT);
  assert.equal(policy.clipMessage(''), null);
  assert.equal(policy.clipMessage(undefined), null, 'an image-only paste has no text');
});

test('keystrokes belong to the focused field while the technician types', async () => {
  const policy = await loadPolicy();
  assert.equal(policy.isTypingTarget({ tagName: 'INPUT' }), true);
  assert.equal(policy.isTypingTarget({ tagName: 'TEXTAREA' }), true);
  assert.equal(policy.isTypingTarget({ tagName: 'CANVAS' }), false, 'the remote screen itself');
  assert.equal(policy.isTypingTarget(null), false);
});
