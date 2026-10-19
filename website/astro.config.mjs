// The docs site, published at https://docs.openrmm.io by
// .github/workflows/docs-site.yml. Its pages are the repository's own guides,
// copied in by scripts/sync-docs.mjs, which also writes the sidebar read below:
// run `npm run build`, not `astro build` alone.
import { readFileSync } from 'node:fs';
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';

const sidebar = JSON.parse(readFileSync(new URL('./src/generated/sidebar.json', import.meta.url), 'utf8'));

export default defineConfig({
  site: 'https://docs.openrmm.io',
  // The only images are the logo and favicon, already small: served as they
  // are, with no image-processing dependency in the build.
  image: { service: passthroughImageService() },
  integrations: [
    starlight({
      title: 'OpenRMM Docs',
      description: 'Install, run and extend OpenRMM, the open-source RMM whose AI assistant proposes and a human approves.',
      logo: { src: './src/assets/logo.png', alt: 'OpenRMM' },
      favicon: '/favicon.png',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/openrmm-project/openrmm' },
        { icon: 'discord', label: 'Discord', href: 'https://discord.gg/VN2jYWuA8X' },
        { icon: 'x.com', label: 'X', href: 'https://x.com/openrmm' },
      ],
      // Each guide sets its own edit link to its source file; this one covers
      // the pages that live in website/ itself.
      editLink: { baseUrl: 'https://github.com/openrmm-project/openrmm/edit/main/website/' },
      lastUpdated: true,
      sidebar,
      customCss: ['./src/styles/custom.css'],
      // A link to a guide that is not published, or to a heading it no longer
      // has, fails the build instead of reaching the site.
      plugins: [starlightLinksValidator()],
    }),
  ],
});
