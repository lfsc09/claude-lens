// Shared across every admin page: formatters, the [data-tip] tooltip
// wiring, and the SSE client that drives the nav's "Proxy Service" badge
// plus each page's own live-update hook.
'use strict';

const txtEncoder = new TextEncoder();

export function pad(n) {
  return String(n).padStart(2, '0');
}

export function esc(s) {
  return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

export function randomId() {
  return crypto.getRandomValues(new Uint32Array(1))[0].toString(16);
}

/**
 * @param {string} str - Input string to hash.
 * @returns {Promise<?string>} Hex-encoded SHA-256 digest, or null when str is falsy.
 */
export async function hashStr(str) {
  if (!str) return null;
  const msgUint8 = txtEncoder.encode(str);
  const hashBuffer = await crypto.subtle.digest('SHA-256', msgUint8);
  return new Uint8Array(hashBuffer).reduce((str, byte) => str + byte.toString(16).padStart(2, '0'), '');
}

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

export function fmtInt(n) {
  if (n == null) return '—';
  return Number(n).toLocaleString('en-US');
}

export function fmtCost(c) {
  if (c == null) return '—';
  return `$${c < 0.01 ? c.toFixed(4) : c.toFixed(2)}`;
}

export function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function fmtBytes(bytes) {
  if (bytes == null) return '—';
  if (bytes < 1024) return `${bytes} Bytes`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

export function valueToStr(content, tabSize = 2) {
  if (typeof content === 'object') {
    return JSON.stringify(content, null, tabSize);
  }
  return String(content || '');
}

/**
 * @param {?string} jsonStr - JSON text to pretty-print.
 * @param {number} [tabSize=2] - Indentation width.
 * @returns {string} Pretty-printed JSON, or the original string if it's not valid JSON.
 */
export function prettyJSON(jsonStr, tabSize = 2) {
  if (jsonStr == null) return '';
  try {
    return JSON.stringify(JSON.parse(jsonStr), null, tabSize);
  } catch {
    return jsonStr;
  }
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

export function costTooltip(row) {
  const parts = [];
  if (row.input_cost != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtCost(row.input_cost)}</span></div>`);
  if (row.output_cost != null) parts.push(`<div class="flex justify-between"><b>Output:</b><span>${fmtCost(row.output_cost)}</span></div>`);
  if (row.cache_creation_cost != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtCost(row.cache_creation_cost)}</span></div>`);
  if (row.cache_read_cost != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtCost(row.cache_read_cost)}</span></div>`);
  return parts.length ? '<div class="w-32 flex flex-col gap-0.5">' + parts.join('') + '</div>' : '';
}

export function tokensTooltip(row) {
  const parts = [];
  if (row.input_tokens != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtTokens(row.input_tokens)}</span></div>`);
  if (row.output_tokens != null) parts.push(`<div class="flex justify-between"><b>Output:</b><span>${fmtTokens(row.output_tokens)}</span></div>`);
  if (row.cache_creation_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtTokens(row.cache_creation_tokens)}</span></div>`);
  if (row.cache_read_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtTokens(row.cache_read_tokens)}</span></div>`);
  return parts.length ? '<div class="w-32 flex flex-col gap-0.5">' + parts.join('') + '</div>' : '';
}

export function dayStr(date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/**
 * Buckets a daily cost into one of 5 heatmap levels using thresholds
 * relative to the max cost across the visible range.
 * @param {number} cost - This day's cost.
 * @param {number} maxCost - Maximum daily cost across the visible range.
 * @param {number} lowThreshold - Upper bound of level 1.
 * @param {number} midThreshold - Upper bound of level 2.
 * @param {number} highThreshold - Upper bound of level 3.
 * @returns {number} Heatmap level from 0 (no activity) to 4 (highest).
 */
export function getLevel(cost, maxCost, lowThreshold, midThreshold, highThreshold) {
  if (!cost || cost === 0 || maxCost === 0) return 0;
  if (cost < lowThreshold) return 1;
  if (cost < midThreshold) return 2;
  if (cost < highThreshold) return 3;
  return 4;
}

/**
 * Adaptive precision up to 2 decimals, then 0 decimals for larger values.
 * @param {number} cost - Cost to format.
 * @returns {string} Formatted cost (e.g. "~$0.00" for sub-cent, "" for zero).
 */
export function fmtCell(cost) {
  if (!cost || cost === 0) return '';
  if (cost < 0.01) return `~$${cost.toFixed(2)}`;
  if (cost < 10) return `$${cost.toFixed(2)}`;
  if (cost < 100) return `$${cost.toFixed(1)}`;
  if (cost > 999) return `(╯°□°)╯`;
  return `$${cost.toFixed(0)}`;
}

export function fmtThreshold(v) {
  if (v < 0.0001) return `$${v.toFixed(6)}`;
  if (v < 0.01) return `$${v.toFixed(4)}`;
  if (v < 1) return `$${v.toFixed(3)}`;
  return `$${v.toFixed(2)}`;
}

// Single floating tooltip driven by [data-tip] elements: one fixed-position
// element repositioned on hover/focus instead of one per triggering cell.
(() => {
  const tip = document.getElementById('app-tooltip');
  if (!tip) return;
  let activeEl = null;

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

  function showTip(el) {
    const textOrHtml = el.getAttribute('data-tip');
    if (!textOrHtml) return;
    tip.innerHTML = textOrHtml;
    activeEl = el;
    tip.classList.remove('opacity-0');
    tip.classList.add('opacity-100');
    positionTip(el);
  }

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

export function connectSSE(range, onMessage) {
  const eventSource = new EventSource('/api/stream' + (range ? `?range=${encodeURIComponent(range)}` : ''));
  eventSource.onmessage = (e) => onMessage(JSON.parse(e.data));
  eventSource.onerror = () => onMessage({ proxy_status: 'unreachable' });
  return eventSource;
}

export function updateProxyStatusBadge(statusValue) {
  const el = document.getElementById('proxy-status');
  if (!el) return;
  switch (statusValue) {
    case 'ok':
      el.textContent = 'Proxy Service: OK';
      el.className = 'ml-auto px-2 py-1 border border-emerald-300 border-dashed text-xs text-gray-500 rounded-lg bg-emerald-100 text-emerald-800';
      break;
    case 'degraded':
      el.textContent = 'Proxy Service: Degraded';
      el.className = 'ml-auto px-2 py-1 border border-yellow-300 border-dashed text-xs text-gray-500 rounded-lg bg-yellow-100 text-yellow-800';
      break;
    case 'unreachable':
      el.textContent = 'Proxy Service: Unreachable';
      el.className = 'ml-auto px-2 py-1 border border-red-300 border-dashed text-xs text-gray-500 rounded-lg bg-red-100 text-red-800 animate-pulse';
      break;
    default:
      el.textContent = 'Proxy Service: Unknown';
      el.className = 'ml-auto px-2 py-1 border border-gray-300 border-dashed text-xs text-gray-500 rounded-lg bg-gray-100 text-gray-800';
  }
}

/**
 * Wires the nav's proxy-status badge on every page and, when given handlers,
 * forwards SSE-pushed totals/new-exchange events to the calling page.
 * onNewExchange only fires when latest_exchange_id actually increases, so
 * pages don't each need their own dedup bookkeeping.
 * @param {?string} range - Dashboard range key to scope totals to, or null.
 * @param {{onTotals?: Function, onNewExchange?: Function}} [handlers] - Optional event callbacks.
 * @returns {EventSource} The underlying SSE connection, for closing on range change.
 */
export function initNav(range, handlers = {}) {
  const { onTotals, onNewExchange } = handlers;
  let knownLatestId = 0;
  return connectSSE(range, (data) => {
    updateProxyStatusBadge(data.proxy_status);
    if (data.totals && typeof onTotals === 'function') onTotals(data.totals);
    if (data.latest_exchange_id && data.latest_exchange_id > knownLatestId) {
      knownLatestId = data.latest_exchange_id;
      if (typeof onNewExchange === 'function') onNewExchange(data.latest_exchange_id);
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
