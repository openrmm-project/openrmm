import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { i18nSandbox } from './i18n-sandbox.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// Loaded with the i18n runtime: the wording comes from the locale tables, so
// the same calls can be checked in English and in another language.
let sandbox;
async function load(locale = 'en') {
  sandbox = await i18nSandbox('src/device-bulk.jsx');
  sandbox.setLocale(locale);
  return sandbox.window.DeviceBulk;
}

test('select all acts on the visible devices only', async () => {
  const B = await load();
  // A filter shows b and c; a was selected earlier and is hidden now.
  let sel = B.toggleAllVisible(['a'], ['b', 'c']);
  assert.deepEqual([...sel], ['a', 'b', 'c']);
  assert.equal(B.allVisibleSelected(sel, ['b', 'c']), true);
  // Clearing the visible ones leaves the hidden selection alone.
  sel = B.toggleAllVisible(sel, ['b', 'c']);
  assert.deepEqual([...sel], ['a']);
  assert.equal(B.allVisibleSelected([], []), false, 'an empty list is not "all selected"');
});

test('toggle adds and removes one device', async () => {
  const B = await load();
  assert.deepEqual([...B.toggle(['a'], 'b')], ['a', 'b']);
  assert.deepEqual([...B.toggle(['a', 'b'], 'a')], ['b']);
});

test('a device that disappears leaves the selection', async () => {
  const B = await load();
  assert.deepEqual([...B.prune(['a', 'b', 'c'], ['a', 'c'])], ['a', 'c']);
  const same = ['a'];
  assert.equal(B.prune(same, ['a', 'b']), same, 'unchanged selections keep their identity, so nothing re-renders');
});

test('a deletion is confirmed with the exact count', async () => {
  const B = await load();
  assert.equal(B.confirmMatches('3', 3), true);
  assert.equal(B.confirmMatches(' 3 ', 3), true);
  assert.equal(B.confirmMatches('2', 3), false);
  assert.equal(B.confirmMatches('', 0), false, 'an empty field confirms nothing');
  assert.equal(B.confirmMatches(undefined, 3), false);
  assert.equal(B.confirmMatches('three', 3), false);
});

test('proposals are described in words', async () => {
  const B = await load();
  const names = { site: (id) => ({ s1: 'HQ' })[id], group: (id) => ({ g1: 'Finance' })[id] };
  assert.equal(B.describe({ kind: 'move', agent_ids: ['a', 'b', 'c'], site_id: 's1', group_id: 'g1' }, names), 'Move 3 devices to HQ ▸ Finance');
  assert.equal(B.describe({ kind: 'move', agent_ids: ['a'], site_id: 's1' }, names), 'Move 1 device to HQ');
  assert.equal(B.describe({ kind: 'move', agent_ids: ['a'], site_id: 'gone' }, names), 'Move 1 device to another site');
  assert.equal(B.describe({ kind: 'delete', agent_ids: ['a', 'b'] }, names), 'Delete 2 devices');
  assert.equal(B.describe({ kind: 'move', agent_ids: ['a', 'b'], site_id: 's1' }), 'Move 2 devices', 'no site list: no destination guessed');
});

test('the dashboard says what is waiting', async () => {
  const B = await load();
  assert.equal(B.approvalSummary(0, 0), 'needs a human');
  assert.equal(B.approvalSummary(3, 0), 'needs a human');
  assert.equal(B.approvalSummary(0, 1), '1 device operation');
  assert.equal(B.approvalSummary(0, 2), '2 device operations');
  assert.equal(B.approvalSummary(1, 2), '1 job · 2 device operations');
  assert.equal(B.approvalSummary(2, 1), '2 jobs · 1 device operation');
});

test('the proposer is named for what it is', async () => {
  const B = await load();
  assert.equal(B.proposer('ai:alice'), 'AI assistant (for alice)');
  assert.equal(B.proposer('mcp:api'), 'MCP client (api)');
  assert.equal(B.proposer(''), 'unknown');
});

test('deciding needs the permission the operation would', async () => {
  const B = await load();
  assert.equal(B.decidePermission({ kind: 'delete' }), 'devices.delete');
  assert.equal(B.decidePermission({ kind: 'move' }), 'clients.manage');
});

test('only waiting proposals need a decision', async () => {
  const B = await load();
  const ops = [{ id: 1, status: 'awaiting_approval' }, { id: 2, status: 'completed' }, { id: 3, status: 'rejected' }];
  assert.deepEqual([...B.awaiting(ops).map((o) => o.id)], [1]);
  assert.deepEqual([...B.awaiting(undefined)], []);
});

test('a site with devices is counted before it is offered for deletion', async () => {
  const B = await load();
  const devices = [{ site_id: 's1' }, { site_id: 's1' }, { site_id: 's2' }, { site_id: null }];
  assert.equal(B.siteDeviceCount(devices, 's1'), 2);
  assert.equal(B.siteDeviceCount(devices, 's3'), 0);
});

// The wording is the console's language, plurals included: "1 dispositivo",
// "3 dispositivos", and zero reads as a plural rather than "0 dispositivo".
test('proposals are described in the console language', async () => {
  const B = await load('pt-BR');
  const names = { site: (id) => (id === 's1' ? 'Matriz' : undefined), group: () => 'Financeiro' };
  assert.equal(B.describe({ kind: 'move', agent_ids: ['a', 'b', 'c'], site_id: 's1', group_id: 'g1' }, names), 'Mover 3 dispositivos para Matriz ▸ Financeiro');
  assert.equal(B.describe({ kind: 'delete', agent_ids: ['a'] }, names), 'Excluir 1 dispositivo');
  assert.equal(B.countLabel(0), '0 dispositivos');
  assert.equal(B.approvalSummary(2, 1), '2 jobs · 1 operação de dispositivo');
  assert.equal(B.proposer('ai:alice'), 'Assistente de IA (para alice)');

  const ja = await load('ja');
  assert.equal(ja.countLabel(3), '3 台のデバイス');
});
