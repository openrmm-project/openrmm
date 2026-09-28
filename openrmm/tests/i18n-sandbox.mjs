// A vm context with the console's i18n runtime and every locale table loaded,
// for testing code that calls t() without a browser. setLocale() switches the
// language the same way the picker does.
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export async function i18nSandbox(...files) {
  const store = {};
  const sandbox = {
    window: {},
    React: {},
    useState: () => [null, () => {}],
    useEffect: () => {},
    localStorage: { getItem: (k) => store[k] ?? null, setItem: (k, v) => { store[k] = String(v); } },
    navigator: { languages: ['en'], language: 'en' },
    document: { documentElement: {} },
    Intl,
  };
  vm.createContext(sandbox);
  const run = async (rel) => vm.runInContext(await readFile(path.join(root, rel), 'utf8'), sandbox, { filename: rel });
  await run('src/i18n.jsx');
  const dir = path.join(root, 'src', 'locales');
  for (const name of (await readdir(dir)).filter((f) => f.endsWith('.jsx')).sort()) await run(path.join('src', 'locales', name));
  for (const f of files) await run(f);
  return sandbox;
}
