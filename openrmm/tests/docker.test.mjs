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

const now = Date.parse('2026-11-20T15:00:00Z');
const checked_at = new Date(now).toISOString();
const report = (update = {}) => ({name:'web',state:'running',health:'healthy',update:{checked_at,status:'up_to_date',version_status:'not_found',...update}});

test('Docker update filtering distinguishes same-tag images and numbered versions', () => {
  const sameTag = report({status:'update_available'});
  const newer = report({version_status:'available',latest_tag:'2.0.0',version_kind:'major'});
  assert.equal(sandbox.dockerUpdateInfo(sameTag,false,now).filter,'available');
  const versionInfo = sandbox.dockerUpdateInfo(newer,false,now);
  assert.equal(versionInfo.filter,'available'); assert.equal(versionInfo.sameTag,false); assert.equal(versionInfo.major,true);
  assert.equal(sandbox.dockerMatches(newer,'web','running','healthy','available',false,now),true);
  assert.equal(sandbox.dockerMatches(newer,'web','exited','healthy','available',false,now),false);
  assert.equal(sandbox.dockerMatches(newer,'web','running','healthy','current',false,now),false);
  for (const version_status of ['not_found','unsupported']) assert.equal(sandbox.dockerUpdateInfo(report({version_status}),false,now).filter,'current');
  assert.equal(sandbox.dockerUpdateInfo(report({status:'unknown',version_status:'available',latest_tag:'1.2.3',version_error_code:'tag_search_incomplete'}),false,now).filter,'available');
  const pinned = sandbox.dockerUpdateInfo(report({status:'pinned',version_status:'available',latest_tag:'17',version_kind:'major'}),false,now);
  assert.equal(pinned.filter,'available'); assert.equal(pinned.sameTag,false); assert.equal(pinned.newerVersion,true); assert.equal(pinned.major,true);
});

test('Missing, incomplete, pinned and unsupported update observations never imply current', () => {
  for (const container of [{}, {update:{}}, report({status:'pinned'}), report({status:'unsupported'}), report({version_status:undefined}), report({version_status:'unknown'}), report({version_status:'available',latest_tag:undefined}), report({status:'unknown'})]) {
    assert.equal(sandbox.dockerUpdateInfo(container,false,now).filter,'unknown',JSON.stringify(container));
  }
  assert.equal(sandbox.dockerUpdateInfo({},false,now).missing,true);
});

test('Stale hosts, expired checks and invalid times retain observations but filter as unknown', () => {
  const candidate = report({status:'update_available',version_status:'available',latest_tag:'3.0',version_kind:'major'});
  const old = {...candidate,update:{...candidate.update,checked_at:new Date(now - 26*60*60*1000 - 1).toISOString()}};
  for (const [container, hostStale] of [[candidate,true],[{...candidate,stale:true},false],[old,false],[report({checked_at:'bad'}),false],[report({checked_at:undefined}),false],[report({checked_at:new Date(now+300001).toISOString()}),false]]) {
    assert.equal(sandbox.dockerUpdateInfo(container,hostStale,now).filter,'unknown');
    assert.equal(sandbox.dockerMatches(container,'','','','available',hostStale,now),false);
    assert.equal(sandbox.dockerMatches(container,'','','','unknown',hostStale,now),true);
  }
  assert.equal(sandbox.dockerUpdateInfo(old,false,now).newerVersion,true);
  assert.equal(sandbox.dockerUpdateInfo(old,false,now).sameTag,true);
  assert.equal(sandbox.dockerUpdateInfo(report({checked_at:new Date(now-26*60*60*1000).toISOString()}),false,now).filter,'current');
  assert.equal(sandbox.dockerUpdateInfo(report({checked_at:new Date(now+300000).toISOString()}),false,now).filter,'current');
});

test('Update cells render separate observations, migration guidance and qualified search results', async () => {
  const [{transform}, {default:React}, {renderToStaticMarkup}] = await Promise.all([import('esbuild'),import('react'),import('react-dom/server')]);
  const englishSource = await readFile(new URL('../src/locales/en.jsx',import.meta.url),'utf8');
  const english = Object.fromEntries([...englishSource.matchAll(/"(docker\.[^"]*)":\s*("(?:[^"\\]|\\.)*")/g)].map(m=>[m[1],JSON.parse(m[2])]));
  const context = {React,window:{},Intl,Date,t:(key,params={})=>(english[key] || key).replace('{when}',params.when || ''),getLocale:()=> 'en',timeAgo:()=> 'just now'};
  vm.createContext(context); vm.runInContext((await transform(source,{loader:'jsx'})).code,context);
  const render = (update,stale=false) => renderToStaticMarkup(React.createElement(context.DockerUpdate,{container:{update:update && {checked_at:new Date().toISOString(),...update}},stale}));
  const both = render({status:'update_available',version_status:'available',latest_tag:'2.0.0',version_kind:'major',version_error_code:'tag_search_incomplete'});
  assert.match(both,/Same tag: new image/); assert.match(both,/Newer version found/); assert.match(both,/2\.0\.0/); assert.match(both,/Major version: review migration requirements/); assert.match(both,/Only some tags were checked/);
  assert.match(both,/data-docker-update="available"/); assert.doesNotMatch(both,/<button|latest version/i);
  const old = render({status:'up_to_date',version_status:'available',latest_tag:'2.1',version_kind:'minor'},true);
  assert.match(old,/data-docker-update="unknown"/); assert.match(old,/Previous result/); assert.match(old,/2\.1/); assert.doesNotMatch(old,/text-ok|Major version/);
  const missing = render(undefined); assert.match(missing,/Awaiting a check or an agent/); assert.doesNotMatch(missing,/Same tag: current/);
  const partial = render({status:'up_to_date',version_status:'unknown'}); assert.match(partial,/data-docker-update="unknown"/); assert.match(partial,/incomplete or unavailable/);
  const unsupported = render({status:'unsupported',error_code:'registry_unsupported',version_status:'unsupported'}); assert.match(unsupported,/This registry is not supported/);
  const privateImage = render({status:'unknown',error_code:'authentication_required',version_status:'unknown'}); assert.match(privateImage,/Registry access is required or was denied/);
});


test('Untimed pinned and unsupported reports are unverified, not previous checks', async () => {
  for (const status of ['pinned','unsupported']) {
    const info = sandbox.dockerUpdateInfo({update:{status,version_status:'unsupported'}},false,now);
    assert.equal(info.filter,'unknown'); assert.equal(info.stale,false); assert.equal(info.fresh,false); assert.equal(info.checkedAt,null);
  }
  for (const checked_at of [undefined,'invalid','2026-11-20T16:00:00Z']) {
    const info = sandbox.dockerUpdateInfo(report({status:'update_available',version_status:'available',latest_tag:'2.0.0',checked_at}),false,now);
    assert.equal(info.filter,'unknown'); assert.equal(info.stale,false); assert.equal(info.sameTag,false); assert.equal(info.newerVersion,false);
  }
  const [{transform}, {default:React}, {renderToStaticMarkup}] = await Promise.all([import('esbuild'),import('react'),import('react-dom/server')]);
  const context = {React,window:{},Intl,Date,t:key=>key,getLocale:()=> 'en',timeAgo:()=> 'just now'};
  vm.createContext(context); vm.runInContext((await transform(source,{loader:'jsx'})).code,context);
  const render = update => renderToStaticMarkup(React.createElement(context.DockerUpdate,{container:{update}}));
  for (const status of ['pinned','unsupported']) {
    const html = render({status,version_status:'unsupported'});
    assert.match(html,new RegExp('docker.update.status.'+status)); assert.match(html,/docker.update.neverChecked/); assert.doesNotMatch(html,/docker.update.stale/);
  }
  for (const status of ['up_to_date','update_available']) {
    const html = render({status,version_status:'available',latest_tag:'2.0.0'});
    assert.match(html,/docker.update.status.unknown/); assert.match(html,/docker.update.version.unknown/);
    assert.doesNotMatch(html,/docker.update.status.up_to_date|docker.update.status.update_available|2\.0\.0|text-ok|docker.update.stale/);
  }
});
