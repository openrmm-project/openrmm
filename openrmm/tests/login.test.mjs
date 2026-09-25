import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// The sign-in screen takes a person's password (and second factor) or SSO,
// nothing else. A "paste a token" mode accepted RMM_ADMIN_TOKEN and API tokens:
// it skipped the second factor and the SSO policy, logged the work as "admin"
// instead of a person, and made a leaked automation token a console login.
test('the sign-in screen offers no token login', async () => {
  const app = await readFile(path.join(root, 'src', 'app.jsx'), 'utf8');
  const login = app.slice(app.indexOf('function LoginScreen('), app.indexOf('function ForgotPasswordScreen('));
  assert.ok(login.length > 0, 'LoginScreen not found');
  assert.match(login, /api\.login\(/);
  assert.doesNotMatch(login, /loginWithToken/, 'the form must not sign in with a pasted token');
  assert.doesNotMatch(login, /auth\.mode\.|auth\.token/);

  // loginWithToken remains for tokens the console itself received: the SSO
  // callback's one-time session and the session restored from this tab.
  const uses = [...app.matchAll(/api\.loginWithToken\(([^)]*)\)/g)].map((m) => m[1]);
  assert.deepEqual(uses.sort(), ['a.token', 'ssoToken']);
});
