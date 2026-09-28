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

// install.sh and install.ps1 refuse an HTTPS install without an update key to
// pin. The command used to omit it, so every production enrollment copied from
// here failed with "production install requires --update-public-key".
test('the install commands carry the server\'s update keys, and say so when there are none', async () => {
  const screens = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const enroll = screens.slice(screens.indexOf('function EnrollCommands('), screens.indexOf('function SiteDetail('));
  assert.match(enroll, /server\?\.update_public_keys/, 'the keys come from the server config');
  assert.match(enroll, /--update-public-key \$\{shellQuote\(keyList\)\}/);
  assert.match(enroll, /-UpdatePublicKey '\$\{keyList\}'/);
  assert.match(enroll, /--token \$\{token\}\$\{keyArg\}/, 'the Linux/macOS command includes the key');
  assert.match(enroll, /-Token \$\{token\}\$\{winKeyArg\}/, 'the Windows command includes the key');
  assert.match(enroll, /missingKey && \(/, 'an HTTPS server without keys is called out, not left to fail');
});
