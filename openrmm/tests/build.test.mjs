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

// The terminal renders under the strict CSP only because xterm's runtime <style>
// elements are mirrored into an adopted CSSStyleSheet, which style-src-elem does
// not govern. Deleting the shim brings back a blank black terminal, and the
// tempting "fix" is to add 'unsafe-inline' to style-src-elem — which the CSP test
// above would then reject. This pins the shim so that loop is never entered.
test('xterm runtime styles are adopted rather than injected as style elements', async () => {
  const devices = await readFile(path.join(root, 'src/screens-devices.jsx'), 'utf8');
  assert.match(devices, /adoptedStyleSheets/);
  assert.match(devices, /new CSSStyleSheet\(\)/);
  // The shim has to be installed before the Terminal is constructed; xterm styles
  // during open(), so installing afterwards would be too late.
  const shimAt = devices.indexOf('adoptDynamicStyles();');
  const termAt = devices.indexOf('new window.Terminal(');
  assert.ok(shimAt > 0 && termAt > 0, 'expected the shim call and the Terminal construction');
  assert.ok(shimAt < termAt, 'adoptDynamicStyles() must run before new Terminal()');
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

test('native remote viewer exposes operator display controls and distinct chat roles', async () => {
  const devices = await readFile(path.join(root, 'src/screens-devices.jsx'), 'utf8');
  assert.match(devices, /Ctrl\+Alt\+Delete/);
  assert.match(devices, /⌘⌥Esc/);
  assert.match(devices, /isWin && <Button[^\n]+>UAC/);
  assert.match(devices, /disabled=\{status !== 'streaming' \|\| !cad.available\}/);
  assert.match(devices, /requestFullscreen/);
  assert.match(devices, /Fit to window/);
  assert.match(devices, /Remote screen resolution/);
  assert.match(devices, /Technician · You/);
  assert.match(devices, /monitors\.list\.length > 1/);
  assert.match(devices, /monitors\.all !== false && <option value="0">All displays/);
  assert.match(devices, /t: 'mon', id/);
  assert.match(devices, /bg-brand text-white/);
  assert.match(devices, /bg-elevated text-ink border border-line2/);
});

test('bearer session is not persisted in localStorage', async () => {
  for (const name of ['src/mockapi.jsx', 'src/app.jsx', 'src/screens-devices.jsx']) {
    const source = await readFile(path.join(root, name), 'utf8');
    assert.doesNotMatch(source, /localStorage\.(?:getItem|setItem|removeItem)\(['"]rmm-ai-auth['"]\)/);
  }
});
