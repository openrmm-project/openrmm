import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('index is self-contained and CSP does not allow eval or inline script/style elements', async () => {
  const html = await readFile(path.join(root, 'index.html'), 'utf8');
  assert.doesNotMatch(html, /https?:\/\//);
  assert.doesNotMatch(html, /unsafe-eval/);
  assert.match(html, /connect-src 'self';/);
  assert.doesNotMatch(html, /connect-src[^;]*(?:https?:|wss?:)/);
  assert.match(html, /style-src-elem 'self'; style-src-attr 'unsafe-inline'/);
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>/i);
  assert.doesNotMatch(html, /<style\b|\sstyle=/i);
});

test('build and remote URLs support an explicit base path and API-derived WebSocket origin', async () => {
  const build = await readFile(path.join(root, 'scripts/build.mjs'), 'utf8');
  const api = await readFile(path.join(root, 'src/mockapi.jsx'), 'utf8');
  const devices = await readFile(path.join(root, 'src/screens-devices.jsx'), 'utf8');
  assert.match(build, /OPENRMM_BASE_PATH/);
  assert.match(build, /window\.__BASE_PATH__/);
  assert.match(api, /websocketURL/);
  assert.match(devices, /api\.websocketURL/);
  assert.doesNotMatch(devices, /location\.origin\.replace\(\/\^http\/|location\.origin\s*\+\s*['"]\/ws/);
});

test('production login does not ship demo credentials or claims', async () => {
  const app = await readFile(path.join(root, 'src/app.jsx'), 'utf8');
  assert.doesNotMatch(app, /DEMO_USERS|Password is anything|setPassword\(['"]demo['"]\)/);
  assert.match(app, /\[username, setUsername\]\s*=\s*useState\(['"]['"]\)/);
  assert.match(app, /\[password, setPassword\]\s*=\s*useState\(['"]['"]\)/);
});

test('mobile navigation and pointer input are present', async () => {
  const app = await readFile(path.join(root, 'src/app.jsx'), 'utf8');
  const chat = await readFile(path.join(root, 'src/screens-chat.jsx'), 'utf8');
  const devices = await readFile(path.join(root, 'src/screens-devices.jsx'), 'utf8');
  assert.match(app, /Open navigation/);
  assert.match(app, /md:hidden/);
  assert.match(chat, /sessionsOpen/);
  assert.match(devices, /pointerdown/);
  assert.match(devices, /pointercancel/);
});

test('bearer session is not persisted in localStorage', async () => {
  for (const name of ['src/mockapi.jsx', 'src/app.jsx', 'src/screens-devices.jsx']) {
    const source = await readFile(path.join(root, name), 'utf8');
    assert.doesNotMatch(source, /localStorage\.(?:getItem|setItem|removeItem)\(['"]rmm-ai-auth['"]\)/);
  }
});
