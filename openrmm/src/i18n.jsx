/* i18n — translation lookup, locale selection and persistence.
 *
 * Hand-rolled rather than react-i18next, for the reason the project brief
 * already gives: no new libraries without need. What is needed here is a lookup
 * with a fallback and a way to re-render — about forty lines — against a library
 * that assumes an import system this build does not have (every source file is
 * wrapped in its own block and shares state through `window`).
 *
 * The contract:
 *
 *   t('nav.devices')                     -> "Devices"
 *   t('users.count', { n: 3 })           -> "3 users"
 *   t('unknown.key')                     -> "unknown.key"   (visible, not blank)
 *
 * A missing key renders as the key itself. That is deliberate: a blank label is
 * invisible in review and ships, while `devices.emptyState` on screen gets
 * noticed and fixed. English is the source language and the fallback, so a
 * partially translated locale degrades to English per string rather than
 * breaking the screen.
 */

// Locales offered in the picker. `complete` says whether every key is present
// and reviewed; the picker marks the rest, because a half-translated console
// that claims to be Japanese is worse than one that admits it is a draft.
const LOCALES = [
  { code: 'en',    name: 'English',    nativeName: 'English',  complete: true },
  { code: 'pt-BR', name: 'Portuguese', nativeName: 'Português', complete: true },
  { code: 'es',    name: 'Spanish',    nativeName: 'Español',  complete: true },
  { code: 'de',    name: 'German',     nativeName: 'Deutsch',  complete: false },
  { code: 'zh-CN', name: 'Chinese',    nativeName: '中文',      complete: false },
  { code: 'ja',    name: 'Japanese',   nativeName: '日本語',     complete: false },
];

const STORAGE_KEY = 'rmm-locale';

// Resolved once at load. Order: what the operator chose, then what the browser
// asks for, then English.
function detectLocale() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved && LOCALES.some((l) => l.code === saved)) return saved;
  } catch (_) { /* private window, or storage disabled */ }

  for (const candidate of navigator.languages || [navigator.language || '']) {
    const exact = LOCALES.find((l) => l.code.toLowerCase() === candidate.toLowerCase());
    if (exact) return exact.code;
    // "pt" and "pt-PT" both land on pt-BR: a close match beats English.
    const base = candidate.split('-')[0].toLowerCase();
    const loose = LOCALES.find((l) => l.code.split('-')[0].toLowerCase() === base);
    if (loose) return loose.code;
  }
  return 'en';
}

let currentLocale = detectLocale();

// Subscribers are React roots that must re-render when the language changes.
// Without this the strings would only update on reload — which works, but makes
// the picker feel broken.
const listeners = new Set();

function lookup(locale, key) {
  const table = (window.LOCALE_STRINGS || {})[locale];
  return table ? table[key] : undefined;
}

/* Interpolates {name} placeholders. Values are inserted as text by React, so
 * there is no markup path here and nothing to escape. */
function interpolate(template, vars) {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name) =>
    (Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : whole));
}

function t(key, vars) {
  const found = lookup(currentLocale, key)
    ?? lookup('en', key)   // English is the source language and the fallback
    ?? key;                // and a missing key shows itself rather than nothing
  return interpolate(found, vars);
}

function getLocale() { return currentLocale; }

function setLocale(code) {
  if (!LOCALES.some((l) => l.code === code)) return;
  currentLocale = code;
  try { localStorage.setItem(STORAGE_KEY, code); } catch (_) { /* not fatal */ }
  // Screen readers and CSS (`:lang`) both key off this, and it is what makes a
  // browser offer to translate — or not offer, once the page says it is already
  // in the reader's language.
  document.documentElement.lang = code;
  listeners.forEach((fn) => fn(code));
}

/* Re-renders the calling component when the language changes. The app root uses
 * it once; everything below re-renders with it. */
function useLocale() {
  const [locale, setLocaleState] = useState(currentLocale);
  useEffect(() => {
    listeners.add(setLocaleState);
    return () => listeners.delete(setLocaleState);
  }, []);
  return locale;
}

document.documentElement.lang = currentLocale;

Object.assign(window, { t, getLocale, setLocale, useLocale, LOCALES });
