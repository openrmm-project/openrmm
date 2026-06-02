# RMM-AI — Frontend (handoff para conectar ao backend)

Este projeto é o **frontend completo** do RMM-AI: um app React (Login, Devices,
Device detail, Jobs/Aprovações, AI Chat, Audit, Alerts, Checks, Tasks, Policies,
Scripts, Clients/Sites, Users/Settings) construído exatamente conforme o
`FRONTEND_DESIGN_BRIEF.md`.

Hoje ele roda contra uma **camada de API simulada** (mock) que espelha 1:1 os
endpoints, os tipos TypeScript e o mapa de status→cor do brief. Para colocar em
produção, você só precisa **trocar essa camada por chamadas `fetch` reais** ao
seu backend Go. Já deixei o cliente real escrito em `handoff/apiClient.ts`.

---

## TL;DR — como conectar ao backend

1. Suba o backend Go (default `http://localhost:8080`).
2. Aponte a base da API (variável `__API_BASE__` no protótipo, ou `VITE_API_BASE`
   num projeto Vite — veja abaixo).
3. Substitua o objeto `window.api` (em `src/mockapi.jsx`) pelo cliente real de
   `handoff/apiClient.ts`. **Só esse arquivo muda.** Toda a UI consome `api.*` e
   não sabe se a origem é mock ou real.
4. A autenticação por bearer token já está implementada: o token de
   `POST /api/auth/login` é guardado em `localStorage` e anexado em todo
   `/api/*`. O gating por papel (`admin`/`tech`/`readonly`) também já existe.

> **Abrindo no Claude Code?** Há um `CLAUDE.md` na raiz com o contexto da tarefa.
> Peça: *"troque a camada mock (src/mockapi.jsx) pelo cliente real de
> handoff/apiClient.ts e remova os dados simulados"* — está tudo mapeado.

---

## Arquitetura

```
index.html              # shell: Tailwind config, tokens de tema, ordem dos scripts
src/
  icons.jsx             # ícones (SVG inline)
  mockapi.jsx           # ⬅️ ÚNICA camada a trocar. Expõe window.api (todos os endpoints)
  ui.jsx                # primitivos (Button, Pill, Drawer, Modal, Toast, CodeBlock, hooks)
  app.jsx               # auth, sidebar, router, gating por papel, monta o React
  screens-core.jsx      # Dashboard + helpers de layout
  screens-devices.jsx   # Devices + Device detail (Inventory/Jobs/Run script/Remote)
  screens-jobs.jsx      # Jobs & Aprovações (tela crítica) + Audit
  screens-chat.jsx      # AI Chat (tool steps + cards de "job proposto")
  screens-rmm.jsx       # Alerts, Checks, Scheduled tasks
  screens-rmm2.jsx      # Scripts, Policies, Clients/Sites, Users/Settings
handoff/
  apiClient.ts          # ⬅️ cliente fetch REAL (pronto), espelha mockapi.jsx
  INTEGRATION.md        # passo a passo + tabela de endpoints + papéis
```

**Importante:** os arquivos são um **protótipo de referência em HTML/React via
Babel** (sem build). Mostram look & feel e comportamento finais. Para produção,
o recomendado é recriar num projeto **Vite + React + TS + Tailwind + shadcn/ui +
TanStack Query** (stack do brief) — os componentes e a lógica aqui servem de base
fiel. Se preferir, dá para portar quase direto: a UI já é React e os hooks
`useQuery/useMutation` em `ui.jsx` têm a mesma semântica do TanStack Query.

---

## Camada de dados (o que trocar)

`src/mockapi.jsx` define `window.api` com **exatamente** estes métodos (todos
retornam Promises). O cliente real em `handoff/apiClient.ts` tem a mesma
assinatura:

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

## Rodando o protótipo (como está, com mock)

É HTML estático — abra `index.html` num servidor estático qualquer:

```bash
npx serve .        # ou: python3 -m http.server
```

Para apontar a base da API sem mudar código, antes dos scripts em `index.html`:

```html
<script>window.__API_BASE__ = "http://localhost:8080";</script>
```

Para resetar os dados simulados: no console do browser, `window.__resetDB()`.
