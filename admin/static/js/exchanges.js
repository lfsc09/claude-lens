import { esc, costTooltip, tokensTooltip, fmtCost, fmtTime, fmtTokens, extractErrorMessage, initNav, makeAbortable } from './app.js';

'use strict';

(() => {
  const DEFAULT_PAGE_SIZE = 50;
  const PAGE_SIZES = [25, 50, 100, 200];

  const params = new URLSearchParams(window.location.search);
  const q = params.get('q') || '';

  let pageSize = parseInt(params.get('size'), 10);
  if (!PAGE_SIZES.includes(pageSize)) pageSize = DEFAULT_PAGE_SIZE;

  let requestedPage = parseInt(params.get('page'), 10);
  if (!Number.isFinite(requestedPage) || requestedPage < 1) requestedPage = 1;

  let currentPage = requestedPage;

  // ── Search box ──────────────────────────────────────────────────────────
  const searchInput = document.getElementById('search-input');
  const searchSize = document.getElementById('search-size');
  const searchClear = document.getElementById('search-clear');
  if (searchInput) searchInput.value = q;
  if (searchSize) searchSize.value = pageSize;
  if (searchClear) searchClear.classList.toggle('hidden', !q);

  function showFilterError(msg) {
    const help = document.getElementById('filter-help');
    const err = document.getElementById('filter-error');
    if (msg) {
      if (help) help.classList.add('hidden');
      if (err) { err.textContent = msg; err.classList.remove('hidden'); }
    } else {
      if (help) help.classList.remove('hidden');
      if (err) err.classList.add('hidden');
    }
  }

  function navigate(page, size, query) {
    const searchParams = new URLSearchParams();
    if (query) searchParams.set('q', query);
    searchParams.set('size', size);
    searchParams.set('page', page);
    window.location.href = '/exchanges?' + searchParams.toString();
  }

  // ── Delete (Clear session) ────────────────────────────────────────────
  const DIALOG_MESSAGE_STYLES = {
    warning: ['text-amber-700', 'bg-amber-50', 'border-amber-200'],
    error: ['text-red-700', 'bg-red-50', 'border-red-200'],
  };

  /**
   * Shared message area inside the delete dialog: shows the "session may
   * still be active" warning and any error the DELETE call comes back with,
   * so failures are visible in the UI instead of only in the console.
   */
  function setDialogMessage(type, text) {
    const el = document.getElementById('delete-dialog-message');
    if (!el) return;
    Object.values(DIALOG_MESSAGE_STYLES).forEach((cls) => el.classList.remove(...cls));
    if (!text) {
      el.classList.add('hidden');
      el.textContent = '';
      return;
    }
    el.classList.remove('hidden');
    el.classList.add(...DIALOG_MESSAGE_STYLES[type]);
    el.textContent = text;
  }

  function setDeleteSessionOption(sessionID, sessionActive) {
    const currentSessionID = sessionID || '';
    const isSessionActive = !!sessionActive;

    const checkbox = document.getElementById('also-delete-claude-session');
    function updateActiveWarning() {
      if (isSessionActive && checkbox?.checked) {
        setDialogMessage('warning', 'This session had Claude Code activity in the last 30 minutes and may still be open in a terminal. Deleting its files now could corrupt or lose data from that session.');
      } else {
        setDialogMessage(null, '');
      }
    }
    if (checkbox) checkbox.onchange = updateActiveWarning;

    // Toggle the visibility of the "Delete Session" button based on whether a session ID is present
    const confirmDeleteBtn = document.getElementById('confirm-delete-btn');
    if (confirmDeleteBtn) {
      confirmDeleteBtn.classList.toggle('hidden', !currentSessionID);
      confirmDeleteBtn.onclick = () => {
        const dialog = document.getElementById('delete-confirmation-dialog');
        if (dialog) {
          dialog.querySelector('#delete-session-id').textContent = currentSessionID;
          if (checkbox) checkbox.checked = false;
          updateActiveWarning();
          dialog.showModal();
        }
      };
    }

    // Set the onclick handler for the "Delete Session" button to open the confirmation dialog
    const submitDeleteBtn = document.getElementById('submit-delete-btn');
    if (submitDeleteBtn) {
      submitDeleteBtn.onclick = async () => {
        const alsoDeleteClaudeSession = checkbox?.checked;
        const url = '/api/exchanges' + (currentSessionID ? `?session_id=${encodeURIComponent(currentSessionID)}` : '') + (alsoDeleteClaudeSession ? '&also_delete_claude_session=true' : '');
        const response = await fetch(url, { method: 'DELETE' });
        if (!response.ok) {
          setDialogMessage('error', await extractErrorMessage(response, response.statusText || 'Failed to delete exchanges.'));
          return;
        }
        window.location.href = !alsoDeleteClaudeSession ? '/exchanges?q=' + encodeURIComponent(q) : '/exchanges';
      };
    }
  }

  // ── Table ───────────────────────────────────────────────────────────────
  function buildRow(row) {
    const label = esc(row.session_name || row.session_id);
    const modelLabel = esc(row.model || '—');
    return `<tr class="hover:bg-gray-50">
      <td class="px-4 py-2"><a href="/exchanges/${row.id}" class="text-emerald-600 hover:underline">${row.id}</a></td>
      <td class="px-4 py-2 max-w-xs truncate text-gray-700">${label}</td>
      <td class="px-4 py-2 font-mono max-w-xs">
        <div class="flex flex-col">
          <span class="text-sm text-gray-700 truncate">${modelLabel}</span>
          <span class="text-xs text-gray-400 truncate">${esc(row.path)}</span>
        </div>
      </td>
      <td class="px-4 py-2 text-gray-700 whitespace-nowrap">${fmtTime(row.timestamp)}</td>
      <td class="px-4 py-2 text-right text-gray-700" data-tip="${esc(tokensTooltip(row))}">${fmtTokens(row.total_tokens)}</td>
      <td class="px-4 py-2 text-right text-gray-700" data-tip="${esc(costTooltip(row))}">${fmtCost(row.cost)}</td>
      <td class="px-4 py-2 text-gray-400 text-center">${row.is_streaming ? '✓' : ''}</td>
      </tr>`;
  }

  // ── Pagination ──────────────────────────────────────────────────────────
  /**
   * Clamps a requested page into range: a stale bookmarked page (or one
   * typed past the last page) degrades to the nearest valid page instead of
   * rendering empty.
   * @param {number} reqPage - Requested page number.
   * @param {number} size - Rows per page.
   * @param {number} total - Total matching rows.
   * @returns {{page: number, totalPages: number, from: number, to: number}} Clamped pagination state.
   */
  function computePagination(reqPage, size, total) {
    const totalPages = Math.max(1, Math.ceil(total / size));
    const page = Math.min(Math.max(reqPage, 1), totalPages);
    if (total === 0) return { page, totalPages, from: 0, to: 0 };
    const from = (page - 1) * size + 1;
    const to = Math.min(from + size - 1, total);
    return { page, totalPages, from, to };
  }

  function renderPaginationControls(page, totalPages, from, to, total) {
    const container = document.getElementById('pagination-controls');
    if (!container) return;

    const navLink = (label, targetPage, enabled) => enabled
      ? `<a href="#" data-page="${targetPage}" class="pagination-link text-emerald-600 hover:underline">${label}</a>`
      : `<span class="text-gray-300">${label}</span>`;

    container.innerHTML = `
      <div class="flex items-center gap-2 text-gray-500">
        <label for="page-size-select">Rows per page</label>
        <select id="page-size-select" class="border border-gray-300 rounded px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-emerald-500">
          ${PAGE_SIZES.map((s) => `<option value="${s}" ${s === pageSize ? 'selected' : ''}>${s}</option>`).join('')}
        </select>
        <span>${total > 0 ? `${from}–${to} of ${total}` : '0 of 0'} results</span>
      </div>
      <div class="flex items-center gap-3">
        ${navLink('First', 1, page > 1)}
        ${navLink('Previous', page - 1, page > 1)}
        <span class="flex items-center gap-1.5 text-gray-500">
          Page
          <input id="page-jump-input" type="number" min="1" max="${totalPages}" value="${page}"
            class="border border-gray-300 rounded px-2 py-1 text-sm w-16 text-center focus:outline-none focus:ring-1 focus:ring-emerald-500">
          of ${totalPages}
        </span>
        ${navLink('Next', page + 1, page < totalPages)}
        ${navLink('Last', totalPages, page < totalPages)}
      </div>`;

    const sizeSelect = document.getElementById('page-size-select');
    if (sizeSelect) {
      sizeSelect.addEventListener('change', () => navigate(1, sizeSelect.value, q));
    }

    const jump = document.getElementById('page-jump-input');
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
        navigate(p, pageSize, q);
      });
    }
  }

  document.addEventListener('click', (e) => {
    const link = e.target.closest('.pagination-link');
    if (!link) return;
    e.preventDefault();
    navigate(Number(link.dataset.page), pageSize, q);
  });

  const loadExchanges = makeAbortable(async (signal) => {
    const searchParams = new URLSearchParams();
    if (q) searchParams.set('q', q);
    searchParams.set('limit', pageSize);
    searchParams.set('offset', (requestedPage - 1) * pageSize);

    const res = await fetch('/api/exchanges?' + searchParams.toString(), { signal });
    const tbody = document.getElementById('exchanges-tbody');

    if (!res.ok) {
      showFilterError(await extractErrorMessage(res, 'Invalid query.'));
      if (tbody) tbody.innerHTML = '<tr><td colspan="7" class="px-4 py-10 text-center text-gray-400">No exchanges found.</td></tr>';
      renderPaginationControls(1, 1, 0, 0, 0);
      return;
    }

    showFilterError('');
    const data = await res.json();
    setDeleteSessionOption(data.session_id, data.session_active);

    if (tbody) {
      tbody.innerHTML = data.rows.length
        ? data.rows.map(buildRow).join('')
        : '<tr><td colspan="7" class="px-4 py-10 text-center text-gray-400">No exchanges found.</td></tr>';
    }

    const pagination = computePagination(requestedPage, pageSize, data.total);
    currentPage = pagination.page;
    renderPaginationControls(pagination.page, pagination.totalPages, pagination.from, pagination.to, data.total);
  });

  // ── Charts ──────────────────────────────────────────────────────────────
  // Always pulls the latest `limit` rows matching the current search filter.
  const SVGNS = 'http://www.w3.org/2000/svg';

  /**
   * @param {number} limit - Max rows to fetch.
   * @param {string} query - Search-box filter query.
   * @param {AbortSignal} signal - Abort signal for the fetch.
   * @returns {Promise<object[]>} Rows in chronological order (oldest first), for left-to-right plotting.
   */
  async function fetchLatestExchanges(limit, query, signal) {
    const searchParams = new URLSearchParams();
    searchParams.set('limit', limit);
    if (query) searchParams.set('q', query);
    const res = await fetch('/api/exchanges?' + searchParams.toString(), { signal });
    if (!res.ok) return [];
    const data = await res.json();
    return (data.rows || []).toReversed();
  }

  function renderLineChart(svg, rows, series, opts = {}) {
    const W = 1000, H = 260;
    const marginLeft = 46, marginRight = 12, marginTop = 10, marginBottom = 24;
    const plotW = W - marginLeft - marginRight;
    const plotH = H - marginTop - marginBottom;

    svg.innerHTML = '';

    if (!rows.length) {
      const text = document.createElementNS(SVGNS, 'text');
      text.setAttribute('x', W / 2);
      text.setAttribute('y', H / 2);
      text.setAttribute('text-anchor', 'middle');
      text.setAttribute('fill', '#9ca3af');
      text.setAttribute('font-size', '12');
      text.textContent = 'No data yet.';
      svg.appendChild(text);
      return;
    }

    const pointCount = rows.length;
    let maxV = 0;
    rows.forEach((r) => {
      series.forEach((s) => {
        const v = s.get(r);
        if (v != null && v > maxV) maxV = v;
      });
    });
    if (maxV === 0) maxV = 1;

    const x = (i) => (pointCount === 1 ? marginLeft + plotW / 2 : marginLeft + (i * plotW) / (pointCount - 1));
    const y = (v) => marginTop + plotH - (v / maxV) * plotH;

    const yTicks = 4;
    for (let tickIndex = 0; tickIndex <= yTicks; tickIndex++) {
      const v = (maxV * tickIndex) / yTicks;
      const yy = y(v);
      const line = document.createElementNS(SVGNS, 'line');
      line.setAttribute('x1', marginLeft);
      line.setAttribute('x2', W - marginRight);
      line.setAttribute('y1', yy);
      line.setAttribute('y2', yy);
      line.setAttribute('stroke', '#e5e7eb');
      line.setAttribute('stroke-width', '1');
      svg.appendChild(line);

      const label = document.createElementNS(SVGNS, 'text');
      label.setAttribute('x', marginLeft - 6);
      label.setAttribute('y', yy + 3);
      label.setAttribute('text-anchor', 'end');
      label.setAttribute('fill', '#9ca3af');
      label.setAttribute('font-size', '9');
      label.textContent = opts.fmtY ? opts.fmtY(v) : v;
      svg.appendChild(label);
    }

    const xTickCount = Math.min(6, pointCount);
    for (let tickIndex = 0; tickIndex < xTickCount; tickIndex++) {
      const i = xTickCount === 1 ? 0 : Math.round((tickIndex * (pointCount - 1)) / (xTickCount - 1));
      const label = document.createElementNS(SVGNS, 'text');
      label.setAttribute('x', x(i));
      label.setAttribute('y', H - marginBottom + 14);
      label.setAttribute('text-anchor', 'middle');
      label.setAttribute('fill', '#9ca3af');
      label.setAttribute('font-size', '9');
      label.textContent = opts.fmtX ? opts.fmtX(rows[i]) : i;
      svg.appendChild(label);
    }

    series.forEach((s) => {
      let pathData = '';
      let started = false;
      rows.forEach((r, i) => {
        const v = s.get(r);
        if (v == null) { started = false; return; }
        pathData += `${started ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)} `;
        started = true;
      });
      if (!pathData) return;
      const path = document.createElementNS(SVGNS, 'path');
      path.setAttribute('d', pathData.trim());
      path.setAttribute('fill', 'none');
      path.setAttribute('stroke', s.color);
      path.setAttribute('stroke-width', s.width || 1.5);
      path.setAttribute('stroke-linejoin', 'round');
      path.setAttribute('stroke-linecap', 'round');
      svg.appendChild(path);
    });
  }

  function renderChartLegend(container, series) {
    container.innerHTML = series.map((s) =>
      `<span class="inline-flex items-center gap-1"><span class="inline-block w-2.5 h-2.5 rounded-sm" style="background:${s.color}"></span>${esc(s.label)}</span>`
    ).join('');
  }

  // Input/Output share the emerald family, Cache creation/read share the
  // purple family (darker = "write" side, lighter = "read" side within
  // each pair), so hue alone tells cache lines apart from non-cache ones.
  const costSeries = [
    { label: 'Total', color: '#111827', width: 2, get: (r) => r.cost },
    { label: 'Input', color: '#1d4ed8', get: (r) => r.input_cost },
    { label: 'Output', color: '#93c5fd', get: (r) => r.output_cost },
    { label: 'Cache create', color: '#9333ea', get: (r) => r.cache_creation_cost },
    { label: 'Cache read', color: '#c084fc', get: (r) => r.cache_read_cost },
  ];

  const tokensSeries = [
    { label: 'Total', color: '#111827', width: 2, get: (r) => r.total_tokens },
    { label: 'Input', color: '#1d4ed8', get: (r) => r.input_tokens },
    { label: 'Output', color: '#93c5fd', get: (r) => r.output_tokens },
    { label: 'Cache create', color: '#9333ea', get: (r) => r.cache_creation_tokens },
    { label: 'Cache read', color: '#c084fc', get: (r) => r.cache_read_tokens },
  ];

  const refreshCharts = makeAbortable(async (signal) => {
    const rows = await fetchLatestExchanges(200, q, signal);
    const costSvg = document.getElementById('cost-chart');
    if (costSvg) renderLineChart(costSvg, rows, costSeries, { fmtY: fmtCost, fmtX: (r) => '#' + r.id });
    const tokensSvg = document.getElementById('tokens-chart');
    if (tokensSvg) renderLineChart(tokensSvg, rows, tokensSeries, { fmtY: fmtTokens, fmtX: (r) => '#' + r.id });
  });

  const costLegend = document.getElementById('cost-chart-legend');
  if (costLegend) renderChartLegend(costLegend, costSeries);
  const tokensLegend = document.getElementById('tokens-chart-legend');
  if (tokensLegend) renderChartLegend(tokensLegend, tokensSeries);

  loadExchanges();
  refreshCharts();

  initNav(null, {
    onNewExchange: () => {
      refreshCharts();
      if (currentPage <= 1) loadExchanges();
    },
  });
})();
