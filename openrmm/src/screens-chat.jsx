/* screens-chat.jsx — AI Chat: sessions, transparent tool steps, proposed-job cards */

// inlineBold renders **bold** spans within a line of text.
function inlineBold(text, keyPrefix) {
  return text.split(/(\*\*[^*]+\*\*)/g).map((p, j) => p.startsWith('**') && p.endsWith('**')
    ? <b key={keyPrefix + j} className="font-semibold text-ink">{p.slice(2, -2)}</b>
    : <span key={keyPrefix + j}>{p}</span>);
}

// isSep reports whether a line is a Markdown table separator (| --- | :--: |).
function isSep(line) {
  const cells = line.trim().replace(/^\||\|$/g, '').split('|');
  return cells.length > 0 && cells.every((c) => /^\s*:?-{2,}:?\s*$/.test(c));
}
function splitRow(line) {
  return line.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
}

// parseBlocks turns assistant text into blocks: text / table / code(csv).
function parseBlocks(text) {
  const lines = (text || '').split('\n');
  const blocks = [];
  let buf = [];
  const flush = () => { if (buf.length) { blocks.push({ type: 'text', lines: buf }); buf = []; } };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // fenced code block ```lang ... ```
    const fence = line.match(/^\s*```(\w*)\s*$/);
    if (fence) {
      flush();
      const lang = fence[1].toLowerCase();
      const body = [];
      i++;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) { body.push(lines[i]); i++; }
      blocks.push({ type: 'code', lang, content: body.join('\n') });
      continue;
    }
    // GitHub-style table: a row line followed by a separator line
    if (line.includes('|') && i + 1 < lines.length && isSep(lines[i + 1])) {
      flush();
      const headers = splitRow(line);
      const rows = [];
      i += 2;
      while (i < lines.length && lines[i].includes('|') && lines[i].trim() !== '') { rows.push(splitRow(lines[i])); i++; }
      i--;
      blocks.push({ type: 'table', headers, rows });
      continue;
    }
    buf.push(line);
  }
  flush();
  return blocks;
}

// tabularCSV returns a downloadable CSV from the first table or ```csv block, or null.
function tabularCSV(blocks) {
  const esc = (v) => { const s = v == null ? '' : String(v); return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; };
  for (const b of blocks) {
    if (b.type === 'code' && b.lang === 'csv' && b.content.trim()) return b.content.trim();
    if (b.type === 'table') return [b.headers, ...b.rows].map((r) => r.map(esc).join(',')).join('\r\n');
  }
  return null;
}

// downloadText saves text as a file from the browser — no server/endpoint write.
function downloadText(name, text, mime) {
  const url = URL.createObjectURL(new Blob([text], { type: (mime || 'text/plain') + ';charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function renderRich(text) {
  return parseBlocks(text).map((b, bi) => {
    if (b.type === 'table') {
      return (
        <div key={bi} className="my-2 overflow-x-auto rounded-lg border border-line">
          <table className="w-full text-[12px] border-collapse">
            <thead><tr className="bg-elevated/60">{b.headers.map((h, j) => <th key={j} className="text-left font-semibold text-ink px-2.5 py-1.5 border-b border-line whitespace-nowrap">{inlineBold(h, bi + '-h' + j + '-')}</th>)}</tr></thead>
            <tbody>{b.rows.map((r, ri) => <tr key={ri} className="odd:bg-card/40">{r.map((c, ci) => <td key={ci} className="px-2.5 py-1.5 border-b border-line/50 text-muted align-top">{inlineBold(c, bi + '-' + ri + '-' + ci + '-')}</td>)}</tr>)}</tbody>
          </table>
        </div>
      );
    }
    if (b.type === 'code') {
      return <pre key={bi} className="my-2 rounded-lg border border-line bg-[#0a0b0e] p-2.5 text-[11.5px] mono text-[#c8d0e0] overflow-x-auto whitespace-pre">{b.content}</pre>;
    }
    return b.lines.map((line, i) => {
      if (line.trim() === '') return <div key={bi + '-' + i} className="h-2" />;
      const bullet = /^\s*[•\-]\s+/.test(line);
      const clean = line.replace(/^\s*[•\-]\s+/, '');
      return <div key={bi + '-' + i} className={cx('leading-relaxed', bullet && 'flex gap-2 pl-1')}>{bullet && <span className="text-brandhi mt-0.5">•</span>}<span>{inlineBold(clean, bi + '-' + i + '-')}</span></div>;
    });
  });
}

function ChatScreen({ navigate }) {
  const sessionsQ = useQuery(['chat-sessions'], () => api.listSessions(), { invalidateOn: ['chat'] });
  const [active, setActive] = useState(null);
  const sessions = sessionsQ.data?.sessions || [];

  // initialize selection + handle dashboard "ask the AI" handoff
  useEffect(() => {
    const prompt = sessionStorage.getItem('rmm-ask');
    if (prompt) {
      sessionStorage.removeItem('rmm-ask');
      api.createSession(prompt.slice(0, 48)).then((s) => { setActive(s.id); setTimeout(() => api.sendMessage(s.id, prompt), 120); });
      return;
    }
    if (!active && sessions.length) setActive(sessions[0].id);
  }, [sessions.length]);

  const newChat = async () => { const s = await api.createSession('New conversation'); setActive(s.id); };

  return (
    <div className="h-full flex">
      {/* sessions */}
      <div className="w-[252px] shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="p-3 border-b border-line">
          <Button variant="primary" icon="plus" className="w-full" onClick={newChat}>New conversation</Button>
        </div>
        <div className="flex-1 overflow-y-auto scroll-area p-2">
          {sessions.map((s) => (
            <button key={s.id} onClick={() => setActive(s.id)} className={cx('w-full text-left rounded-lg px-2.5 py-2 mb-0.5 transition group', active === s.id ? 'bg-elevated' : 'hover:bg-elevated/50')}>
              <div className="flex items-center gap-2">
                <Icon name="chat" size={13} className={active === s.id ? 'text-brandhi' : 'text-faint'} />
                <span className={cx('text-[12.5px] truncate flex-1', active === s.id ? 'text-ink font-medium' : 'text-muted')}>{s.title}</span>
              </div>
              <div className="text-[10.5px] text-faint mt-0.5 pl-5">{timeAgo(s.created_at)}</div>
            </button>
          ))}
        </div>
      </div>

      {/* thread */}
      <div className="flex-1 min-w-0 flex flex-col bg-app">
        {active ? <ChatThread key={active} sessionId={active} navigate={navigate} /> : (
          <div className="flex-1 flex items-center justify-center"><EmptyState icon="sparkle" title="Start a conversation" msg="Ask the AI to inspect or act on your fleet. It proposes; you approve." action={<Button variant="primary" icon="plus" onClick={newChat}>New conversation</Button>} /></div>
        )}
      </div>
    </div>
  );
}

const SUGGESTIONS = [
  'Install 7-zip on the finance group',
  'Give me a report of offline devices',
  'PLANT-HMI-01 disk is almost full, fix it',
  'Restart the print spooler on the domain controller',
];

function ChatThread({ sessionId, navigate }) {
  const msgsQ = useQuery([sessionId, 'msgs'], () => api.getMessages(sessionId), { invalidateOn: ['chat'], poll: 1200 });
  const [input, setInput] = useState('');
  const [working, setWorking] = useState(false);
  const scrollRef = useRef(null);
  const messages = (msgsQ.data?.messages || []).filter((m) => m.role !== 'system');

  useEffect(() => { if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight; }, [messages.length, working]);
  useEffect(() => {
    // stop the working indicator once the latest assistant message arrives
    if (working) { const last = messages[messages.length - 1]; if (last && last.role === 'assistant') setWorking(false); }
  }, [messages.length]);

  const send = async (text) => {
    const content = (text ?? input).trim();
    if (!content || working) return;
    setInput(''); setWorking(true);
    try { await api.sendMessage(sessionId, content); } finally { setWorking(false); }
  };

  const empty = messages.length === 0;

  return (
    <>
      <div ref={scrollRef} className="flex-1 overflow-y-auto overflow-x-hidden scroll-area">
        <div className="max-w-[760px] mx-auto px-5 py-6 min-w-0">
          {empty && !working && (
            <div className="pt-10">
              <div className="flex items-center gap-2.5 mb-2"><div className="h-9 w-9 rounded-xl bg-brandsoft border border-brand/30 flex items-center justify-center text-brandhi"><Icon name="sparkle" size={18} /></div><div className="text-[15px] font-semibold text-ink">How can I help with your fleet?</div></div>
              <p className="text-[13px] text-muted ml-[46px] mb-5">I can inspect devices, propose scripts, and queue actions. I never run anything without your approval.</p>
              <div className="grid sm:grid-cols-2 gap-2 ml-[46px]">
                {SUGGESTIONS.map((s) => <button key={s} onClick={() => send(s)} className="text-left rounded-xl border border-line bg-card hover:border-line2 hover:bg-elevated transition px-3.5 py-2.5 text-[12.5px] text-muted hover:text-ink flex items-center gap-2"><Icon name="arrowright" size={13} className="text-faint shrink-0" />{s}</button>)}
              </div>
            </div>
          )}
          <div className="space-y-4">
            {messages.map((m) => <ChatMessage key={m.id} m={m} navigate={navigate} />)}
            {working && <WorkingIndicator />}
          </div>
        </div>
      </div>

      <div className="border-t border-line bg-panel/70 backdrop-blur px-5 py-3.5">
        <div className="max-w-[760px] mx-auto">
          <div className="flex items-end gap-2 rounded-2xl border border-line2 bg-app px-3 py-2 focus-within:border-brand focus-within:ring-2 focus-within:ring-brand/25 transition">
            <textarea value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } }} rows={1} placeholder="Ask the AI to inspect or act…" className="flex-1 bg-transparent resize-none outline-none text-[13.5px] text-ink placeholder:text-faint py-1.5 max-h-32" style={{ minHeight: 24 }} />
            <Button variant="primary" size="icon" onClick={() => send()} disabled={!input.trim() || working}><Icon name="send" size={16} /></Button>
          </div>
          <div className="text-[11px] text-faint mt-1.5 text-center flex items-center justify-center gap-1.5"><Icon name="shield" size={11} />The AI proposes actions as approval-gated jobs. Nothing runs until you approve it.</div>
        </div>
      </div>
    </>
  );
}

function WorkingIndicator() {
  return (
    <div className="flex items-start gap-3 animate-fade-in">
      <div className="h-7 w-7 rounded-lg bg-brandsoft border border-brand/30 flex items-center justify-center text-brandhi shrink-0 mt-0.5"><Icon name="sparkle" size={14} /></div>
      <div className="flex items-center gap-1.5 h-7">
        <span className="text-[12.5px] text-muted">Working</span>
        <span className="flex gap-1">{[0, 1, 2].map((i) => <span key={i} className="h-1.5 w-1.5 rounded-full bg-brandhi/70 animate-bounce" style={{ animationDelay: i * 140 + 'ms' }} />)}</span>
      </div>
    </div>
  );
}

function ChatMessage({ m, navigate }) {
  if (m.role === 'user') {
    return (
      <div className="flex justify-end animate-fade-in">
        <div className="max-w-[80%] rounded-2xl rounded-br-md bg-elevated border border-line2 px-3.5 py-2.5 text-[13.5px] text-ink">{m.content}</div>
      </div>
    );
  }
  if (m.role === 'tool') return <ToolStep m={m} />;
  // assistant
  const jobIds = m.tool_calls?.created_job_ids || [];
  const csv = tabularCSV(parseBlocks(m.content));
  return (
    <div className="flex items-start gap-3 animate-fade-in">
      <div className="h-7 w-7 rounded-lg bg-brandsoft border border-brand/30 flex items-center justify-center text-brandhi shrink-0 mt-0.5"><Icon name="sparkle" size={14} /></div>
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] text-ink space-y-0.5">{renderRich(m.content)}</div>
        {csv && (
          <button onClick={() => downloadText('report-' + (m.id || 'export') + '.csv', csv, 'text/csv')} className="mt-2 inline-flex items-center gap-1.5 rounded-lg border border-line bg-card hover:border-line2 hover:bg-elevated/60 transition px-2.5 py-1.5 text-[12px] text-muted hover:text-ink">
            <Icon name="download" size={13} className="text-brandhi" />Baixar CSV
          </button>
        )}
        {jobIds.map((id) => <ProposedJob key={id} jobId={id} navigate={navigate} />)}
      </div>
    </div>
  );
}

function ToolStep({ m }) {
  const [open, setOpen] = useState(false);
  const labels = { list_devices: 'Looked up devices', inventory_query: 'Inspected inventory', run_check: 'Ran a check', reason: 'Reasoned about the request' };
  return (
    <div className="flex items-start gap-3 animate-fade-in">
      <div className="h-7 w-7 rounded-lg bg-elevated border border-line flex items-center justify-center text-faint shrink-0 mt-0.5"><Icon name="wrench" size={13} /></div>
      <div className="min-w-0 flex-1">
        <button onClick={() => setOpen((o) => !o)} className="inline-flex items-center gap-2 rounded-lg border border-line bg-card hover:border-line2 hover:bg-elevated/50 transition px-2.5 py-1.5 text-[12px] text-muted">
          <span className="text-faint mono text-[11px]">{m.name || 'tool'}</span>
          <span className="text-ink">{labels[m.name] || 'Tool step'}</span>
          <Icon name={open ? 'chevdown' : 'chevright'} size={13} className="text-faint" />
        </button>
        {open && <div className="mt-1.5 rounded-lg border border-line bg-card px-3 py-2 text-[12px] text-muted leading-relaxed mono">{m.content}</div>}
      </div>
    </div>
  );
}

function ProposedJob({ jobId, navigate }) {
  const { can } = useAuth();
  const toast = useToast();
  const jobQ = useQuery([jobId, 'pj'], () => api.getJob(jobId).catch(() => null), { invalidateOn: ['jobs'], poll: 2000 });
  const [busy, setBusy] = useState(false);
  const j = jobQ.data;
  if (!j) return null;
  const isAwaiting = j.status === 'awaiting_approval';
  const done = j.status === 'completed' || j.status === 'failed';

  const act = async (fn, label) => { setBusy(true); try { await fn(j.id); toast.success(label); } catch (e) { toast.error('Failed', e.message); } finally { setBusy(false); } };

  return (
    <div className="mt-3 rounded-2xl border border-warn/30 bg-warn/[0.03] overflow-hidden animate-scale-in">
      <div className="flex items-center justify-between px-3.5 h-11 border-b border-line/60">
        <div className="flex items-center gap-2 min-w-0">
          <Icon name="flash" size={15} className="text-warn shrink-0" />
          <span className="text-[12.5px] font-semibold text-ink truncate">Proposed job</span>
          <StatusPill status={j.status} size="sm" pulse />
        </div>
        <button onClick={() => navigate('jobs', { focus: j.id })} className="text-[11.5px] text-muted hover:text-ink flex items-center gap-1 shrink-0">Open in queue <Icon name="external" size={12} /></button>
      </div>
      <div className="p-3.5 space-y-3">
        <div className="text-[12.5px] text-ink font-medium">{j.description}</div>
        <CodeBlock code={j.script} shell={j.shell} dryRun={isAwaiting} maxHeight={200} />
        {done && (j.stdout || j.stderr) && <OutputBlock stdout={j.stdout} stderr={j.stderr} exitCode={j.exit_code} />}
        {isAwaiting && (
          <div className="flex items-center justify-between gap-2">
            <span className="text-[11.5px] text-faint flex items-center gap-1.5"><Icon name="eye" size={12} />Nothing has run yet.</span>
            {can('tech') ? (
              <div className="flex gap-2">
                <Button size="sm" variant="danger" icon="reject" onClick={() => act(api.rejectJob, 'Rejected')} disabled={busy}>Reject</Button>
                <Button size="sm" variant="success" icon="approve" loading={busy} onClick={() => act(api.approveJob, 'Approved — dispatching')}>Approve & run</Button>
              </div>
            ) : <span className="inline-flex items-center gap-1.5 text-[11.5px] text-faint"><Icon name="shield" size={12} />Tech role needed to approve</span>}
          </div>
        )}
        {j.status === 'running' && <div className="flex items-center gap-2 text-[12px] text-run"><Spinner size={13} className="text-run" />Executing…</div>}
      </div>
    </div>
  );
}

window.ChatScreen = ChatScreen;
