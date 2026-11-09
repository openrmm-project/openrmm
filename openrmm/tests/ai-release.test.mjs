import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// Exercise the real component without a browser or listener. In particular,
// a withdrawal response must not silently change what the editor reviewed.
async function panel(script, response) {
  const source = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
  const start = source.indexOf('function AIReleasePanel(');
  const end = source.indexOf('/* ================================ POLICIES', start);
  const { code } = transformSync(source.slice(start, end), { loader: 'jsx' });
  const states = [];
  let cursor = 0;
  const calls = [];
  const sandbox = {
    React: { createElement: (type, props, ...children) => ({ type, props, children }) },
    useState(initial) {
      const i = cursor++;
      if (!(i in states)) states[i] = initial;
      return [states[i], (v) => { states[i] = v; }];
    },
    useAuth: () => ({ can: () => true }),
    useToast: () => ({ success() {}, error() {} }),
    api: { async setScriptAIRelease(...args) { calls.push(args); return response; } },
    t: (key) => key,
    cx: (...args) => args.join(' '),
    timeAgo: () => '',
    Icon: 'Icon', Button: 'Button',
  };
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox);
  const render = () => {
    cursor = 0;
    return sandbox.AIReleasePanel({ script, contentChanged: false });
  };
  function button(node) {
    if (!node || typeof node !== 'object') return;
    if (node.type === 'Button') return node;
    for (const child of node.children || []) {
      const found = button(child);
      if (found) return found;
    }
  }
  return { calls, click: async () => button(render()).props.onClick() };
}

test('a withdrawal response cannot replace the hash of the visibly reviewed content', async () => {
  const reviewed = { id: 'script-1', ai_released: true, content_hash: 'hash-A' };
  const changed = { id: 'script-1', ai_released: false, content_hash: 'hash-B' };
  const p = await panel(reviewed, changed);
  await p.click(); // withdraw; server returns content changed elsewhere
  await p.click(); // release while the editor still displays A
  assert.equal(p.calls[0][1], false);
  assert.equal(p.calls[1][1], true);
  assert.equal(p.calls[1][2], 'hash-A');
});

test('an unchanged reviewed script can still be released', async () => {
  const reviewed = { id: 'script-1', ai_released: false, content_hash: 'hash-A' };
  const p = await panel(reviewed, { ...reviewed, ai_released: true });
  await p.click();
  assert.equal(p.calls[0][1], true);
  assert.equal(p.calls[0][2], 'hash-A');
});
