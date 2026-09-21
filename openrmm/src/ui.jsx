/* ui.jsx — shared primitives, query hooks, code viewer. Exports to window. */
const { useState, useEffect, useRef, useCallback, useMemo, createContext, useContext } = React;

const cx = (...a) => a.filter(Boolean).join(' ');

/* ------------------------------ time helpers ------------------------------ */
function timeAgo(iso) {
  if (!iso) return '—';
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return 'just now';
  if (s < 45) return s + 's ago';
  const m = Math.floor(s / 60); if (m < 60) return m + 'm ago';
  const h = Math.floor(m / 60); if (h < 24) return h + 'h ago';
  const d = Math.floor(h / 24); if (d < 30) return d + 'd ago';
  return new Date(iso).toLocaleDateString();
}
function fmtTime(iso) { if (!iso) return '—'; return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }); }
function fmtClock(iso) { if (!iso) return '—'; return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }); }
// Undefined in, em dash out: a missing window on a policy is a blank field, not
// "NaN:NaN" — and never an exception that takes the screen with it.
function minuteToHHMM(m) {
  const n = Number(m);
  if (!Number.isFinite(n)) return '—';
  const h = Math.floor(n / 60); const mm = Math.floor(n % 60);
  return String(h).padStart(2, '0') + ':' + String(mm).padStart(2, '0');
}

/* --------------------------- query / mutation ----------------------------- */
function useQuery(deps, fn, opts = {}) {
  const [state, setState] = useState({ data: opts.initial, loading: true, error: null });
  const fnRef = useRef(fn); fnRef.current = fn;
  const mounted = useRef(true);
  const load = useCallback(async (silent) => {
    if (!silent) setState((s) => ({ ...s, loading: s.data === undefined }));
    try { const d = await fnRef.current(); if (mounted.current) setState({ data: d, loading: false, error: null }); }
    catch (e) { if (mounted.current) setState((s) => ({ ...s, loading: false, error: e })); }
  }, []);
  useEffect(() => {
    mounted.current = true; load();
    const invalidate = opts.invalidateOn;
    const off = window.dataBus.sub((k) => { if (k === '*' || !invalidate || invalidate.includes(k)) load(true); });
    let t; if (opts.poll) t = setInterval(() => load(true), opts.poll);
    return () => { mounted.current = false; off(); if (t) clearInterval(t); };
    // eslint-disable-next-line
  }, deps);
  return { ...state, refetch: () => load(true) };
}

function useMutation(fn) {
  const [pending, setPending] = useState(false);
  const run = useCallback(async (...args) => { setPending(true); try { return await fn(...args); } finally { setPending(false); } }, [fn]);
  return [run, pending];
}

/* -------------------------------- toasts ---------------------------------- */
const ToastCtx = createContext(null);
function useToast() { return useContext(ToastCtx); }
function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const push = useCallback((t) => {
    const id = Math.random().toString(36).slice(2);
    setToasts((x) => [...x, { id, ...t }]);
    setTimeout(() => setToasts((x) => x.filter((y) => y.id !== id)), t.duration || 4200);
  }, []);
  const toast = useMemo(() => ({
    success: (title, msg) => push({ kind: 'success', title, msg }),
    error: (title, msg) => push({ kind: 'error', title, msg }),
    info: (title, msg) => push({ kind: 'info', title, msg }),
  }), [push]);
  return (
    <ToastCtx.Provider value={toast}>
      {children}
      <div className="fixed bottom-5 right-5 z-[200] flex flex-col gap-2.5 w-[360px] max-w-[calc(100vw-2rem)]">
        {toasts.map((t) => {
          const c = t.kind === 'success' ? 'ok' : t.kind === 'error' ? 'bad' : 'run';
          const icon = t.kind === 'success' ? 'approve' : t.kind === 'error' ? 'alert' : 'info';
          return (
            <div key={t.id} className="animate-slide-in flex gap-3 items-start rounded-xl border border-line bg-elevated/95 backdrop-blur px-3.5 py-3 shadow-2xl shadow-black/40">
              <span className={`mt-0.5 text-${c}`}><Icon name={icon} size={17} /></span>
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold text-ink">{t.title}</div>
                {t.msg && <div className="text-[12.5px] text-muted mt-0.5 leading-snug break-words">{t.msg}</div>}
              </div>
            </div>
          );
        })}
      </div>
    </ToastCtx.Provider>
  );
}

/* -------------------------------- buttons --------------------------------- */
function Button({ variant = 'secondary', size = 'md', icon, iconRight, children, className = '', loading, ...rest }) {
  const base = 'inline-flex items-center justify-center gap-2 font-medium rounded-lg transition-all duration-150 select-none disabled:opacity-45 disabled:pointer-events-none whitespace-nowrap focus:outline-none focus-visible:ring-2 focus-visible:ring-brand/60';
  const sizes = { sm: 'h-7 px-2.5 text-[12.5px]', md: 'h-9 px-3.5 text-[13px]', lg: 'h-11 px-5 text-[14px]', icon: 'h-9 w-9' };
  const variants = {
    primary: 'bg-brand text-white hover:bg-brandhi shadow-sm shadow-brand/30',
    secondary: 'bg-elevated text-ink border border-line2 hover:border-faint hover:bg-card',
    ghost: 'text-muted hover:text-ink hover:bg-elevated',
    subtle: 'bg-elevated/60 text-muted hover:text-ink hover:bg-elevated border border-line',
    danger: 'bg-bad/15 text-bad border border-bad/30 hover:bg-bad/25',
    success: 'bg-ok text-[#04130c] hover:brightness-110 shadow-sm shadow-ok/30 font-semibold',
  };
  return (
    <button className={cx(base, sizes[size], variants[variant], className)} {...rest}>
      {loading ? <Spinner size={size === 'lg' ? 16 : 14} /> : icon && <Icon name={icon} size={size === 'lg' ? 17 : 15} />}
      {children}
      {iconRight && <Icon name={iconRight} size={15} />}
    </button>
  );
}

function Spinner({ size = 16, className = '' }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" className={cx('animate-spin', className)} fill="none"><circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.25" strokeWidth="3" /><path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" /></svg>;
}

/* ------------------------------ status pill ------------------------------- */
const STATUS_META = {
  online: { c: 'ok', label: 'Online' }, completed: { c: 'ok', label: 'Completed' }, passing: { c: 'ok', label: 'Passing' },
  awaiting_approval: { c: 'warn', label: 'Awaiting approval' }, pending: { c: 'warn', label: 'Pending' }, warning: { c: 'warn', label: 'Warning' }, acked: { c: 'warn', label: 'Acked' }, new: { c: 'warn', label: 'New' },
  running: { c: 'run', label: 'Running' }, sent: { c: 'run', label: 'Sent' }, info: { c: 'run', label: 'Info' },
  failed: { c: 'bad', label: 'Failed' }, offline: { c: 'bad', label: 'Offline' }, rejected: { c: 'bad', label: 'Rejected' }, failing: { c: 'bad', label: 'Failing' }, error: { c: 'bad', label: 'Error' },
  suppressed: { c: 'run', label: 'Suppressed' },
  resolved: { c: 'muted', label: 'Resolved' }, disabled: { c: 'muted', label: 'Disabled' }, unknown: { c: 'muted', label: 'Unknown' },
};
function StatusPill({ status, label, size = 'md', pulse }) {
  const m = STATUS_META[status] || { c: 'muted', label: label || status };
  const c = m.c;
  const animate = pulse && (status === 'running' || status === 'sent' || status === 'pending');
  const colorMap = {
    ok: 'text-ok bg-oksoft border-ok/25', warn: 'text-warn bg-warnsoft border-warn/25',
    run: 'text-run bg-runsoft border-run/25', bad: 'text-bad bg-badsoft border-bad/25',
    muted: 'text-faint bg-elevated border-line2',
  };
  return (
    <span className={cx('inline-flex items-center gap-1.5 rounded-full border font-medium whitespace-nowrap', colorMap[c], size === 'sm' ? 'px-1.5 py-0.5 text-[10.5px]' : 'px-2 py-[3px] text-[11.5px]')}>
      <span className={cx('rounded-full', size === 'sm' ? 'h-1.5 w-1.5' : 'h-[7px] w-[7px]', c === 'ok' ? 'bg-ok' : c === 'warn' ? 'bg-warn' : c === 'run' ? 'bg-run' : c === 'bad' ? 'bg-bad' : 'bg-faint', animate && 'animate-pulse')} />
      {label || m.label}
    </span>
  );
}

/* ----------------------------- card / panel ------------------------------- */
function Card({ children, className = '', ...rest }) {
  return <div className={cx('rounded-2xl border border-line bg-card', className)} {...rest}>{children}</div>;
}

/* ------------------------------ drawer ------------------------------------ */
function Drawer({ open, onClose, children, width = 'max-w-[840px]' }) {
  useEffect(() => {
    if (!open) return;
    const h = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', h); return () => window.removeEventListener('keydown', h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-[120]">
      <div className="absolute inset-0 bg-black/55 backdrop-blur-[2px] animate-fade-in" onClick={onClose} />
      <div className={cx('absolute right-0 top-0 h-full w-full bg-panel border-l border-line shadow-2xl animate-slide-in flex flex-col', width)}>
        {children}
      </div>
    </div>
  );
}

/* ------------------------------- modal ------------------------------------ */
function Modal({ open, onClose, children, width = 'max-w-lg' }) {
  useEffect(() => {
    if (!open) return;
    const h = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', h); return () => window.removeEventListener('keydown', h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-[140] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-[2px] animate-fade-in" onClick={onClose} />
      <div className={cx('relative w-full max-h-[calc(100vh-2rem)] overflow-y-auto scroll-area rounded-2xl border border-line bg-panel shadow-2xl animate-scale-in', width)}>{children}</div>
    </div>
  );
}

/* -------------------------------- tabs ------------------------------------ */
function Tabs({ tabs, value, onChange, className = '' }) {
  return (
    <div className={cx('flex items-center gap-1 border-b border-line', className)}>
      {tabs.map((t) => (
        <button key={t.value} onClick={() => onChange(t.value)}
          className={cx('relative px-3.5 h-9 text-[13px] font-medium transition-colors -mb-px border-b-2',
            value === t.value ? 'text-ink border-brand' : 'text-faint border-transparent hover:text-muted')}>
          {t.label}{t.count != null && <span className="ml-1.5 text-[11px] text-faint tabular">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

/* ------------------------------ form controls ----------------------------- */
function Field({ label, hint, children, className = '' }) {
  return (
    <label className={cx('block', className)}>
      {label && <div className="text-[12px] font-medium text-muted mb-1.5">{label}</div>}
      {children}
      {hint && <div className="text-[11.5px] text-faint mt-1">{hint}</div>}
    </label>
  );
}
const inputCls = 'w-full h-9 rounded-lg bg-app border border-line2 px-3 text-[13px] text-ink placeholder:text-faint focus:outline-none focus:border-brand focus:ring-2 focus:ring-brand/25 transition';
function Input(props) { return <input {...props} className={cx(inputCls, props.className)} />; }
function Textarea(props) { return <textarea {...props} className={cx('w-full rounded-lg bg-app border border-line2 px-3 py-2 text-[13px] text-ink placeholder:text-faint focus:outline-none focus:border-brand focus:ring-2 focus:ring-brand/25 transition resize-none', props.className)} />; }
function Select({ children, ...props }) {
  return (
    <div className="relative">
      <select {...props} className={cx(inputCls, 'appearance-none pr-8 cursor-pointer', props.className)}>{children}</select>
      <Icon name="chevdown" size={14} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-faint pointer-events-none" />
    </div>
  );
}
// Combobox is a searchable single-select. Drop-in for <Select> when the option
// list is long: options are [{ value, label, group? }]; typing filters by label
// (and group). Keeps the native Select styling so the two read the same.
// creatable turns the picker into "choose one of these, or name a new one": the
// search box doubles as the field for a value that does not exist yet, offered
// as an explicit "Create …" row. Without it a free-text field is the only way to
// file something under a new name, and free text is how one category becomes
// two that differ by a keystroke.
// With creatable the value need not appear in options, so it is displayed as
// typed rather than falling back to the placeholder.
function Combobox({ value, onChange, options = [], placeholder = 'Select…', disabled, className, creatable }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const ref = useRef(null);
  const inputRef = useRef(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);
  useEffect(() => { if (open && inputRef.current) inputRef.current.focus(); }, [open]);
  const selected = options.find((o) => o.value === value);
  const ql = q.trim().toLowerCase();
  const filtered = ql ? options.filter((o) => o.label.toLowerCase().includes(ql) || (o.group || '').toLowerCase().includes(ql)) : options;
  const groups = [];
  filtered.forEach((o) => {
    const g = o.group || '';
    let bucket = groups.find((x) => x.name === g);
    if (!bucket) { bucket = { name: g, items: [] }; groups.push(bucket); }
    bucket.items.push(o);
  });
  const pick = (v) => { onChange(v); setOpen(false); setQ(''); };
  // Offer creation only for a name that is not already on the list — otherwise
  // "Create Networking" would sit under the Networking that already exists.
  const typed = q.trim();
  const canCreate = creatable && typed !== '' && !options.some((o) => o.label.toLowerCase() === typed.toLowerCase());
  const shown = selected ? selected.label : (creatable && value ? value : placeholder);
  return (
    <div className={cx('relative', className)} ref={ref}>
      <button type="button" disabled={disabled} onClick={() => setOpen((v) => !v)}
        className={cx(inputCls, 'appearance-none pr-8 text-left flex items-center', disabled ? 'opacity-60 cursor-not-allowed' : 'cursor-pointer', !selected && !(creatable && value) && 'text-faint')}>
        <span className="truncate">{shown}</span>
      </button>
      <Icon name="chevdown" size={14} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-faint pointer-events-none" />
      {open && (
        <div className="absolute z-30 mt-1 w-full rounded-lg bg-elevated border border-line shadow-xl overflow-hidden">
          <div className="p-1.5 border-b border-line">
            <div className="relative">
              <Icon name="search" size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-faint" />
              <input ref={inputRef} value={q} onChange={(e) => setQ(e.target.value)} placeholder={creatable ? 'Search or type a new one…' : 'Search…'}
                className={cx(inputCls, 'h-8 pl-8')}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') { setOpen(false); return; }
                  // Enter commits the typed name, so naming a new one never needs
                  // the mouse.
                  if (e.key === 'Enter' && canCreate) { e.preventDefault(); pick(typed); }
                }} />
            </div>
          </div>
          <div className="max-h-60 overflow-auto py-1">
            {groups.length === 0 && !canCreate && <div className="px-3 py-2 text-[12px] text-faint">No matches</div>}
            {canCreate && (
              <button type="button" onClick={() => pick(typed)}
                className="w-full text-left px-3 py-1.5 text-[13px] flex items-center gap-2 text-brandhi hover:bg-app transition">
                <Icon name="plus" size={14} className="shrink-0" />
                <span className="truncate">Create “{typed}”</span>
              </button>
            )}
            {canCreate && groups.length > 0 && <div className="my-1 border-t border-line" />}
            {groups.map((g) => (
              <div key={g.name || '_'}>
                {g.name && <div className="px-3 pt-1.5 pb-0.5 text-[10.5px] font-semibold uppercase tracking-wide text-faint">{g.name}</div>}
                {g.items.map((o) => (
                  <button key={o.value} type="button" onClick={() => pick(o.value)}
                    className={cx('w-full text-left px-3 py-1.5 text-[13px] flex items-center gap-2 hover:bg-app transition', o.value === value ? 'text-brandhi' : 'text-ink')}>
                    <span className="truncate flex-1">{o.label}</span>
                    {o.value === value && <Icon name="check" size={14} className="text-brandhi shrink-0" />}
                  </button>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
function Toggle({ checked, onChange, disabled }) {
  return (
    <button type="button" disabled={disabled} onClick={() => onChange(!checked)}
      className={cx('relative h-[22px] w-[38px] rounded-full transition-colors shrink-0 disabled:opacity-40', checked ? 'bg-brand' : 'bg-line2')}>
      <span className={cx('absolute top-[3px] h-4 w-4 rounded-full bg-white transition-all shadow', checked ? 'left-[19px]' : 'left-[3px]')} />
    </button>
  );
}

/* ------------------------------ misc bits --------------------------------- */
function Avatar({ name, size = 28 }) {
  const initials = (name || '?').split(/[@\s._-]/).filter(Boolean).slice(0, 2).map((s) => s[0].toUpperCase()).join('');
  const tone = (name || '').split('').reduce((a, c) => a + c.charCodeAt(0), 0) % 8;
  return <span className={cx('inline-flex items-center justify-center rounded-full font-semibold text-white shrink-0', `avatar-${size}`, `avatar-tone-${tone}`)}>{initials}</span>;
}
function RoleBadge({ role }) {
  const map = { admin: 'text-brandhi bg-brandsoft border-brand/30', tech: 'text-run bg-runsoft border-run/25', readonly: 'text-faint bg-elevated border-line2' };
  return <span className={cx('inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-wide', map[role] || map.readonly)}>{role}</span>;
}
function Kbd({ children }) { return <kbd className="inline-flex items-center h-5 px-1.5 rounded border border-line2 bg-elevated text-[10.5px] text-muted font-sans">{children}</kbd>; }

function Skeleton({ className = '' }) { return <div className={cx('relative overflow-hidden rounded-md bg-elevated', className)}><div className="absolute inset-0 -translate-x-full animate-[shimmer_1.4s_infinite] bg-gradient-to-r from-transparent via-white/[0.04] to-transparent" /></div>; }

function EmptyState({ icon = 'info', title, msg, action }) {
  return (
    <div className="flex flex-col items-center justify-center text-center py-16 px-6">
      <div className="h-12 w-12 rounded-2xl bg-elevated border border-line flex items-center justify-center text-faint mb-3.5"><Icon name={icon} size={22} /></div>
      <div className="text-[14px] font-semibold text-ink">{title}</div>
      {msg && <div className="text-[13px] text-muted mt-1 max-w-sm">{msg}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

function Tip({ children, label }) {
  return (
    <span className="relative group inline-flex">
      {children}
      <span className="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 whitespace-nowrap rounded-md bg-elevated border border-line2 px-2 py-1 text-[11px] text-ink opacity-0 group-hover:opacity-100 transition-opacity z-50 shadow-lg">{label}</span>
    </span>
  );
}

// copyText copies to the clipboard, falling back to a legacy execCommand path.
// The async Clipboard API (navigator.clipboard) only exists in SECURE contexts
// (HTTPS or localhost); when the console is served over plain HTTP on a LAN IP it
// is undefined, so we drop to a hidden-textarea + execCommand('copy'). Returns
// whether the copy succeeded so the UI only confirms on success.
function copyText(value) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(value).catch(() => execCopy(value));
      return true;
    }
  } catch (e) { /* fall through */ }
  return execCopy(value);
}

function execCopy(value) {
  try {
    const ta = document.createElement('textarea');
    ta.value = value;
    ta.setAttribute('readonly', '');
    ta.className = 'clipboard-fallback';
    document.body.appendChild(ta);
    ta.select();
    ta.setSelectionRange(0, value.length);
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch (e) {
    return false;
  }
}

function CopyButton({ value, label = 'Copy', size = 'sm', variant = 'subtle' }) {
  const [done, setDone] = useState(false);
  const toast = useToast();
  const onCopy = () => {
    if (copyText(value)) {
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    } else {
      toast?.error?.('Could not copy', 'Select the text and copy it manually (Ctrl+C).');
    }
  };
  return (
    <Button size={size} variant={variant} icon={done ? 'approve' : 'copy'} onClick={onCopy}>
      {done ? 'Copied' : label}
    </Button>
  );
}

/* ============================ CODE HIGHLIGHTER ============================= */
const CODE_COLORS = { comment: 'code-comment', string: 'code-string', variable: 'code-variable', number: 'code-number', flag: 'code-flag', keyword: 'code-keyword', builtin: 'code-builtin', op: 'code-op' };
const BASH_KW = new Set(['set', 'if', 'then', 'else', 'elif', 'fi', 'for', 'while', 'do', 'done', 'case', 'esac', 'function', 'return', 'exit', 'local', 'export', 'in', 'sudo', 'source']);
const BASH_BUILTIN = new Set(['echo', 'apt', 'apt-get', 'find', 'awk', 'df', 'du', 'grep', 'sed', 'cat', 'curl', 'wget', 'systemctl', 'service', 'uptime', 'btrfs', 'mdls', 'defaults', 'rm', 'ls', 'ps', 'kill', 'chmod', 'chown', 'head', 'tail', 'printf', 'ipconfig']);
const PS_KW = new Set(['if', 'else', 'elseif', 'foreach', 'param', 'function', 'try', 'catch', 'finally', 'return', 'while', 'for', 'switch', 'in', 'iex']);
const PY_KW = new Set(['and', 'as', 'assert', 'break', 'class', 'continue', 'def', 'del', 'elif', 'else', 'except', 'finally', 'for', 'from', 'global', 'if', 'import', 'in', 'is', 'lambda', 'nonlocal', 'not', 'or', 'pass', 'raise', 'return', 'try', 'while', 'with', 'yield', 'None', 'True', 'False', 'async', 'await']);
const PY_BUILTIN = new Set(['print', 'len', 'range', 'open', 'str', 'int', 'float', 'bool', 'list', 'dict', 'set', 'tuple', 'sorted', 'sum', 'min', 'max', 'abs', 'enumerate', 'zip', 'isinstance', 'type', 'input', 'format', 'repr', 'exit', 'self']);

function tokenizeLine(line, shell) {
  const isPS = shell === 'powershell';
  const isCmd = shell === 'cmd';
  const isPy = shell === 'python';
  const nodes = []; let key = 0;
  // whole-line comment for cmd REM / ::
  if (isCmd && /^\s*(rem|::)/i.test(line)) { return [<span key={0} className={CODE_COLORS.comment}>{line}</span>]; }
  const re = /(#[^\n]*)|("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(\$\{[^}]+\}|\$[A-Za-z_][\w]*|\$[!@#?*]?)|(\b\d+(?:\.\d+)?\b)|(--?[A-Za-z][\w-]*)|([A-Za-z_][\w-]*)/g;
  let last = 0, m;
  while ((m = re.exec(line))) {
    if (m.index > last) nodes.push(<span key={key++}>{line.slice(last, m.index)}</span>);
    let color = null, txt = m[0];
    if (m[1]) color = CODE_COLORS.comment;
    else if (m[2]) color = CODE_COLORS.string;
    else if (m[3]) color = CODE_COLORS.variable;
    else if (m[4]) color = CODE_COLORS.number;
    else if (m[5]) color = CODE_COLORS.flag;
    else if (m[6]) {
      const w = m[6];
      if (isPS) { if (/^[A-Z][a-z]+-[A-Za-z]+$/.test(w)) color = CODE_COLORS.builtin; else if (PS_KW.has(w.toLowerCase())) color = CODE_COLORS.keyword; }
      else if (isPy) { if (PY_KW.has(w)) color = CODE_COLORS.keyword; else if (PY_BUILTIN.has(w)) color = CODE_COLORS.builtin; }
      else { if (BASH_KW.has(w)) color = CODE_COLORS.keyword; else if (BASH_BUILTIN.has(w)) color = CODE_COLORS.builtin; }
    }
    nodes.push(color ? <span key={key++} className={color}>{txt}</span> : <span key={key++}>{txt}</span>);
    last = m.index + m[0].length;
  }
  if (last < line.length) nodes.push(<span key={key++}>{line.slice(last)}</span>);
  return nodes;
}

// wrap soft-wraps long lines instead of scrolling them sideways. Off by default:
// a script is easier to read when its indentation is literal and a long line is
// visibly long. It is for the one-line commands an operator has to read and copy
// — an install command scrolled off the right edge is one they cannot check
// before running it as Administrator.
function CodeBlock({ code = '', shell = 'bash', maxHeight = 320, showCopy = true, label, dryRun, wrap }) {
  const lines = code.replace(/\n$/, '').split('\n');
  return (
    <div className="rounded-xl border border-line2 bg-[#0a0b0e] overflow-hidden">
      {(label || dryRun || showCopy) && (
        <div className="flex items-center justify-between px-3 h-9 border-b border-line bg-elevated/40">
          <div className="flex items-center gap-2 min-w-0">
            <Icon name="terminal" size={13} className="text-faint" />
            <span className="text-[11.5px] text-muted font-medium">{label || shell}</span>
            {dryRun && <span className="inline-flex items-center gap-1 rounded-md bg-warnsoft border border-warn/25 text-warn px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide whitespace-nowrap"><Icon name="eye" size={11} />Dry-run preview</span>}
          </div>
          {showCopy && <CopyButton value={code} variant="ghost" size="sm" />}
        </div>
      )}
      <div className={cx('overflow-auto scroll-area mono text-[12.5px] leading-[1.65]', `max-h-code-${maxHeight}`)}>
        <table className="w-full border-collapse">
          <tbody>
            {lines.map((ln, i) => (
              <tr key={i} className="hover:bg-white/[0.02]">
                <td className="select-none text-right pr-3 pl-3 text-[#3a4150] w-10 align-top tabular sticky left-0 bg-[#0a0b0e]">{i + 1}</td>
                <td className={cx('pr-4 py-px align-top text-[#c8d0e0]', wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre')}>{tokenizeLine(ln, shell).length ? tokenizeLine(ln, shell) : '\u00a0'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/* editable code editor: textarea + highlighted underlay */
function CodeEditor({ value, onChange, shell = 'bash', minHeight = 200, placeholder }) {
  const lines = (value || '').split('\n');
  const taRef = useRef(null);
  const preRef = useRef(null);
  const sync = (e) => { if (preRef.current) { preRef.current.scrollTop = e.target.scrollTop; preRef.current.scrollLeft = e.target.scrollLeft; } };
  return (
    <div className="rounded-xl border border-line2 bg-[#0a0b0e] overflow-hidden focus-within:border-brand focus-within:ring-2 focus-within:ring-brand/25 transition">
      <div className="flex items-center gap-2 px-3 h-8 border-b border-line bg-elevated/40">
        <Icon name="terminal" size={12} className="text-faint" />
        <span className="text-[11px] text-muted font-medium">{shell}</span>
        <span className="ml-auto text-[10.5px] text-faint">{lines.length} lines</span>
      </div>
      <div className={cx('relative mono text-[12.5px] leading-[1.65]', `min-h-code-${minHeight}`)}>
        <pre ref={preRef} aria-hidden="true" className="absolute inset-0 m-0 p-3 overflow-auto whitespace-pre pointer-events-none text-[#c8d0e0]">
          {lines.map((ln, i) => <div key={i}>{tokenizeLine(ln, shell).length ? tokenizeLine(ln, shell) : '\u00a0'}</div>)}
        </pre>
        <textarea ref={taRef} value={value} onChange={(e) => onChange(e.target.value)} onScroll={sync} spellCheck={false} placeholder={placeholder}
          className={cx('absolute inset-0 w-full h-full m-0 p-3 bg-transparent text-transparent caret-white resize-none outline-none whitespace-pre overflow-auto scroll-area placeholder:text-faint', `min-h-code-${minHeight}`)} />
      </div>
    </div>
  );
}

/* output console block */
function OutputBlock({ stdout, stderr, exitCode }) {
  const has = stdout || stderr;
  return (
    <div className="rounded-xl border border-line2 bg-[#0a0b0e] overflow-hidden">
      <div className="flex items-center justify-between px-3 h-8 border-b border-line bg-elevated/40">
        <span className="text-[11.5px] text-muted font-medium flex items-center gap-1.5"><Icon name="terminal" size={12} />Output</span>
        {exitCode != null && <span className={cx('text-[11px] font-semibold mono', exitCode === 0 ? 'text-ok' : 'text-bad')}>exit {exitCode}</span>}
      </div>
      <div className="p-3 mono text-[12px] leading-relaxed max-h-64 overflow-auto scroll-area whitespace-pre-wrap">
        {stdout && <div className="text-[#c8d0e0]">{stdout}</div>}
        {stderr && <div className="text-bad mt-1">{stderr}</div>}
        {!has && <div className="text-faint">no output</div>}
      </div>
    </div>
  );
}

/* sparkline for check history */
function Sparkline({ data, width = 160, height = 36, colorFn }) {
  if (!data || !data.length) return null;
  const vals = data.map((d) => d.value ?? 0);
  const min = Math.min(...vals), max = Math.max(...vals, 1);
  const range = max - min || 1;
  const pts = data.map((d, i) => {
    const x = (i / (data.length - 1 || 1)) * width;
    const y = height - 4 - ((d.value ?? 0) - min) / range * (height - 8);
    return [x, y];
  });
  const path = pts.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ');
  const last = data[data.length - 1];
  const lc = last.status === 'failing' ? 'var(--bad)' : last.status === 'warning' ? 'var(--warn)' : 'var(--ok)';
  return (
    <svg width={width} height={height} className="overflow-visible">
      <path d={path} fill="none" stroke={lc} strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" opacity="0.85" />
      {pts.length > 0 && <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r="2.5" fill={lc} />}
    </svg>
  );
}

/* ------------------------------ csv / download ---------------------------- */
// csvEscape quotes a value when it contains a comma, quote or newline.
function csvEscape(v) { const s = v == null ? '' : String(v); return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; }
// toCSV builds a CRLF-joined CSV string from a header row + data rows.
function toCSV(headers, rows) { return [headers, ...rows].map((r) => r.map(csvEscape).join(',')).join('\r\n'); }
// downloadText saves text as a file from the browser — no server/endpoint write.
function downloadText(name, text, mime) {
  const url = URL.createObjectURL(new Blob([text], { type: (mime || 'text/plain') + ';charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/* confirm dialog hook-ish component */
function ConfirmDialog({ open, onClose, onConfirm, title, body, confirmLabel = 'Confirm', danger, requireType }) {
  const [typed, setTyped] = useState('');
  useEffect(() => { if (open) setTyped(''); }, [open]);
  const ok = !requireType || typed.trim().toUpperCase() === requireType.toUpperCase();
  return (
    <Modal open={open} onClose={onClose} width="max-w-md">
      <div className="p-5">
        <div className="flex items-start gap-3">
          <div className={cx('h-9 w-9 rounded-xl flex items-center justify-center shrink-0', danger ? 'bg-badsoft text-bad' : 'bg-brandsoft text-brandhi')}><Icon name={danger ? 'alert' : 'shield'} size={18} /></div>
          <div className="flex-1 min-w-0">
            <div className="text-[15px] font-semibold text-ink">{title}</div>
            <div className="text-[13px] text-muted mt-1 leading-relaxed">{body}</div>
          </div>
        </div>
        {requireType && (
          <div className="mt-4">
            <Field label={`Type ${requireType} to confirm`}>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={requireType} autoFocus />
            </Field>
          </div>
        )}
        <div className="flex justify-end gap-2 mt-5">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant={danger ? 'danger' : 'primary'} disabled={!ok} onClick={() => { onConfirm(); onClose(); }}>{confirmLabel}</Button>
        </div>
      </div>
    </Modal>
  );
}

Object.assign(window, {
  cx, timeAgo, fmtTime, fmtClock, minuteToHHMM, useQuery, useMutation, useToast, ToastProvider,
  Button, Spinner, StatusPill, STATUS_META, Card, Drawer, Modal, Tabs, Field, Input, Textarea, Select, Combobox, Toggle, inputCls,
  Avatar, RoleBadge, Kbd, Skeleton, EmptyState, Tip, CopyButton, CodeBlock, CodeEditor, OutputBlock, Sparkline, ConfirmDialog,
  csvEscape, toCSV, downloadText,
});
