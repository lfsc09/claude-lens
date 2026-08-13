// Shared across every admin page: formatters, the [data-tip] tooltip
// wiring, and the SSE client that drives the nav's "Proxy Service" badge
// plus each page's own live-update hook. Loaded before any page-specific
// <script>, as plain globals (no bundler, no modules).

function pad(n) {
  return String(n).padStart(2, '0');
}

function esc(s) {
  return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Under 1000 renders as a plain integer; larger values abbreviate to 1
// decimal place (1.7k, 6.3M, 1.2B). Use for summarized counts (tables,
// cards); use fmtInt for exact values (exchange detail page).
function fmtTokens(n) {
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

function fmtInt(n) {
  if (n == null) return '—';
  return Number(n).toLocaleString('en-US');
}

function fmtCost(c) {
  if (c == null) return '—';
  return `$${c < 0.01 ? c.toFixed(4) : c.toFixed(2)}`;
}

function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

// nil only when both inputs are nil, so an unpriced cache rate on one side
// still shows the other's cost instead of collapsing to "—".
function addCost(a, b) {
  if (a == null && b == null) return null;
  return (a ?? 0) + (b ?? 0);
}

function costTooltip(row) {
  const parts = [];
  if (row.input_cost != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtCost(row.input_cost)}</span></div>`);
  if (row.output_cost != null) parts.push(`<div class="flex justify-between"><b>Output:</b><span>${fmtCost(row.output_cost)}</span></div>`);
  if (row.cache_creation_cost != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtCost(row.cache_creation_cost)}</span></div>`);
  if (row.cache_read_cost != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtCost(row.cache_read_cost)}</span></div>`);
  return parts.length ? '<div class="w-32 flex flex-col gap-0.5">' + parts.join('') + '</div>' : '';
}

function tokensTooltip(row) {
  const parts = [];
  if (row.input_tokens != null) parts.push(`<div class="flex justify-between"><b>Input:</b><span>${fmtTokens(row.input_tokens)}</span></div>`);
  if (row.output_tokens != null) parts.push(`<div class="flex justify-between"><b>Output:</b><span>${fmtTokens(row.output_tokens)}</span></div>`);
  if (row.cache_creation_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache create:</b><span>${fmtTokens(row.cache_creation_tokens)}</span></div>`);
  if (row.cache_read_tokens != null) parts.push(`<div class="flex justify-between"><b>Cache read:</b><span>${fmtTokens(row.cache_read_tokens)}</span></div>`);
  return parts.length ? '<div class="w-32 flex flex-col gap-0.5">' + parts.join('') + '</div>' : '';
}

// Single floating tooltip driven by [data-tip] elements. A per-cell
// absolutely-positioned tooltip would get clipped by the tables'
// overflow-x-auto wrappers, so this uses one fixed-position element
// repositioned on hover/focus instead.
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

function connectSSE(range, onMessage) {
  const es = new EventSource('/api/stream' + (range ? `?range=${encodeURIComponent(range)}` : ''));
  es.onmessage = (e) => onMessage(JSON.parse(e.data));
  es.onerror = () => onMessage({ proxy_status: 'unreachable' });
  return es;
}

function updateProxyStatusBadge(statusValue) {
  const el = document.getElementById('proxy-status');
  if (!el) return;
  switch (statusValue) {
    case 'ok':
      el.textContent = 'Proxy Service: OK';
      el.className = 'ml-auto px-2 py-1 border border-green-300 border-dashed text-xs text-gray-500 bg-green-100 text-green-800';
      break;
    case 'degraded':
      el.textContent = 'Proxy Service: Degraded';
      el.className = 'ml-auto px-2 py-1 border border-yellow-300 border-dashed text-xs text-gray-500 bg-yellow-100 text-yellow-800';
      break;
    case 'unreachable':
      el.textContent = 'Proxy Service: Unreachable';
      el.className = 'ml-auto px-2 py-1 border border-red-300 border-dashed text-xs text-gray-500 bg-red-100 text-red-800 animate-pulse';
      break;
    default:
      el.textContent = 'Proxy Service: Unknown';
      el.className = 'ml-auto px-2 py-1 border border-gray-300 border-dashed text-xs text-gray-500 bg-gray-100 text-gray-800';
  }
}

// initNav wires the nav's proxy-status badge on every page and, when given
// handlers, forwards SSE-pushed totals/new-exchange events to the calling
// page. onNewExchange only fires when latest_exchange_id actually
// increases, so pages don't each need their own dedup bookkeeping.
function initNav(range, handlers = {}) {
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
