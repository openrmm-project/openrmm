import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const src = (file) => readFile(path.join(root, 'src', file), 'utf8');

// The support link is an administrator's setting: the key is typed once,
// never shown back, and a Cloud console has nothing to configure.
test('the support tab is for administrators and never shows the key', async () => {
  const screens = await src('screens-rmm2.jsx');
  assert.match(screens, /can\('settings\.manage'\) \? \[\{ value: 'support'/);
  assert.match(screens, /tab === 'support' && can\('settings\.manage'\) && <SupportPanel \/>/);
  const panel = screens.slice(screens.indexOf('function SupportPanel('), screens.indexOf('function SMTPPanel('));
  assert.ok(panel.length > 0, 'SupportPanel not found');
  assert.match(panel, /d\.cloud \?/, 'a Cloud console shows no key field');
  assert.match(panel, /<Input type="password" autoComplete="off"/, 'the key is typed like a password');
  assert.doesNotMatch(panel, /value=\{d\.|d\.key|state\.key/, 'the stored key is never read back into the page');
  assert.match(panel, /rel="noopener noreferrer"/);
  const client = await src('mockapi.jsx');
  assert.match(client, /support\(\) \{ return GET\('\/api\/support'\); \}/);
  assert.match(client, /setSupportKey\(key\) \{ return PUT\('\/api\/support', \{ key \}\); \}/);
  assert.match(client, /removeSupportKey\(\) \{ return DELETE\('\/api\/support'\); \}/);
});

// Over the plan, administrators are told; nothing else changes.
test('the over-plan banner informs administrators only', async () => {
  const app = await src('app.jsx');
  const banner = app.slice(app.indexOf('function SupportBanner('), app.indexOf('function Shell('));
  assert.ok(banner.length > 0, 'SupportBanner not found');
  assert.match(banner, /const visible = can\('settings\.manage'\)/);
  assert.match(banner, /!st \|\| !st\.over_limit \|\| st\.status === 'ended'/);
  assert.match(banner, /rel="noopener noreferrer"/);
  assert.match(app.slice(app.indexOf('function Shell(')), /<SupportBanner \/>/);
});

test('every language explains the support link and keeps its placeholders', async () => {
  const holes = {
    'support.plan': 'devices,interval', 'support.usage': 'limit,used', 'support.over': 'limit,used',
    'support.overBy': 'date,limit,used', 'support.paidThrough': 'date', 'support.checked': 'date',
    'support.lastError': 'error', 'support.intro': '', 'support.cloud': '', 'support.keyHint': '',
    'settings.tab.support': '', 'support.status.active': '', 'support.status.ending': '', 'support.status.ended': '',
  };
  for (const locale of ['en', 'pt-BR', 'es', 'de', 'zh-CN', 'ja', 'ru', 'ko', 'ms']) {
    const table = await src(`locales/${locale}.jsx`);
    for (const [key, want] of Object.entries(holes)) {
      const m = table.match(new RegExp(`'${key.replace(/\./g, '\\.')}': (['"])(.+?)\\1,`));
      assert.ok(m, `${locale}: ${key}`);
      const found = [...m[2].matchAll(/\{(\w+)\}/g)].map((h) => h[1]).sort().join(',');
      assert.equal(found, want, `${locale}: ${key} placeholders`);
    }
  }
});
