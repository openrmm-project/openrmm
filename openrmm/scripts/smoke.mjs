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
const permissions = ['ai_chat.use', 'alerts.view', 'alerts.manage', 'jobs.view', 'jobs.create', 'jobs.manage', 'checks.view', 'checks.manage', 'tasks.view', 'tasks.manage', 'policies.view', 'policies.manage', 'scripts.view', 'scripts.manage', 'patches.view', 'patches.manage', 'patches.policies.manage', 'devices.view', 'devices.manage', 'devices.delete', 'devices.remote', 'devices.files', 'devices.terminal', 'clients.view', 'clients.manage', 'reports.view', 'audit.view', 'settings.manage', 'enrollment.manage', 'api_tokens.manage', 'users.manage', 'roles.manage'];
const user = { id: 'user-1', username: 'smoke-admin', role: 'admin', permissions, created_at: '2026-01-01T00:00:00Z' };
const apiRequests = [];

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
      apiRequests.push({ method: req.method, pathname, token });
      if (!token) return json(res, 401, { error: 'missing bearer token' });
      if (token === 'expire-token' && pathname === '/api/devices') return json(res, 401, { error: 'expired token' });
      if (pathname === '/api/me') return json(res, 200, user);
      if (pathname === '/api/devices') return json(res, 200, { devices: [{ id: 'dev-1', hostname: 'SMOKE-PC', description: 'Smoke device', os: 'Linux', arch: 'amd64', version: '1.0.0', status: 'online', tags: [], last_seen: '2026-01-01T00:00:00Z' }] });
      if (pathname === '/api/jobs') return json(res, 200, { jobs: [] });
      if (pathname === '/api/audit') return json(res, 200, { entries: [] });
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

function runChrome(chrome, url, width = 1280, height = 800) {
  return new Promise((resolve, reject) => {
    const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', `--window-size=${width},${height}`, '--virtual-time-budget=1800', '--dump-dom', url], { stdio: ['ignore', 'pipe', 'pipe'] });
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

async function runMobileChrome(chrome, url) {
  const port = await freePort();
  const profile = await mkdtemp(path.join(os.tmpdir(), 'openrmm-smoke-'));
  const child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
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
    await command('Emulation.setDeviceMetricsOverride', { width: 375, height: 812, deviceScaleFactor: 1, mobile: true });
    await command('Page.navigate', { url });
    await delay(250);
    const initialWidth = await command('Runtime.evaluate', { expression: 'innerWidth', returnByValue: true });
    if (initialWidth.result.value !== 375) {
      await command('Emulation.setDeviceMetricsOverride', { width: 750 - initialWidth.result.value, height: 812, deviceScaleFactor: 1, mobile: true });
    }
    await delay(1750);
    const evaluated = await command('Runtime.evaluate', { expression: 'document.documentElement.outerHTML', returnByValue: true });
    return { code: 0, stdout: evaluated.result.value, stderr: exceptions.join('\n') };
  } finally {
    if (socket?.readyState === WebSocket.OPEN) socket.close();
    child.kill('SIGTERM');
    await new Promise((resolve) => child.once('close', resolve));
    await rm(profile, { recursive: true, force: true });
  }
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
  if (chrome) {
    assertBrowser(await runChrome(chrome, baseURL + '/'), /Sign in to your instance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/'), /Fleet health at a glance/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=Devices'), /1 agents · 1 online/);
    assertBrowser(await runChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    const mobile = await runMobileChrome(chrome, baseURL + '/authenticated/?route=Open%20navigation');
    assertMobile(mobile, /smoke-admin/);
    assert.match(mobile.stdout, /translate-x-0/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=Devices'), /1 agents · 1 online/);
    assertMobile(await runMobileChrome(chrome, baseURL + '/authenticated/?route=AI%20Chat'), /Start a conversation/);

    assertBrowser(await runChrome(chrome, baseURL + '/expired/'), /Sign in to your instance/);
    assert.ok(apiRequests.some((request) => request.pathname === '/api/me' && request.token === 'smoke-token'), 'authenticated API requests must include the bearer token');
    assert.ok(apiRequests.some((request) => request.pathname === '/api/devices' && request.token === 'expire-token'), '401 scenario must exercise an authenticated request');
  }
  console.log(`Smoke passed${chrome ? ` in ${chrome}` : ' (static checks; browser unavailable)'}.`);
} finally {
  await new Promise((resolve) => server.close(resolve));
}
