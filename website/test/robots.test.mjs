// public/robots.txt: crawlers may read every page and are pointed at the
// sitemap the build writes, on the host the site is published under.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const website = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const robots = readFileSync(path.join(website, 'public', 'robots.txt'), 'utf8');
const config = readFileSync(path.join(website, 'astro.config.mjs'), 'utf8');

test('robots.txt blocks nothing and names the sitemap of the published site', () => {
  const site = config.match(/site:\s*'([^']+)'/)[1];
  assert.match(robots, /^User-agent: \*$/m);
  assert.doesNotMatch(robots, /^Disallow:\s*\S/m, 'nothing on the docs site is private');
  assert.match(robots, new RegExp(`^Sitemap: ${site.replace(/[.]/g, '\\.')}/sitemap-index\\.xml$`, 'm'));
  // Starlight writes sitemap-index.xml at build time whenever `site` is set.
  assert.match(config, /starlight\(/, 'the sitemap it names is written by the build');
});
