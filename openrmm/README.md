# OpenRMM Console

Frontend React conectado ao backend Go. `src/mockapi.jsx` manteve o nome
histórico, mas é o cliente `fetch` real (`window.api`); não contém dados mockados.
As telas cobrem login, dashboard, devices, jobs/aprovações, AI Chat, auditoria,
alertas, checks, tasks, policies, scripts, clientes/sites/departamentos, patches,
relatórios, usuários, papéis e configurações.

## Build atual

O frontend de produção **tem build**. React, xterm, QRCode, Tailwind e fontes são
dependências npm fixadas no lockfile e empacotadas localmente; não há Babel nem
CDN no navegador.

```bash
npm ci
npm test
npm run build
```

O build usa esbuild + PostCSS/Tailwind e recria `dist/`. A imagem Docker executa o
mesmo fluxo e copia apenas `dist/` para nginx. `npm run smoke` recompila e executa
o smoke test headless quando Chrome/Chromium está disponível.

Arquivos emitidos no base path configurado:

```text
dist/
  index.html
  config.js
  assets/app.js
  assets/app.css
  assets/styles.css
  assets/fonts/*
  assets/*.png
```

## Base path e API

`OPENRMM_BASE_PATH` é aplicado **no build** e deve ser um caminho com barras
inicial/final, por exemplo `/console/`. O script normaliza as barras e rejeita
origem, query, fragmento e segmentos `.`/`..`:

```bash
OPENRMM_BASE_PATH=/console/ npm run build
```

`index.html` carrega todos os assets e `config.js` desse caminho. `config.js`
define `window.__API_BASE__`, por padrão `location.origin`. No deploy de produção,
nginx gera `${OPENRMM_BASE_PATH}config.js` com a origem acessada. A CSP atual usa
`connect-src 'self'`, portanto o modo suportado é API/WebSocket na mesma origem.
Um backend cross-origin exige alterar deliberadamente a CSP além de CORS; apenas
trocar `config.js` não basta.

O backend e o build devem concordar sobre o base path. O proxy continua expondo
`/api/*` e `/ws/remote` na origem, enquanto o shell/assets ficam no base path.

## Segurança no navegador

- A sessão `{token,user,role,permissions}` fica em
  `sessionStorage["rmm-ai-auth"]`, não em `localStorage`. Ela sobrevive ao reload
  da aba, não a uma nova sessão do navegador. Preferências não secretas, como
  tema e colunas ocultas, usam `localStorage`.
- O bearer é anexado às chamadas de API; `401` limpa a sessão e `429` é repetido
  com backoff limitado.
- A UI usa permission keys (`jobs.manage`, `devices.remote`, etc.); papéis são
  apenas presets. O backend continua sendo o controle autoritativo e retorna
  `403`.
- O papel built-in `readonly` possui `devices.remote`. Portanto ele pode iniciar
  desktop remoto, embora não tenha file transfer nem terminal; não deve ser
  tratado como puramente observacional.
- O HTML e nginx aplicam CSP **enforcing** sem `unsafe-eval`; scripts, fontes e
  estilos de elemento são locais. `style-src-attr 'unsafe-inline'` permanece para
  estilos inline emitidos pela UI atual.
- A aba de remote nativo usa a mesma sessão da aba via `sessionStorage`; esse
  armazenamento não é compartilhado com abas abertas sem relação de sessão.

## Organização

```text
index.html                 shell e CSP
config.js                  configuração runtime da origem da API
scripts/build.mjs          bundle e base path
scripts/smoke.mjs          smoke headless
src/mockapi.jsx            cliente fetch real e adaptadores
src/ui.jsx                 primitivos e hooks de query/mutation
src/app.jsx                auth, navegação, permission gating e router
src/screens-*.jsx          telas
tests/build.test.mjs       invariantes do artefato de produção
handoff/apiClient.ts       referência TS histórica; fora do build e pode defasar
dist/                      saída gerada
```

## Estado conhecido

- Checks buscam o último resultado individualmente, causando uma requisição
  adicional por check.
- O inventário adapta o payload plano do agente; live CPU load não é coletado e
  software é limitado no agente.
- Jobs ad hoc continuam approval-gated pelo backend, independentemente da UI.
- O backend agora exige senha atual em setup/disable de 2FA e, durante rotação,
  também o TOTP ativo. O `TwoFactorCard`/cliente atual ainda não envia esses
  campos; setup, rotação e disable pela UI falham até essa integração ser
  atualizada. `enable` do segredo pendente e regeneração de recovery codes já
  usam o código esperado.

Veja [`RUNNING.md`](RUNNING.md) para execução e
[`handoff/INTEGRATION.md`](handoff/INTEGRATION.md) para o contrato da camada de
dados.
