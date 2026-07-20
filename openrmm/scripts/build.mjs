import { readFile, writeFile, rm, mkdir, cp } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import autoprefixer from 'autoprefixer';
import { build, transform } from 'esbuild';
import postcss from 'postcss';
import tailwindcss from 'tailwindcss';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dist = path.join(root, 'dist');
const require = createRequire(import.meta.url);
const tailwindConfig = require(path.join(root, 'tailwind.config.cjs'));
const rawBasePath = (process.env.OPENRMM_BASE_PATH || '/').trim();
if (/[:?#]/.test(rawBasePath)) throw new Error('OPENRMM_BASE_PATH must be a URL path, not an origin, query, or fragment');
if (rawBasePath.split('/').some((part) => part === '.' || part === '..')) throw new Error('OPENRMM_BASE_PATH cannot contain . or .. segments');
const basePath = ('/' + rawBasePath.replace(/^\/+|\/+$/g, '') + '/').replace(/\/{2,}/g, '/');
const outputRoot = basePath === '/' ? dist : path.join(dist, basePath.slice(1, -1));
const assetsOut = path.join(outputRoot, 'assets');
const sources = [
  'src/icons.jsx',
  'src/mockapi.jsx',
  'src/ui.jsx',
  'src/screens-core.jsx',
  'src/screens-devices.jsx',
  'src/screens-jobs.jsx',
  'src/screens-chat.jsx',
  'src/screens-rmm.jsx',
  'src/screens-rmm2.jsx',
  'src/screens-reports.jsx',
  'src/screens-patches.jsx',
  'src/app.jsx',
];

await rm(dist, { recursive: true, force: true });
await mkdir(assetsOut, { recursive: true });

const sourceBlocks = await Promise.all(sources.map(async (name) => {
  const source = await readFile(path.join(root, name), 'utf8');
  return `\n{\n/* ${name} */\n${source}\n}\n`;
}));
const entry = `
import React from 'react';
import { createRoot } from 'react-dom/client';
import QRCode from 'qrcodejs';
import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import '@xterm/xterm/css/xterm.css';

window.React = React;
window.ReactDOM = { createRoot };
window.QRCode = QRCode;
window.Terminal = Terminal;
window.FitAddon = { FitAddon };
window.__BASE_PATH__ = ${JSON.stringify(basePath)};
Object.assign(window, {
  useState: React.useState,
  useEffect: React.useEffect,
  useRef: React.useRef,
  useCallback: React.useCallback,
  useMemo: React.useMemo,
  createContext: React.createContext,
  useContext: React.useContext,
});
${sourceBlocks.join('')}
`;

await build({
  absWorkingDir: root,
  stdin: { contents: entry, loader: 'jsx', resolveDir: root, sourcefile: 'openrmm-entry.jsx' },
  bundle: true,
  define: { 'process.env.NODE_ENV': '"production"' },
  minify: true,
  outfile: path.join(assetsOut, 'app.js'),
  assetNames: 'fonts/[name]-[hash]',
  loader: { '.woff2': 'file' },
  platform: 'browser',
  target: ['es2020'],
  legalComments: 'none',
});

const inputCss = await readFile(path.join(root, 'src/styles.css'), 'utf8');
const css = await postcss([tailwindcss(tailwindConfig), autoprefixer]).process(inputCss, {
  from: path.join(root, 'src/styles.css'),
  to: path.join(assetsOut, 'styles.css'),
});
const minifiedCss = await transform(css.css, { loader: 'css', minify: true });
await writeFile(path.join(assetsOut, 'styles.css'), minifiedCss.code);
await cp(path.join(root, 'assets'), path.join(outputRoot, 'assets'), { recursive: true, force: false });
const html = (await readFile(path.join(root, 'index.html'), 'utf8')).replaceAll('__OPENRMM_BASE_PATH__', basePath);
await Promise.all([
  writeFile(path.join(outputRoot, 'index.html'), html),
  cp(path.join(root, 'config.js'), path.join(outputRoot, 'config.js')),
]);
