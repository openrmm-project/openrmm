// Labels computed from what the server sends, in the console's language. Plain
// JS (no JSX) so the tests can run it next to the i18n runtime; the screens
// reach it through window, like every other shared file here.

// An audit action ('job.approve') in the console's language: a whole-action
// entry when the table has one, otherwise resource and verb put together by the
// locale's own format — so Portuguese can say "Política · alteração" where a
// composed "Política atualizado" would get the gender wrong. Parts the table
// does not know stay as they came, so a newer server's action still reads.
function auditActionLabel(action) {
  const a = String(action || '');
  const whole = tEnum('audit.action', a);
  if (whole !== a) return whole;
  const i = a.lastIndexOf('.');
  if (i < 0) return a;
  const res = a.slice(0, i), verb = a.slice(i + 1);
  const r = tEnum('audit.res', res), v = tEnum('audit.verb', verb);
  if (r === res && v === verb) return a.replace(/\./g, ' ');
  return t('audit.format', { resource: r, verb: v });
}

// Short weekday name in the console's language, index 0 = Sunday — the
// numbering the server stores (Go's time.Weekday). 4 January 2026 was a Sunday.
function weekdayShort(i) {
  return new Date(Date.UTC(2026, 0, 4 + Number(i))).toLocaleDateString(getLocale(), { weekday: 'short', timeZone: 'UTC' });
}

// An interval in seconds as the console shows it: seconds under a minute,
// minutes under an hour, whole hours after that when it divides evenly.
function fmtInterval(sec) {
  const s = Number(sec) || 0;
  if (s < 60) return t('unit.sec', { n: s });
  if (s < 3600 || s % 3600 !== 0) return t('unit.min', { n: Math.round(s / 60) });
  return t('unit.hour', { n: s / 3600 });
}

window.auditActionLabel = auditActionLabel;
window.weekdayShort = weekdayShort;
window.fmtInterval = fmtInterval;
