# Website Design Brief — openrmm.org

Use this to design and build the **public website** for OpenRMM at
`openrmm.org` (e.g. in Claude Design / Claude Artifacts). This is the
**marketing + documentation site** — NOT the product console. The product UI
(the in-app dashboard) already exists in `openrmm/` and has its own brief
(`FRONTEND_DESIGN_BRIEF.md`); do not rebuild that here.

This file is self-contained: product context, goals, stack recommendation,
sitemap, page-by-page specs, design tokens, and content sources.

---

## 1. What this site is (and is not)

| | |
|---|---|
| **Is** | A fast, polished **marketing landing + documentation portal** that explains OpenRMM, sells the vision, and lets a self-hoster get from "what is this" → "running it" with zero friction. |
| **Is not** | The product console (that's the authenticated app). No login, no live fleet data. The site is 100% static/SSG content. |
| **Domain** | `openrmm.org` (docs may live at `/docs` or `docs.openrmm.org`). |
| **Goal in one line** | Convince a skeptical IT admin / self-hoster that OpenRMM is the real, open, AI-native alternative to closed RMM SaaS — and make it trivial to try. |

## 2. Product in one paragraph (the message)

**OpenRMM** is an **open-source, self-hosted RMM** (Remote Monitoring &
Management) with a **native AI assistant**. You manage fleets of computers in
plain language — *"install 7-zip on the finance group"*, *"configure printer X
at IP Y on host Z"*, *"report all offline devices"* — and the AI inspects the
fleet and **proposes** the scripts/actions. **A human approves before anything
runs.** Bring your own AI model (any OpenAI-compatible endpoint, including local
models like Ollama/vLLM), so command and inventory data never has to leave your
infrastructure. Think *"modern Tactical RMM, AI-native and privacy-first."*

**The gap it fills:** every AI-copilot RMM today (Atera, SuperOps, Acronis, Neo)
is **closed SaaS**. There is no open-source, self-hosted, bring-your-own-model
RMM with native AI. That's the whole pitch — own your data, own your model, own
your infrastructure.

## 3. Audience & tone

- **Primary:** IT admins, MSP technicians, homelab / self-hosting enthusiasts,
  privacy- and sovereignty-minded orgs (govt, healthcare, EU data-residency).
- **Secondary:** developers who might contribute (it's open source).
- **Tone:** confident, technical, no marketing fluff. Closer to the voice of
  **Tailscale, Vercel, Linear, Supabase, PlanetScale** docs/landing pages than
  to legacy enterprise IT vendors. Show real code, real commands, real
  screenshots. Respect the reader's intelligence.

## 4. Stack recommendation (you asked to evaluate alternatives)

The site is **content-heavy (marketing + docs)** with a few interactive
flourishes. The baseline ask is **React** — all options below let you author in
React/JSX. Recommendation, then the evaluation:

> **Primary recommendation: Astro + Starlight, with React islands.**
> Best Lighthouse/SEO out of the box (ships ~zero JS by default), first-class
> MDX, and **Starlight** (Astro's docs theme) gives you sidebar nav, search,
> versioning, dark mode, and edit-on-GitHub for free — which is exactly the
> docs half of this site. You still write **React components** for interactive
> bits (animated hero, copy-command widget, pricing/compare table) as
> "islands." One framework covers marketing **and** docs.

| Option | React? | Best at | Trade-off | Verdict |
|---|---|---|---|---|
| **Astro + Starlight** | ✅ via islands | Marketing **+** docs in one repo; top performance/SEO; minimal JS | Astro-specific concepts (islands, content collections) | **Recommended** |
| **Next.js (App Router) + Fumadocs/Nextra** | ✅ pure React | All-React ecosystem; SSG/ISR; easy to add dynamic later (interactive demos, blog, auth) | Heavier JS baseline than Astro; docs theming is more assembly | **Strong alt** — pick if you want a pure-React/Next codebase or plan dynamic features |
| **Docusaurus** | ✅ pure React | Purpose-built **docs** (versioning, search, i18n, MDX) | Marketing/landing pages feel constrained | Good for a **docs-only** subdomain |
| **VitePress** | ❌ (Vue) | Extremely fast docs | Not React | Skip (stack is React) |
| **Plain React + Vite (SPA)** | ✅ | Full control | Weak SEO for a marketing site (CSR); you'd reinvent docs infra | Not recommended for a content site |

**Decision:** Build with **Astro + Starlight + React islands**. If the team
strongly prefers a single pure-React/Next stack, **Next.js + Fumadocs** is the
sanctioned fallback — keep the IA and design identical either way.

**Shared tooling regardless of framework:** TypeScript, **Tailwind CSS** (reuse
the product's design tokens, §8), MDX for content, `shiki`/`rehype-pretty-code`
for syntax highlighting, `pagefind` (Astro) or built-in search for docs,
Plausible/Umami for privacy-friendly analytics (no GA — it's an off-message for
a privacy product).

## 5. Information architecture (sitemap)

```
/                      Landing (hero, problem, how it works, features, proof, CTA)
/features             Deep feature tour (AI chat, remote access, classic RMM, MCP)
/docs                 Documentation portal (Starlight) — see §7
  /docs/overview        ← docs/00-overview.md
  /docs/architecture    ← docs/01-architecture.md
  /docs/getting-started ← docs/02-getting-started.md   (the money page)
  /docs/api-reference   ← docs/04-api-reference.md
  /docs/agent-protocol  ← docs/05-agent-protocol.md
  /docs/ai-chat         ← docs/06-ai-chat.md
  /docs/guardrails      ← docs/07-guardrails-security.md
  /docs/remote-access   ← docs/08-remote-access.md
  /docs/monitoring      ← docs/10-monitoring-policies.md
  /docs/mcp             ← docs/11-mcp.md
  /docs/security-rollout← docs/13-security-rollout.md
  /docs/roadmap         ← docs/09-roadmap.md
/deploy               Get started / self-host guide (Docker compose, enroll agents, TLS)
/security             Security & guardrails (the trust page — propose→approve, audit, crypto)
/ai                   "Bring your own model" page (OpenAI / Ollama / vLLM; MCP connectors)
/vs                   Comparison vs closed RMM SaaS (Atera/SuperOps/…) + vs MeshCentral/Tactical
/roadmap              Public roadmap (phased; what's done vs planned)
/blog                 (optional) release notes / posts
404
```

Top nav: **Features · Docs · Deploy · Security · Roadmap · GitHub ★**.
Persistent primary CTA: **"Get started"** (→ /deploy) and a secondary **"Star on
GitHub"** with the live star count.

## 6. Page-by-page specs

### 6.1 Landing (`/`) — the most important page

Sections, top to bottom:

1. **Hero**
   - Headline: *"The open-source RMM with a built-in AI — that you actually
     control."* (or A/B: *"Manage your whole fleet by chatting with an AI.
     Self-hosted. Bring your own model."*)
   - Sub: one sentence — self-hosted, AI-native, BYO-model, human-in-the-loop.
   - Two CTAs: **Get started** (primary) · **View on GitHub** (secondary, with
     star count).
   - Visual: an **animated terminal/chat mock** showing the core loop —
     user types *"install 7-zip on the finance group"* → AI replies *"I've
     prepared a script for 3 devices, awaiting your approval"* → an **approval
     card** with a script preview. This single animation tells the whole story.
   - Trust strip: "Open source · Self-hosted · No telemetry · Your AI model".

2. **The problem** (3 short cards): *AI-copilot RMMs are closed SaaS* · *Your
   command data leaves your network* · *You can't self-host or pick your model.*

3. **How it works** (the guardrail — sell the safety): a 4-step horizontal flow
   **Ask → AI proposes → Human approves → Audited execution.** Emphasize **"The
   AI proposes, humans dispose — enforced in code, not policy."** This is the #1
   trust message; make it prominent.

4. **Feature grid** (icon cards, link to /features and deep docs):
   - **AI chat with tool-calling** — reads fleet state, proposes actions.
   - **Bring your own model** — OpenAI, Ollama, vLLM; data stays in your infra.
   - **Own Go agent** — single static cross-platform binary, self-updating.
   - **Pluggable remote access** — native desktop + file transfer + chat,
     elevated terminal, MeshCentral.
   - **Classic RMM** — Client→Site→Agent, RBAC, checks, tasks, policies, alerts.
   - **MCP server** — connect Claude/ChatGPT to your fleet, same approval gate.
   - **Guardrails & audit** — immutable audit log, encrypted secrets, signed
     updates, 2FA.
   - **Per-user access scope** — restrict users to Company/Site/device.

5. **AI chat showcase** — a larger, realistic chat panel (use real tool names:
   `list_devices`, `get_inventory`, `propose_script`, `install_software`).
   Render the tool steps as collapsible cards ("the AI looked up X / proposed
   job Y") — transparency is the selling point.

6. **Privacy / BYO-model band** — "Run a local model (Ollama/vLLM) and command
   data never leaves your infrastructure." Diagram: fleet ↔ self-hosted server ↔
   *your* model (cloud or local).

7. **Quick start teaser** — a copy-paste `docker compose up -d` block + "enroll
   an agent in one command", linking to /deploy.

8. **Proof / openness** — GitHub stars, "MIT/AGPL open source", link to the
   security page and the public roadmap.

9. **Final CTA** — Get started · Read the docs · Star on GitHub.

### 6.2 Features (`/features`)
Long-form tour, one rich section per pillar, each linking to its doc:
- **AI chat & tool-calling** (link docs/06) — the loop, the tool catalog,
  high-level OS-aware actions (install_software, install_printer, flush_dns,
  reboot_device), in-chat CSV reports.
- **Remote access** (link docs/08) — the four providers (native, files,
  terminal, meshcentral); CGO-free tile-delta engine; Windows + Linux/Xorg;
  login-screen capture; in-session chat; bidirectional file transfer; the
  **file-transfer-only** session.
- **Classic RMM** (link docs/10) — org hierarchy, RBAC, script library,
  monitoring checks, scheduled tasks, inherited policies, alerts with
  auto-resolve, rich inventory.
- **MCP** (link docs/11) — external AI connectors, IP allowlist, scoped tokens.
- **Agent** (link docs/05) — single static binary, enroll → heartbeat →
  inventory → jobs, self-update via signed ETag.

Each section: short prose + a code/terminal snippet or a UI screenshot + a
"Learn more →" deep link.

### 6.3 Deploy / Get started (`/deploy`) — the conversion page
Mirror `docs/02-getting-started.md`, but as a polished, copy-friendly guide:
- **Prerequisites** (Docker, or Go toolchain).
- **1. Run the server** — `.env` setup (`RMM_ADMIN_TOKEN` via
  `openssl rand -hex 32`, AI keys) + `docker compose up -d` (postgres + server +
  nginx). Show the compose snippet.
- **2. First admin** — bootstrap the first admin user.
- **3. Enroll an agent** — create an enroll token, then the one-line installer
  for **Linux/macOS** (`curl … | sudo bash -s -- …`) and **Windows**
  (`iwr … | iex`). Tabs per OS.
- **4. Production hardening** — put it behind TLS (Caddy/nginx/Traefik), enable
  `RMM_REQUIRE_TLS`, signed agent updates (`RMM_UPDATE_VERIFY=strict`), MCP IP
  allowlist. Link docs/13.
- A persistent **copy button** on every command. Callouts for the security
  defaults (approval mandatory, admin token ≥ 32 chars).

### 6.4 Security (`/security`) — the trust page
This product runs code as root/SYSTEM across a fleet, so the security story is a
**feature**, not fine print. Pull from `docs/07`:
- **Human-in-the-loop, enforced in code** — the AI's only write action is
  `propose_script`; nothing runs without `POST /jobs/{id}/approve` by a human.
  External AI (MCP) has no execute/delete tools.
- **Immutable audit log** of every sensitive transition.
- **Crypto** — bcrypt passwords, AES-256-GCM for secrets at rest, agent tokens
  stored hashed, constant-time compares, **Ed25519-signed agent/sidecar
  updates**, short-lived remote-session tokens.
- **RBAC + 2FA + per-user access scope** (Company/Site/device), enforced on read,
  write, and the AI/MCP surface.
- **Independent security audit (2026-06)** summary — no SQL injection, approval
  guardrail holds, rate limiting, no secret leakage.
- Be honest: link the "threat-model notes / hardening TODO" so it reads as a
  mature, transparent project (builds more trust than pretending it's perfect).

### 6.5 Bring your own model (`/ai`)
- Provider table: **OpenAI** (`api.openai.com/v1`), **Ollama** (`localhost:11434/v1`),
  **vLLM** — any OpenAI-compatible `/chat/completions`.
- "Local model = data sovereignty" diagram.
- **MCP connectors** — connect Claude Desktop / ChatGPT to your fleet; secure
  exposure (Tailscale or reverse-proxy + TLS), scoped revocable tokens, IP
  allowlist. Same approval guardrail. Link docs/11.

### 6.6 Compare (`/vs`)
- **vs closed RMM SaaS** (Atera/SuperOps/Acronis/Neo): open source ✓,
  self-hosted ✓, bring-your-own-model ✓, no data egress ✓, AI-native ✓.
- **vs MeshCentral / Tactical RMM**: AI-native chat ✓, modern UI ✓, pluggable
  remote (incl. embedding MeshCentral) ✓.
- Keep it factual and fair (no FUD) — a clean checkmark table.

### 6.7 Roadmap (`/roadmap`)
Render `docs/09` as a visual phased timeline: Phases 0–4 **done** (skeleton, AI
chat, classic RMM, remote access, frontend), with "what's next" (osquery
inventory, patch management, hardware codecs, macOS engine, script sandboxing).
Clear ✅ / 🚧 / 🔜 states. Link the GitHub issues/milestones.

## 7. Documentation portal (`/docs`)

- Render the repo's **`docs/*.md` as MDX** with a left sidebar grouped by the
  numbered order (00→13). Starlight (or Fumadocs) gives sidebar + on-page TOC +
  search + "previous/next" + **Edit this page on GitHub** automatically.
- Keep the docs as the **single source of truth** — ideally the site **pulls the
  `.md` files from the repo** at build time (git submodule, or a build script
  that copies `docs/` into the content dir), so docs never drift from code.
- Code blocks: syntax highlighting + copy button. Render Mermaid/ASCII diagrams.
- Cross-links between docs must keep working (they reference each other).
- Add a top-level **"Architecture at a glance"** diagram (from `docs/01`).

## 8. Design language & tokens (match the product for brand cohesion)

Reuse the **exact tokens** from the product console so the site and app feel like
one brand. Dark-first, light supported.

```css
/* Brand */
--brand:    #6366f1;  /* indigo  */
--brandhi:  #818cf8;  /* indigo light (gradients, hovers) */
--brandsoft:rgba(99,102,241,.14);

/* Dark theme (default) */
--app:   #0a0b0d;  --panel: #0e1014;  --card: #15161b;  --elevated: #1c1e25;
--line:  #24262f;  --ink:   #e7e8ec;  --muted:#9396a1;   --faint:   #62656f;

/* Light theme */
--app:#f6f7f9; --panel:#ffffff; --card:#ffffff; --ink:#14161a; --muted:#5b606b;

/* Semantic (status) */
--ok:#34d399 (green) · --warn:#fbbf24 (amber) · --run:#60a5fa (blue) · --bad:#f87171 (red)
```

- **Type:** **Inter** (sans, UI/marketing) + **JetBrains Mono** (code, commands,
  IDs, terminal). Already the product's fonts.
- **Aesthetic:** dark slate canvas, single indigo accent, generous whitespace in
  marketing sections, dense/calm in docs. Rounded corners (10–14px). Subtle
  motion only (fade/slide on scroll, the hero terminal typing animation). A
  faint indigo radial glow at the top (the product uses
  `radial-gradient(circle at 50% 0%, rgba(99,102,241,.05), …)`).
- **Semantic color usage** should mirror the app: green=online/completed,
  amber=awaiting approval, blue=running, red=failed/offline — so screenshots and
  marketing diagrams are consistent.

## 9. Brand assets

- **Logo:** `openrmm/assets/icone_openrmm.png` (square, in the repo). Use it in
  the nav and footer. Provide an SVG version if possible for crispness.
- **Favicon:** `openrmm/assets/icone_page_openrmm.png` (square, in the repo).
- **Wordmark:** "OpenRMM" in Inter SemiBold; tagline *"Remote Monitoring &
  Management"* or *"Self-hosted · AI-native · Privacy-first"*.
- **Product screenshots:** capture from the running console (`openrmm/`) — the
  dashboard, the AI chat with a proposed-job card, the approval queue with a
  script preview, the remote viewer. These are the strongest marketing assets;
  use real ones, not invented mockups.

## 10. Components to build

- Sticky top nav (transparent → solid on scroll) with GitHub star count.
- **Animated hero terminal/chat** (typing effect → approval card).
- **Copy-command** block (tabbed per OS where relevant) with a copy button.
- Feature card grid; comparison checkmark table; phased roadmap timeline.
- Code block with syntax highlighting + copy (shiki).
- Docs layout: sidebar + content + on-page TOC + search (Starlight gives this).
- Theme toggle (dark/light), footer (docs links, GitHub, license, status).
- Privacy-friendly analytics (Plausible/Umami) — **no Google Analytics**.

## 11. SEO, performance, accessibility

- **SSG** every page (great Core Web Vitals; Astro ships ~0 JS by default).
- Per-page `<title>`/meta description/OpenGraph + Twitter cards; a generated
  **OG image** per doc/page. `sitemap.xml`, `robots.txt`, canonical URLs, JSON-LD
  (SoftwareApplication).
- Target **Lighthouse ≥ 95** across the board. Self-host fonts (don't block on
  Google Fonts). Lazy-load images; use AVIF/WebP.
- **A11y:** semantic HTML, keyboard nav, visible focus rings, WCAG AA contrast
  (the dark palette already aims for this), `prefers-reduced-motion` disables the
  hero animation.

## 12. Deploying the website itself

The site is static — host anywhere that serves SSG output: **Cloudflare Pages /
Netlify / Vercel / GitHub Pages**, or the same nginx box as OpenRMM. CI builds
on push to `main` and deploys the `dist/`. Point `openrmm.org` (apex + `www`) at
the host; if docs are a subdomain, add `docs.openrmm.org`. Keep it free of
trackers and external calls (consistent with the privacy message).

## 13. Content sources (don't invent — pull from the repo)

| Site section | Source of truth |
|---|---|
| Positioning / pitch | `README.md`, `docs/00-overview.md` |
| Architecture diagram | `docs/01-architecture.md` |
| Get started / deploy | `docs/02-getting-started.md`, `docker-compose.yml`, `deploy/public/install.{sh,ps1}` |
| AI chat & tools | `docs/06-ai-chat.md` |
| Guardrails / security | `docs/07-guardrails-security.md`, `docs/13-security-rollout.md`, `SECURITY.md` |
| Remote access | `docs/08-remote-access.md` |
| Monitoring / RBAC | `docs/10-monitoring-policies.md` |
| MCP | `docs/11-mcp.md` |
| Roadmap | `docs/09-roadmap.md` |
| API reference | `docs/04-api-reference.md` |
| Design tokens / brand | `openrmm/index.html` (`:root` vars), `openrmm/assets/*` |

> Rule: the website **paraphrases for marketing** on landing pages but renders
> the `docs/*.md` **verbatim** in `/docs`. Keep the docs portal wired to the repo
> so it never drifts from the code.

## 14. First build target (MVP)

1. Landing (`/`) with hero animation, how-it-works guardrail flow, feature grid,
   quick-start teaser, final CTA.
2. `/docs` portal rendering the existing `docs/*.md` with sidebar + search.
3. `/deploy` copy-paste self-host guide (per-OS enroll tabs).
4. `/security` trust page.

That covers "understand → trust → run it." Features/compare/roadmap/blog follow.
