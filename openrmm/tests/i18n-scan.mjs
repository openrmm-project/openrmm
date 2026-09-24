// The scanner behind i18n-coverage.test.mjs, in its own module so it can also
// be run by hand to list what is left:
//   node tests/i18n-scan.mjs [file…]
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export const SCREENS = [
  'src/app.jsx', 'src/ui.jsx', 'src/device-bulk.jsx', 'src/labels.jsx', 'src/screens-core.jsx',
  'src/screens-devices.jsx', 'src/screens-jobs.jsx', 'src/screens-chat.jsx',
  'src/screens-rmm.jsx', 'src/screens-rmm2.jsx', 'src/screens-reports.jsx',
  'src/screens-patches.jsx',
];

// Proper names and technical tokens: identical in every locale by nature.
const SAME_EVERYWHERE = new Set([
  'OpenRMM', 'MeshCentral', 'RustDesk', 'UAC', 'IPv4', 'IPv6', 'MAC', 'BIOS', 'JSON', 'CSV', 'MCP', 'OIDC',
  'SMTP', 'TLS', 'STARTTLS', 'SSO', 'API', 'URL', 'ID', 'KB', 'MB', 'GB', 'TB', 'CPU', 'RAM', 'OK',
  'Windows', 'macOS', 'Linux', 'PowerShell', 'bash', 'sh', 'cmd', 'python', 'powershell', 'zsh',
  'Ctrl+Alt+Delete', '⌘⌥Esc', 'OpenAI', 'Anthropic', 'Ollama', 'Entra', 'Okta', 'Google', 'Microsoft',
  'SN', 'Slack', 'Teams', 'Discord', 'Telegram', 'webhook', 'Webhook', 'cron', 'UTC', 'TOTP', 'QR', 'HTTP', 'HTTPS',
]);

export function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, (m, p) => p + ' '.repeat(m.length - p.length))
    .replace(/\{\/\*[\s\S]*?\*\/\}/g, (m) => m.replace(/[^\n]/g, ' '));
}

// Visible words: at least one run of 2+ letters, after dropping allowed names,
// numbers, placeholders and punctuation.
const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
// Longest first and whole words only, so "sh" never eats the middle of
// "powershell".
const SAME_RE = new RegExp('(?<![\\p{L}\\d])(?:' + [...SAME_EVERYWHERE].sort((a, b) => b.length - a.length).map(escapeRe).join('|') + ')(?![\\p{L}\\d])', 'gu');

function hasWords(text) {
  const rest = ` ${text} `
    .replace(/[a-z][\w+.-]*:\/\/\S*/gi, ' ')          // URLs and schemes: rustdesk://
    .replace(/\S+@\S+\.\w+/g, ' ')                     // e-mail addresses in placeholders
    .replace(/\b[\w-]+\.(?:json|csv|txt|sh|ps1|log|exe|pkg|msi)\b/gi, ' ') // file names
    .replace(SAME_RE, ' ')
    .replace(/&[a-z]+;/g, ' ').replace(/\{[^}]*\}/g, ' ');
  return /\p{L}{2,}/u.test(rest);
}

export const lineOf = (src, index) => src.slice(0, index).split('\n').length;

export function findLiterals(source) {
  const src = stripComments(source);
  // A line may opt out with an `i18n-ignore` comment, for text that is code or
  // data rather than interface: a font stack, a sample script.
  const ignored = new Set(source.split('\n').map((l, i) => (/i18n-ignore/.test(l) ? i + 1 : 0)).filter(Boolean));
  const hits = [];
  const add = (index, text) => {
    const line = lineOf(src, index);
    if (hasWords(text) && !ignored.has(line)) hits.push(`${line}: ${text.trim().slice(0, 70)}`);
  };

  // Text between tags. Skips expression bodies ({...}) and anything that looks
  // like code rather than prose (arrows, operators, semicolons).
  for (const m of src.matchAll(/>([^<>{}]+)</g)) {
    const text = m[1];
    // Code that happens to sit between a '>' and a '<': ternaries, calls, lengths.
    if (!text.trim() || /=>|&&|\|\||;|\(\)|===|\.length|\.push\(|^\s*[:?)]|[?:(]\s*$|\w\)\s|<<|[&^]\s*\(|\.slice\(|startsWith\(/.test(text)) continue;
    add(m.index + 1, text);
  }
  // Attributes whose value a person reads.
  for (const m of src.matchAll(/\b(label|title|placeholder|hint|msg|subtitle|confirmLabel|aria-label|emptyText|body)="([^"]*)"/g)) {
    add(m.index, m[2]);
  }
  // Prose chosen in an expression: {dark ? 'Light mode' : 'Dark mode'},
  // {name || 'Unassigned'}. Only literals that read as words for a person —
  // capitalised or several words — so CSS classes ('text-ok bg-oksoft') and
  // single-word values ('online', 'bash') are left alone.
  for (const m of src.matchAll(/(?:[?:]|\|\||\?\?)\s*(['"])((?:(?!\1)[^\\\n])*)\1/g)) {
    const text = m[2];
    const cssLike = /^[a-z0-9\-:[\]/.#%_ !]*$/.test(text) && /-/.test(text);
    // A colon inside another string ('dev:') can open a false match that runs
    // across code; prose has no semicolons, braces or assignments.
    if (/[;={}]|\)\)/.test(text)) continue;
    const prose = /^\p{Lu}/u.test(text) ? /\p{L}{2,}/u.test(text) : /\p{L}{2,}\s+\p{L}{2,}/u.test(text);
    if (!cssLike && prose) add(m.index, text);
  }
  // Toasts, prompts and confirms with literal text.
  for (const m of src.matchAll(/\b(?:toast\??\.(?:success|error|info|warn)\??|window\.(?:prompt|confirm|alert))\(\s*(['"`])((?:(?!\1)[^\\]|\\.)*)\1/g)) {
    add(m.index, m[2]);
  }
  return hits;
}


if (process.argv[1] === fileURLToPath(import.meta.url)) {
  for (const file of process.argv.slice(2).length ? process.argv.slice(2) : SCREENS) {
    const hits = findLiterals(await readFile(path.join(root, file), 'utf8'));
    console.log(`== ${file} (${hits.length})`);
    for (const h of hits) console.log('  ' + h);
  }
}
