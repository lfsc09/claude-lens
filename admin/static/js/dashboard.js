(() => {
  const RANGES = ['today', 'this-week', 'this-month', 'last-7-days', 'last-15-days', 'last-30-days', 'last-60-days'];
  const DEFAULT_RANGE = 'this-week';

  const params = new URLSearchParams(window.location.search);
  let range = params.get('range');
  if (!RANGES.includes(range)) range = DEFAULT_RANGE;

  const select = document.getElementById('predefined-date-filter');
  if (select) select.value = range;

  function setText(id, text) {
    const el = document.getElementById(id);
    if (el) el.textContent = text;
  }

  function dateStr(d) {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  }

  // Mirrors admin/dashboard_range.go's sinceForRange — every range's upper bound
  // is implicitly "now", so only the lower bound varies by preset.
  function rangeFrom(rangeKey, now) {
    const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    switch (rangeKey) {
      case 'today': return midnight;
      case 'this-week': { const d = new Date(midnight); d.setDate(d.getDate() - d.getDay()); return d; }
      case 'this-month': return new Date(now.getFullYear(), now.getMonth(), 1);
      case 'last-7-days': { const d = new Date(midnight); d.setDate(d.getDate() - 6); return d; }
      case 'last-15-days': { const d = new Date(midnight); d.setDate(d.getDate() - 14); return d; }
      case 'last-30-days': { const d = new Date(midnight); d.setDate(d.getDate() - 29); return d; }
      case 'last-60-days': { const d = new Date(midnight); d.setDate(d.getDate() - 59); return d; }
      default: return rangeFrom(DEFAULT_RANGE, now);
    }
  }

  function updateDateLabels() {
    const now = new Date();
    setText('date-from-value', dateStr(rangeFrom(range, now)));
    setText('date-to-value', dateStr(now));
  }
  updateDateLabels();

  // ── Stats cards ─────────────────────────────────────────────────────────
  function renderTotals(totals) {
    if (!totals) return;
    const inputTokens = totals.total_input_tokens ?? 0;
    const outputTokens = totals.total_output_tokens ?? 0;
    const cacheTokens = (totals.total_cache_creation_tokens ?? 0) + (totals.total_cache_read_tokens ?? 0);
    setText('total-input-tokens', fmtTokens(inputTokens));
    setText('total-input-cost', fmtCost(totals.total_input_cost));
    setText('total-output-tokens', fmtTokens(outputTokens));
    setText('total-output-cost', fmtCost(totals.total_output_cost));
    setText('total-cache-tokens', fmtTokens(cacheTokens));
    setText('total-cache-cost', fmtCost(addCost(totals.total_cache_creation_cost, totals.total_cache_read_cost)));
    setText('total-tokens', fmtTokens(inputTokens + outputTokens + cacheTokens));
    setText('total-cost', fmtCost(totals.total_cost));
  }

  // ── Per-session table ─────────────────────────────────────────────────────
  function buildSessionRow(s) {
    const cacheTok = (s.total_cache_creation_tokens ?? 0) + (s.total_cache_read_tokens ?? 0);
    const inputTok = s.total_input_tokens ?? 0;
    const outputTok = s.total_output_tokens ?? 0;
    const totalTok = inputTok + outputTok + cacheTok;
    const cost = s.total_cost ?? 0;
    const costStr = cost > 0 ? `$${cost.toFixed(4)}` : '—';
    const sessionQuery = 'session = ' + JSON.stringify(s.session_id);
    const nameHtml = `<a href="/exchanges?q=${encodeURIComponent(sessionQuery)}" class="text-emerald-600 hover:underline font-medium">${esc(s.session_name || s.session_id)}</a>${s.session_name ? `<span class="block text-xs text-gray-400 font-mono">${esc(s.session_id)}</span>` : ''}`;
    return `<tr class="hover:bg-gray-50">
      <td class="px-4 py-2">${nameHtml}</td>
      <td class="px-4 py-2 text-right text-gray-700">${s.exchange_count}</td>
      <td class="px-4 py-2 text-right text-gray-700">${fmtTokens(inputTok)}</td>
      <td class="px-4 py-2 text-right text-gray-700">${fmtTokens(outputTok)}</td>
      <td class="px-4 py-2 text-right text-gray-700">${fmtTokens(cacheTok)}</td>
      <td class="px-4 py-2 text-right text-gray-700">${fmtTokens(totalTok)}</td>
      <td class="px-4 py-2 text-right text-gray-700">${costStr}</td>
      <td class="px-4 py-2 text-gray-400 whitespace-nowrap">${fmtTime(s.last_updated)}</td>
      </tr>`;
  }

  // AbortControllers below cancel a request that's superseded before it
  // resolves — e.g. two onNewExchange events firing in quick succession, or
  // a filter switch racing with the previous range's in-flight fetch. Each
  // fetch aborts its own predecessor rather than sharing one controller, so
  // unrelated calls (totals vs. daily-costs) never cancel each other.
  let sessionStatsAbort = null;
  async function refreshSessionStats() {
    sessionStatsAbort?.abort();
    const controller = new AbortController();
    sessionStatsAbort = controller;
    try {
      const res = await fetch('/api/session-stats', { signal: controller.signal });
      if (!res.ok) return;
      const rows = await res.json();
      const tbody = document.getElementById('session-stats-tbody');
      if (!tbody) return;
      tbody.innerHTML = rows.length
        ? rows.map(buildSessionRow).join('')
        : '<tr><td colspan="8" class="px-4 py-8 text-center text-gray-400">No sessions yet.</td></tr>';
    } catch (err) {
      if (err.name !== 'AbortError') throw err;
    }
  }

  // ── Heatmap ─────────────────────────────────────────────────────────────
  let _dailyCosts = [];

  function renderHeatmap() {
    const container = document.getElementById('cost-heatmap');
    if (!container) return;

    const costByDay = {};
    let maxCost = 0;
    _dailyCosts.forEach((d) => {
      costByDay[d.day] = d.daily_cost;
      if (d.daily_cost > maxCost) maxCost = d.daily_cost;
    });

    function dayStr(date) {
      return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
    }

    // Relative thresholds based on max daily cost
    const t1 = maxCost * 0.25;
    const t2 = maxCost * 0.5;
    const t3 = maxCost * 0.75;

    const LEVELS = [
      { bg: 'bg-gray-100 border border-gray-200', text: 'text-gray-300' },
      { bg: 'bg-emerald-100', text: 'text-emerald-800' },
      { bg: 'bg-emerald-300', text: 'text-emerald-900' },
      { bg: 'bg-emerald-500', text: 'text-white' },
      { bg: 'bg-emerald-700', text: 'text-white' },
    ];

    function getLevel(cost) {
      if (!cost || cost === 0 || maxCost === 0) return 0;
      if (cost < t1) return 1;
      if (cost < t2) return 2;
      if (cost < t3) return 3;
      return 4;
    }

    /**
     * Adaptive precision up to 2 decimal, then 0 decimal for larger values.
     * If precision ends up $0.00, it will show ~$0.00, which is fine for a heatmap.
     */
    function fmtCell(cost) {
      if (!cost || cost === 0) return '';
      if (cost < 0.01) return `~$${cost.toFixed(2)}`;
      if (cost < 10) return `$${cost.toFixed(2)}`;
      if (cost < 100) return `$${cost.toFixed(1)}`;
      if (cost > 999) return `(╯°□°)╯`;
      return `$${cost.toFixed(0)}`;
    }

    function fmtThreshold(v) {
      if (v < 0.0001) return `$${v.toFixed(6)}`;
      if (v < 0.01) return `$${v.toFixed(4)}`;
      if (v < 1) return `$${v.toFixed(3)}`;
      return `$${v.toFixed(2)}`;
    }

    const today = new Date();
    today.setHours(23, 59, 59, 999);

    // Start 60 days back
    const start = new Date(today);
    start.setDate(start.getDate() - 59);
    start.setHours(0, 0, 0, 0);

    const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
    const DAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

    const weeks = [];
    const cur = new Date(start);
    while (cur <= today) {
      const week = [null, null, null, null, null, null, null];
      const weekMonth = cur.getMonth();
      const curDayOfWeek = cur.getDay();
      for (let i = curDayOfWeek; i < 7; i++) {
        week[i] = new Date(cur);
        cur.setDate(cur.getDate() + 1);
        if (cur.getMonth() !== weekMonth) break;  // Stop if month changes mid-week
      }
      weeks.push(week);
    }

    // Day label column — height matches cell h-7 + gap-1
    const dayLabelsHtml = DAY_LABELS.map((d) => `<div class="text-xs text-gray-400 w-8 h-7 flex items-center">${d}</div>`).join('');

    const monthLabelSeen = {};

    const weeksHtml = weeks.map((week) => {
      const weekMonth = week.find((day) => day !== null)?.getMonth();
      const gapClass = !monthLabelSeen[weekMonth] ? 'ml-5' : '';
      // First week always shows its month; subsequent weeks show label only when month rolls over.
      const monthLabel = !monthLabelSeen[weekMonth] ? MONTHS[weekMonth] ?? '' : '';
      monthLabelSeen[weekMonth] = true;

      const daysHtml = week.map((date) => {
        if (!date) return '<div class="w-16 h-7"></div>';

        const key = dayStr(date);
        const isWeekend = date.getDay() === 0 || date.getDay() === 6;
        const cost = costByDay[key] ?? 0;
        const isFuture = date > today;

        if (isFuture) return '<div class="w-16 h-7 rounded-sm"></div>';

        const clr = LEVELS[getLevel(cost)];
        const txt = fmtCell(cost);
        const tip = `<div class="flex gap-2"><b>${key}:</b><span>${cost > 0 ? `$${cost.toFixed(6)}` : 'no activity'}</span></div>`;

        return `<div class="w-16 h-7 rounded-sm ${clr.bg} ${clr.text} ${isWeekend && getLevel(cost) === 0 ? 'bg-gray-200' : ''} flex items-center justify-center text-[.7rem] font-mono overflow-hidden cursor-default" data-tip="${esc(tip)}">${txt ? `<span class="select-none">${txt}</span>` : ''}</div>`;
      }).join('');

      return `<div class="flex flex-col gap-1 ${gapClass}">
        <div class="text-xs text-gray-500 font-medium h-7 flex items-end pb-0.5 whitespace-nowrap">${monthLabel}</div>
        ${daysHtml}
        </div>`;
    }).join('');

    container.innerHTML = `<div class="flex gap-1 items-start">
      <div class="flex flex-col gap-1 pt-8 flex-shrink-0 mr-1">${dayLabelsHtml}</div>
      ${weeksHtml}
      </div>`;

    // Populate dynamic legend with actual thresholds
    const legend = document.getElementById('heatmap-legend');
    if (legend) {
      legend.innerHTML = maxCost === 0
        ? '<span class="text-gray-400">No spending data yet.</span>'
        : `<div class="flex items-center gap-1.5"><div class="w-3 h-3 rounded-sm bg-gray-100 border border-gray-200 flex-shrink-0"></div><span>$0.00</span></div>
          <div class="flex items-center gap-1.5"><div class="w-3 h-3 rounded-sm bg-emerald-100 flex-shrink-0"></div><span>&lt;${fmtThreshold(t1)}</span></div>
          <div class="flex items-center gap-1.5"><div class="w-3 h-3 rounded-sm bg-emerald-300 flex-shrink-0"></div><span>&lt;${fmtThreshold(t2)}</span></div>
          <div class="flex items-center gap-1.5"><div class="w-3 h-3 rounded-sm bg-emerald-500 flex-shrink-0"></div><span>&lt;${fmtThreshold(t3)}</span></div>
          <div class="flex items-center gap-1.5"><div class="w-3 h-3 rounded-sm bg-emerald-700 flex-shrink-0"></div><span>&ge;${fmtThreshold(t3)}</span></div>`;
    }
  }

  let dailyCostsAbort = null;
  async function refreshDailyCosts() {
    dailyCostsAbort?.abort();
    const controller = new AbortController();
    dailyCostsAbort = controller;
    try {
      const res = await fetch('/api/daily-costs?days=60', { signal: controller.signal });
      if (!res.ok) return;
      _dailyCosts = await res.json();
      renderHeatmap();
    } catch (err) {
      if (err.name !== 'AbortError') throw err;
    }
  }

  // range-dependent, unlike refreshSessionStats/refreshDailyCosts above —
  // used both for the initial load and for switchRange below.
  let totalsAbort = null;
  async function fetchTotals(rangeKey) {
    totalsAbort?.abort();
    const controller = new AbortController();
    totalsAbort = controller;
    try {
      const res = await fetch('/api/totals?range=' + encodeURIComponent(rangeKey), { signal: controller.signal });
      if (res.ok) renderTotals(await res.json());
    } catch (err) {
      if (err.name !== 'AbortError') throw err;
    }
  }

  // ── Live updates ──────────────────────────────────────────────────────────
  // The SSE connection's range is fixed at connect time (the server computes
  // `since` from it once), so changing range requires closing and reopening
  // it rather than just updating a variable.
  let navHandle = null;
  function reconnectNav() {
    navHandle?.close();
    navHandle = initNav(range, {
      onTotals: renderTotals,
      onNewExchange: () => {
        refreshSessionStats();
        refreshDailyCosts();
      },
    });
  }

  // ── Filter switching (SPA-style, no full page reload) ──────────────────────
  function switchRange(newRange) {
    if (!RANGES.includes(newRange) || newRange === range) return;
    range = newRange;
    history.pushState(null, '', '/?range=' + encodeURIComponent(range));
    updateDateLabels();
    fetchTotals(range);
    reconnectNav();
  }

  if (select) {
    select.addEventListener('change', () => switchRange(select.value));
  }

  window.addEventListener('popstate', () => {
    const p = new URLSearchParams(window.location.search);
    let r = p.get('range');
    if (!RANGES.includes(r)) r = DEFAULT_RANGE;
    if (r === range) return;
    range = r;
    if (select) select.value = range;
    updateDateLabels();
    fetchTotals(range);
    reconnectNav();
  });

  // ── Initial load ──────────────────────────────────────────────────────────
  async function loadDashboard() {
    await Promise.all([fetchTotals(range), refreshDailyCosts(), refreshSessionStats()]);
  }

  loadDashboard();
  reconnectNav();
})();
