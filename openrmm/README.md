# OpenRMM — Frontend (handoff para conectar ao backend)

Este projeto é o **frontend completo** do OpenRMM: um app React (Login, Devices,
Device detail, Jobs/Aprovações, AI Chat, Audit, Alerts, Checks, Tasks, Policies,
Scripts, Clients/Sites, Users/Settings) construído exatamente conforme o
[`../docs/14-frontend-design-brief.md`](../docs/14-frontend-design-brief.md).

Ele **já está conectado ao backend Go**: `src/mockapi.jsx` deixou de ser mock e
hoje é o cliente `fetch` real (`window.api`) que fala com a API (bearer token +
CORS); os dados simulados foram removidos. O cliente em `handoff/apiClient.ts` é a
versão TypeScript do mesmo contrato, para um futuro projeto Vite/TS. Como rodar
(backend + bootstrap + servir o front): ver `RUNNING.md`.

---

## TL;DR — como rodar

1. Suba o backend Go (default `http://localhost:8080`).
2. Aponte a base da API se não for o default: `<script>window.__API_BASE__ =
   "http://..."</script>` antes dos scripts em `index.html`.
3. Sirva o front (HTML estático) — veja `RUNNING.md` para o fluxo completo
   (backend + bootstrap do primeiro admin + servir).
4. Autenticação por bearer já implementada: o token de `POST /api/auth/login` é
   guardado em `localStorage` e anexado em todo `/api/*`; o gating por papel
   (`admin`/`tech`/`readonly`) também já existe.

> Toda a UI consome `api.*` (em `src/mockapi.jsx`, hoje o cliente `fetch` real) e
> não sabe se a origem é mock ou real — por isso a camada de dados fica isolada num
> único arquivo. `handoff/apiClient.ts` é a versão TypeScript do mesmo contrato,
> para um futuro projeto Vite/TS.

---

## Arquitetura

```
index.html              # shell: Tailwind config, tokens de tema, ordem dos scripts
src/
  icons.jsx             # ícones (SVG inline)
  mockapi.jsx           # camada de dados isolada: cliente fetch REAL, expõe window.api
  ui.jsx                # primitivos (Button, Pill, Drawer, Modal, Toast, CodeBlock, hooks)
  app.jsx               # auth, sidebar, router, gating por papel, monta o React
  screens-core.jsx      # Dashboard + helpers de layout
  screens-devices.jsx   # Devices + Device detail (Inventory/Jobs/Run script/Remote)
  screens-jobs.jsx      # Jobs & Aprovações (tela crítica) + Audit
  screens-chat.jsx      # AI Chat (tool steps + cards de "job proposto")
  screens-rmm.jsx       # Alerts, Checks, Scheduled tasks
  screens-rmm2.jsx      # Scripts, Policies, Clients/Sites, Users/Settings
handoff/
  apiClient.ts          # versão TypeScript do mesmo cliente (para um futuro projeto Vite/TS)
  INTEGRATION.md        # passo a passo + tabela de endpoints + papéis
```

**Importante:** os arquivos são um **protótipo de referência em HTML/React via
Babel** (sem build). Mostram look & feel e comportamento finais. Para produção,
o recomendado é recriar num projeto **Vite + React + TS + Tailwind + shadcn/ui +
TanStack Query** (stack do brief) — os componentes e a lógica aqui servem de base
fiel. Se preferir, dá para portar quase direto: a UI já é React e os hooks
`useQuery/useMutation` em `ui.jsx` têm a mesma semântica do TanStack Query.

---

## Camada de dados

`src/mockapi.jsx` (o cliente `fetch` real) define `window.api` com **exatamente**
estes métodos (todos retornam Promises). `handoff/apiClient.ts` tem a mesma
assinatura, em TypeScript:

| Método (`api.*`) | Vira a chamada HTTP |
|---|---|
| `login(user, pass)` | `POST /api/auth/login` |
| `me()` | `GET /api/me` |
| `logout()` | `POST /api/auth/logout` |
| `listDevices()` | `GET /api/devices` |
| `getDevice(id)` / `getInventory(id)` | `GET /api/devices/{id}` / `.../inventory` |
| `startRemote(id, provider)` | `POST /api/devices/{id}/remote` |
| `setDeviceSite(id, siteId)` | `PATCH /api/devices/{id}/site` |
| `listJobs({agent_id,limit})` | `GET /api/jobs` |
| `createJob(body)` | `POST /api/jobs` |
| `approveJob(id)` / `rejectJob(id)` | `POST /api/jobs/{id}/approve` · `/reject` |
| `listAudit(limit)` | `GET /api/audit` |
| `createEnrollToken(body)` | `POST /api/enroll-tokens` |
| `listSessions()` / `createSession()` | `GET/POST /api/chat/sessions` |
| `getMessages(id)` / `sendMessage(id, content)` | `GET/POST /api/chat/sessions/{id}/messages` |
| `listUsers/createUser/deleteUser` | `GET/POST/DELETE /api/users` |
| `listClients/createClient/...` | `GET/POST/DELETE /api/clients` · `/sites` |
| `listScripts/.../runScript` | `GET/POST/PUT/DELETE /api/scripts` · `/devices/{id}/run-script` |
| `listChecks/.../getCheckResults` | `GET/POST/PUT/DELETE /api/checks` · `/checks/{id}/results` |
| `listTasks/...` | `GET/POST/PUT/DELETE /api/tasks` |
| `listPolicies/.../assignments` | `GET/POST/DELETE /api/policies` · `/assignments` |
| `listAlerts(status)` / `ackAlert` / `resolveAlert` | `GET /api/alerts` · `/ack` · `/resolve` |
| `getConfig()` | exibição read-only (AI/remote/server) |

A tabela completa com bodies e shapes está em `handoff/INTEGRATION.md`.

---

## Regras de UX de segurança (já implementadas — não regredir)

1. **Ação da IA nunca aparece como já executada.** Jobs propostos pela IA são
   *propostas* com status `awaiting_approval` e o **script completo visível**.
2. **Aprovação é deliberada** — botão primário, com **confirmação digitada
   ("RUN")** quando o script parece destrutivo.
3. **Dry-run sempre** — o script aparece antes de rodar, toda vez.
4. **Trilha de auditoria visível** — quem aprovou o quê, sempre registrado.
5. **Gating por papel** — `readonly` vê tudo, mas ações de `tech`/`admin` ficam
   ocultas/bloqueadas (o backend também deve aplicar 403).

---

## Rodando o protótipo

É HTML estático — abra `index.html` num servidor estático qualquer (e suba o
backend Go antes; fluxo completo em `RUNNING.md`):

```bash
npx serve .        # ou: python3 -m http.server
```

Para apontar a base da API sem mudar código, antes dos scripts em `index.html`:

```html
<script>window.__API_BASE__ = "http://localhost:8080";</script>
```
