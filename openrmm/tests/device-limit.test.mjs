import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const src = (file) => readFile(path.join(root, 'src', file), 'utf8');

// The plan's device limit is shown to the people who add devices, from 80%,
// and the way to increase it only to administrators. A console with no limit
// (every self-hosted one) shows nothing at all.
test('the device-limit banner appears near or at the limit, for those who add devices', async () => {
  const app = await src('app.jsx');
  const banner = app.slice(app.indexOf('function DeviceLimitBanner('), app.indexOf('function Shell('));
  assert.ok(banner.length > 0, 'DeviceLimitBanner not found');
  assert.match(banner, /api\.deviceLimit\(\)/);
  assert.match(banner, /can\('enrollment\.manage'\) \|\| can\('settings\.manage'\)/);
  assert.match(banner, /!\(d\.limit > 0\)/, 'no limit, no banner');
  assert.match(banner, /d\.level !== 'warning' && d\.level !== 'limit'/, 'only from 80%');
  assert.match(banner, /d\.upgrade_url && can\('settings\.manage'\)/, 'the upgrade link is for administrators');
  assert.match(banner, /rel="noopener noreferrer"/);
  assert.match(app.slice(app.indexOf('function Shell(')), /<DeviceLimitBanner \/>/);
});

test('the devices screen counts against the plan only when there is one', async () => {
  const devices = await src('screens-devices.jsx');
  assert.match(devices, /limitQ\.data\?\.limit > 0 \? ' · ' \+ t\('devlimit\.ofPlan'/);
  const client = await src('mockapi.jsx');
  assert.match(client, /deviceLimit\(\) \{ return GET\('\/api\/devices\/limit'\); \}/);
});

test('every language explains the limit and keeps its placeholders', async () => {
  const keys = ['devlimit.warning', 'devlimit.full', 'devlimit.fullFrom', 'devlimit.increase', 'devlimit.ofPlan'];
  for (const locale of ['en', 'pt-BR', 'es', 'de', 'zh-CN', 'ja', 'ru', 'ko', 'ms']) {
    const table = await src(`locales/${locale}.jsx`);
    for (const key of keys) {
      const m = table.match(new RegExp(`'${key.replace('.', '\\.')}': (['"])(.+?)\\1,`));
      assert.ok(m, `${locale}: ${key}`);
      const holes = [...m[2].matchAll(/\{(\w+)\}/g)].map((h) => h[1]).sort().join(',');
      const want = { 'devlimit.warning': 'limit,used', 'devlimit.full': 'limit,used', 'devlimit.fullFrom': 'date,limit,used',
        'devlimit.increase': '', 'devlimit.ofPlan': 'limit,used' }[key];
      assert.equal(holes, want, `${locale}: ${key} placeholders`);
    }
  }
});
