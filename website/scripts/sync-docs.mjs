// Copies the repository's Markdown guides into the docs site before a build.
//
// The guides stay where they are (docs/, deploy/, the top-level files), readable
// on GitHub as before; this script is the only bridge to the site. For each one
// it writes src/content/docs/<slug>.md with front matter (title from the first
// heading, an edit link to the source file, the date of its last commit) and
// rewrites relative links: to another published guide they become site links,
// to anything else in the repository they become GitHub links. It also writes
// src/generated/sidebar.json, which astro.config.mjs reads.
//
// A guide added to docs/ needs no change here: it is published under "More"
// until it is given a place in GROUPS.
//
//   node scripts/sync-docs.mjs
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const website = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const repo = path.resolve(website, '..');
const out = path.join(website, 'src', 'content', 'docs');
const generated = path.join(website, 'src', 'generated');
const GITHUB = 'https://github.com/openrmm-project/openrmm';

// Sidebar groups, in order: [source path, slug, sidebar label].
const GROUPS = [
  ['Get started', [
    ['deploy/QUICKSTART.md', 'quickstart', 'Self-hosting quick start'],
    ['docs/00-overview.md', 'overview', 'Overview'],
    ['deploy/README-production.md', 'production', 'Production deployment'],
    ['docs/02-getting-started.md', 'run-from-source', 'Run from source'],
  ]],
  ['Using OpenRMM', [
    ['docs/06-ai-chat.md', 'ai-chat', 'AI assistant'],
    ['docs/23-ai-autonomy.md', 'ai-autonomy', 'AI autonomy'],
    ['docs/08-remote-access.md', 'remote-access', 'Remote access'],
    ['docs/10-monitoring-policies.md', 'monitoring-policies', 'Monitoring, policies and alerts'],
    ['docs/16-patch-management.md', 'patch-management', 'Update management'],
    ['docs/20-email-notifications.md', 'email-notifications', 'Email notifications'],
    ['docs/15-reports.md', 'reports', 'Reports'],
    ['docs/11-mcp.md', 'mcp', 'MCP server'],
    ['docs/17-mcp-oauth.md', 'mcp-oauth', 'MCP sign-in (OAuth)'],
    ['docs/21-single-sign-on.md', 'single-sign-on', 'Single sign-on'],
  ]],
  ['Security', [
    ['SECURITY.md', 'security', 'Security overview'],
    ['docs/07-guardrails-security.md', 'guardrails-security', 'Guardrails in depth'],
    ['docs/18-update-signing-runbook.md', 'update-signing-runbook', 'Signed updates'],
    ['docs/13-security-rollout.md', 'security-rollout', 'Hardening and rollout'],
  ]],
  ['For developers', [
    ['docs/01-architecture.md', 'architecture', 'Architecture'],
    ['docs/04-api-reference.md', 'api-reference', 'API reference'],
    ['docs/05-agent-protocol.md', 'agent-protocol', 'Agent protocol'],
    ['docs/03-database-schema.md', 'database-schema', 'Database schema'],
    ['CONTRIBUTING.md', 'contributing', 'Contributing'],
  ]],
  ['Project', [
    ['docs/09-roadmap.md', 'roadmap', 'Roadmap'],
    ['CHANGELOG.md', 'changelog', 'Changelog'],
    ['docs/12-native-remote-fork.md', 'native-remote-fork', 'Native remote desktop design'],
    ['docs/14-frontend-design-brief.md', 'frontend-design-brief', 'Console design brief'],
    ['docs/19-production-readiness-plan.md', 'production-readiness-plan', 'Production readiness plan'],
  ]],
];

const BANNER = 'Rather not run it yourself? <a href="https://openrmm.io/#planos">OpenRMM Cloud</a> hosts it for you.';

// Every guide: the ones placed above, then any other docs/*.md under "More".
const pages = [];
for (const [group, items] of GROUPS) {
  for (const [source, slug, label] of items) pages.push({ source, slug, label, group });
}
const placed = new Set(pages.map((p) => p.source));
for (const file of readdirSync(path.join(repo, 'docs')).sort()) {
  const source = `docs/${file}`;
  if (!file.endsWith('.md') || placed.has(source)) continue;
  pages.push({ source, slug: file.replace(/\.md$/, '').replace(/^\d+-/, '').toLowerCase(), label: null, group: 'More' });
}
const bySource = new Map(pages.map((p) => [p.source, p]));
const slugs = new Set();
for (const page of pages) {
  if (!existsSync(path.join(repo, page.source))) throw new Error(`${page.source} is listed but does not exist`);
  if (slugs.has(page.slug)) throw new Error(`two guides publish to /${page.slug}/`);
  slugs.add(page.slug);
}

// A link in `from` (a repository path) to `target`, as the site should have it.
function rewrite(target, from) {
  if (/^([a-z][a-z0-9+.-]*:|#|\/\/)/i.test(target)) return target; // absolute, or an anchor on this page
  const [rawPath, hash = ''] = target.split(/(?=#)/);
  const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(from), decodeURI(rawPath)));
  if (resolved.startsWith('..')) return target;
  const page = bySource.get(resolved);
  if (page) return `/${page.slug}/${hash}`;
  const onDisk = path.join(repo, resolved);
  const kind = existsSync(onDisk) && statSync(onDisk).isDirectory() ? 'tree' : 'blob';
  return `${GITHUB}/${kind}/main/${encodeURI(resolved)}${hash}`;
}

// Rewrites inline links/images and reference definitions outside code: fenced
// blocks and inline code spans are left exactly as written.
function rewriteLinks(markdown, from) {
  let fence = null;
  return markdown.split('\n').map((line) => {
    const opener = line.match(/^\s*(`{3,}|~{3,})/);
    if (fence) {
      if (opener && opener[1][0] === fence[0] && opener[1].length >= fence.length && line.trim() === opener[1]) fence = null;
      return line;
    }
    if (opener) { fence = opener[1]; return line; }
    const definition = line.match(/^(\s{0,3}\[[^\]]+\]:\s*)(\S+)(.*)$/);
    if (definition) return definition[1] + rewrite(definition[2], from) + definition[3];
    return line.split(/(`[^`]*`)/).map((part) => (part.startsWith('`') ? part
      : part.replace(/(\]\()(<[^>]+>|[^)\s]+)(\s+"[^"]*")?\)/g, (_, open, url, title = '') => {
        const bare = url.startsWith('<') ? url.slice(1, -1) : url;
        return `${open}${rewrite(bare, from)}${title})`;
      }))).join('');
  }).join('\n');
}

function lastCommit(source) {
  try {
    const date = execFileSync('git', ['-c', 'core.fsmonitor=false', 'log', '-1', '--format=%cI', '--', source], { cwd: repo, encoding: 'utf8' }).trim();
    return date || null;
  } catch { return null; }
}

mkdirSync(out, { recursive: true });
mkdirSync(generated, { recursive: true });
for (const file of readdirSync(out)) if (file.endsWith('.md')) rmSync(path.join(out, file));

for (const page of pages) {
  const text = readFileSync(path.join(repo, page.source), 'utf8').replace(/\r\n/g, '\n');
  // The first heading is the page title; Starlight renders it from the front matter.
  const heading = text.match(/^\s*# (.+)\n/);
  const title = (heading ? heading[1] : page.label || page.slug).replace(/^\d+\s*[—–-]\s*/, '').trim();
  const body = heading ? text.slice(heading[0].length) : text;
  page.label ||= title;
  const updated = lastCommit(page.source);
  const front = {
    title,
    editUrl: `${GITHUB}/edit/main/${page.source}`,
    ...(updated ? { lastUpdated: updated } : {}),
    banner: { content: BANNER },
  };
  // JSON strings are valid YAML; the date stays unquoted so YAML reads it as one.
  const yaml = Object.entries(front).map(([k, v]) => (k === 'lastUpdated' ? `${k}: ${v}`
    : typeof v === 'object'
      ? `${k}:\n${Object.entries(v).map(([k2, v2]) => `  ${k2}: ${JSON.stringify(v2)}`).join('\n')}`
      : `${k}: ${JSON.stringify(v)}`)).join('\n');
  writeFileSync(path.join(out, `${page.slug}.md`), `---\n${yaml}\n---\n\n${rewriteLinks(body, page.source).trimStart()}`);
}

const groups = [...new Set(pages.map((p) => p.group))].map((label) => ({
  label,
  items: pages.filter((p) => p.group === label).map((p) => ({ slug: p.slug, label: p.label })),
}));
writeFileSync(path.join(generated, 'sidebar.json'), JSON.stringify(groups, null, 2) + '\n');
console.log(`synced ${pages.length} guides into ${path.relative(repo, out)}`);
