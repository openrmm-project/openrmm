import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// A policy's name and description used to be write-once: the screen could create
// one and delete it, and nothing in between. Fixing a typo in a policy assigned
// across the fleet meant rebuilding it, losing its checks, tasks and assignments.
test('a policy can be renamed and re-described from the drawer', async () => {
  const api = await readFile(path.join(root, 'src/mockapi.jsx'), 'utf8');
  assert.match(api, /updatePolicy\(id, body\) \{ return PUT\('\/api\/policies\/' \+ id, body\); \}/,
    'the client needs an updatePolicy that PUTs to the policy');

  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const detail = source.slice(source.indexOf('function PolicyDetail('), source.indexOf('/* ============================ CLIENTS & SITES'));

  assert.match(detail, /api\.updatePolicy\(policy\.id, \{ name: [^}]*description: [^}]*\}\)/,
    'the drawer must send both fields');
  // Editing is a manage action; viewers see the drawer without it.
  assert.match(detail, /can\('policies\.manage'\) && <Button[^>]*onClick=\{\(\) => setEditing\(/,
    'the Edit button must be gated on policies.manage');
  // The drawer holds its own copy of the policy, so a rename has to be handed
  // back up or the header keeps showing the old name.
  assert.match(detail, /onSaved\?\.\(updated\)/, 'the saved policy must be handed back to the screen');
  // Arrow functions in the props mean `[^>]*` cannot span the element's
  // attributes, so match the wiring in two pieces.
  assert.match(source, /<PolicyDetail policy=\{detail\}/);
  assert.match(source, /onSaved=\{onPolicySaved\}/);
});

// The drawer listed a policy's checks and tasks as plain rows — the enabled
// toggle was `disabled` with an empty onChange — so the only way to change
// anything a policy contains was to find it again on the Checks or Scheduled
// tasks screen. The editors live in screens-rmm.jsx, and the build gives each
// source file its own block scope, so they have to travel through window.
test('a policy\'s checks and tasks can be edited from the drawer', async () => {
  const rmm = await readFile(path.join(root, 'src/screens-rmm.jsx'), 'utf8');
  assert.match(rmm, /window\.CheckEditor = CheckEditor;/);
  assert.match(rmm, /window\.TaskEditor = TaskEditor;/);
  // Opened from inside a policy, "which policy" is not a question worth re-asking.
  for (const editor of ['CheckEditor', 'TaskEditor']) {
    const start = rmm.indexOf(`function ${editor}(`);
    const signature = rmm.slice(start, rmm.indexOf('{', rmm.indexOf(')', start)));
    assert.match(signature, /defaultPolicyId/, `${editor} must accept defaultPolicyId`);
  }
  assert.match(rmm, /defaultPolicyId \? 'pol:' \+ defaultPolicyId : ''/,
    'the default has to seed the target, and only when nothing else did');

  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const detail = source.slice(source.indexOf('function PolicyDetail('), source.indexOf('/* ============================ CLIENTS & SITES'));
  assert.match(detail, /<CheckEditor[\s\S]{0,220}defaultPolicyId=\{policy\.id\}/);
  assert.match(detail, /<TaskEditor[\s\S]{0,260}defaultPolicyId=\{policy\.id\}/);
  // The toggle was inert: `onChange={() => { }} disabled`.
  assert.doesNotMatch(detail, /onChange=\{\(\) => \{ \}\}/, 'the task toggle must do something');
  assert.match(detail, /onChange=\{\(\) => setConfirmToggleTask\(task\)\}/);
  assert.match(detail, /api\.updateTask\(task\.id, \{ \.\.\.task, enabled: !task\.enabled \}\)/,
    'toggling goes through PUT, so the whole task has to be resent');
  // Changing what a policy contains is gated separately from editing the policy.
  assert.match(detail, /const canChecks = can\('checks\.manage'\), canTasks = can\('tasks\.manage'\)/);
});

// The API always accepted a description; the create form never sent one, so
// every policy read "No description" and nothing could change it afterwards.
test('the new-policy form sends a description', async () => {
  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const screen = source.slice(source.indexOf('function PoliciesScreen('), source.indexOf('function PolicyCard('));
  assert.match(screen, /api\.createPolicy\(\{ name: [^}]*description: [^}]*\}\)/,
    'create must send the description the API accepts');
  assert.match(screen, /<Field label=\{t\('common\.description'\)\}/, 'the form needs a description field');
});
