import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const LOCALES = ['en', 'pt-BR', 'es', 'de', 'zh-CN', 'ja', 'ru', 'ko', 'ms'];

async function goFiles(dir, out = []) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) await goFiles(p, out);
    else if (p.endsWith('.go') && !p.endsWith('_test.go')) out.push(p);
  }
  return out;
}

// Every action the server writes to the audit trail must read as words in
// every console language. The label is either a whole-action entry or a
// resource plus a verb (see src/labels.jsx); without one the audit screen shows
// the raw identifier — "oauth app revoke" in the middle of a Portuguese page —
// which is how ten actions went unnoticed until this test existed.
//
// The scan used to read only AddAudit("…") literals, and patch.decline, every
// remote.share.* action and the alert transitions went out unlabelled: they
// are written through helpers (jobs' audit, auditShare), picked into a
// variable first (action := "…"), or assembled at run time. All three are
// covered now; the run-time names are listed by hand next to where they are
// built, and a new one belongs in RUNTIME_ACTIONS.
const RUNTIME_ACTIONS = [
  'patch.approve', 'patch.decline', // api_patches.go: "patch." + decision
  'alert.ack', 'alert.resolve', 'alert.suppress', // api_monitoring.go: "alert." + action
  'alert.bulk.ack', 'alert.bulk.resolve', 'alert.bulk.suppress', // "alert.bulk." + in.Action
  'alert_rule.create', 'alert_rule.update', 'alert_rule.delete', // api_alert_rules.go: "alert_rule." + action
];

test('every audit action the server records has a label in every language', async () => {
  const actions = new Set(RUNTIME_ACTIONS);
  const literal = '"([a-z0-9_]+(?:\\.[a-z0-9_]+)+)"';
  const calls = new RegExp(`(?:AddAudit|AddAuditAt|\\baudit|auditShare)\\([^,]+,\\s*[^,]+,\\s*${literal}`, 'g');
  const picked = new RegExp(`\\baction\\s*:?=\\s*${literal}`, 'g');
  for (const file of await goFiles(path.join(root, '..', 'internal'))) {
    const src = await readFile(file, 'utf8');
    for (const m of src.matchAll(calls)) actions.add(m[1]);
    for (const m of src.matchAll(picked)) actions.add(m[1]);
  }
  for (const a of ['job.propose', 'remote.share.redeem', 'remote.share.accept', 'user.disable']) {
    assert.ok(actions.has(a), `the scan no longer finds ${a}; it is broken`);
  }
  assert.ok(actions.size > 40, `found only ${actions.size} audit actions; the scan is broken`);

  for (const locale of LOCALES) {
    const table = await readFile(path.join(root, 'src', 'locales', `${locale}.jsx`), 'utf8');
    const has = (key) => table.includes(`'${key}':`);
    const missing = [...actions].filter((a) => {
      if (has(`audit.action.${a}`)) return false;
      const i = a.lastIndexOf('.');
      return i < 0 || !(has(`audit.res.${a.slice(0, i)}`) && has(`audit.verb.${a.slice(i + 1)}`));
    });
    assert.deepEqual(missing, [], `${locale}: audit actions without a label`);
  }
});

// The detail column repeated the Target column (a remote session's host and
// agent id, a variable's name) and wrapped onto a third line; the CSV must
// still carry every pair.
test('the audit detail leaves out what the Target column already shows', async () => {
  const { i18nSandbox } = await import('./i18n-sandbox.mjs');
  const sb = await i18nSandbox('src/labels.jsx');
  const text = sb.window.auditDetailText;
  const agent = 'bb0397c9-ed56-43b2-8758-43a557b7d06e';
  const remote = { host: 'WINDOWS-TEST', agent, provider: 'native' };
  assert.equal(text(remote, [agent, 'WINDOWS-TEST']), 'provider: native');
  assert.equal(text(remote), `host: WINDOWS-TEST · agent: ${agent} · provider: native`, 'the CSV keeps everything');
  assert.equal(text({ scope: { type: 'client' }, note: null }, ['x']), 'scope: {"type":"client"} · note: null');
  assert.equal(text({}, [agent]), '');
  assert.equal(text(null), '');
  // An object is never hidden, even if it would print like the target.
  assert.equal(text({ count: 3, ok: true }, ['3', 'true']), '');
});

// One long value (a bare UUID, a JSON detail) used to widen its column until
// the table spilled out of the card, which cut off the columns on the right.
test('the audit table keeps its columns', async () => {
  const src = await readFile(path.join(root, 'src', 'screens-jobs.jsx'), 'utf8');
  const screen = src.slice(src.indexOf('function AuditScreen('), src.indexOf('window.JobsScreen'));
  assert.match(screen, /<div className="overflow-x-auto">\s*<table className="[^"]*\btable-fixed\b[^"]*">/);
  assert.equal((screen.match(/<col\b/g) || []).length, (screen.match(/<th\b/g) || []).length, 'one <col> per column');
  assert.match(screen, /<tr key=\{e\.id\} className="[^"]*\balign-middle\b[^"]*\bleading-5\b/);
  for (const cell of ['{displayActor(e.actor)}</td>', '{displayTarget(e)}</td>', '{window.auditDetailText(e.detail, ']) {
    const at = screen.indexOf(cell);
    const td = screen.lastIndexOf('<td', at);
    assert.match(screen.slice(td, at), /\[overflow-wrap:anywhere\]/, `${cell} must wrap inside its column`);
  }
});
