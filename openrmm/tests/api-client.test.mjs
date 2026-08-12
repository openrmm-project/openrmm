import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// src/mockapi.jsx is the real fetch client (the filename is historical). It
// contains no JSX — it is plain JS that assigns to window — so it runs in a vm
// sandbox with a stubbed window and fetch. That buys a behavioural test of the
// client instead of grepping the source for URL strings: the assertions below
// fail if a path, a method, or a request body changes.
async function loadApi() {
  const source = await readFile(path.join(root, 'src', 'mockapi.jsx'), 'utf8');
  const calls = [];
  const store = new Map();
  const sandbox = {
    console,
    URL,
    URLSearchParams,
    setTimeout,
    clearTimeout,
    Event: class { constructor(type) { this.type = type; } },
    location: { origin: 'https://console.test', href: 'https://console.test/' },
    sessionStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, v),
      removeItem: (k) => store.delete(k),
    },
    async fetch(url, init) {
      calls.push({ url, method: init.method, body: init.body, headers: init.headers });
      return {
        ok: true,
        status: 200,
        statusText: 'OK',
        headers: { get: () => null },
        async text() { return JSON.stringify({ ok: true }); },
        async json() { return { ok: true }; },
      };
    },
    dispatchEvent() {},
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox, { filename: 'mockapi.jsx' });
  return { api: sandbox.api, calls, sandbox, last: () => calls[calls.length - 1] };
}

// Two transports, two endpoints. The client must not collapse them: a save aimed
// at the alerts profile that lands on /api/config/smtp overwrites the settings
// that deliver password resets.
test('the alerts SMTP profile has endpoints of its own', async () => {
  const { api, last } = await loadApi();

  await api.getSMTPConfig();
  assert.equal(last().url, 'https://console.test/api/config/smtp');
  assert.equal(last().method, 'GET');

  await api.getAlertSMTPConfig();
  assert.equal(last().url, 'https://console.test/api/config/smtp/alerts');

  await api.updateSMTPConfig({ host: 'smtp.account.test' });
  assert.equal(last().url, 'https://console.test/api/config/smtp');
  assert.equal(last().method, 'PUT');
  assert.equal(JSON.parse(last().body).host, 'smtp.account.test');

  await api.updateAlertSMTPConfig({ host: 'relay.alerts.test' });
  assert.equal(last().url, 'https://console.test/api/config/smtp/alerts');
  assert.equal(last().method, 'PUT');
  assert.equal(JSON.parse(last().body).host, 'relay.alerts.test');

  await api.testSMTP('a@b.test');
  assert.equal(last().url, 'https://console.test/api/config/smtp/test');

  await api.testAlertSMTP('a@b.test');
  assert.equal(last().url, 'https://console.test/api/config/smtp/alerts/test');
  assert.equal(JSON.parse(last().body).to, 'a@b.test');
});

// The address is administrative: it is where a password reset link is delivered,
// so the client sets it through the users route, which the backend guards with
// users.email.manage.
test('a user address is set through the users route, not self-service', async () => {
  const { api, last } = await loadApi();

  await api.setUserEmail('user-1', 'tech@example.test');
  assert.equal(last().url, 'https://console.test/api/users/user-1/email');
  assert.equal(last().method, 'PUT');
  assert.deepEqual(JSON.parse(last().body), { email: 'tech@example.test' });

  // Preferences stay self-service, and carry no address.
  await api.updateMyNotifications({ subscriptions: { job_failed: true } });
  assert.equal(last().url, 'https://console.test/api/me/notifications');
  assert.equal(last().method, 'PUT');
  const body = JSON.parse(last().body);
  assert.ok(!('email' in body), 'the preferences call must not carry an address');
  assert.deepEqual(body.subscriptions, { job_failed: true });
});

// The console keeps a copy of the default role permissions, used when a stored
// session has no permission list of its own. A missing key there hides the
// address editor from an administrator who does hold the permission — which is
// exactly what happened once already.
test('the admin permission fallback carries users.email.manage', async () => {
  const { sandbox } = await loadApi();
  assert.ok(sandbox.DEFAULT_ROLE_PERMS.admin.includes('users.email.manage'));
  for (const role of ['tech', 'readonly']) {
    assert.ok(!sandbox.DEFAULT_ROLE_PERMS[role].includes('users.email.manage'),
      `${role} must not hold users.email.manage`);
  }
});

// The Email screen has to offer both transports, and the notification card must
// not offer an address field back — the backend rejects the change, so a field
// there would be a control that silently fails.
test('the settings screen wires both SMTP profiles and no self-service address', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm2.jsx'), 'utf8');
  for (const call of ['getAlertSMTPConfig', 'updateAlertSMTPConfig', 'testAlertSMTP']) {
    assert.match(screens, new RegExp(`api\\.${call}\\b`), `the Email screen should call ${call}`);
  }
  assert.match(screens, /value: 'alerts'/, 'the Email screen should offer an alerts tab');
  // The preferences card saves subscriptions only.
  assert.match(screens, /api\.updateMyNotifications\(\{ subscriptions: subs \}\)/);
});

// Updating a task is a PUT, and the API validates the complete schedule. The
// inline enabled toggle must therefore preserve the task instead of sending a
// PATCH-shaped body containing only `enabled`.
test('the task toggle keeps the complete task when changing enabled state', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm.jsx'), 'utf8');

  assert.match(screens,
    /api\.updateTask\(t\.id,\s*\{\s*\.\.\.t,\s*enabled:\s*!t\.enabled\s*\}\)/);
  assert.match(screens, /const \[confirmToggleTask, setConfirmToggleTask\] = useState\(null\)/);
  assert.match(screens, /onChange=\{\(\) => setConfirmToggleTask\(t\)\}/);
  assert.match(screens, /title=\{confirmToggleTask\?\.enabled \? 'Disable task\?' : 'Enable task\?'\}/);
  assert.match(screens, /onConfirm=\{\(\) => toggle\(confirmToggleTask\)\}/);
});

// Checks also use a full PUT update. Toggling one must retain its definition,
// keep the row click handler from opening the details drawer, and offer the
// same control in the editor for changes saved alongside other fields.
test('checks can be enabled or disabled from the list and editor', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm.jsx'), 'utf8');
  const ui = await readFile(path.join(root, 'src', 'ui.jsx'), 'utf8');

  assert.match(screens,
    /api\.updateCheck\(c\.id,\s*\{\s*\.\.\.c,\s*enabled:\s*!c\.enabled\s*\}\)/);
  assert.match(screens,
    /onClick=\{\(e\) => e\.stopPropagation\(\)\}\s*>\s*<Toggle checked=\{c\.enabled\}/);
  assert.match(screens, /const \[confirmToggle, setConfirmToggle\] = useState\(null\)/);
  assert.match(screens, /title=\{confirmToggle\?\.enabled \? 'Disable check\?' : 'Enable check\?'\}/);
  assert.match(screens, /onConfirm=\{\(\) => toggle\(confirmToggle\)\}/);
  assert.match(screens, /const \[enabled, setEnabled\] = useState\(check\?\.enabled \?\? true\)/);
  assert.match(screens, /interval_sec: Number\(interval\), enabled/);
  assert.match(screens, /Check will be disabled when saved/);
  assert.match(screens, /c\.enabled \? \(c\.latest\?\.status \|\| 'passing'\) : 'disabled'/);
  assert.match(ui, /disabled: \{ c: 'muted', label: 'Disabled' \}/);
});

test('the software report exposes its manufacturer column', async () => {
  const reports = await readFile(path.join(root, 'src', 'screens-reports.jsx'), 'utf8');

  assert.match(reports,
    /\{ key: 'name', label: 'Software',[^\n]+\n\s*\{ key: 'manufacturer', label: 'Manufacturer', text: \(r\) => r\.manufacturer \}/,
    'Manufacturer should appear immediately after Software');
});
