import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import { transformSync } from 'esbuild';

const src = (name) => readFile(new URL('../src/' + name, import.meta.url), 'utf8');
const ui = await src('ui.jsx');
const csvCode = ui.slice(ui.indexOf('function csvEscape('), ui.indexOf('// downloadText saves'));
const { code } = transformSync(await src('screens-reports.jsx'), { loader: 'jsx' });

// Renders the Reports screen on the software tab with the given rows, presses
// Download CSV, and returns what was saved.
function exportReport(rows) {
  const downloads = [], states = [];
  let cursor = 0;
  const components = Object.fromEntries([...code.matchAll(/React\.createElement\(\s*([A-Z]\w*)/g)].map((m) => [m[1], m[1]]));
  const sandbox = {
    ...components,
    React: { createElement: (type, props, ...children) => ({ type, props: props || {}, children }) },
    useState(initial) {
      const i = cursor++;
      if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial;
      return [states[i], (v) => { states[i] = typeof v === 'function' ? v(states[i]) : v; }];
    },
    useEffect() {}, useRef: () => ({ current: null }), useMemo: (fn) => fn(),
    useAuth: () => ({ can: () => true }), useToast: () => ({ info() {}, success() {} }),
    useQuery: (key) => ({ data: key[0] === 'software' ? { rows } : {}, loading: false }),
    api: {}, localStorage: { getItem: () => null, setItem() {} },
    t: (k) => k, tn: (k) => k, cx: (...v) => v.filter(Boolean).join(' '), fmtTime: (v) => v, timeAgo: (v) => v,
    downloadText: (name, text) => downloads.push([name, text]),
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(csvCode + code, sandbox);
  const tree = sandbox.ReportsScreen();
  const find = (n, fn, out = []) => {
    if (!n || typeof n !== 'object') return out;
    if (fn(n)) out.push(n);
    (n.children || []).flat(Infinity).forEach((c) => find(c, fn, out));
    return out;
  };
  find(tree, (n) => n.type === 'Button' && n.props.icon === 'download')[0].props.onClick();
  return downloads;
}

// Software names and hostnames come from the endpoint's own inventory: they
// must not reach a spreadsheet as a formula, while ordinary text, commas and
// quotes survive the round trip.
test('Reports CSV export guards endpoint-controlled inventory text', () => {
  const rows = [
    { name: '=HYPERLINK("https://example.invalid","x")', manufacturer: '@SUM(A1)', version: '1.0', hostname: '=1+1', client: 'São Paulo, Ltd', site: '', department: '' },
    { name: '7-Zip', manufacturer: 'Igor "7z" Pavlov', version: '24.08', hostname: 'WINDOWS-TEST', client: 'Acme', site: 'HQ', department: 'IT' },
  ];
  const before = JSON.stringify(rows);
  const downloads = exportReport(rows);
  assert.equal(downloads.length, 1);
  const [name, csv] = downloads[0];
  assert.match(name, /^report-software-\d{4}-\d{2}-\d{2}\.csv$/);
  const lines = csv.split('\r\n');
  assert.equal(lines[0], 'rep.col.name,rep.col.manufacturer,rep.col.version,rep.col.hostname,rep.col.client,rep.col.site,rep.col.department');
  assert.equal(lines[1], '"\'=HYPERLINK(""https://example.invalid"",""x"")","\'@SUM(A1)",1.0,"\'=1+1","São Paulo, Ltd",,');
  assert.equal(lines[2], '7-Zip,"Igor ""7z"" Pavlov",24.08,WINDOWS-TEST,Acme,HQ,IT');
  assert.equal(JSON.stringify(rows), before, 'only the exported representation changes');
});
