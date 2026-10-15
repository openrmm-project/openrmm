# Integração do frontend com o backend Go

O frontend já está integrado. `src/mockapi.jsx` é a implementação autoritativa
carregada no bundle e expõe `window.api`; o nome é legado.
`handoff/apiClient.ts` é somente uma referência TypeScript histórica, não
participa do build e não é garantido que acompanhe todos os métodos atuais.

## Transporte e sessão

- `config.js` define `window.__API_BASE__`; o default é `location.origin`.
- `OPENRMM_BASE_PATH` define onde shell e assets são construídos/servidos, não
  muda os paths `/api/*` e `/ws/remote`.
- Login guarda bearer, usuário, papel e permissões em
  `sessionStorage["rmm-ai-auth"]`.
- Toda chamada usa `Authorization: Bearer`; `401` encerra a sessão e `429` recebe
  retries limitados com backoff.
- O console entra por usuário/senha (com TOTP ou recovery code, quando o 2FA está
  ativo) ou por SSO (`/api/auth/oidc/start`, depois `POST /api/auth/oidc/exchange`
  com o código de uso único). Não aceita colar um token: tokens de API e o
  `RMM_ADMIN_TOKEN` servem à API e à automação, não ao login do console.
- Permission keys retornadas pelo backend controlam navegação e ações. O backend
  é autoritativo; não use gating visual como autorização.

## Contrato principal usado pela UI

`window.api` cobre:

- auth/me/password/2FA;
- devices, inventário, capabilities, metadata, site/group e remote;
- jobs individuais/bulk, aprovação, rejeição, cancelamento e complete manual;
- chat, auditoria e enrollment;
- usuários, escopos, papéis, permissões e API tokens;
- clientes, sites, departamentos, scripts, checks, tasks, policies e alerts;
- relatórios de software/hardware/máquinas/compliance;
- catálogo, scan/install/reboot, approvals e policies de patches;
- configuração runtime.

A lista de rotas, bodies e permissões autoritativa está em
[`../../docs/04-api-reference.md`](../../docs/04-api-reference.md). Ao alterar um
handler, atualize `src/mockapi.jsx`, a tela consumidora e essa referência TS se
ela continuar sendo mantida.

## Escopo e delegação

Papéis e escopo são ortogonais. Um usuário sem linhas em `user_scopes` é
irrestrito; com linhas, recebe a união de Company/Site/Department/device indicada.
Tokens vinculados a um usuário herdam esse escopo.

Um principal restrito com `users.manage` não pode ampliar privilégio ao delegar:

- só gerencia usuários cujas permissões são subconjunto das suas e cujos escopos
  estão inteiramente dentro do seu;
- não cria usuário irrestrito;
- não delega o papel global `admin`;
- não atribui papel com permissões que ele próprio não possui.

O preset `readonly` contém `devices.remote`. Ele não possui file transfer nem
terminal, mas ainda inicia desktop remoto; use papel customizado para observação
sem controle remoto.

## 2FA pendente

O backend não sobrescreve diretamente um TOTP ativo. `setup` grava um segredo
pendente criptografado; `enable` promove o candidato de forma atômica e troca os
recovery codes apenas após validar o novo código.

Bodies atuais:

```text
POST /api/me/2fa/setup
  { current_password, code? }
  code é obrigatório quando 2FA já está ativo (rotação)

POST /api/me/2fa/enable
  { code }

POST /api/me/2fa/disable
  { current_password, code }

POST /api/me/2fa/recovery-codes
  { code }
```

No console, `TwoFactorCard` pede a senha atual para iniciar o setup e senha mais
código para desativar, e regenera os recovery codes com o código atual. Ele não
oferece a rotação direta (`setup` com `code` enquanto o 2FA está ativo): pela UI,
troca-se de autenticador desativando e ativando de novo. Um código TOTP vale uma
vez só; o backend guarda o último passo aceito por usuário.

## Polling e WebSocket

A UI faz polling para listas/status; não existe WebSocket geral de jobs/alerts.
O WebSocket implementado no browser é o relay de remote. `GET /api/checks` já
traz o estado de cada check por dispositivo (`instances` e `summary`), então a
tela de Checks faz uma requisição só; o histórico de resultados é buscado ao abrir
um check. Considere o polling ao dimensionar o rate limit.

## Checklist

- `npm ci`, `npm test` e `npm run build` concluem.
- `dist/config.js`, base path e origem da API estão corretos.
- CSP bloqueia scripts externos/inline não autorizados.
- Login e expiração limpam `sessionStorage`.
- Um usuário scoped não vê nem atua fora do escopo.
- `readonly` tem remote desktop, mas não files/terminal.
- Job percorre `awaiting_approval` até resultado sem bypass implícito.
- Console e remote funcionam pelo edge TLS real.
