import { debounce, esc, extractErrorMessage, fmtBytes, fmtInt, initNavPolling } from './app.js';

'use strict';

(() => {
  const versionEl = document.getElementById('about-version');
  const errorEl = document.getElementById('about-error');
  const dbPathEl = document.getElementById('about-db-path');
  const tablesBody = document.getElementById('about-tables-tbody');
  const logPathEl = document.getElementById('about-log-path');
  const logSizeEl = document.getElementById('about-log-size');
  const logTailEl = document.getElementById('about-log-tail');
  const logsSearchEl = document.getElementById('logs-search');

  // Newest-first log lines from the last load, kept around so search can
  // re-render highlights without re-fetching.
  let logLines = [];

  function showError(msg) {
    errorEl.textContent = msg;
    errorEl.classList.remove('hidden');
  }

  function renderTables(tables) {
    if (!tables.length) {
      tablesBody.innerHTML = '<tr><td colspan="3" class="text-center text-gray-400 px-4 py-8">No tables found.</td></tr>';
      return;
    }

    let totalRows = 0;
    let totalSize = 0;
    let html = '';
    for (const t of tables) {
      totalRows += t.row_count;
      totalSize += t.size_bytes;
      html += `
        <tr>
          <td class="px-4 py-2 font-mono">${esc(t.name)}</td>
          <td class="text-right px-4 py-2">${fmtInt(t.row_count)}</td>
          <td class="text-right px-4 py-2">${fmtBytes(t.size_bytes)}</td>
        </tr>`;
    }
    html += `
      <tr class="font-medium bg-gray-50">
        <td class="px-4 py-2">Total</td>
        <td class="text-right px-4 py-2">${fmtInt(totalRows)}</td>
        <td class="text-right px-4 py-2">${fmtBytes(totalSize)}</td>
      </tr>`;
    tablesBody.innerHTML = html;
  }

  /**
   * Escapes line and wraps every case-sensitive occurrence of query in
   * <mark>. query is matched as a literal substring (via String.split), not
   * a regex, so search terms with regex-special characters behave as plain
   * text.
   * @param {string} line - Raw (unescaped) log line.
   * @param {string} query - Search term; falsy means no highlighting.
   * @returns {string} HTML-safe line, with matches wrapped in <mark>.
   */
  function highlightLine(line, query) {
    if (!query) return esc(line);
    const parts = line.split(query);
    if (parts.length === 1) return esc(line);
    return parts.map((part) => esc(part)).join(`<mark class="bg-yellow-200 rounded-sm">${esc(query)}</mark>`);
  }

  function renderLogTail() {
    if (!logLines.length) {
      logTailEl.textContent = 'No log entries yet.';
      return;
    }
    const query = logsSearchEl.value;
    logTailEl.innerHTML = logLines.map((line) => highlightLine(line, query)).join('\n');

    const firstMatch = query ? logTailEl.querySelector('mark') : null;
    if (firstMatch) {
      firstMatch.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    } else {
      logTailEl.scrollTop = 0;
      logTailEl.scrollLeft = 0;
    }
  }

  function renderLogs(logPath, logSizeBytes, logTail) {
    logPathEl.textContent = logPath || '—';
    logSizeEl.textContent = fmtBytes(logSizeBytes);
    // Log tail arrives oldest-first (natural file order); show newest on top.
    logLines = logTail.toReversed();
    renderLogTail();
  }

  async function load() {
    try {
      const res = await fetch('/api/about');
      if (!res.ok) {
        showError(await extractErrorMessage(res, 'Failed to load about info.'));
        return;
      }
      const data = await res.json();
      versionEl.textContent = data.version || '—';
      dbPathEl.textContent = data.db_path || '—';
      renderTables(data.tables || []);
      renderLogs(data.log_path, data.log_size_bytes || 0, data.log_tail || []);
    } catch {
      showError('Failed to load about info.');
    }
  }

  logsSearchEl.addEventListener('input', debounce(renderLogTail, 200));

  load();
  initNavPolling();
})();
