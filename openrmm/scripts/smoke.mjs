import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import WebSocket from 'ws';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const types = { '.css': 'text/css', '.html': 'text/html', '.js': 'text/javascript', '.png': 'image/png', '.woff2': 'font/woff2' };
const permissions = ['ai_chat.use', 'alerts.view', 'alerts.manage', 'jobs.view', 'jobs.create', 'jobs.manage', 'checks.view', 'checks.manage', 'tasks.view', 'tasks.manage', 'policies.view', 'policies.manage', 'scripts.view', 'scripts.manage', 'scripts.variables', 'patches.view', 'patches.manage', 'patches.policies.manage', 'devices.view', 'devices.manage', 'devices.delete', 'devices.remote', 'devices.files', 'devices.terminal', 'clients.view', 'clients.manage', 'reports.view', 'audit.view', 'settings.manage', 'enrollment.manage', 'api_tokens.manage', 'users.manage', 'roles.manage'];
const user = { id: 'user-1', username: 'smoke-admin', role: 'admin', permissions, created_at: '2026-01-01T00:00:00Z' };
const apiRequests = [];
const ruleChecks = [];
const extraChecks = Array.from({ length: 60 }, (_, i) => ({ id: 'sample-rule-' + (i + 1), name: 'Monitor ' + String(i + 1).padStart(2, '0'), type: 'ping', agent_id: 'dev-1', config: { host: '127.0.0.1' }, enabled: true, interval_sec: 300 }));
const ruleDefaults = Object.fromEntries(['connectivity', 'job_failure', 'patch_failure', 'reboot_pending', 'agent_outdated'].map((type) => [type, { type, config: type === 'connectivity' ? { mode: 'alert', severity: 'error', after_min: 3 } : { mode: 'ignore', severity: 'warning' } }]));
let rulePrefs = { mode: 'all', include_defaults: true, check_ids: [] };

const scriptVariables = [];
// Enough definitions that the Variables tab and the editor's picker must scroll and be searched.
const extraVariables = Array.from({ length: 199 }, (_, i) => ({ id: 'variable-extra-' + i, name: 'SAMPLE_VAR_' + String(i).padStart(3, '0'), description: 'Synthetic variable ' + i, secret: i % 2 === 0, values: [] }));
const libraryScripts = [{ id: 'script-e2e', name: 'Variable installer', description: '', category: 'Tests', shell: 'sh', body: 'printf \'%s\' "$customerKey"', params: [], variables: [] }];

const remoteConfig = {
  ai: { provider: 'openai-compatible', base_url: '', model: '', api_key_set: true, configured: true },
  remote: { default: 'native', providers: ['native', 'meshcentral', 'rustdesk'], meshcentral_enabled: true, meshcentral_url: 'https://mesh.example.invalid', meshcentral_user: 'admin', meshcentral_token_set: true, meshcentral_shared_account: true, meshcentral_match_hostname: false, rustdesk_enabled: false, guest_access: false },
  jobs: { allow_unapproved: false }, server: { version: 'e2e', base_url: 'https://agents.example.invalid' },
};

function json(res, status, body) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    const pathname = url.pathname;

    if (pathname.startsWith('/api/')) {
      const token = (req.headers.authorization || '').replace(/^Bearer\s+/, '');
      let payload;
      if (req.method === 'POST' || req.method === 'PUT') {
        let raw = '';
        for await (const chunk of req) raw += chunk;
        if (raw) payload = JSON.parse(raw);
      }
      apiRequests.push({ method: req.method, pathname, token, payload });
      if (!token) return json(res, 401, { error: 'missing bearer token' });
      if (token === 'expire-token') return json(res, 401, { error: 'expired token' });
      if (pathname === '/api/me') return json(res, 200, user);
      if (pathname === '/api/script-variables') {
        if (req.method === 'POST') {
          const item = { id: 'variable-e2e', ...payload, values: [] }; scriptVariables.push(item);
          return json(res, 201, item);
        }
        return json(res, 200, { variables: [...scriptVariables, ...extraVariables] });
      }
      if (pathname === '/api/script-variables/variable-e2e/values' && req.method === 'PUT') {
        const item = scriptVariables[0];
        item.values = [{ scope_type: payload.scope_type, scope_id: payload.scope_id, set: true, ...(item.secret ? {} : { value: payload.value }) }];
        return json(res, 200, { name: item.name, set: true });
      }
      if (pathname === '/api/scripts') return json(res, 200, { scripts: libraryScripts });
      if (pathname === '/api/script-categories') return json(res, 200, { categories: [{ name: 'Tests', count: 1 }] });
      if (pathname === '/api/scripts/script-e2e' && req.method === 'PUT') {
        Object.assign(libraryScripts[0], payload); return json(res, 200, libraryScripts[0]);
      }
      if (pathname === '/api/devices/dev-1/run-script' && req.method === 'POST') return json(res, 201, { id: 'variable-job-e2e', status: 'awaiting_approval', requires_approval: true });
      if (pathname === '/api/checks') {
        if (req.method === 'POST') {
          const check = { id: ruleChecks.length ? 'service-e2e-' + ruleChecks.length : 'rule-e2e', ...payload };
          ruleChecks.push(check);
          return json(res, 201, check);
        }
        return json(res, 200, { checks: [...ruleChecks, ...extraChecks] });
      }
      if (pathname === '/api/policies') return json(res, 200, { policies: [] });
      if (pathname === '/api/alert-defaults') return json(res, 200, { defaults: ruleDefaults, can_manage: true });
      if (pathname.startsWith('/api/alert-defaults/') && req.method === 'PUT') {
        const type = pathname.split('/').at(-1);
        ruleDefaults[type] = { type, config: payload };
        return json(res, 200, ruleDefaults[type]);
      }
      if (pathname === '/api/me/notifications') {
        if (req.method === 'PUT') rulePrefs = payload.rules;
        return json(res, 200, { email: 'e2e@example.invalid', delivery_configured: true, subscriptions: [{ event_type: 'agent_offline', enabled: true }], rules: rulePrefs, available_rules: [...ruleChecks, ...extraChecks].map(({ id, name, type }) => ({ id, name, type })) });
      }
      if (pathname === '/api/config') {
        if (req.method === 'PUT') {
          if (payload?.remote?.default && !['native', 'meshcentral', 'rustdesk'].includes(payload.remote.default)) return json(res, 400, { error: 'invalid remote provider' });
          if (payload?.remote?.default === 'rustdesk' && !(payload.remote.rustdesk_enabled ?? remoteConfig.remote.rustdesk_enabled)) return json(res, 400, { error: 'RustDesk must be enabled to be the default remote provider' });
          const { meshcentral_token_key, ...remote } = payload.remote || {};
          Object.assign(remoteConfig.remote, remote);
          if (meshcentral_token_key?.trim()) remoteConfig.remote.meshcentral_token_set = true;
        }
        return json(res, 200, remoteConfig);
      }
      if (pathname === '/api/devices') return json(res, 200, { devices: [{ id: 'dev-1', hostname: 'SMOKE-PC', description: 'Smoke device', os: 'Linux', arch: 'amd64', version: '1.0.0', status: 'online', tags: [], last_seen: '2026-01-01T00:00:00Z' }] });
      if (pathname === '/api/jobs') return json(res, 200, { jobs: [] });
      if (pathname === '/api/audit') {
        const entries = Number(url.searchParams.get('limit')) >= 50 ? [
          { id: 'audit-snapshot', actor: user.id, action: 'alert.ack', target: 'alert-target-1', detail: { hostname: '=1+1' }, created_at: '2026-11-10T12:00:00Z' },
          { id: 'audit-legacy', actor: user.id, action: 'alert.resolve', target: 'alert-target-2', detail: { version: 2 }, target_label: 'ORDINARY-HOST', created_at: '2026-11-10T12:00:00Z' },
        ] : [];
        return json(res, 200, { entries, total: entries.length });
      }
      if (pathname === '/api/alerts') return json(res, 200, { alerts: [] });
      if (pathname === '/api/sites') return json(res, 200, { sites: [] });
      if (pathname === '/api/clients') return json(res, 200, { clients: [] });
      if (pathname === '/api/chat/sessions') return json(res, 200, { sessions: [] });
      return json(res, 200, {});
    }

    if (pathname === '/auth-config.js') {
      const token = url.searchParams.get('expired') === '1' ? 'expire-token' : 'smoke-token';
      res.setHeader('Content-Type', 'text/javascript');
      res.end(`window.__API_BASE__=location.origin;addEventListener('error',(event)=>document.documentElement.dataset.smokeError=event.message);addEventListener('unhandledrejection',(event)=>document.documentElement.dataset.smokeError=String(event.reason));sessionStorage.setItem('rmm-ai-auth',${JSON.stringify(JSON.stringify({ token, user, role: 'admin', permissions }))});const route=new URLSearchParams(location.search).get('route');if(route){const timer=setInterval(()=>{const button=route==='Open navigation'?document.querySelector('[aria-label="Open navigation"]'):[...document.querySelectorAll('button')].find((el)=>el.textContent.trim()===route);if(button){button.click();clearInterval(timer)}},25)}setTimeout(()=>{document.documentElement.dataset.smokeWidth=innerWidth+':'+document.documentElement.scrollWidth},900);`);
      return;
    }

    if (pathname === '/authenticated/' || pathname === '/expired/') {
      const html = await readFile(path.join(root, 'index.html'), 'utf8');
      const expired = pathname === '/expired/' ? '?expired=1' : '';
      res.setHeader('Content-Type', 'text/html');
      res.end(html.replace('/config.js', '/auth-config.js' + expired));
      return;
    }

    const requested = pathname === '/' ? 'index.html' : pathname.slice(1);
    const file = path.resolve(root, requested);
    assert.ok(file.startsWith(root + path.sep));
    const info = await stat(file);
    if (!info.isFile()) throw new Error('not a file');
    res.setHeader('Content-Type', types[path.extname(file)] || 'application/octet-stream');
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.setHeader('Content-Type', 'text/plain');
    res.end('not found');
  }
});

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const { port } = server.address();
const baseURL = `http://127.0.0.1:${port}`;

// The console follows the browser's language, and headless Chrome takes that
// from the environment on Linux: on a pt_BR machine every assertion below read
// Portuguese and failed. The checks are written in English, so pin it.
const chromeEnv = { ...process.env, LANG: 'en_US.UTF-8', LANGUAGE: 'en_US', LC_ALL: 'en_US.UTF-8' };

function runChrome(chrome, url, width = 1280, height = 800) {
  return new Promise((resolve, reject) => {
    const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--lang=en-US', `--window-size=${width},${height}`, '--virtual-time-budget=1800', '--dump-dom', url], { stdio: ['ignore', 'pipe', 'pipe'], env: chromeEnv });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (data) => { stdout += data; });
    child.stderr.on('data', (data) => { stderr += data; });
    child.on('error', reject);
    child.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function freePort() {
  const probe = createServer();
  await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve));
  const { port } = probe.address();
  await new Promise((resolve) => probe.close(resolve));
  return port;
}

async function runMobileChrome(chrome, url, { width = 375, flow } = {}) {
  const port = await freePort();
  const profile = await mkdtemp(path.join(os.tmpdir(), 'openrmm-smoke-'));
  const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--lang=en-US', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'], env: chromeEnv });
  let chromeErrors = '';
  child.stderr.on('data', (data) => { chromeErrors += data; });
  let socket;
  try {
    let ready = false;
    for (let attempt = 0; attempt < 50; attempt++) {
      try { ready = (await fetch(`http://127.0.0.1:${port}/json/version`)).ok; } catch {}
      if (ready) break;
      await delay(50);
    }
    assert.ok(ready, `Chrome DevTools did not start: ${chromeErrors}`);
    const target = await (await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' })).json();
    socket = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { socket.once('open', resolve); socket.once('error', reject); });
    let commandId = 0;
    const pending = new Map();
    const exceptions = [];
    socket.on('message', (raw) => {
      const message = JSON.parse(raw.toString());
      if (message.id && pending.has(message.id)) {
        const { resolve, reject } = pending.get(message.id);
        pending.delete(message.id);
        if (message.error) reject(new Error(message.error.message)); else resolve(message.result);
      } else if (message.method === 'Runtime.exceptionThrown') {
        exceptions.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text);
      }
    });
    const command = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++commandId;
      pending.set(id, { resolve, reject });
      socket.send(JSON.stringify({ id, method, params }));
    });
    await command('Page.enable');
    await command('Runtime.enable');
    await command('Emulation.setDeviceMetricsOverride', { width, height: 812, deviceScaleFactor: 1, mobile: width === 375 });
    await command('Page.navigate', { url });
    await delay(250);
    const initialWidth = await command('Runtime.evaluate', { expression: 'innerWidth', returnByValue: true });
    if (initialWidth.result.value !== width) {
      await command('Emulation.setDeviceMetricsOverride', { width: 2 * width - initialWidth.result.value, height: 812, deviceScaleFactor: 1, mobile: width === 375 });
    }
    await delay(1750);
    if (flow) {
      const result = await command('Runtime.evaluate', { expression: `(${flow.toString()})()`, returnByValue: true, awaitPromise: true });
      assert.equal(result.exceptionDetails, undefined, JSON.stringify(result.exceptionDetails));
      assert.equal(result.result.value, true, 'browser rule flow did not finish');
    }
    const evaluated = await command('Runtime.evaluate', { expression: 'document.documentElement.outerHTML', returnByValue: true });
    return { code: 0, stdout: evaluated.result.value, stderr: exceptions.join('\n') };
  } finally {
    if (socket?.readyState === WebSocket.OPEN) socket.close();
    child.kill('SIGTERM');
    await new Promise((resolve) => child.once('close', resolve));
    await rm(profile, { recursive: true, force: true });
  }
}

// Actual browser interactions against the bundled console and HTTP fixture.
// This script starts listeners and is run only by the CI smoke step.
async function alertRulesBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) {
      const result = fn();
      if (result) return result;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error('Alert rule E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const choose = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Checks'))).click();
  (await wait(() => button('New check'))).click();
  const type = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'connectivity')));
  choose(type, 'connectivity');
  (await wait(() => button('Select target…'))).click();
  (await wait(() => button('SMOKE-PC'))).click();
  const mode = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'ignore')));
  choose(mode, 'ignore');
  (await wait(() => button('Create check') && !button('Create check').disabled && button('Create check'))).click();
  await wait(() => !button('Create check'));
  (await wait(() => button('Fleet defaults'))).click();
  const delayInput = await wait(() => document.querySelector('input[max="10080"]'));
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(delayInput, '30');
  delayInput.dispatchEvent(new Event('input', { bubbles: true }));
  delayInput.dispatchEvent(new Event('change', { bubbles: true }));
  (await wait(() => button('Save'))).click();
  await wait(() => !document.querySelector('input[max="10080"]'));
  (await wait(() => button('Users & Settings'))).click();
  const card = await wait(() => [...document.querySelectorAll('div')].find((el) => el.textContent.trim() === 'Email notifications')?.closest('.max-w-2xl'));
  const ruleMode = await wait(() => card.querySelector('select'));
  choose(ruleMode, 'selected');
  const labels = await wait(() => card.querySelectorAll('label').length > 1 && card.querySelectorAll('label'));
  const defaults = [...labels].find((el) => el.textContent.includes('Include built-in alerts'));
  defaults.querySelector('button').click();
  const rule = [...labels].find((el) => el.textContent.includes('Device connectivity'));
  if (!rule) throw new Error('Created rule missing from email preferences');
  rule.querySelector('button').click();
  const list = await wait(() => document.getElementById('notification-custom-rules'));
  const rows = list.querySelectorAll('label');
  if (rows.length < 60 || list.scrollHeight <= list.clientHeight) throw new Error('Large rule list must scroll');
  if (list.clientHeight > rows[0].getBoundingClientRect().height * 5 + 2) throw new Error('More than five rule rows visible');
  const search = card.querySelector('input[aria-controls="notification-custom-rules"]');
  const filter = (value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(search, value);
    search.dispatchEvent(new Event('input', { bubbles: true }));
    search.dispatchEvent(new Event('change', { bubbles: true }));
  };
  filter('MONITOR 60');
  await wait(() => list.querySelectorAll('label').length === 1);
  list.querySelector('label button').click();
  filter('no matching monitor');
  await wait(() => list.querySelectorAll('label').length === 0);
  filter('');
  await wait(() => list.querySelectorAll('label').length > 60);
  (await wait(() => button('Save', card))).click();
  await wait(() => document.body.textContent.includes('Notification settings saved'));
  document.documentElement.dataset.alertRulesE2e = 'passed';
  return true;
}

async function scriptVariablesBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Script variables E2E timed out: ' + fn.toString());
  };
  const button = (text, root = document) => [...root.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const change = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Scripts'))).click();
  (await wait(() => document.getElementById('scripts-tab-variables'))).click();
  (await wait(() => button('New variable'))).click();
  const definition = await wait(() => document.querySelector('[role="dialog"][aria-label="Script variables"]'));
  change(definition.querySelector('input[aria-label="Name"]'), 'customerKey');
  definition.querySelector('input[type="checkbox"]').click();
  (await wait(() => button('Save', definition) && !button('Save', definition).disabled && button('Save', definition))).click();
  await wait(() => !document.contains(definition));
  // 200 variables: a bounded scroll area, a counter, search and the secret filter.
  const list = await wait(() => document.getElementById('script-variables-list'));
  await wait(() => list.querySelectorAll('button[aria-expanded]').length === 200);
  if (list.scrollHeight <= list.clientHeight) throw new Error('The variables list must scroll');
  if (!document.body.textContent.includes('200 of 200 variables')) throw new Error('Variable counter missing');
  change(document.querySelector('select[aria-label="Filter variables"]'), 'secret');
  await wait(() => document.getElementById('script-variables-list')?.querySelectorAll('button[aria-expanded]').length === 101);
  change(document.querySelector('select[aria-label="Filter variables"]'), 'all');
  change(document.querySelector('input[aria-label="Search variables…"]'), 'CUSTOMERKEY');
  await wait(() => document.getElementById('script-variables-list')?.querySelectorAll('button[aria-expanded]').length === 1);
  // Values open on demand.
  const row = document.getElementById('script-variables-list').querySelector('button[aria-expanded]');
  if (row.getAttribute('aria-expanded') !== 'false') throw new Error('Values must start closed');
  row.click();
  (await wait(() => button('Set a value'))).click();
  const dialog = await wait(() => document.querySelector('[role="dialog"][aria-label="Value"]'));
  const input = dialog.querySelector('input[type="password"]');
  if (!input || input.value !== '') throw new Error('Secret must start blank in a password field');
  change(input, 'q7X');
  (await wait(() => button('Save', dialog) && !button('Save', dialog).disabled && button('Save', dialog))).click();
  await wait(() => !document.contains(dialog) && document.body.textContent.includes('Stored (hidden)'));
  if (document.body.textContent.includes('q7X')) throw new Error('Secret rendered after save');
  document.getElementById('scripts-tab-library').click();
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Variable installer')))).click();
  // The editor's picker: bounded to five rows, searchable, selection shown as a chip.
  const picker = await wait(() => document.getElementById('script-variable-picker'));
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 200);
  if (picker.scrollHeight <= picker.clientHeight) throw new Error('The variable picker must scroll');
  if (picker.clientHeight > picker.querySelector('label').getBoundingClientRect().height * 5 + 2) throw new Error('More than five picker rows visible');
  change(document.querySelector('input[aria-controls="script-variable-picker"]'), 'customerkey');
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 1);
  const label = await wait(() => [...picker.querySelectorAll('label')].find((el) => el.textContent.includes('customerKey') && el.querySelector('input[type="checkbox"]')));
  label.querySelector('input').click();
  change(document.querySelector('input[aria-controls="script-variable-picker"]'), 'nothing like this');
  await wait(() => picker.querySelectorAll('input[type="checkbox"]').length === 0);
  await wait(() => [...document.querySelectorAll('button')].some((el) => el.getAttribute('aria-label') === 'Remove: customerKey'));
  (await wait(() => button('Save script'))).click();
  await wait(() => !button('Save script'));
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Variable installer') && el.textContent.includes('Script variables')))).click();
  (await wait(() => button('Run on device'))).click();
  const device = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'dev-1')));
  change(device, 'dev-1');
  (await wait(() => button('Create job') && !button('Create job').disabled && button('Create job'))).click();
  await wait(() => !button('Create job'));
  document.documentElement.dataset.scriptVariablesE2e = 'passed'; return true;
}

async function servicesAndRemoteSettingsBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const value = fn(); if (value) return value; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Services/settings E2E timed out: ' + fn.toString());
  };
  const button = (text) => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === text);
  const change = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  (await wait(() => button('Checks'))).click();
  for (const [kind, service] of [['linuxservice', 'sshd.service'], ['macservice', 'system/com.apple.notifyd']]) {
    (await wait(() => button('New check'))).click();
    const type = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === kind)));
    change(type, kind);
    const input = await wait(() => document.querySelector('input[placeholder="' + service + '"]'));
    change(input, service);
    if (kind === 'macservice') {
      const state = await wait(() => [...document.querySelectorAll('select')].find((el) => [...el.options].some((o) => o.value === 'loaded')));
      change(state, 'loaded');
    }
    (await wait(() => button('Select target…'))).click();
    (await wait(() => button('SMOKE-PC'))).click();
    (await wait(() => button('Create check') && !button('Create check').disabled && button('Create check'))).click();
    await wait(() => !button('Create check'));
  }
  (await wait(() => button('Users & Settings'))).click();
  (await wait(() => button('Configuration'))).click();
  const tab = (key) => document.getElementById('remote-config-tab-' + key);
  const panel = () => document.querySelector('[role="tabpanel"]:not([hidden])');
  await wait(() => tab('native'));
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  assert(panel().id === 'remote-config-panel-native', 'Native is the initial provider tab');
  assert(!panel().querySelector('input[type="password"]'), 'Native does not show MeshCentral credentials');
  tab('meshcentral').click();
  await wait(() => panel().id === 'remote-config-panel-meshcentral');
  change(panel().querySelector('input[placeholder="https://mesh.example.com"]'), 'https://updated-mesh.example.invalid');
  change(panel().querySelector('input[type="password"]'), 'e2e-token-placeholder');
  tab('rustdesk').click();
  await wait(() => panel().id === 'remote-config-panel-rustdesk');
  change(panel().querySelector('select'), 'on');
  tab('default').click();
  await wait(() => panel().id === 'remote-config-panel-default');
  change(panel().querySelector('select'), 'rustdesk');
  tab('native').click();
  await wait(() => panel().id === 'remote-config-panel-native');
  change(panel().querySelector('select'), 'on');
  tab('meshcentral').click();
  await wait(() => panel().id === 'remote-config-panel-meshcentral');
  assert(panel().querySelector('input[type="password"]').value === 'e2e-token-placeholder', 'Unsaved token survives tab navigation');
  tab('meshcentral').focus();
  tab('meshcentral').dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }));
  await wait(() => panel().id === 'remote-config-panel-default');
  assert(document.activeElement === tab('default'), 'Keyboard moves focus to selected provider');
  (await wait(() => button('Save configuration'))).click();
  await wait(() => document.body.textContent.includes('Configuration saved'));
  document.documentElement.dataset.servicesSettingsE2e = 'passed';
  return true;
}

async function auditCsvBrowserFlow() {
  const wait = async (fn) => {
    for (let i = 0; i < 150; i++) { const v = fn(); if (v) return v; await new Promise((r) => setTimeout(r, 50)); }
    throw new Error('Audit CSV flow timed out');
  };
  (await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.trim() === 'Audit log'))).click();
  const exportButton = await wait(() => [...document.querySelectorAll('button')].find((el) => el.textContent.includes('Export CSV') && !el.disabled));
  const original = URL.createObjectURL;
  let download;
  URL.createObjectURL = (blob) => { download = blob.text(); return original.call(URL, blob); };
  try {
    exportButton.click();
    await wait(() => download);
    const csv = await download;
    if (!csv.includes('"\'=1+1","alert-target-1"') || !csv.includes('"ORDINARY-HOST","alert-target-2"')) throw new Error('CSV guard or legitimate target missing');
    document.documentElement.dataset.auditCsvE2e = 'passed';
    return true;
  } finally { URL.createObjectURL = original; }
}

function assertBrowser(result, pattern) {
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, pattern, result.stderr);
  assert.doesNotMatch(result.stderr, /Content Security Policy.*(?:refused|violat)|(?:refused|violat).*Content Security Policy/i);
}

function assertMobile(result, pattern) {
  assertBrowser(result, pattern);
  assert.match(result.stdout, /data-smoke-width="(\d+):(\d+)"/);
  const [, viewportWidth, scrollWidth] = result.stdout.match(/data-smoke-width="(\d+):(\d+)"/);
  assert.equal(Number(viewportWidth), 375, 'mobile smoke must use a 375px CSS viewport');
  assert.ok(Number(scrollWidth) <= Number(viewportWidth) + 1, `mobile page overflows horizontally: ${scrollWidth} > ${viewportWidth}`);
}

try {
  const response = await fetch(baseURL + '/');
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.doesNotMatch(html, /https?:\/\/(?:cdn|unpkg|fonts\.)/i);
  for (const asset of ['assets/app.js', 'assets/app.css', 'assets/styles.css', 'assets/logo_open_rmm_edit3.png']) {
    const assetResponse = await fetch(`${baseURL}/${asset}`);
    assert.equal(assetResponse.status, 200, `${asset} must be served`);
    assert.notEqual(assetResponse.headers.get('content-type'), 'text/html', `${asset} must not fall back to HTML`);
  }
  assert.equal((await fetch(baseURL + '/assets/missing.js')).status, 404, 'missing assets must return 404');

  const candidates = [process.env.CHROME_BIN, 'google-chrome', 'chromium', 'chromium-browser'].filter(Boolean);
  const chrome = candidates.find((candidate) => spawnSync(candidate, ['--version'], { stdio: 'ignore' }).status === 0);
  if (process.env.OPENRMM_REQUIRE_BROWSER === '1') assert.ok(chrome, 'CI browser E2E requires Chrome');
  if (chrome) {
    assertBrowser(await runChrome(chrome, baseURL + '/'), /Sign in to your instance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/'), /Fleet health at a glance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=Devices'), /1 agent · 1 online/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    const mobile = await runMobileChrome(chrome, baseURL + '/authenticated/?route=Open%20navigation');
    assertMobile(mobile, /smoke-admin/);
    assert.match(mobile.stdout, /translate-x-0/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=Devices'), /1 agent · 1 online/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    assertBrowser(await runChrome(chrome, baseURL + '/expired/'), /Sign in to your instance/);
    const rules = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: alertRulesBrowserFlow });
    assertBrowser(rules, /data-alert-rules-e2e="passed"/);
    const created = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/checks');
    assert.equal(created?.payload.type, 'connectivity');
    assert.equal(created?.payload.config.mode, 'ignore');
    assert.equal(created?.payload.enabled, true);
    assert.equal(ruleDefaults.connectivity.config.after_min, 30);
    assert.deepEqual(rulePrefs, { mode: 'selected', include_defaults: false, check_ids: ['rule-e2e', 'sample-rule-60'] });
    const settingsFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: servicesAndRemoteSettingsBrowserFlow });
    assertBrowser(settingsFlow, /data-services-settings-e2e="passed"/);
    for (const [type, service, state] of [['linuxservice', 'sshd.service', undefined], ['macservice', 'system/com.apple.notifyd', 'loaded']]) {
      const body = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/checks' && r.payload?.type === type)?.payload;
      assert.ok(body, type + ' was submitted');
      assert.equal(body.config.service, service);
      assert.equal(body.config.state, state);
      assert.equal('warn_threshold' in body, false);
    }
    const saved = apiRequests.find((r) => r.method === 'PUT' && r.pathname === '/api/config')?.payload;
    assert.equal(saved?.remote.default, 'rustdesk');
    assert.equal(saved?.remote.meshcentral_url, 'https://updated-mesh.example.invalid');
    assert.equal(saved?.remote.meshcentral_token_key, 'e2e-token-placeholder');
    assert.equal(saved?.remote.rustdesk_enabled, true);
    assert.equal(saved?.remote.guest_access, true);
    assert.equal(saved?.jobs.allow_unapproved, false);
    assert.equal(remoteConfig.remote.default, 'rustdesk');
    assert.equal(remoteConfig.remote.rustdesk_enabled, true);
    assert.equal('meshcentral_token_key' in remoteConfig.remote, false, 'GET config stays write-only');
    const variablesFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: scriptVariablesBrowserFlow });
    assertBrowser(variablesFlow, /data-script-variables-e2e="passed"/);
    assert.equal(scriptVariables[0]?.name, 'customerKey');
    assert.equal(scriptVariables[0]?.secret, true);
    const variableValue = apiRequests.find((r) => r.method === 'PUT' && r.pathname === '/api/script-variables/variable-e2e/values');
    assert.equal(variableValue?.payload.value, 'q7X', 'short secrets must be submitted without rewriting');
    assert.equal('value' in scriptVariables[0].values[0], false, 'secret read-back is write-only');
    assert.deepEqual(libraryScripts[0].variables, ['customerKey']);
    const variableJob = apiRequests.find((r) => r.method === 'POST' && r.pathname === '/api/devices/dev-1/run-script');
    assert.deepEqual(variableJob?.payload, { script_id: 'script-e2e', params: {} });
    const csvFlow = await runMobileChrome(chrome, baseURL + '/authenticated/', { width: 1280, flow: auditCsvBrowserFlow });
    assertBrowser(csvFlow, /data-audit-csv-e2e="passed"/);
    assert.ok(apiRequests.some((request) => request.pathname === '/api/me' && request.token === 'smoke-token'), 'authenticated API requests must include the bearer token');
    assert.ok(apiRequests.some((request) => request.pathname === '/api/me' && request.token === 'expire-token'), '401 scenario must exercise an authenticated request');
  }
  console.log(`Smoke passed${chrome ? ` in ${chrome}` : ' (static checks; browser unavailable)'}.`);
} finally {
  await new Promise((resolve) => server.close(resolve));
}
