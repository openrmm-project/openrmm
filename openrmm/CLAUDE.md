# CLAUDE.md — OpenRMM frontend

## O que é este projeto
Frontend completo do **OpenRMM** (RMM self-hosted com assistente de IA), em
React. Construído conforme `../docs/14-frontend-design-brief.md`. Telas: Login, Dashboard,
Devices (+detail), Jobs/Aprovações, AI Chat, Audit, Alerts, Checks, Tasks,
Policies, Scripts, Clients/Sites, Users/Settings. Dark-first, com light mode.

## Estado atual: JÁ CONECTADO ao backend Go
`src/mockapi.jsx` **deixou de ser mock** — agora é o cliente `fetch` real
(`window.api`) que fala com o backend Go (bearer token + CORS). Os dados
simulados foram removidos. Como rodar (backend + bootstrap + servir o front):
ver **`RUNNING.md`**.

Base da API: `window.__API_BASE__`, definida por `config.js` (default
`location.origin`). No deploy, nginx gera esse arquivo no `OPENRMM_BASE_PATH`;
a CSP atual restringe conexões a `self`, então o deployment suportado mantém
API/WebSocket na mesma origem.

Referência histórica: `handoff/apiClient.ts`. O cliente carregado e autoritativo
é `src/mockapi.jsx`; o arquivo TS não participa do build e pode estar defasado.

## Arquitetura (uma camada de dados)
- `src/mockapi.jsx` → cliente real e fonte de dados da UI.
- `src/ui.jsx` → primitivos + `useQuery/useMutation` (semântica de TanStack Query).
- `src/app.jsx` → auth (`sessionStorage` `rmm-ai-auth`), sidebar, router, gating.
- `src/screens-*.jsx` → telas.
- `handoff/apiClient.ts` → referência TS histórica, fora do build.

## Regras de UX que NÃO podem regredir
1. Ação proposta pela IA nunca aparece como já executada (status
   `awaiting_approval`, script visível).
2. Aprovação é deliberada (botão primário; confirmação digitada "RUN" p/ scripts
   destrutivos).
3. Dry-run sempre: mostrar o script antes de rodar.
4. Trilha de auditoria visível.
5. Gating por permission key (`jobs.create`, `jobs.manage`, `devices.terminal`, etc.; o backend aplica 403).

## Convenções
- O frontend de produção é gerado por `npm run build` em `dist/` (esbuild +
  PostCSS/Tailwind); não usa Babel/CDN no navegador.
- Cores/tipografia: usar os tokens `--*` definidos em `src/styles.css` e no
  `tailwind.config.cjs`.
- Não introduzir libs novas sem necessidade; preferir os primitivos de `ui.jsx`.
