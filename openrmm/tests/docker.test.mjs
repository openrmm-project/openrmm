import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';

const source = await readFile(new URL('../src/screens-docker.jsx', import.meta.url), 'utf8');
const pure = source.slice(0, source.indexOf('function useDockerQuery'));
const sandbox = { Intl, t:(key)=>key, getLocale:()=> 'en' };
vm.createContext(sandbox); vm.runInContext(pure, sandbox);

test('Docker filtering combines exact state/health and case-insensitive metadata', () => {
  const c = {name:'Web',image:'example:v1',state:'running',health:'none',compose_project:'My Project'};
  assert.equal(sandbox.dockerMatches(c,' project ','running','none'),true);
  assert.equal(sandbox.dockerMatches(c,'web','exited',''),false);
  assert.equal(sandbox.dockerMatches(c,'web','','healthy'),false);
  assert.equal(sandbox.dockerMetricBytes(null),'docker.noSample');
  assert.equal(sandbox.dockerMetricBytes(0),'0 B');
  assert.equal(sandbox.dockerMetricBytes(2097152),'2 MiB');
});

test('Docker polling rejects older responses, hides data after HTTP denial and ignores unmounted requests', async () => {
  const callbacks = []; const requests = []; let state; let cleanup;
  const context = { useRef:v=>({current:v}), useState:v=>[v,s=>state=s], useEffect:fn=>cleanup=fn(), setInterval:fn=>{callbacks.push(fn);return 1;}, clearInterval:()=>{} };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('function useDockerQuery'),source.indexOf('function DockerCollection')),context);
  const fetcher = () => new Promise((resolve,reject)=>requests.push({resolve,reject}));
  context.useDockerQuery('one',fetcher);
  callbacks[0]();
  requests[1].resolve({name:'new'}); await new Promise(r=>setImmediate(r));
  requests[0].resolve({name:'old'}); await new Promise(r=>setImmediate(r));
  assert.equal(state.data.name,'new');
  callbacks[0](); requests[2].reject(new Error('Forbidden')); await new Promise(r=>setImmediate(r));
  assert.equal(state.data,undefined); assert.equal(state.error.message,'Forbidden');
  callbacks[0](); cleanup(); requests[3].resolve({name:'after unmount'}); await new Promise(r=>setImmediate(r));
  assert.equal(state.data,undefined);
});

test('all nine locales provide Docker keys with identical placeholders', async () => {
  const entries = async locale => {
    const text = await readFile(new URL(`../src/locales/${locale}.jsx`,import.meta.url),'utf8');
    return Object.fromEntries([...text.matchAll(/"((?:docker\.|check\.type\.dockercontainer|check\.cfg\.container|nav\.docker|title\.docker)[^"]*)":\s*("(?:[^"\\]|\\.)*")/g)].map(m=>[m[1],JSON.parse(m[2])]));
  };
  const english = await entries('en'); assert.ok(Object.keys(english).length>50);
  for(const locale of ['de','es','ja','ko','ms','pt-BR','ru','zh-CN']) {
    const translated = await entries(locale); assert.deepEqual(Object.keys(translated).sort(),Object.keys(english).sort(),locale);
    for(const key of Object.keys(english)) assert.deepEqual(translated[key].match(/\{\w+\}/g),english[key].match(/\{\w+\}/g),locale+': '+key);
  }
});


test('Docker empty view distinguishes missing observations, collection errors and genuinely empty engines', () => {
  const state = (hosts, filtered=false) => Array.from(sandbox.dockerEmptyState(hosts,filtered));
  const unknown = {status:'unknown'};
  assert.deepEqual(state([unknown]),['docker.awaitingData','docker.awaitingDataHint']);
  assert.equal(state([unknown],true)[0],'docker.empty');
  assert.equal(state([{status:'unavailable',error_code:'engine_unavailable'}])[0],'docker.collectionUnavailable');
  assert.equal(state([{status:'not_installed'},unknown])[0],'docker.collectionUnavailable');
  assert.equal(state([{status:'available',last_success_at:'2026-11-20T14:00:00Z'}])[0],'docker.noContainers');
  assert.equal(state([unknown,{status:'available'}])[0],'docker.noContainers');
  assert.equal(state([])[0],'docker.noContainers');
});
