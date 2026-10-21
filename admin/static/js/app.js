// Shared across every admin page: the <site-nav> Custom Element, formatters,
// the [data-tip] tooltip wiring, and the SSE client that drives the nav's
// "Proxy Service" badge plus each page's own live-update hook.
'use strict';

const txtEncoder = new TextEncoder();

const NAV_LINK_ACTIVE = 'text-emerald-50 py-1 px-2 rounded-lg bg-emerald-700 shadow-md hover:shadow-emerald-700/50 transition';
const NAV_LINK_INACTIVE = 'text-fg-muted hover:text-fg';

/** Text and background classes per badge tone, each with an explicit dark-theme shade. */
const BADGE_TONES = {
  blue: 'text-blue-700 dark:text-blue-400 bg-blue-50 dark:bg-blue-950',
  emerald: 'text-emerald-700 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-950',
  amber: 'text-amber-700 dark:text-amber-400 bg-amber-100 dark:bg-amber-900',
  muted: 'text-fg-subtle bg-surface-hover',
};

/** Text, background and border classes of an inline error message. */
export const ERROR_MESSAGE_CLASS = 'text-red-700 dark:text-red-400 bg-red-50 dark:bg-red-950 border-red-200 dark:border-red-800';

const PROXY_BADGE_BASE = 'px-2 py-1 border border-dashed text-xs rounded-lg';
const PROXY_STATUSES = {
  ok: {
    label: 'OK',
    badge: 'border-emerald-300 dark:border-emerald-700 bg-emerald-100 dark:bg-emerald-900 text-emerald-800 dark:text-emerald-300',
    dot: 'bg-emerald-500',
  },
  degraded: {
    label: 'Degraded',
    badge: 'border-yellow-300 dark:border-yellow-700 bg-yellow-100 dark:bg-yellow-900 text-yellow-800 dark:text-yellow-400',
    dot: 'bg-yellow-500',
  },
  unreachable: {
    label: 'Unreachable',
    badge: 'border-red-300 dark:border-red-700 bg-red-100 dark:bg-red-900 text-red-800 dark:text-red-300',
    dot: 'bg-red-500 animate-pulse',
  },
  unknown: {
    label: 'Unknown',
    badge: 'border-line-strong bg-surface-hover text-fg',
    dot: 'bg-fg-subtle',
  },
};

/**
 * Renders a small colored label.
 * @param {keyof BADGE_TONES} tone - Color family of the label.
 * @param {string} html - Label content; the caller escapes untrusted text.
 * @param {string} [shape='rounded'] - Border-radius class.
 * @returns {string} Badge markup.
 */
export function badge(tone, html, shape = 'rounded') {
  return `<span class="font-medium px-1.5 py-0.5 ${BADGE_TONES[tone]} ${shape}">${html}</span>`;
}

/**
 * Renders the track and knob of an on/off switch; the nearest `group`
 * ancestor's aria-checked state drives its look.
 * @returns {string} Switch markup.
 */
function switchTrack() {
  return `<span class="relative inline-flex h-5 w-9 shrink-0 rounded-full bg-surface-strong group-aria-checked:bg-emerald-600 transition-colors" aria-hidden="true">
    <span class="absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-surface shadow transition-transform group-aria-checked:translate-x-4"></span>
  </span>`;
}

/**
 * Renders a standalone on/off switch button.
 * @param {boolean} active - Whether the switch is on.
 * @param {string} label - Accessible name of the switch.
 * @returns {string} Button markup, marked with the `toggle-active-btn` class for delegated click handlers.
 */
export function switchToggle(active, label) {
  return `<button type="button" role="switch" aria-checked="${active}" aria-label="${esc(label)}" class="toggle-active-btn group inline-flex items-center rounded-full">${switchTrack()}</button>`;
}

/**
 * Applies the given theme to the document, persists it, and notifies
 * listeners (e.g. the JSON viewer) so they can follow along.
 * @param {boolean} dark - Whether dark mode should be active.
 */
export function applyTheme(dark) {
  document.documentElement.classList.toggle('dark', dark);
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
  localStorage.setItem('theme', dark ? 'dark' : 'light');
  window.dispatchEvent(new CustomEvent('themechange', { detail: { dark } }));
}

/**
 * Whether dark mode is currently active.
 * @returns {boolean}
 */
export function isDarkTheme() {
  return document.documentElement.classList.contains('dark');
}

/**
 * Theme name for the andypf-json-viewer component, following the site's
 * current light/dark setting.
 * @returns {string}
 */
export function jsonViewerTheme() {
  return isDarkTheme() ? 'google-dark' : 'google-light';
}

/**
 * Site-wide header/nav. The `active` attribute highlights the current page:
 * "dashboard" | "exchanges" | "analyze-exchange" | "prices" | "limiters" |
 * "proxy-integrations" | "notification-integrations" | "about". Renders into
 * light DOM so page code can reach its elements by id.
 */
class SiteNav extends HTMLElement {
  connectedCallback() {
    const active = this.getAttribute('active');
    const linkClass = (key) => (key === active ? NAV_LINK_ACTIVE : NAV_LINK_INACTIVE);
    const menuItemClass = (key) => (key === active
      ? `block font-semibold px-3 py-2 ${BADGE_TONES.emerald}`
      : 'block text-fg-muted hover:text-fg px-3 py-2 hover:bg-surface-hover');
    const dropdown = (id, buttonClass, buttonContent, panelClass, body) => `
      <div class="relative" data-nav-menu>
        <button type="button" id="${id}-menu-button" aria-haspopup="true" aria-expanded="false" aria-controls="${id}-menu" class="flex items-center gap-1.5 ${buttonClass}">
          ${buttonContent}
          <svg class="h-3 w-3 transition-transform" viewBox="0 0 12 12" fill="none" aria-hidden="true">
            <path d="M2.5 4.5L6 8l3.5-3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
        <div id="${id}-menu" role="menu" aria-labelledby="${id}-menu-button" class="hidden absolute z-50 mt-2 py-1 bg-surface rounded-lg border border-line shadow-lg ${panelClass}">
          ${body}
        </div>
      </div>`;
    const menu = (id, label, items) => dropdown(
      id,
      items.some(([key]) => key === active) ? NAV_LINK_ACTIVE : NAV_LINK_INACTIVE,
      label,
      'left-0 w-48',
      items.map(([key, href, text]) => `<a href="${href}" role="menuitem" class="${menuItemClass(key)}">${text}</a>`).join('\n              '),
    );
    const switchRow = (id, label, hint = '') => `<button type="button" id="${id}" role="menuitemcheckbox" aria-checked="false" class="group flex items-center justify-between w-full text-left text-fg-muted gap-4 px-3 py-2 hover:text-fg hover:bg-surface-hover disabled:opacity-50 disabled:pointer-events-none">
                <span class="flex flex-col">
                  ${label}
                  ${hint && `<span class="text-xs text-fg-subtle">${hint}</span>`}
                </span>
                ${switchTrack()}
              </button>`;
    const settingsMenu = dropdown(
      'settings',
      'text-xs text-fg-muted px-2 py-1.5 border border-line-strong rounded-lg bg-surface hover:text-fg hover:bg-surface-hover transition',
      '<span id="proxy-status-dot" class="h-2 w-2 rounded-full" aria-hidden="true"></span>Settings',
      'right-0 w-72',
      `<div role="menuitem" class="flex items-center justify-between text-fg-muted gap-4 px-3 py-2">
                Proxy Service
                <span id="proxy-status"></span>
              </div>
              ${switchRow('theme-toggle', 'Dark mode')}
              ${switchRow('save-raw-toggle', 'Save raw payloads', 'Keep request/response bodies of new exchanges')}`,
    );

    this.innerHTML = `
      <header class="px-6 py-3 bg-surface border-b border-line">
        <nav class="flex items-center text-sm font-medium gap-6">
          <span class="flex items-center gap-2">
            <img src="/img/logo.png" alt="" class="h-6 w-6 rounded-md">
            <span class="text-base font-bold text-fg">claude-lens</span>
          </span>
          <a href="/" class="${linkClass('dashboard')}">Dashboard</a>
          ${menu('exchanges', 'Exchanges', [['exchanges', '/exchanges', 'All Exchanges'], ['analyze-exchange', '/exchanges/analyze', 'Analyze Exchange']])}
          <a href="/prices" class="${linkClass('prices')}">Prices</a>
          <a href="/limiters" class="${linkClass('limiters')}">Limiters</a>
          ${menu('integrations', 'Integrations', [['proxy-integrations', '/integrations/proxy', 'Proxy'], ['notification-integrations', '/integrations/notifications', 'Notifications']])}
          <a href="/about" class="${linkClass('about')}">About</a>
          <div class="ml-auto">${settingsMenu}
          </div>
        </nav>
      </header>
    `;

    this.menus = [...this.querySelectorAll('[data-nav-menu]')];
    this.themeToggle = this.querySelector('#theme-toggle');
    this.saveRawToggle = this.querySelector('#save-raw-toggle');
    this.onDocumentClick = (e) => {
      this.menus.forEach((m) => {
        if (!m.contains(e.target)) this.setMenuOpen(m, false);
      });
    };
    this.onDocumentKeydown = (e) => {
      if (e.key === 'Escape') this.menus.forEach((m) => this.setMenuOpen(m, false));
    };
    this.addEventListener('click', (e) => {
      const trigger = e.target.closest('[data-nav-menu] > button');
      if (!trigger) return;
      const current = trigger.parentElement;
      const opening = current.querySelector('[role="menu"]').classList.contains('hidden');
      this.menus.forEach((menu) => this.setMenuOpen(menu, menu === current && opening));
    });
    this.onThemeChange = () => this.themeToggle.setAttribute('aria-checked', String(isDarkTheme()));
    this.querySelector('#settings-menu').addEventListener('click', (e) => {
      const toggle = e.target.closest('[role="menuitemcheckbox"]');
      if (toggle === this.themeToggle) applyTheme(!isDarkTheme());
      else if (toggle === this.saveRawToggle) this.toggleSaveRawPayloads();
    });
    this.onThemeChange();
    this.saveRawToggle.disabled = true;
    this.loadSettings();
    updateProxyStatusBadge();
    window.addEventListener('themechange', this.onThemeChange);
    document.addEventListener('click', this.onDocumentClick);
    document.addEventListener('keydown', this.onDocumentKeydown);
  }

  disconnectedCallback() {
    window.removeEventListener('themechange', this.onThemeChange);
    document.removeEventListener('click', this.onDocumentClick);
    document.removeEventListener('keydown', this.onDocumentKeydown);
  }

  /**
   * Fetches the global settings and reflects them on their switches, which
   * stay disabled until the server's values are known.
   * @returns {Promise<void>}
   */
  async loadSettings() {
    try {
      const settings = await getJSON('/api/settings');
      this.saveRawToggle.setAttribute('aria-checked', String(settings.save_raw_payloads));
      this.saveRawToggle.disabled = false;
    } catch (err) {
      notifyError(err.message);
    }
  }

  /**
   * Flips the save-raw-payloads setting optimistically, restoring the
   * previous state if the server rejects the change. The switch is disabled
   * while the request is in flight so rapid clicks cannot interleave.
   * @returns {Promise<void>}
   */
  async toggleSaveRawPayloads() {
    const enabled = this.saveRawToggle.getAttribute('aria-checked') !== 'true';
    this.saveRawToggle.setAttribute('aria-checked', String(enabled));
    this.saveRawToggle.disabled = true;
    try {
      const res = await fetch('/api/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ save_raw_payloads: enabled }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
    } catch {
      this.saveRawToggle.setAttribute('aria-checked', String(!enabled));
      notifyError('Failed to update the save raw payloads setting.');
    } finally {
      this.saveRawToggle.disabled = false;
    }
  }

  /**
   * Opens or closes one dropdown, keeping its button's aria-expanded and
   * chevron in sync.
   * @param {HTMLElement} menu - The [data-nav-menu] wrapper.
   * @param {boolean} open - Whether the dropdown should be shown.
   */
  setMenuOpen(menu, open) {
    menu.querySelector('[role="menu"]').classList.toggle('hidden', !open);
    menu.querySelector('button').setAttribute('aria-expanded', String(open));
    menu.querySelector('svg').classList.toggle('rotate-180', open);
  }
}
customElements.define('site-nav', SiteNav);

/**
 * Left-pads a number to two digits.
 * @param {number} n - Number to pad.
 * @returns {string} Two-digit string.
 */
export function pad(n) {
  return String(n).padStart(2, '0');
}

/**
 * Escapes text for safe interpolation into HTML markup and attributes.
 * @param {*} s - Value to escape; null and undefined become ''.
 * @returns {string} Escaped text.
 */
export function esc(s) {
  return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/**
 * Measures a string's UTF-8 size.
 * @param {?string} str - Text to measure.
 * @returns {number} Byte length, 0 for empty input.
 */
export function estimateBytes(str) {
  if (!str) return 0;
  return txtEncoder.encode(String(str)).length;
}

/**
 * Abbreviates a token count to 1 decimal place above 1000 (1.7k, 6.3M, 1.2B),
 * or renders it plain below that. Use for summarized counts (tables, cards);
 * use fmtInt for exact values (exchange detail page).
 * @param {?number} n - Token count.
 * @returns {string} Formatted token count, or '—' when n is null.
 */
export function fmtTokens(n) {
  if (n == null) return '—';
  const neg = n < 0;
  const abs = Math.abs(n);
  let suffix, divisor;
  if (abs >= 1000000000) { suffix = 'B'; divisor = 1000000000; }
  else if (abs >= 1000000) { suffix = 'M'; divisor = 1000000; }
  else if (abs >= 1000) { suffix = 'k'; divisor = 1000; }
  else return `${neg ? '-' : ''}${abs}`;
  const val = (abs / divisor).toFixed(1).replace(/\.0$/, '');
  return `${neg ? '-' : ''}${val}${suffix}`;
}

/**
 * Formats an exact integer with thousands separators.
 * @param {?number} n - Value to format.
 * @returns {string} Formatted number, or '—' when n is null.
 */
export function fmtInt(n) {
  if (n == null) return '—';
  return Number(n).toLocaleString('en-US');
}

/**
 * Formats a USD cost to cents, with "<$0.01" for smaller amounts.
 * @param {?number} c - Cost in USD.
 * @returns {string} Formatted cost, or '—' when c is null.
 */
export function fmtCost(c) {
  if (c == null) return '—';
  return c < 0.01 ? '<$0.01' : `$${c.toFixed(2)}`;
}

/**
 * Formats a Unix-seconds timestamp as local "YYYY-MM-DD HH:MM:SS".
 * @param {?number} ts - Unix timestamp in seconds.
 * @returns {string} Formatted time, or '—' when ts is empty.
 */
export function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

const relativeTimeFormat = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

const RELATIVE_UNITS = [
  ['second', 60],
  ['minute', 60],
  ['hour', 24],
  ['day', 30],
  ['month', 12],
  ['year', Infinity],
];

/**
 * Human-readable time elapsed since a Unix-seconds timestamp, e.g.
 * "5 minutes ago", "yesterday".
 * @param {?number} ts - Unix timestamp in seconds.
 * @returns {string} Relative time, "just now" under a minute, or '—' when ts is empty.
 */
export function fmtAgo(ts) {
  if (!ts) return '—';
  let value = ts - Date.now() / 1000;
  if (Math.abs(value) < 60) return 'just now';
  for (const [unit, size] of RELATIVE_UNITS) {
    if (Math.abs(value) < size) return relativeTimeFormat.format(Math.round(value), unit);
    value /= size;
  }
}

/**
 * Renders a timestamp as relative text with the exact time as its tooltip;
 * the text is kept current by the page-wide ticker below.
 * @param {?number} ts - Unix timestamp in seconds.
 * @returns {string} `<time>` markup, or '—' when ts is empty.
 */
export function timeAgoHtml(ts) {
  if (!ts) return '—';
  return `<time datetime="${new Date(ts * 1000).toISOString()}" data-ts="${ts}" title="${fmtTime(ts)}">${fmtAgo(ts)}</time>`;
}

const RELATIVE_TICK_MS = 30_000;

setInterval(() => {
  document.querySelectorAll('time[data-ts]').forEach((el) => {
    el.textContent = fmtAgo(Number(el.dataset.ts));
  });
}, RELATIVE_TICK_MS);

/**
 * Human-readable time remaining until a Unix-seconds timestamp, e.g.
 * "in 3 mins", "in 2 hrs", "in 5 days".
 * @param {?number} ts - Target Unix timestamp in seconds.
 * @returns {string} Countdown text, or '—' when ts is null.
 */
export function fmtCountdown(ts) {
  if (!ts) return '—';
  const diff = ts - Date.now() / 1000;
  if (diff <= 0) return 'refreshing…';
  const mins = Math.round(diff / 60);
  if (mins < 1) return 'in <1 min';
  if (mins < 60) return `in ${mins} min${mins === 1 ? '' : 's'}`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `in ${hrs} hr${hrs === 1 ? '' : 's'}`;
  const days = Math.round(hrs / 24);
  return `in ${days} day${days === 1 ? '' : 's'}`;
}

/**
 * Formats a limiter's active period as HTML.
 * @param {{active_start_hour: number|null, active_end_hour: number|null, is_active: boolean, within_active_period: boolean}} l - Limiter record.
 * @returns {string} HTML markup for the active period badge.
 */
export function fmtActivePeriod(l) {
  if (l.active_start_hour == null) return badge(l.is_active ? 'blue' : 'muted', 'Always');
  const tone = l.within_active_period && l.is_active ? 'emerald' : 'muted';
  return badge(tone, `${pad(l.active_start_hour)}:00 – ${pad(l.active_end_hour)}:59`);
}

/**
 * Progress bar + "spent of limit" caption for a limiter's current spend.
 * @param {{current_cost: number, limit_amount: number, within_active_period: boolean, is_active: boolean}} l - Limiter record.
 * @param {string} [height='h-1.5'] - Height class of the bar.
 * @returns {string} Progress-bar markup.
 */
export function progressBar(l, height = 'h-1.5') {
  const pct = l.limit_amount > 0 ? Math.min(100, (l.current_cost / l.limit_amount) * 100) : 0;
  const barColor = pct >= 100 ? 'bg-red-500' : pct >= 80 ? 'bg-amber-500' : 'bg-emerald-500';
  return `<div class="flex flex-col items-end w-full">
    <div class="${height} w-full overflow-hidden bg-surface-active rounded-full">
      <div class="h-full ${l.within_active_period && l.is_active ? barColor : 'bg-surface-strong'}" style="width:${pct}%"></div>
    </div>
    <div class="text-xs text-fg-muted mt-1">${fmtCost(l.current_cost)} of ${fmtCost(l.limit_amount)}</div>
  </div>`;
}

/**
 * Formats a byte count with a binary unit.
 * @param {?number} bytes - Size in bytes.
 * @returns {string} Formatted size, or '—' when bytes is null.
 */
export function fmtBytes(bytes) {
  if (bytes == null) return '—';
  if (bytes < 1024) return `${bytes} Bytes`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

/**
 * Shortens a session id to its head and tail around an ellipsis.
 * @param {?string} sessionId - Session id.
 * @param {number} [maxLength=15] - Longest id shown unshortened.
 * @returns {string} Shortened id, or '-' when empty.
 */
export function fmtSessionId(sessionId, maxLength = 15) {
  if (!sessionId) return '-';
  return sessionId.length > maxLength ? `${sessionId.slice(0, Math.floor(maxLength / 2))} … ${sessionId.slice(-Math.floor(maxLength / 2))}` : sessionId;
}

/**
 * Sums two nullable costs. Result is nil only when both inputs are nil, so an
 * unpriced cache rate on one side still shows the other's cost instead of
 * collapsing to "—".
 * @param {?number} a - First cost component.
 * @param {?number} b - Second cost component.
 * @returns {?number} Sum of the non-null components, or null when both are null.
 */
export function addCost(a, b) {
  if (a == null && b == null) return null;
  return (a ?? 0) + (b ?? 0);
}

/**
 * Builds the tooltip markup listing an exchange's per-component costs.
 * @param {object} row - Exchange record.
 * @returns {string} Tooltip HTML, or '' when no component has a cost.
 */
export function costTooltip(row) {
  const parts = [];
  if (row.input_cost != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtCost(row.input_cost)}</span></div>`);
  if (row.cache_creation_cost != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtCost(row.cache_creation_cost)}</span></div>`);
  if (row.cache_read_cost != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtCost(row.cache_read_cost)}</span></div>`);
  if (row.output_cost != null) parts.push(`<div class="flex justify-between mt-1.5"><b>Output:</b><span>${fmtCost(row.output_cost)}</span></div>`);
  return parts.length ? '<div class="flex flex-col w-32 gap-0.5">' + parts.join('') + '</div>' : '';
}

/**
 * Builds the tooltip markup listing an exchange's per-component token counts.
 * @param {object} row - Exchange record.
 * @returns {string} Tooltip HTML, or '' when no component has tokens.
 */
export function tokensTooltip(row) {
  const parts = [];
  if (row.input_tokens != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtTokens(row.input_tokens)}</span></div>`);
  if (row.cache_creation_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtTokens(row.cache_creation_tokens)}</span></div>`);
  if (row.cache_read_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtTokens(row.cache_read_tokens)}</span></div>`);
  if (row.output_tokens != null) parts.push(`<div class="flex justify-between mt-1.5"><b>Output:</b><span>${fmtTokens(row.output_tokens)}</span></div>`);
  return parts.length ? '<div class="flex flex-col w-32 gap-0.5">' + parts.join('') + '</div>' : '';
}

// Single floating tooltip driven by [data-tip] elements, repositioned on
// hover and focus.
(() => {
  const tip = document.createElement('div');
  tip.id = 'app-tooltip';
  tip.setAttribute('role', 'tooltip');
  tip.className = 'fixed z-50 max-w-xs whitespace-pre-line text-xs leading-snug px-2.5 py-1.5 rounded-md bg-fg text-canvas shadow-lg pointer-events-none opacity-0 transition-opacity duration-150';
  document.body.append(tip);
  let activeEl = null;

  /** Places the tooltip above el, or below it when there is no room above. */
  function positionTip(el) {
    const rect = el.getBoundingClientRect();
    const tipRect = tip.getBoundingClientRect();
    let left = rect.left + rect.width / 2 - tipRect.width / 2;
    left = Math.max(4, Math.min(left, window.innerWidth - tipRect.width - 4));
    let top = rect.top - tipRect.height - 8;
    if (top < 4) top = rect.bottom + 8;
    tip.style.left = `${left}px`;
    tip.style.top = `${top}px`;
  }

  /** Fills the tooltip from el's data-tip attribute and shows it. */
  function showTip(el) {
    const textOrHtml = el.getAttribute('data-tip');
    if (!textOrHtml) return;
    tip.innerHTML = textOrHtml;
    activeEl = el;
    tip.classList.remove('opacity-0');
    tip.classList.add('opacity-100');
    positionTip(el);
  }

  /** Hides the tooltip. */
  function hideTip() {
    activeEl = null;
    tip.classList.remove('opacity-100');
    tip.classList.add('opacity-0');
  }

  document.addEventListener('mouseover', (e) => {
    const el = e.target.closest('[data-tip]');
    if (el) showTip(el);
  });
  document.addEventListener('mouseout', (e) => {
    if (activeEl && e.target.closest('[data-tip]') === activeEl) hideTip();
  });
  document.addEventListener('focusin', (e) => {
    const el = e.target.closest('[data-tip]');
    if (el) showTip(el);
  });
  document.addEventListener('focusout', (e) => {
    if (activeEl && e.target.closest('[data-tip]') === activeEl) hideTip();
  });
  document.addEventListener('scroll', () => {
    if (activeEl) positionTip(activeEl);
  }, true);
  window.addEventListener('resize', hideTip);
})();

/**
 * Opens the live-stream SSE connection and keeps it open only while the page
 * is visible, so hidden tabs and pages parked in the back/forward cache never
 * hold one of the browser's few per-origin HTTP/1.1 connections.
 * @param {?string} range - Dashboard range key to scope totals to, or null.
 * @param {Function} onMessage - Called with each parsed payload, or `{proxy_status: 'unreachable'}` on connection errors.
 * @returns {{close: Function}} Handle that permanently closes the connection.
 */
export function connectSSE(range, onMessage) {
  const url = '/api/stream' + (range ? `?range=${encodeURIComponent(range)}` : '');
  const lifecycle = new AbortController();
  let eventSource = null;

  function open() {
    if (eventSource) return;
    eventSource = new EventSource(url);
    eventSource.onmessage = (e) => onMessage(JSON.parse(e.data));
    eventSource.onerror = () => onMessage({ proxy_status: 'unreachable' });
  }

  function suspend() {
    eventSource?.close();
    eventSource = null;
  }

  const syncWithVisibility = () => (document.hidden ? suspend() : open());
  const { signal } = lifecycle;
  document.addEventListener('visibilitychange', syncWithVisibility, { signal });
  window.addEventListener('pageshow', syncWithVisibility, { signal });
  window.addEventListener('pagehide', suspend, { signal });
  syncWithVisibility();

  return {
    close() {
      lifecycle.abort();
      suspend();
    },
  };
}

/**
 * Reflects the proxy service status on the settings menu's badge and on the
 * status dot of the menu's trigger button.
 * @param {string} [statusValue] - 'ok' | 'degraded' | 'unreachable'; anything else renders as unknown.
 */
export function updateProxyStatusBadge(statusValue) {
  const badge = document.getElementById('proxy-status');
  const dot = document.getElementById('proxy-status-dot');
  if (!badge || !dot) return;
  const status = PROXY_STATUSES[Object.hasOwn(PROXY_STATUSES, statusValue) ? statusValue : 'unknown'];
  badge.textContent = status.label;
  badge.className = `${PROXY_BADGE_BASE} ${status.badge}`;
  dot.className = `h-2 w-2 rounded-full ${status.dot}`;
}

/**
 * Wires the nav's proxy-status badge on every page and, when given handlers,
 * forwards SSE-pushed totals/new-exchange/limiters-changed events to the
 * calling page. onNewExchange/onLimitersChanged only fire when their
 * respective version fields actually increase, so pages don't each need
 * their own dedup bookkeeping.
 * @param {?string} range - Dashboard range key to scope totals to, or null.
 * @param {{onTotals?: Function, onNewExchange?: Function, onLimitersChanged?: Function}} [handlers] - Optional event callbacks.
 * @returns {{close: Function}} The SSE connection handle, for closing on range change.
 */
export function initNav(range, handlers = {}) {
  const { onTotals, onNewExchange, onLimitersChanged } = handlers;
  let knownLatestId = 0;
  let knownLimitersVersion = 0;
  return connectSSE(range, (data) => {
    updateProxyStatusBadge(data.proxy_status);
    if (data.totals && typeof onTotals === 'function') onTotals(data.totals);
    if (data.latest_exchange_id && data.latest_exchange_id > knownLatestId) {
      knownLatestId = data.latest_exchange_id;
      if (typeof onNewExchange === 'function') onNewExchange(data.latest_exchange_id);
    }
    if (data.limiters_version && data.limiters_version > knownLimitersVersion) {
      knownLimitersVersion = data.limiters_version;
      if (typeof onLimitersChanged === 'function') onLimitersChanged(data.limiters_version);
    }
  });
}

/**
 * Lighter alternative to initNav for pages that only need the proxy-status
 * badge and have no use for live totals/new-exchange events (exchange
 * detail, prices): polls /api/health instead of holding open an SSE
 * connection.
 * @param {number} [intervalMs=15000] - Poll interval in milliseconds.
 * @returns {number} The interval id, for cleanup via clearInterval.
 */
export function initNavPolling(intervalMs = 15000) {
  async function poll() {
    try {
      const res = await fetch('/api/health');
      updateProxyStatusBadge(res.ok ? (await res.json()).proxy : 'unreachable');
    } catch {
      updateProxyStatusBadge('unreachable');
    }
  }
  poll();
  return setInterval(poll, intervalMs);
}

/**
 * Clamps a requested page into range: a stale bookmarked page (or one typed
 * past the last page) degrades to the nearest valid page instead of
 * rendering empty.
 * @param {number} reqPage - Requested page number.
 * @param {number} size - Rows per page.
 * @param {number} total - Total matching rows.
 * @returns {{page: number, totalPages: number, from: number, to: number}} Clamped pagination state.
 */
export function computePagination(reqPage, size, total) {
  const totalPages = Math.max(1, Math.ceil(total / size));
  const page = Math.min(Math.max(reqPage, 1), totalPages);
  if (total === 0) return { page, totalPages, from: 0, to: 0 };
  const from = (page - 1) * size + 1;
  const to = Math.min(from + size - 1, total);
  return { page, totalPages, from, to };
}

/**
 * Renders a "rows per page" select, an "X–Y of Z results" caption, and
 * First/Previous/page-jump/Next/Last controls into containerId. Rebuilds the
 * container's innerHTML on every call and wires only the two elements that
 * get recreated each time (size select, page-jump input) — First/Previous/
 * Next/Last links are handled separately by wirePaginationNav, since that
 * listener only needs to be attached once.
 * @param {string} containerId - Id of the element to render into.
 * @param {{page: number, totalPages: number, from: number, to: number, total: number, pageSize: number}} state - Pagination state, e.g. from computePagination plus the active page size.
 * @param {number[]} pageSizes - Selectable rows-per-page options.
 * @param {function(number, number): void} onSizeOrJumpChange - Called with (page, size) when the size select or page-jump input changes.
 */
export function renderPaginationControls(containerId, state, pageSizes, onSizeOrJumpChange) {
  const container = document.getElementById(containerId);
  if (!container) return;
  const { page, totalPages, from, to, total, pageSize } = state;

  const navLink = (label, targetPage, enabled) => enabled
    ? `<a href="#" data-page="${targetPage}" class="pagination-link text-emerald-600 dark:text-emerald-400 hover:underline">${label}</a>`
    : `<span class="text-fg-subtle">${label}</span>`;

  container.innerHTML = `
    <div class="flex items-center text-fg-muted gap-2">
      <label for="${containerId}-page-size-select">Rows per page</label>
      <select id="${containerId}-page-size-select" class="text-sm px-2 py-1 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500">
        ${pageSizes.map((s) => `<option value="${s}" ${s === pageSize ? 'selected' : ''}>${s}</option>`).join('')}
      </select>
      <span>${total > 0 ? `${from}–${to} of ${total}` : '0 of 0'} results</span>
    </div>
    <div class="flex items-center gap-3">
      ${navLink('First', 1, page > 1)}
      ${navLink('Previous', page - 1, page > 1)}
      <span class="flex items-center gap-1.5 text-fg-muted">
        Page
        <input id="${containerId}-page-jump-input" type="number" min="1" max="${totalPages}" value="${page}"
          class="w-16 text-sm text-center px-2 py-1 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500">
        of ${totalPages}
      </span>
      ${navLink('Next', page + 1, page < totalPages)}
      ${navLink('Last', totalPages, page < totalPages)}
    </div>`;

  const sizeSelect = document.getElementById(`${containerId}-page-size-select`);
  if (sizeSelect) {
    sizeSelect.addEventListener('change', () => onSizeOrJumpChange(1, Number(sizeSelect.value)));
  }

  const jump = document.getElementById(`${containerId}-page-jump-input`);
  if (jump) {
    jump.addEventListener('focus', () => jump.select());
    jump.addEventListener('keydown', (e) => {
      if (e.key !== 'Enter') return;
      e.preventDefault();
      jump.blur();
    });
    jump.addEventListener('change', () => {
      let p = parseInt(jump.value, 10);
      if (!Number.isFinite(p)) p = 1;
      p = Math.min(Math.max(p, 1), totalPages);
      onSizeOrJumpChange(p, pageSize);
    });
  }
}

/**
 * Wires a one-time delegated click listener for First/Previous/Next/Last
 * .pagination-link anchors inside #containerId. Kept separate from
 * renderPaginationControls so repeated re-renders (which replace the
 * container's innerHTML but not the container itself) don't stack duplicate
 * listeners.
 * @param {string} containerId - Id of the pagination container.
 * @param {function(number): void} onNavigate - Called with the target page number.
 */
export function wirePaginationNav(containerId, onNavigate) {
  document.addEventListener('click', (e) => {
    const link = e.target.closest(`#${containerId} .pagination-link`);
    if (!link) return;
    e.preventDefault();
    onNavigate(Number(link.dataset.page));
  });
}

/**
 * Wraps an async task so a new call cancels its predecessor via AbortSignal,
 * instead of letting a slow, superseded response overwrite fresher data.
 * The wrapped function forwards its own args after the injected signal, e.g.
 * makeAbortable(async (signal, rangeKey) => {...}) is called as fn(rangeKey).
 * @param {Function} taskFn - Async function taking (signal, ...args).
 * @returns {Function} Abortable wrapper with the same trailing-args signature.
 */
export function makeAbortable(taskFn) {
  let currentController = null;
  return async (...args) => {
    currentController?.abort();
    const controller = new AbortController();
    currentController = controller;
    try {
      await taskFn(controller.signal, ...args);
    } catch (err) {
      if (err.name !== 'AbortError') throw err;
    }
  };
}

/**
 * Extracts the "error" field from a JSON error response, falling back to a
 * caller-supplied message when the body is missing or isn't valid JSON.
 * @param {Response} res - Fetch response to read.
 * @param {string} fallback - Message to use when no error field is present.
 * @returns {Promise<string>} The error message to display.
 */
export async function extractErrorMessage(res, fallback) {
  const body = await res.json().catch(() => ({}));
  return body.error || fallback;
}

/**
 * Wraps fn so a rapid burst of calls only runs it once, delayMs after the
 * last call — e.g. re-rendering on every keystroke of a search input.
 * @param {Function} fn - Function to debounce.
 * @param {number} [delayMs=200] - Quiet period required before fn runs.
 * @returns {Function} Debounced wrapper with the same signature as fn.
 */
export function debounce(fn, delayMs = 200) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delayMs);
  };
}

/**
 * POSTs payload as JSON. Returns the raw Response for the caller to check
 * ok/status and read the body.
 * @param {string} url - Request URL.
 * @param {object} payload - Body to JSON-encode.
 * @returns {Promise<Response>} The fetch response.
 */
export function postJSON(url, payload) {
  return fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

/**
 * PUTs payload as JSON. Returns the raw Response for the caller to check
 * ok/status and read the body.
 * @param {string} url - Request URL.
 * @param {object} payload - Body to JSON-encode.
 * @returns {Promise<Response>} The fetch response.
 */
export function putJSON(url, payload) {
  return fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

/**
 * Prompts the browser to save text content as a local file, entirely
 * client-side (no upload involved).
 * @param {string} filename - Name for the downloaded file.
 * @param {string} content - File content to write.
 * @param {string} [mimeType='application/json'] - MIME type for the download.
 */
export function downloadTextFile(filename, content, mimeType = 'application/json') {
  const blob = new Blob([content], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

/**
 * Copies text to the clipboard, flashing the trigger button's label to
 * reflect success/failure before it reverts to idleLabel.
 * @param {HTMLButtonElement} button - Button that triggered the copy.
 * @param {string} text - Text to copy.
 * @param {string} idleLabel - Label to restore once the flash ends.
 */
export async function copyTextToClipboard(button, text, idleLabel) {
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = 'Copied!';
  } catch {
    button.textContent = 'Failed';
  } finally {
    setTimeout(() => { button.textContent = idleLabel; }, 1500);
  }
}

const SPINNER_SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" aria-hidden="true"><!-- Icon from SVG Spinners by Utkarsh Verma - https://github.com/n3r4zzurr0/svg-spinners/blob/main/LICENSE --><path fill="currentColor" d="M10.14,1.16a11,11,0,0,0-9,8.92A1.59,1.59,0,0,0,2.46,12,1.52,1.52,0,0,0,4.11,10.7a8,8,0,0,1,6.66-6.61A1.42,1.42,0,0,0,12,2.69h0A1.57,1.57,0,0,0,10.14,1.16Z"><animateTransform attributeName="transform" dur="0.75s" repeatCount="indefinite" type="rotate" values="0 12 12;360 12 12"/></path></svg>';

/**
 * Runs task while button is disabled and shows a spinner after its label;
 * removes the spinner and re-enables the button afterwards, even when task
 * throws. The label is left untouched, so task may change it.
 * @param {HTMLButtonElement} button - Button that triggered the task; must lay out its children with flex.
 * @param {function(): Promise<void>} task - Async work to await.
 * @returns {Promise<void>}
 */
export async function withPending(button, task) {
  button.disabled = true;
  button.insertAdjacentHTML('beforeend', SPINNER_SVG);
  const spinner = button.lastElementChild;
  try {
    await task();
  } finally {
    spinner.remove();
    button.disabled = false;
  }
}

/**
 * Builds a setDialogMessage(type, text) function for one dialog: shows text
 * styled per type, or hides the element when text is falsy.
 * @param {string} elementId - Id of the message element inside the dialog.
 * @param {Object<string, string>} [styles] - Space-separated classes to apply per type; defaults to an `error` style.
 * @returns {function(?string, string): void} setDialogMessage(type, text).
 */
export function makeDialogMessage(elementId, styles = { error: ERROR_MESSAGE_CLASS }) {
  const tokens = Object.fromEntries(
    Object.entries(styles).map(([type, cls]) => [type, cls.split(/\s+/)]),
  );
  return function setDialogMessage(type, text) {
    const el = document.getElementById(elementId);
    if (!el) return;
    Object.values(tokens).forEach((cls) => el.classList.remove(...cls));
    if (!text) {
      el.classList.add('hidden');
      el.textContent = '';
      return;
    }
    el.classList.remove('hidden');
    el.classList.add(...tokens[type]);
    el.textContent = text;
  };
}

/**
 * GETs url and decodes its JSON body.
 * @param {string} url - Request URL.
 * @returns {Promise<*>} The decoded body.
 * @throws {Error} With the server's error message when the response is not ok.
 */
export async function getJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(await extractErrorMessage(res, `Request failed (HTTP ${res.status}).`));
  return res.json();
}

/**
 * Awaits a request made for its side effect (toggle, delete) and reports a
 * failure to the user.
 * @param {Promise<Response>} request - The pending fetch.
 * @param {string} failureMessage - Message shown when the request fails without a server-provided one.
 * @returns {Promise<boolean>} Whether the request succeeded.
 */
export async function runAction(request, failureMessage) {
  try {
    const res = await request;
    if (res.ok) return true;
    notifyError(await extractErrorMessage(res, failureMessage));
  } catch {
    notifyError(failureMessage);
  }
  return false;
}

/**
 * Renders a centered paragraph reporting a failed load.
 * @param {string} message - Error text; escaped here.
 * @returns {string} Paragraph markup, spanning every column of a grid parent.
 */
export function errorBlock(message) {
  return `<p class="col-span-full text-center text-red-700 dark:text-red-400 py-8" role="alert">${esc(message)}</p>`;
}

/**
 * Renders a full-width table row reporting a failed load.
 * @param {number} colspan - Number of columns the row spans.
 * @param {string} message - Error text; escaped here.
 * @returns {string} Row markup.
 */
export function errorRow(colspan, message) {
  return `<tr><td colspan="${colspan}">${errorBlock(message)}</td></tr>`;
}

const TOAST_LIFETIME_MS = 6000;

/**
 * Shows a dismissible error toast in the bottom-right corner; it closes on
 * click or after a few seconds.
 * @param {string} message - Text to show.
 */
export function notifyError(message) {
  let stack = document.getElementById('app-toasts');
  if (!stack) {
    stack = document.createElement('div');
    stack.id = 'app-toasts';
    stack.className = 'fixed z-50 bottom-4 right-4 flex flex-col gap-2 max-w-sm';
    document.body.append(stack);
  }
  const toast = document.createElement('div');
  toast.setAttribute('role', 'alert');
  toast.className = `text-sm px-4 py-3 border rounded-lg shadow-lg cursor-pointer ${ERROR_MESSAGE_CLASS}`;
  toast.textContent = message;
  toast.addEventListener('click', () => toast.remove());
  stack.append(toast);
  setTimeout(() => toast.remove(), TOAST_LIFETIME_MS);
}
