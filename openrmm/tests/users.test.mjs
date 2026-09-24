import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// The build concatenates each source file into its own `{ ... }` block, so a
// component's useState bindings are visible to nothing outside that component.
// A JSX block pasted into the wrong component therefore compiles, ships, and
// throws ReferenceError the first time that component renders — which unmounts
// the whole console, since there is no error boundary above the screens. That is
// exactly how the users Disable dialog ended up inside APITokensPanel: the
// button set state nobody read, and opening the API tokens tab blanked the page.
const SCREEN_SOURCES = [
  'src/screens-core.jsx', 'src/screens-devices.jsx', 'src/screens-jobs.jsx',
  'src/screens-chat.jsx', 'src/screens-rmm.jsx', 'src/screens-rmm2.jsx',
  'src/screens-reports.jsx', 'src/screens-patches.jsx', 'src/app.jsx', 'src/ui.jsx',
];

// Comments, string bodies and JSX text are not code; words inside them
// ("password", "created") must not read as identifier references.
function stripNonCode(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    .replace(/'(?:[^'\\\n]|\\.)*'/g, "''")
    .replace(/"(?:[^"\\\n]|\\.)*"/g, '""')
    // Template literals: keep the ${…} expressions, drop the literal text — a
    // `warn ${c.warn_threshold}` label otherwise reads as a `warn` reference.
    .replace(/`(?:[^`\\]|\\.)*`/g, (literal) => '`' + (literal.match(/\$\{[^{}]*\}/g) || []).join('') + '`')
    // JSX text: from a tag-closing '>' or an expression-closing '}' up to the
    // next '<' or '{'. The lookbehind keeps '=>' (and comparisons) intact, which
    // would otherwise swallow real code.
    // The lookbehind protects `=>`, `>>` and `->`. It must NOT protect `<`: the
    // only `>` preceded by one is a JSX fragment (`<>`), whose text is prose and
    // has to go the same way as any other.
    .replace(/(?<![=>!-])>[^<>{}]*/g, '>')
    .replace(/\}[^<>{}]*</g, '}<')
    // Prose between two JSX expressions: `{n} selected · {m}`. Anything with
    // punctuation that code uses is left alone, so `} catch (e) {` survives.
    .replace(/\}[^<>{}()=;]*\{/g, '}{');
}

function identifiersIn(fragment) {
  const out = new Set();
  for (const part of fragment.split(',')) {
    const name = part.replace(/[[\]{}.]/g, ' ').split(/[:=]/)[0].trim();
    if (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name)) out.add(name);
  }
  return out;
}

// Every binding form the screens actually use. Over-collecting only costs a
// missed report; under-collecting produces false failures, so params of nested
// arrows and callbacks are included too.
function declaredIn(body) {
  const names = new Set();
  const add = (fragment) => { for (const n of identifiersIn(fragment)) names.add(n); };
  const patterns = [
    /(?:const|let|var)\s*\[([^\]]*)\]/g,          // const [a, setA] = useState()
    /(?:const|let|var)\s*\{([^}]*)\}/g,           // const { can } = useAuth()
    /(?:const|let|var)\s+([A-Za-z0-9_$]+)/g,      // const x =
    // Extra declarators after a comma: `const u = a?.user, role = a?.role;`
    // binds role too, and missing it reads as a reference to someone else's.
    /,\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*=(?!=)/g,
    /function\s+([A-Za-z0-9_$]+)/g,               // function x()
    /function\s*[A-Za-z0-9_$]*\s*\(([^)]*)\)/g,   // parameters
    /\(([^()]*)\)\s*=>/g,                         // (a, b) =>
    /(?:^|[^\w$.])([A-Za-z_$][A-Za-z0-9_$]*)\s*=>/g, // a =>
    /catch\s*\(([^)]*)\)/g,
  ];
  for (const re of patterns) {
    let m;
    while ((m = re.exec(body))) add(m[1] || '');
  }
  return names;
}

// True when `name` is read as a value somewhere in `body`. Excludes the places
// that merely spell the same word: property access (`u.role`), object keys
// (`role:`), and JSX attribute names — both `status={…}` and the valueless
// boolean form (`<Drawer open onClose={…}>`), which share their vocabulary with
// state variables without referring to any.
function references(body, name) {
  const pattern = new RegExp(`(?<![.\\w$])${name}(?![\\w$:]|\\s*=[^=])`, 'g');
  for (const match of body.matchAll(pattern)) {
    const after = body.slice(match.index + name.length);
    const bareAttribute = /^\s*\/?>/.test(after) || /^\s+[A-Za-z_$][\w$]*\s*=[^=]/.test(after);
    if (!bareAttribute) return true;
  }
  return false;
}

test('no component references another component\'s useState bindings', async () => {
  const leaks = [];
  for (const rel of SCREEN_SOURCES) {
    const lines = (await readFile(path.join(root, rel), 'utf8')).split('\n');
    const heads = [];
    lines.forEach((line, i) => {
      const m = /^(?:export\s+)?function\s+([A-Za-z0-9_$]+)\s*\(/.exec(line);
      if (m) heads.push({ name: m[1], line: i });
    });
    const blocks = heads.map((h, i) => ({
      ...h,
      body: stripNonCode(lines.slice(h.line, i + 1 < heads.length ? heads[i + 1].line : lines.length).join('\n')),
    }));
    // Names declared above the first function belong to the whole file block.
    const moduleScope = declaredIn(stripNonCode(lines.slice(0, heads.length ? heads[0].line : lines.length).join('\n')));
    const scopes = new Map(blocks.map((b) => [b.name, declaredIn(b.body)]));

    for (const block of blocks) {
      for (const other of blocks) {
        if (other.name === block.name) continue;
        // Only useState-style array bindings: those are unambiguously local
        // React state, so a reference from elsewhere is always a mistake.
        const stateBindings = [...(other.body.matchAll(/(?:const|let)\s*\[([^\]]*)\]\s*=\s*useState/g))]
          .flatMap((m) => [...identifiersIn(m[1])]);
        for (const name of stateBindings) {
          if (scopes.get(block.name).has(name) || moduleScope.has(name)) continue;
          if (references(block.body, name)) {
            leaks.push(`${rel}: ${block.name}() references '${name}', which only ${other.name}() declares`);
          }
        }
      }
    }
  }
  assert.deepEqual(leaks, [], 'components must not reference state they do not own:\n' + leaks.join('\n'));
});

// "Restricted to…" with nothing selected serializes to the same empty scope list
// as "Full access", so saving it silently grants the whole fleet — the console
// reported "Scope updated · full access" while widening the account. The picker
// cannot decide this alone (the payload is identical), so the mode is owned by
// the caller and every caller has to refuse the empty-restricted save.
test('an empty "restricted" scope selection cannot be saved as full access', async () => {
  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');

  // The picker must not own its mode privately again.
  assert.match(source, /function ScopePicker\(\{[^}]*\bmode\b[^}]*\bsetMode\b[^}]*\}\)/,
    'ScopePicker must take mode/setMode from its caller');
  assert.doesNotMatch(source, /const \[mode, setMode\] = useState/,
    'ScopePicker must not hold the mode privately — the caller needs it to block the save');

  // Both call sites pass it through.
  const callSites = source.match(/<ScopePicker[^/]*\/>/g) || [];
  assert.equal(callSites.length, 2, 'expected the create form and the edit modal');
  for (const site of callSites) {
    assert.match(site, /mode=\{/, `ScopePicker call site missing mode: ${site}`);
    assert.match(site, /setMode=\{/, `ScopePicker call site missing setMode: ${site}`);
  }

  // And the guard exists and gates both submit buttons.
  assert.match(source, /const scopeIncomplete = \(mode, list\) => mode === 'restricted' && list\.length === 0;/);
  // The <Button …> element whose label is t(key), back to its opening tag.
  const button = (key) => {
    const end = source.indexOf(`>{t('${key}')}</Button>`);
    assert.notEqual(end, -1, `could not find the ${key} button`);
    return source.slice(source.lastIndexOf('<Button', end), end);
  };
  assert.match(button('users.create'), /disabled=\{[^}]*scopeIncomplete\(scopeMode, scopes\)/,
    'Create user must be disabled while the scope is restricted-but-empty');
  assert.match(button('users.saveScope'), /disabled=\{[^}]*scopeIncomplete\(editScopeMode, editScopes\)/,
    'Save scope must be disabled while the scope is restricted-but-empty');
});

// Row actions live behind one kebab. Remove used to sit inline, one slip away
// from Disable, on every row.
test('user row actions are grouped into a single kebab menu', async () => {
  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const usersPanel = source.slice(source.indexOf('function UsersPanel('), source.indexOf('function RolesPanel('));
  const row = usersPanel.slice(usersPanel.indexOf('<tbody'), usersPanel.indexOf('</tbody>'));

  assert.match(row, /<Dropdown[^>]*trigger=/, 'the row needs a Dropdown trigger');
  assert.match(row, /Icon name="dots"/, 'the trigger is the three-dot icon');
  // Arrow functions in the props mean `[^>]*` cannot span a MenuItem's
  // attributes; match the label as the element's child instead.
  for (const key of ['users.emailAddress', 'users.accessScope', 'users.reset2fa', 'users.remove', 'users.enableSignIn', 'users.disableSignIn']) {
    assert.ok(row.includes(`>{t('${key}')}</MenuItem>`), `missing menu item: ${key}`);
  }
  // No action may stay inline — that is the layout being replaced.
  assert.doesNotMatch(row, /<Button[^>]*>\{t\('users\./,
    'row actions must be inside the menu, not inline buttons');
  // The kebab is a control, so it needs a name for anyone not using a mouse.
  assert.match(row, /aria-label=\{t\('users\.actionsFor', \{ user: u\.username \}\)\}/);
});

// These menus open from rows inside cards that clip their overflow, so an
// absolutely positioned menu is cut off — the last row's was invisible. Fixed
// positioning is the reason it works; a revert to `absolute` brings the bug back
// on every table in the console at once.
test('dropdown menus escape the overflow of the card they open from', async () => {
  const core = await readFile(path.join(root, 'src/screens-core.jsx'), 'utf8');
  const dropdown = core.slice(core.indexOf('function Dropdown('), core.indexOf('function MenuItem('));
  assert.match(dropdown, /position: 'fixed'/, 'the menu must be positioned fixed');
  assert.doesNotMatch(dropdown, /className=\{cx\('absolute/, 'an absolutely positioned menu is clipped by the card');
  // Fixed elements do not follow their trigger, so movement has to dismiss them.
  assert.match(dropdown, /addEventListener\('scroll'/);
  assert.match(dropdown, /addEventListener\('resize'/);
  // And a trigger near the bottom of the window has to open upward.
  assert.match(dropdown, /const up = /);
});

// The dialog and the state that drives it have to live in the same component;
// this is the specific pairing the leak check above generalizes.
test('the user disable confirmation lives in the panel that owns its state', async () => {
  const source = await readFile(path.join(root, 'src/screens-rmm2.jsx'), 'utf8');
  const usersPanel = source.slice(source.indexOf('function UsersPanel('), source.indexOf('function RolesPanel('));
  assert.match(usersPanel, /const \[confirmDisable, setConfirmDisable\] = useState/);
  assert.match(usersPanel, /open=\{!!confirmDisable\}/,
    'the "Disable this user?" dialog must render inside UsersPanel');
  assert.match(usersPanel, /onConfirm=\{\(\) => toggleDisabled\(confirmDisable\)\}/);

  const tokensPanel = source.slice(source.indexOf('function APITokensPanel('), source.indexOf('function SettingsScreen('));
  assert.doesNotMatch(tokensPanel, /confirmDisable/,
    'APITokensPanel must not reference the users panel state');
});
