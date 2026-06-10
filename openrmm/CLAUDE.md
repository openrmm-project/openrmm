# CLAUDE.md — OpenRMM frontend

## O que é este projeto
Frontend completo do **OpenRMM** (RMM self-hosted com assistente de IA), em
React. Construído conforme `FRONTEND_DESIGN_BRIEF.md`. Telas: Login, Dashboard,
Devices (+detail), Jobs/Aprovações, AI Chat, Audit, Alerts, Checks, Tasks,
Policies, Scripts, Clients/Sites, Users/Settings. Dark-first, com light mode.

## Estado atual: JÁ CONECTADO ao backend Go
`src/mockapi.jsx` **deixou de ser mock** — agora é o cliente `fetch` real
(`window.api`) que fala com o backend Go (bearer token + CORS). Os dados
simulados foram removidos. Como rodar (backend + bootstrap + servir o front):
ver **`RUNNING.md`**.

Base da API: `window.__API_BASE__` (default `http://localhost:8080`); para
sobrescrever, adicione um `<script>window.__API_BASE__='...'</script>` antes dos
script tags em `index.html`.

Referência: `handoff/apiClient.ts` (versão TS do mesmo cliente, para um futuro
projeto Vite/TS). Pontos de atenção da integração: seção final de `RUNNING.md`.

## Arquitetura (uma camada de dados)
- `src/mockapi.jsx` → **única** fonte de dados (trocar por backend real).
- `src/ui.jsx` → primitivos + `useQuery/useMutation` (semântica de TanStack Query).
- `src/app.jsx` → auth (localStorage `rmm-ai-auth`), sidebar, router, gating.
- `src/screens-*.jsx` → telas.
- `handoff/apiClient.ts` → cliente `fetch` real (destino da migração).

## Regras de UX que NÃO podem regredir
1. Ação proposta pela IA nunca aparece como já executada (status
   `awaiting_approval`, script visível).
2. Aprovação é deliberada (botão primário; confirmação digitada "RUN" p/ scripts
   destrutivos).
3. Dry-run sempre: mostrar o script antes de rodar.
4. Trilha de auditoria visível.
5. Gating por papel `admin`/`tech`/`readonly` (e o backend aplica 403).

## Convenções
- HTML/React via Babel (sem build) no protótipo. Manter HTML canônico (tags
  fechadas, atributos com aspas) para edição direta.
- Cores/tipografia: usar apenas os tokens `--*` definidos em `index.html`.
- Não introduzir libs novas sem necessidade; preferir os primitivos de `ui.jsx`.
