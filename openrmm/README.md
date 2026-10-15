# OpenRMM Console

Frontend React conectado ao backend Go. `src/mockapi.jsx` manteve o nome
histórico, mas é o cliente `fetch` real (`window.api`); não contém dados mockados.
As telas cobrem login (senha com segundo fator, ou SSO), dashboard, devices (com
as abas Inventory, Jobs, Checks, Tasks, Patches, Run script e Notes no detalhe),
jobs/aprovações, operações propostas pela IA, AI Chat, auditoria, alertas,
checks, tasks, policies, scripts, clientes/sites/departamentos, patches,
relatórios, usuários, papéis, tokens de API e aplicações MCP, configuração e
e-mail de alertas.

O console está traduzido em seis idiomas (`src/locales/`): inglês, português
(Brasil), espanhol, alemão, japonês e chinês simplificado; `tests/i18n*.test.mjs`
exige o conjunto completo de chaves em todos.

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

- O login é por usuário e senha (mais o segundo fator, quando ativo) ou por SSO;
  o console não aceita colar um token. No SSO, o callback devolve um código de uso
  único, válido por 60 segundos, que só o navegador que iniciou o login troca por
  uma sessão (`POST /api/auth/oidc/exchange`, com o cookie de vínculo).
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
scripts/build.mjs          bundle e base path (lista a ordem dos fontes)
scripts/smoke.mjs          smoke headless
src/i18n.jsx               t(), idiomas oferecidos e detecção do idioma
src/locales/*.jsx          uma tabela de textos por idioma
src/mockapi.jsx            cliente fetch real e adaptadores
src/ui.jsx                 primitivos, hooks de query/mutation e ErrorBoundary
src/labels.jsx             rótulos e formatação compartilhados
src/icons.jsx              ícones SVG
src/app.jsx                auth, navegação, permission gating e router
src/screens-*.jsx          telas
src/device-bulk.jsx        seleção e ações em lote de dispositivos
src/remote-input.jsx       teclado, atalhos e área de transferência do remoto
src/qr-adapter.js          QR do 2FA (qrcode-generator)
tests/*.test.mjs           invariantes do build, das telas, do cliente e do i18n
handoff/apiClient.ts       referência TS histórica; fora do build e pode defasar
dist/                      saída gerada
```

Cada arquivo de `src/` vira um bloco próprio no bundle: o que uma tela usa de
outro arquivo chega por `window` (por exemplo `window.checkType` e
`window.taskSchedule`, de `screens-rmm.jsx`, usados no detalhe do dispositivo).

## Estado conhecido

- Uma tela (ou aba de Configurações) que falha ao renderizar mostra um aviso com
  "Tentar de novo" no lugar dela; o resto do console continua funcionando. O erro
  vai para o console do navegador.
- O inventário adapta o payload plano do agente. A carga de CPU ao vivo não entra
  no inventário (só no check `cpuload`), e o agente envia no máximo 600 itens de
  software por máquina.
- Jobs ad hoc continuam approval-gated pelo backend, independentemente da UI.

Veja [`RUNNING.md`](RUNNING.md) para execução e
[`handoff/INTEGRATION.md`](handoff/INTEGRATION.md) para o contrato da camada de
dados.
