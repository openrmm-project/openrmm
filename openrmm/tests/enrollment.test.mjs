import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// The Windows install command is a single line carrying the server URL and the
// token, so it ran off the right edge of the code block and had to be scrolled
// to be read. It is a command people paste into an elevated PowerShell — being
// unable to see it before running it is the part that matters, not the scrolling.
test('the enrollment command is readable without scrolling sideways', async () => {
  const ui = await readFile(path.join(root, 'src/ui.jsx'), 'utf8');
  const block = ui.slice(ui.indexOf('function CodeBlock('), ui.indexOf('/* editable code editor'));
  assert.match(block, /wrap \? 'whitespace-pre-wrap break-words' : 'whitespace-pre'/,
    'CodeBlock must be able to soft-wrap, and must not do it by default');

  const screens = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const enroll = screens.slice(screens.indexOf('function EnrollCommands('), screens.indexOf('function SiteDetail('));
  assert.match(enroll, /<CodeBlock[^>]*\bwrap\b/, 'the enrollment command must be wrapped');
  // The copied value is the command itself, so wrapping the display cannot
  // corrupt what lands on the clipboard.
  assert.match(enroll, /<CopyButton value=\{cmd\}/);
});
