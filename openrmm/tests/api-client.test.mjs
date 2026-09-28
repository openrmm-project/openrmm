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

  // The account transport is env-backed (RMM_SMTP_*), so the client offers no
  // way to write it: there is no PUT endpoint behind it to call.
  assert.equal(api.updateSMTPConfig, undefined,
    'the account SMTP profile must not be writable from the console');

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
test('the settings screen edits only the alerts SMTP profile', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm2.jsx'), 'utf8');
  for (const call of ['getAlertSMTPConfig', 'updateAlertSMTPConfig', 'testAlertSMTP']) {
    assert.match(screens, new RegExp(`api\\.${call}\\b`), `the Email screen should call ${call}`);
  }
  // Password-reset mail is configured with RMM_SMTP_* in the server environment.
  // A form here would let a settings administrator repoint authentication mail,
  // so the screen must not touch that profile.
  for (const call of ['updateSMTPConfig', 'getSMTPConfig', 'testSMTP\\(']) {
    assert.doesNotMatch(screens, new RegExp(`api\\.${call}`),
      `the Email screen must not call ${call}: the account transport is env-only`);
  }
  // The preferences card saves subscriptions only.
  assert.match(screens, /api\.updateMyNotifications\(\{ subscriptions: subs \}\)/);
});

// Updating a task is a PUT, and the API validates the complete schedule. The
// inline enabled toggle must therefore preserve the task instead of sending a
// PATCH-shaped body containing only `enabled`.
test('the task toggle keeps the complete task when changing enabled state', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm.jsx'), 'utf8');

  assert.match(screens,
    /api\.updateTask\(task\.id,\s*\{\s*\.\.\.task,\s*enabled:\s*!task\.enabled\s*\}\)/);
  assert.match(screens, /const \[confirmToggleTask, setConfirmToggleTask\] = useState\(null\)/);
  assert.match(screens, /onChange=\{\(\) => setConfirmToggleTask\(task\)\}/);
  assert.match(screens, /title=\{confirmToggleTask\?\.enabled \? t\('tasks\.disableTitle'\) : t\('tasks\.enableTitle'\)\}/);
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
  assert.match(screens, /title=\{confirmToggle\?\.enabled \? t\('checks\.disableTitle'\) : t\('checks\.enableTitle'\)\}/);
  assert.match(screens, /onConfirm=\{\(\) => toggle\(confirmToggle\)\}/);
  assert.match(screens, /const \[enabled, setEnabled\] = useState\(check\?\.enabled \?\? true\)/);
  assert.match(screens, /interval_sec: Number\(interval\), enabled/);
  assert.match(screens, /t\('checks\.willDisable'\)/);
  assert.match(screens, /function checkAggregateStatus\(c\)/);
  assert.match(screens, /if \(!c\.enabled\) return 'disabled'/);
  assert.match(screens, /if \(x\.failing > 0\) return 'failing'/);
  assert.match(screens, /if \(x\.warning > 0\) return 'warning'/);
  assert.match(screens, /if \(x\.unknown > 0 \|\| !x\.total\) return 'unknown'/);
  assert.match(screens, /<StatusPill status=\{checkAggregateStatus\(c\)\}/);
  assert.match(ui, /disabled: 'muted'/);
  const en = await readFile(path.join(root, 'src', 'locales', 'en.jsx'), 'utf8');
  assert.match(en, /'status\.disabled': 'Disabled'/);
});

test('alerts client carries filters, optimistic versions, bulk actions and suppressions', async () => {
  const { api, last } = await loadApi();

  await api.listAlerts({ status: 'open', severity: 'error', cursor: 'next-page', limit: 50 });
  const listURL = new URL(last().url);
  assert.equal(listURL.pathname, '/api/alerts');
  assert.equal(listURL.searchParams.get('status'), 'open');
  assert.equal(listURL.searchParams.get('severity'), 'error');
  assert.equal(listURL.searchParams.get('cursor'), 'next-page');

  await api.ackAlert('alert-1', { expected_version: 7 });
  assert.equal(last().url, 'https://console.test/api/alerts/alert-1/ack');
  assert.equal(last().method, 'POST');
  assert.deepEqual(JSON.parse(last().body), { expected_version: 7 });

  await api.bulkAlertAction({ ids: ['alert-1'], action: 'resolve', expected_versions: { 'alert-1': 7 } });
  assert.equal(last().url, 'https://console.test/api/alerts/bulk');
  assert.equal(JSON.parse(last().body).action, 'resolve');

  await api.createAlertSuppression({
    kind: 'maintenance', scope_type: 'site', scope_id: 'site-1',
    ends_at: '2026-08-15T12:00:00Z',
  });
  assert.equal(last().url, 'https://console.test/api/alert-suppressions');
  assert.equal(last().method, 'POST');
  await api.cancelAlertSuppression('suppression-1');
  assert.equal(last().url, 'https://console.test/api/alert-suppressions/suppression-1');
  assert.equal(last().method, 'DELETE');
});

test('the software report exposes its manufacturer column', async () => {
  const reports = await readFile(path.join(root, 'src', 'screens-reports.jsx'), 'utf8');

  assert.match(reports,
    /\{ key: 'name',[^\n]+\n\s*\{ key: 'manufacturer', text: \(r\) => r\.manufacturer \}/,
    'Manufacturer should appear immediately after Software');
  const en = await readFile(path.join(root, 'src', 'locales', 'en.jsx'), 'utf8');
  assert.match(en, /'rep\.col\.name': 'Software'/);
  assert.match(en, /'rep\.col\.manufacturer': 'Manufacturer'/);
});

// An AI proposal is a row nobody has reviewed: created disabled and stamped with
// its author. The console has to mark it, because a proposal that looks like
// ordinary configuration is a proposal nobody will review.
test('the console marks AI proposals awaiting review', async () => {
  const rmm = await readFile(path.join(root, 'src', 'screens-rmm.jsx'), 'utf8');

  assert.match(rmm, /function awaitingReview\(row\)/,
    'the screens need a shared predicate for an unreviewed proposal');
  assert.match(rmm, /!!row\?\.proposed_by && !row\?\.approved_by/,
    'awaiting review means proposed but not yet approved: both fields matter');

  // Both screens that own runnable configuration must show it.
  const taskRow = rmm.slice(rmm.indexOf('function TasksScreen'));
  assert.match(taskRow, /awaitingReview\(task\) && <ProposalBadge \/>/,
    'the tasks table should badge an unreviewed proposal');
  const checkRow = rmm.slice(rmm.indexOf('function ChecksScreen'), rmm.indexOf('function TasksScreen'));
  assert.match(checkRow, /awaitingReview\(c\) && <ProposalBadge \/>/,
    'the checks table should badge an unreviewed proposal');

  // The policies screen is a separate file, so the helpers must be exported onto
  // window — each source file is wrapped in its own block scope by the build.
  assert.match(rmm, /window\.awaitingReview = awaitingReview/);
  assert.match(rmm, /window\.ProposalBadge = ProposalBadge/);
  const rmm2 = await readFile(path.join(root, 'src', 'screens-rmm2.jsx'), 'utf8');
  assert.match(rmm2, /awaitingReview\(policy\) &&[\s\S]{0,60}<ProposalBadge \/>/,
    'a proposed policy should be badged on its card');
});

// i18n. The key-set and placeholder checks live in i18n.test.mjs, which loads
// every table from src/locales. What stays here is the picker's side of it.
//
// A locale offered in the picker with no table behind it would render every
// label as its own key — the failure the fallback exists to prevent.
test('every offered locale has a table and unreviewed translations are explained', async () => {
  const core = await readFile(path.join(root, 'src', 'i18n.jsx'), 'utf8');
  const offered = [...core.matchAll(/code: '([^']+)'/g)].map((m) => m[1]);
  assert.ok(offered.includes('en'), 'English must be offered');
  const build = await readFile(path.join(root, 'scripts', 'build.mjs'), 'utf8');
  for (const code of offered) {
    const table = await readFile(path.join(root, 'src', 'locales', `${code}.jsx`), 'utf8');
    assert.match(table, new RegExp(`\\['${code}'\\] = \\{`), `src/locales/${code}.jsx must register ${code}`);
    assert.ok(build.includes(`'src/locales/${code}.jsx'`), `the build must bundle src/locales/${code}.jsx`);
  }

  // Full translation coverage and linguistic review are separate states.
  const screens = await readFile(path.join(root, 'src', 'screens-rmm2.jsx'), 'utf8');
  assert.match(screens, /!chosen\.nativeReviewed/, 'the picker must explain pending linguistic review');
  assert.match(screens, /t\('lang\.reviewHint'\)/);
  assert.doesNotMatch(screens, /lang\.draft/, 'the picker must not call a fully populated locale a draft');
});

// The nav labels must stay keys. Resolving them at module load would freeze the
// language for the session, and the bug looks like "the picker does nothing".
test('navigation labels are keys resolved at render', async () => {
  const app = await readFile(path.join(root, 'src', 'app.jsx'), 'utf8');
  assert.match(app, /label: 'nav\.devices'/, 'NAV should hold keys, not text');
  assert.match(app, /\{t\(it\.label\)\}/, 'the sidebar must resolve the key at render');
  assert.match(app, /\{t\(grp\.section\)\}/);
  assert.match(app, /useLocale\(\)/, 'the root must re-render when the language changes');
});

// The chat's "Download CSV" used to be built from the model's own answer —
// scanning it for a table or a ```csv block and saving that. The file therefore
// held what the model had RETYPED, capped at whatever it managed to write, and
// cost tokens twice. At a thousand rows it was not the data the database held.
test('the chat downloads a server-minted export, not the model output', async () => {
  const chat = await readFile(path.join(root, 'src', 'screens-chat.jsx'), 'utf8');

  assert.doesNotMatch(chat, /function tabularCSV/,
    'the model-output scraper must be gone, not merely unused');
  assert.doesNotMatch(chat, /b\.lang === 'csv'/,
    'nothing should build a download out of what the assistant wrote');

  assert.match(chat, /\/api\/exports\/csv\?token=/, 'it should look for a minted export link');
  assert.match(chat, /function ExportDownload/);

  // A plain <a href> would send no Authorization header — this console carries a
  // bearer token, not a cookie — so the click would come back 401.
  assert.match(chat, /api\.downloadExport\(path\)/,
    'the export must be fetched with credentials, not linked');

  // Only the path is kept from the matched URL. Following the origin a message
  // carries would let a model, or something that reached its context, point a
  // click somewhere else.
  assert.match(chat, /url\.pathname \+ url\.search/,
    'the origin in the message must be discarded');
});

test('downloadExport sends the bearer token', async () => {
  const api = await readFile(path.join(root, 'src', 'mockapi.jsx'), 'utf8');
  const fn = api.slice(api.indexOf('async downloadExport'));
  assert.match(fn.slice(0, 600), /headers\.Authorization = 'Bearer ' \+ session\.token/,
    'the export request must carry the session token');
  assert.match(fn.slice(0, 900), /content-disposition/i,
    'the filename should come from the server, not be invented here');
});

// The audit screen must filter and page on the SERVER. Doing it in the browser
// means every search silently answers "among the rows that happened to be
// fetched" — which is the opposite of what an audit log is consulted for, and
// the failure is invisible because the screen still looks like it worked.
test('the audit screen filters and pages server-side', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-jobs.jsx'), 'utf8');
  const audit = screens.slice(screens.indexOf('function AuditScreen'));

  assert.match(audit, /api\.listAudit\(query\)/, 'the query must go to the API');
  for (const param of ['limit:', 'cursor:', 'actor:', 'action:', 'target:', 'since:', 'until:']) {
    assert.ok(audit.includes(param), `the query should carry ${param}`);
  }
  // Offset drifts on an append-only table read newest-first: entries arrive at
  // the top, so every later page shifts under a reader who is paging while
  // others work, and the same rows appear twice.
  assert.doesNotMatch(audit, /offset: page \* PAGE/,
    'paging must anchor on a cursor, not on a computed offset');
  assert.match(audit, /setCursors\(\[\.\.\.cursors, nextCursor\]\)/,
    'Next should follow the cursor the server handed back');
  assert.match(audit, /setCursors\(cursors\.slice\(0, -1\)\)/,
    'Previous should step back through the cursors already followed');
  // A cursor points into the previous result set; following one after a filter
  // change would page through the wrong query.
  assert.match(audit, /setPage\(0\); setCursors\(\[\]\)/,
    'changing a filter must discard the cursors');
  // No client-side array filtering: that is the bug this replaced.
  assert.doesNotMatch(audit, /entries\.filter\(/,
    'entries must not be filtered in the browser — the server has the whole set');

  assert.match(audit, /auditQ\.data\?\.total/, 'paging needs the total from the server');
  assert.match(audit, /setPage\(page \+ 1\)/, 'there must be a way to reach later pages');

  // An export that stopped at the visible page would be read as the complete
  // record, so it pages the API and reports when it hits the ceiling.
  assert.match(audit, /for \(let offset = 0; offset < EXPORT_MAX/,
    'the export must cover the whole filtered set, not the page on screen');
  assert.match(audit, /t\('audit\.truncated'\)/, 'a truncated export has to say so');

  // A date input yields a local day; the end of a range must include that day.
  assert.match(audit, /next\.setDate\(next\.getDate\(\) \+ 1\)/,
    '`until` must be the start of the following day, or the chosen day is excluded');
});

// Alerts are the tenant's own mail. The screen must not describe, or offer, a
// way to send them through the operator's account relay: no "inherited" state,
// the connection and login fields always editable, and the server's refusal of a
// sender without a host shown in words.
test('the alerts email screen offers no ride on the account relay', async () => {
  const screens = await readFile(path.join(root, 'src', 'screens-rmm2.jsx'), 'utf8');
  const panel = screens.slice(screens.indexOf('function SMTPProfilePanel('), screens.indexOf('function EnrollPanel('));
  assert.ok(panel.length > 0, 'SMTPProfilePanel not found');
  assert.doesNotMatch(panel, /inherits|effective|mail\.inherited|mail\.credsInherited|mail\.accountTransport/);
  assert.doesNotMatch(panel, /disabled=\{inherits\}/);
  assert.match(panel, /e\.code === 'alerts_need_own_server'/);
  assert.match(panel, /disabled=\{!c\.enabled \|\| dirty\}/);
});

// One desktop session per operator per device. The server says why it refused
// in a code, and the viewer acts on that code rather than on the English
// message: it offers "continue here" instead of retrying 40 times into the same
// refusal, and a tab that was moved elsewhere must not reconnect and take the
// session back.
test('a refused remote session reaches the viewer as a code it acts on', async () => {
  const { api, sandbox, last } = await loadApi();

  await api.startRemote('dev-1', 'native', { replace: true, resume: 'sess-1' });
  assert.equal(last().url, 'https://console.test/api/devices/dev-1/remote');
  assert.deepEqual(JSON.parse(last().body), { provider: 'native', replace: true, resume: 'sess-1' });
  await api.startRemote('dev-1', 'terminal');
  assert.deepEqual(JSON.parse(last().body), { provider: 'terminal' });

  sandbox.fetch = async () => ({
    ok: false, status: 409, statusText: 'Conflict', headers: { get: () => null },
    async json() { return { error: 'you already have a remote session open on this device', code: 'remote_session_active' }; },
  });
  await assert.rejects(api.startRemote('dev-1', 'native'), (e) => e.status === 409 && e.code === 'remote_session_active');

  const viewer = await readFile(path.join(root, 'src', 'screens-devices.jsx'), 'utf8');
  assert.match(viewer, /e\.code === 'remote_session_active'/);
  // The close codes are the relay's; a mismatch would turn "moved to another
  // tab" into a reconnect loop that steals the session back.
  const relay = await readFile(path.join(root, '..', 'internal', 'remote', 'relay.go'), 'utf8');
  const active = relay.match(/CloseSessionActive = (\d+)/)[1];
  const replaced = relay.match(/CloseSessionReplaced = (\d+)/)[1];
  assert.match(viewer, new RegExp(`ev\\.code === ${active} \\|\\| ev\\.code === ${replaced}\\)`));
  assert.match(viewer, new RegExp(`ev\\.code === ${replaced} \\? 'replaced' : 'active'`));
});
