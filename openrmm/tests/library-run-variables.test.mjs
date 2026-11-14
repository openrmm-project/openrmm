import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

// A library script's script variables and parameters reach the device only when
// the saved script itself runs, by id: the server reads which names it receives
// from what is stored. A copy of its text carries none of that, and only the
// built-in RMM_* context arrives. So the device page runs a saved script it was
// filled from by id while unedited, says so once it is edited, and the script
// editor runs the saved version only (no unsaved edit is lost silently).
const devices = await readFile(new URL('../src/screens-devices.jsx', import.meta.url), 'utf8');
const rmm2 = await readFile(new URL('../src/screens-rmm2.jsx', import.meta.url), 'utf8');
const form = devices.slice(devices.indexOf('function RunScriptForm('), devices.indexOf('function DevicePatches('));

const variables = [{ id: 'v1', name: 'HUNTRESS_ACCOUNT_KEY', secret: true }, { id: 'v2', name: 'SITE_CODE', secret: false }, { id: 'v3', name: 'PROPOSED_KEY', pending: true }];
// Who may run jobs but not bind variables to scripts.
const runner = ['jobs.create', 'jobs.manage', 'settings.manage', 'scripts.view'];
const scripts = [
  { id: 's-vars', name: 'Huntress install', shell: 'powershell', body: 'Write-Host $env:HUNTRESS_ACCOUNT_KEY', params: [], variables: ['HUNTRESS_ACCOUNT_KEY'] },
  { id: 's-params', name: 'Restart service', shell: 'bash', body: 'systemctl restart "$OPENRMM_PARAM_SERVICE"', params: [{ name: 'SERVICE', kind: 'pattern', pattern: '^[a-z]+$' }], variables: [] },
  { id: 's-plain', name: 'Uptime', shell: 'bash', body: 'uptime', params: [], variables: [] },
];

function harness({ allowUnapproved = false, perms } = {}) {
  const states = [], calls = []; let cursor = 0;
  const sandbox = {
    React: { createElement(type, props, ...children) { return typeof type === 'function' ? type({ ...props, children }) : { type, props: props || {}, children }; } },
    useState(initial) { const i = cursor++; if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial; return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }]; },
    useAuth: () => ({ can: (p) => !perms || perms.includes(p) }),
    useToast: () => ({ success() {}, info(...a) { calls.push({ name: 'toast.info', args: a }); }, error(...a) { calls.push({ name: 'toast.error', args: a }); } }),
    useQuery: (key) => ({ data: key[0] === 'scripts-pick' ? { scripts } : key[0] === 'script-variables' ? { variables } : { jobs: { allow_unapproved: allowUnapproved } } }),
    t: (key, vars) => (vars ? key + ' ' + JSON.stringify(vars) : key),
    cx: (...v) => v.filter(Boolean).join(' '),
    api: Object.fromEntries(['createJob', 'runLibraryScript'].map((name) => [name, async (...args) => { calls.push({ name, args: JSON.parse(JSON.stringify(args)) }); return {}; }])),
    ...Object.fromEntries(['Field', 'Select', 'Input', 'Button', 'CodeEditor', 'Icon', 'EmptyState', 'Combobox'].map((n) => [n, n])),
  };
  vm.createContext(sandbox);
  vm.runInContext(transformSync(form, { loader: 'jsx' }).code, sandbox);
  let tree;
  const device = { id: 'dev-1', hostname: 'PC-01', os: 'windows' };
  const render = () => { cursor = 0; tree = sandbox.RunScriptForm({ device, onCreated() {} }); return tree; };
  const find = (pred, node = tree, out = []) => { if (!node || typeof node !== 'object') return out; if (pred(node)) out.push(node); (node.children || []).flat(Infinity).forEach((c) => find(pred, c, out)); return out; };
  const text = (node) => (typeof node === 'string' ? node : (node?.children || []).flat(Infinity).map(text).join(''));
  render();
  const ui = {
    calls, render, find, text: () => text(tree),
    pick(id) { find((n) => n.type === 'Select' && n.props.value === '')[0].props.onChange({ target: { value: id } }); render(); },
    type(value) { find((n) => n.type === 'CodeEditor')[0].props.onChange(value); render(); },
    submit() { return find((n) => n.type === 'Button' && n.props.variant === 'primary')[0].props.onClick(); },
    submitDisabled() { return find((n) => n.type === 'Button' && n.props.variant === 'primary')[0].props.disabled; },
    notice(kind) { return find((n) => n.props?.['data-run'] === kind)[0]; },
    runNow() { find((n) => n.type === 'input' && n.props.type === 'checkbox')[0].props.onChange({ target: { checked: true } }); render(); },
  };
  return ui;
}

test('a saved script with variables, picked unedited, runs by id so its variables go with it, at once where allowed', async () => {
  const ui = harness({ allowUnapproved: true });
  ui.pick('s-vars');
  assert.ok(ui.notice('library'), 'the form says the saved script itself runs');
  assert.match(ui.text(), /run\.libraryBound .*Huntress install/);
  assert.deepEqual(ui.calls.find((c) => c.name === 'toast.info')?.args[1], 'run.loadedBoundHint');
  await ui.submit();
  assert.deepEqual(ui.calls.find((c) => c.name === 'runLibraryScript')?.args, ['dev-1', 's-vars', {}, { direct: false }]);
  assert.equal(ui.calls.some((c) => c.name === 'createJob'), false, 'not sent as a copy of its text');
  // Where the instance lets this person skip approval, the saved script can run at once too.
  ui.runNow();
  await ui.submit();
  assert.deepEqual(ui.calls.filter((c) => c.name === 'runLibraryScript').at(-1).args, ['dev-1', 's-vars', {}, { direct: true }]);
});

test('declared parameters are asked for and sent with the id', async () => {
  const ui = harness();
  ui.pick('s-params');
  assert.equal(ui.submitDisabled(), true, 'a parameter without a value blocks the run');
  ui.find((n) => n.type === 'Input' && n.props.placeholder === '^[a-z]+$')[0].props.onChange({ target: { value: 'nginx' } });
  ui.render();
  assert.equal(ui.submitDisabled(), false);
  await ui.submit();
  assert.deepEqual(ui.calls.find((c) => c.name === 'runLibraryScript')?.args, ['dev-1', 's-params', { SERVICE: 'nginx' }, { direct: false }]);
});

test('for someone who may not bind variables, an edited copy says they no longer go with it, runs as text, and can be undone', async () => {
  const ui = harness({ perms: runner });
  ui.pick('s-vars');
  ui.type('Write-Host edited');
  assert.equal(ui.notice('library'), undefined);
  assert.ok(ui.notice('edited'), 'the edit is called out');
  assert.match(ui.text(), /run\.libraryEdited .*Huntress install/);
  assert.equal(ui.notice('variables'), undefined, 'no variable picker without scripts.manage and scripts.variables');
  await ui.submit();
  const job = ui.calls.find((c) => c.name === 'createJob')?.args[0];
  assert.equal(job?.script, 'Write-Host edited');
  assert.equal('variables' in job, false, 'nothing asks for a variable');
  assert.equal(ui.calls.some((c) => c.name === 'runLibraryScript'), false);
  // Undo puts the saved script back, and with it the run by id.
  ui.find((n) => n.type === 'Button' && (n.children || []).flat(Infinity).includes('run.undoEdits'))[0].props.onClick();
  ui.render();
  assert.ok(ui.notice('library'));
  assert.equal(ui.notice('edited'), undefined);
});

test('someone who may bind variables keeps them on an edited copy, picks more, and may skip approval where allowed', async () => {
  const ui = harness({ allowUnapproved: true });
  ui.pick('s-vars');
  ui.type('Write-Host edited $env:HUNTRESS_ACCOUNT_KEY');
  assert.match(ui.text(), /run\.libraryEditedVars .*Huntress install/);
  assert.ok(ui.notice('variables'), 'the picker is offered');
  assert.match(ui.text(), /HUNTRESS_ACCOUNT_KEY/, "the script's names stay selected");
  const picker = ui.find((n) => n.type === 'Combobox')[0];
  assert.deepEqual([...picker.props.options].map((o) => o.value), ['SITE_CODE'], 'selected and proposed names are not offered');
  picker.props.onChange('SITE_CODE'); ui.render();
  await ui.submit();
  const job = ui.calls.find((c) => c.name === 'createJob')?.args[0];
  assert.deepEqual(job?.variables, ['HUNTRESS_ACCOUNT_KEY', 'SITE_CODE']);
  assert.equal(job?.require_approval, true, 'approval unless "run immediately" is chosen');
  // Removing a name drops it from the next job; "run immediately" skips approval
  // where the instance allows it, with the variables.
  ui.find((n) => n.type === 'button' && n.props['aria-label'] === 'action.remove: SITE_CODE')[0].props.onClick(); ui.render();
  ui.runNow();
  await ui.submit();
  const now = ui.calls.filter((c) => c.name === 'createJob').at(-1).args[0];
  assert.deepEqual(now.variables, ['HUNTRESS_ACCOUNT_KEY']);
  assert.equal(now.require_approval, false);
});

test('a one-off script from scratch takes variables only when picked, and CMD takes none', async () => {
  const ui = harness({ allowUnapproved: true });
  ui.type('echo hi');
  assert.ok(ui.notice('variables'));
  assert.equal(ui.find((n) => n.type === 'input' && n.props.type === 'checkbox').length, 1, '"run immediately" stays for a job without variables');
  await ui.submit();
  assert.equal('variables' in ui.calls.find((c) => c.name === 'createJob').args[0], false);
  ui.find((n) => n.type === 'Combobox')[0].props.onChange('SITE_CODE'); ui.render();
  ui.find((n) => n.type === 'Select' && n.props.value === 'powershell')[0].props.onChange({ target: { value: 'cmd' } }); ui.render();
  assert.equal(ui.notice('variables'), undefined, 'CMD gets no picker');
  assert.match(ui.text(), /vars\.cmdUnsupported/);
  await ui.submit();
  assert.equal('variables' in ui.calls.filter((c) => c.name === 'createJob').at(-1).args[0], false, 'CMD never asks for a variable');
});

test('a script without variables or parameters still runs as its text, with no notice', async () => {
  const ui = harness();
  ui.pick('s-plain');
  assert.equal(ui.notice('library'), undefined);
  assert.equal(ui.notice('edited'), undefined);
  assert.deepEqual(ui.calls.find((c) => c.name === 'toast.info')?.args[1], 'run.loadedHint');
  await ui.submit();
  assert.equal(ui.calls.find((c) => c.name === 'createJob')?.args[0].script, 'uptime');
});

test('the script editor runs the saved script, and waits for unsaved changes to be saved', () => {
  const editor = rmm2.slice(rmm2.indexOf('function ScriptEditor('), rmm2.indexOf('function RunOnDevice('));
  assert.match(editor, /const runBlocked = contentChanged && !readOnly;/);
  assert.match(editor, /onClick=\{\(\) => setRunOpen\(true\)\} disabled=\{runBlocked\}/, 'Run on device is off with unsaved changes');
  assert.match(editor, /runBlocked \? t\('script\.saveBeforeRun'\)/, 'and says why');
  assert.match(editor, /<RunOnDevice script=\{\{ id: script\.id, name: script\.name, shell: script\.shell, body: script\.body, params: script\.params \|\| \[\], variables: script\.variables \|\| \[\] \}\}/,
    'the run gets the saved script, not the editor state');
});

test('the job card names the variables a job receives, and says from the server when the code is a one-off script', () => {
  const jobsSrc = readFileSync(new URL('../src/screens-jobs.jsx', import.meta.url), 'utf8');
  assert.match(jobsSrc, /\{\(job\.variables \|\| \[\]\)\.length > 0 && <JobVariables names=\{job\.variables\} oneOff=\{!job\.script_id\} \/>\}/,
    'one-off comes from script_id, not from the description its creator typed');
  const component = jobsSrc.slice(jobsSrc.indexOf('function JobVariables('), jobsSrc.indexOf('window.JobVariables'));
  const sandbox = { React: { createElement(type, props, ...children) { return { type, props: props || {}, children }; } }, t: (k) => k, Icon: 'Icon' };
  vm.createContext(sandbox); vm.runInContext(transformSync(component, { loader: 'jsx' }).code, sandbox);
  const library = JSON.stringify(sandbox.JobVariables({ names: ['HUNTRESS_ACCOUNT_KEY'], oneOff: false }));
  assert.match(library, /jobs\.variables/); assert.match(library, /HUNTRESS_ACCOUNT_KEY/); assert.match(library, /jobs\.variablesHint/);
  assert.doesNotMatch(library, /jobs\.variablesOneOff/);
  assert.match(JSON.stringify(sandbox.JobVariables({ names: ['HUNTRESS_ACCOUNT_KEY'], oneOff: true })), /jobs\.variablesOneOff/);
  const chatSrc = readFileSync(new URL('../src/screens-chat.jsx', import.meta.url), 'utf8');
  assert.match(chatSrc, /<window\.JobVariables names=\{j\.variables\} oneOff=\{!j\.script_id\} \/>/);
});

test('a bulk deployment says that only its text is sent', () => {
  const bulk = devices.slice(devices.indexOf('function BulkDeployModal('), devices.indexOf('function RunScriptForm('));
  assert.match(bulk, /<Field label=\{t\('deploy\.script'\)\} hint=\{t\('deploy\.noVariablesHint'\)\}>/);
});
