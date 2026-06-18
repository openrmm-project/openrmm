# INTEGRATION.md — conectar o frontend ao backend Go

O frontend inteiro consome `api.*` (objeto `window.api`). Hoje esse objeto é
**simulado** em `src/mockapi.jsx`. Para usar o backend real, troque-o pelo
cliente `handoff/apiClient.ts` (já pronto, com a mesma assinatura).

## Opção A — manter o protótipo HTML (mais rápido)

1. Adicione no topo do `<body>` em `index.html` (antes dos `<script src="src/...">`):
   ```html
   <script>window.__API_BASE__ = "http://localhost:8080";</script>
   ```
2. Compile `handoff/apiClient.ts` para JS (ou reescreva como `.js` sem tipos) e
   carregue-o **no lugar de** `src/mockapi.jsx`, terminando o arquivo com:
   ```js
   window.api = api; window.ROLES = ROLES;
   ```
3. Pronto. `useQuery/useMutation` (em `ui.jsx`) continuam funcionando: eles fazem
   polling e revalidam via um event bus interno. Como agora os dados vêm do
   backend, troque o bus por refetch normal (ou mantenha — não atrapalha).

## Opção B — portar para Vite + React + TS (recomendado p/ produção)

Stack do brief: **Vite + React + TS + Tailwind + shadcn/ui + TanStack Query +
Monaco/CodeMirror**.

1. `npm create vite@latest rmm-ai -- --template react-ts`
2. Adicione Tailwind e cole os tokens de tema do `<style>` de `index.html`
   (variáveis `--app/--panel/--card/--line/--brand/--ok/--warn/--run/--bad`...).
   O `tailwind.config` (cores → `var(--*)`, fontes Inter/JetBrains Mono) está no
   `<script>tailwind.config=...</script>` do `index.html`.
3. Copie `handoff/apiClient.ts` para `src/lib/api.ts`. Defina `VITE_API_BASE` em
   `.env`.
4. Porte cada `screens-*.jsx` para componentes `.tsx`. A lógica é a mesma; troque
   `useQuery(deps, fn, {invalidateOn, poll})` pelo `useQuery` do TanStack:
   ```ts
   useQuery({ queryKey: ["jobs"], queryFn: () => api.listJobs(), refetchInterval: 2500 })
   ```
   e `useMutation` idem (invalide as queries no `onSuccess`).
5. Editor de código: o protótipo usa um highlighter leve (`CodeBlock`/
   `CodeEditor` em `ui.jsx`). Em produção, troque por **Monaco** ou **CodeMirror**
   mantendo o bloco "dry-run preview" e os números de linha.

## Autenticação (bearer) — já implementado em apiClient.ts

- `login()` faz `POST /api/auth/login`, guarda `{token, user, role}` em
  `localStorage["rmm-ai-auth"]`.
- Todo request anexa `Authorization: Bearer <token>`.
- `401` limpa a sessão (volta para o Login).
- Se a instância estiver vazia, mostre a tela de setup e chame `bootstrap()`
  (`POST /api/auth/bootstrap`). O frontend já tem a tela de login; adicione o
  ramo de bootstrap se quiser detectar "sem usuários".

## Gating por papel

`api.can("tech")` / `api.can("admin")` controlam a UI (botões de aprovar, ack,
CRUD). O backend **também** deve retornar `403` — o frontend trata o erro e
mostra o motivo. `readonly` = só leitura; `tech` = run/approve/ack; `admin` =
tudo.

## Polling vs WebSocket

O MVP faz polling (jobs a cada ~2.5s, alerts ~5s, chat ~1.2s). Quando expor um
WebSocket de jobs/resultados, troque o `refetchInterval` por invalidação via
socket nas mesmas query keys.

## Bodies e shapes principais

```
POST /api/jobs
  { agent_id, shell: "bash"|"sh"|"powershell"|"cmd", script, description, require_approval }
POST /api/devices/{id}/remote      { provider: "native"|"terminal"|"meshcentral" } -> { connect_url, ... }
PATCH /api/devices/{id}/site       { site_id }
POST /api/enroll-tokens            { description, uses, ttl_hours, site_id? } -> { token }  (mostrar 1x)
POST /api/chat/sessions/{id}/messages  { content } -> { content, created_job_ids: string[] }
POST /api/checks                   { type, name, config, warn_threshold?, err_threshold?, interval_sec, agent_id?|policy_id? }
GET  /api/checks/{id}/results?agent_id=  -> { results: CheckResult[] }
POST /api/policies/{id}/assignments    { scope_type: "client"|"site"|"agent", scope_id }
GET  /api/alerts?status=new|acked|resolved
```

Todos os tipos TypeScript estão em `handoff/apiClient.ts` (copiados do brief, com
os acréscimos `Agent.site_id` e `Agent.description`).

## `getConfig()` — ajustar

O protótipo exibe provider de IA / remote / versão do server em modo read-only.
Aponte `api.getConfig()` para o seu endpoint real (ex.: `GET /api/config`) ou
remova o card se não houver.

## Checklist de "ligado ao backend"

- [ ] `VITE_API_BASE` / `__API_BASE__` apontando para o backend
- [ ] `src/mockapi.jsx` substituído por `apiClient.ts` (sem dados simulados)
- [ ] Login real funciona e o token persiste entre reloads
- [ ] `403` em ação acima do papel mostra mensagem (não quebra a tela)
- [ ] Job: criar → `awaiting_approval` → approve → `running` → `completed`
- [ ] Chat: `created_job_ids` renderiza o card "job proposto" com o script
- [ ] Alerts: ack/resolve refletem o status do backend
