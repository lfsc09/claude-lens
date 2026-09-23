import { debounce, esc, extractErrorMessage, fmtBytes, fmtInt, initNavPolling, makeDialogMessage, postJSON } from './app.js';

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
  const clearAllDataBtn = document.getElementById('clear-all-data-btn');

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
      clearAllDataBtn.disabled = true;
      return;
    }

    let totalRows = 0;
    let totalSize = 0;
    let html = '';
    for (const t of tables) {
      totalRows += t.row_count;
      totalSize += t.size_bytes;
      if (t.name === 'exchanges') clearAllDataBtn.disabled = t.row_count === 0;
      html += `
        <tr>
          <td class="font-mono px-4 py-2">${esc(t.name)}</td>
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

  // ── Clear All Data ───────────────────────────────────────────────────────
  // Wipes every exchange, and optionally session_names + on-disk Claude Code
  // files, via the bulk-delete endpoint with confirm_all set and session_ids
  // empty.
  const setDialogMessage = makeDialogMessage('clear-all-dialog-message', {
    warning: ['text-amber-700', 'bg-amber-50', 'border-amber-200'],
    error: ['text-red-700', 'bg-red-50', 'border-red-200'],
    success: ['text-emerald-700', 'bg-emerald-50', 'border-emerald-200'],
  });

  async function fetchSessionCount() {
    const res = await fetch('/api/session-stats?limit=1');
    if (!res.ok) return 0;
    const data = await res.json();
    return data.total || 0;
  }

  clearAllDataBtn.onclick = async () => {
    const dialog = document.getElementById('clear-all-data-dialog');
    const checkbox = document.getElementById('clear-all-also-delete-claude-session');
    const countEl = document.getElementById('clear-all-session-count');
    if (checkbox) checkbox.checked = false;
    setDialogMessage(null, '');
    if (countEl) countEl.textContent = String(await fetchSessionCount());
    if (checkbox) {
      checkbox.onchange = () => {
        setDialogMessage(checkbox.checked ? 'warning' : null, checkbox.checked
          ? 'Some of these sessions may still be open in a terminal. Deleting their files now could corrupt or lose data from those sessions.'
          : '');
      };
    }
    dialog?.showModal();
  };

  const submitClearAllBtn = document.getElementById('submit-clear-all-btn');
  if (submitClearAllBtn) {
    submitClearAllBtn.onclick = async () => {
      const checkbox = document.getElementById('clear-all-also-delete-claude-session');
      const res = await postJSON('/api/exchanges/bulk-delete', {
        session_ids: [],
        also_delete_claude_session: !!checkbox?.checked,
        confirm_all: true,
      });
      if (!res.ok) {
        setDialogMessage('error', await extractErrorMessage(res, res.statusText || 'Failed to clear data.'));
        return;
      }
      setDialogMessage('success', 'All exchanges data cleared.');
      setTimeout(() => {
        document.getElementById('clear-all-data-dialog')?.close();
        load();
      }, 800);
    };
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
