# docs.openrmm.io

The documentation site, built with [Astro Starlight](https://starlight.astro.build)
from the guides already in this repository. The guides stay the source:
`docs/*.md`, `deploy/QUICKSTART.md`, `deploy/README-production.md`,
`SECURITY.md`, `CONTRIBUTING.md` and `CHANGELOG.md` read the same on GitHub as
before, and nothing under `website/` holds a copy of them.

## How a change reaches the site

1. `scripts/sync-docs.mjs` copies each guide into `src/content/docs/` (ignored by
   git). It turns the first heading into the page title, links the page's
   "Edit page" to the source file, and dates it from that file's last commit.
   Relative links become site links when they point at another guide, or GitHub
   links when they point at anything else in the repository.
2. `astro build` renders the site. It also builds the search index, and
   `starlight-links-validator` fails the build on a link to a page or heading
   that does not exist.
3. `.github/workflows/docs-site.yml` runs both on every pull request that
   touches a guide or `website/`. On `main` it then publishes to GitHub Pages,
   with no credentials beyond the workflow's own token.

## Adding or moving a guide

- A new `docs/*.md` is published automatically, under **More** in the sidebar.
  To place it in a group with a shorter label, add it to `GROUPS` in
  `scripts/sync-docs.mjs`.
- A guide outside `docs/` must be added to `GROUPS`, and to the `paths` of
  `.github/workflows/docs-site.yml` so that editing it republishes the site.
- Renaming a guide changes nothing on the site: its address comes from `GROUPS`
  (or from the file name, under **More**).

## Building it locally

```sh
cd website
npm ci --ignore-scripts
npm test
npm run build      # the site is in dist/
```

## Hosting

GitHub Pages serves the site, with the source set to **GitHub Actions** and the
custom domain `docs.openrmm.io`. DNS is a `CNAME` record from `docs` to
`openrmm-project.github.io`. The domain is verified for the organization, so no
other GitHub account can serve a page on it.
