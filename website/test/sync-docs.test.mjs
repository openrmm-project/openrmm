// scripts/sync-docs.mjs: every guide in the repository reaches the site, and
// its links still lead somewhere once they are no longer relative to the file.
import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const website = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const repo = path.resolve(website, '..');
const out = path.join(website, 'src', 'content', 'docs');
execFileSync(process.execPath, [path.join(website, 'scripts', 'sync-docs.mjs')], { stdio: 'ignore' });

const pages = readdirSync(out).filter((f) => f.endsWith('.md'));
const read = (file) => readFileSync(path.join(out, file), 'utf8');
const source = (file) => read(file).match(/^editUrl: "https:\/\/github\.com\/openrmm-project\/openrmm\/edit\/main\/([^"]+)"$/m)[1];
// Fenced blocks are copied as they are: a link inside one is an example.
const fences = (md) => md.match(/^(```|~~~)[\s\S]*?^\1/gm) || [];
const prose = (md) => md.replace(/^(```|~~~)[\s\S]*?^\1/gm, '').replace(/`[^`\n]*`/g, '');

test('every guide in docs/ is published, each from one source file', () => {
  const published = pages.map(source);
  for (const file of readdirSync(path.join(repo, 'docs')).filter((f) => f.endsWith('.md'))) {
    assert.ok(published.includes(`docs/${file}`), `docs/${file} is not on the site`);
  }
  assert.equal(new Set(published).size, published.length, 'a source published twice');
});

test('a page is its source without the first heading, which becomes the title', () => {
  for (const file of pages) {
    const page = read(file);
    const original = readFileSync(path.join(repo, source(file)), 'utf8').replace(/\r\n/g, '\n');
    const heading = original.match(/^\s*# (.+)\n/);
    if (heading) {
      const title = heading[1].replace(/^\d+\s*[—–-]\s*/, '').trim();
      assert.ok(page.includes(`\ntitle: ${JSON.stringify(title)}\n`), `${file}: title`);
    }
    assert.deepEqual(fences(page.slice(page.indexOf('\n---\n') + 5)), fences(original), `${file}: code blocks changed`);
  }
});

test('no link is left relative to a file the site does not have', () => {
  for (const file of pages) {
    const text = prose(read(file).slice(read(file).indexOf('\n---\n') + 5));
    const links = [...text.matchAll(/\]\(<?([^)\s>]+)>?(?:\s+"[^"]*")?\)/g), ...text.matchAll(/^\s{0,3}\[[^\]]+\]:\s*(\S+)/gm)].map((m) => m[1]);
    for (const link of links) {
      assert.match(link, /^(https?:|mailto:|#|\/[a-z0-9-]+\/(#.*)?$)/, `${file}: ${link}`);
    }
  }
});

test('the sidebar lists every page once', () => {
  const sidebar = JSON.parse(readFileSync(path.join(website, 'src', 'generated', 'sidebar.json'), 'utf8'));
  const slugs = sidebar.flatMap((group) => group.items.map((item) => item.slug));
  assert.deepEqual([...slugs].sort(), pages.map((f) => f.replace(/\.md$/, '')).sort());
});
