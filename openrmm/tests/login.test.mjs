import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';
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

  assert.doesNotMatch(app, /loginWithToken\(ssoToken\)/);
});

async function restore(hash, stored, exchange) {
  const app = await readFile(path.join(root, 'src', 'app.jsx'), 'utf8');
  const source = app.slice(app.indexOf('async function restoreConsoleAuth('), app.indexOf('\nfunction App()'));
  const fn = vm.runInNewContext(source + '\nrestoreConsoleAuth', { URLSearchParams });
  const calls = [];
  const browser = { location: { hash, pathname: '/', search: '' }, history: { replaceState() { calls.push('strip'); } },
    sessionStorage: { getItem() { return stored && JSON.stringify(stored); }, setItem(k,v) { stored = JSON.parse(v); } } };
  const api = { exchangeSSO: async code => { calls.push('exchange:' + code); return exchange(code); },
    loginWithToken: async token => { calls.push('restore:' + token); return { token }; } };
  return { result: await fn(api, browser, 'auth'), calls, stored };
}

test('a bearer supplied in a fragment cannot replace the existing session', async () => {
  const out = await restore('#sso_token=attacker', { token: 'owner' }, () => { throw Error('unexpected'); });
  assert.equal(out.result.token, 'owner');
  assert.deepEqual(out.calls, ['strip', 'restore:owner']);
  assert.equal(out.stored.token, 'owner');
});
test('an invalid handoff preserves the existing session', async () => {
  const out = await restore('#sso_code=wrong-browser', { token: 'owner' }, () => { throw Error('rejected'); });
  assert.equal(out.result.token, 'owner');
  assert.equal(out.stored.token, 'owner');
});
test('a legitimate exchanged handoff becomes the tab session', async () => {
  const out = await restore('#sso_code=one-use', { token: 'old' }, () => ({ token: 'new' }));
  assert.equal(out.result.token, 'new');
  assert.equal(out.stored.token, 'new');
  assert.deepEqual(out.calls, ['strip', 'exchange:one-use']);
});
test('an unsolicited bearer cannot sign in an empty tab', async () => {
  const out = await restore('#sso_token=attacker', null, () => { throw Error('unexpected'); });
  assert.equal(out.result, null);
  assert.deepEqual(out.calls, ['strip']);
});

test('the real transport preserves a current session when the exchange returns 401', async () => {
  const source=await readFile(path.join(root,'src','mockapi.jsx'),'utf8');
  let stored=JSON.stringify({token:'owner',role:'tech'});
  const browser={};
  const storage={getItem(){return stored;},setItem(k,v){stored=v;},removeItem(){stored=null;}};
  const context={window:browser,location:{origin:'https://console.invalid',href:'https://console.invalid/'},sessionStorage:storage,URL,
    fetch:async(url,options)=>{assert.equal(url,'https://console.invalid/api/auth/oidc/exchange');assert.equal(options.headers.Authorization,undefined);return new Response(JSON.stringify({error:'invalid sign-in code'}),{status:401});},
    t:key=>key,Response,setTimeout,Event};
  vm.runInNewContext(source,context);
  await assert.rejects(browser.api.exchangeSSO('unbound'));
  assert.equal(JSON.parse(stored).token,'owner');
  assert.equal(browser.api.currentRole(),'tech');
});
