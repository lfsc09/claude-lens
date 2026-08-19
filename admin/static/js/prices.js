(() => {
  const tbody = document.getElementById('prices-tbody');
  const FIELD_NAMES = ['input_per_m', 'output_per_m', 'cache_write_per_m', 'cache_read_per_m'];

  // Baseline values (as last saved) and dirty tracking, keyed by model_prefix.
  const originalByPrefix = {};
  const rowsByPrefix = new Map();
  const dirtyPrefixes = new Set();

  function buildRow(price) {
    return `<tr class="hover:bg-gray-50 transition-colors" data-prefix="${esc(price.model_prefix)}">
      <td class="prefix-cell px-4 py-2 font-mono text-xs transition-shadow duration-150">
        ${esc(price.model_prefix)}
        <span class="dirty-dot hidden ml-1.5 inline-block h-1.5 w-1.5 rounded-full bg-amber-500 align-middle" title="Unsaved changes"></span>
      </td>
      <td class="px-4 py-2 text-right">
        <input type="number" step="0.01" min="0" name="input_per_m" value="${price.input_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input type="number" step="0.01" min="0" name="output_per_m" value="${price.output_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input type="number" step="0.01" min="0" name="cache_write_per_m" value="${price.cache_write_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input type="number" step="0.01" min="0" name="cache_read_per_m" value="${price.cache_read_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-gray-500 whitespace-nowrap updated-cell">${fmtTime(price.updated_at)}</td>
      <td class="px-4 py-2 text-right whitespace-nowrap">
        <button type="button" class="delete-btn text-sm text-gray-400 hover:text-red-600">Delete</button>
      </td>
      </tr>`;
  }

  function readRowValues(row) {
    const values = {};
    FIELD_NAMES.forEach((name) => {
      values[name] = parseFloat(row.querySelector(`[name="${name}"]`).value);
    });
    return values;
  }

  function putPrice(prefix, values) {
    return fetch(`/api/prices/${encodeURIComponent(prefix)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(values),
    });
  }

  async function deletePrice(prefix) {
    if (!confirm(`Delete price for ${prefix}?`)) return;
    await fetch(`/api/prices/${encodeURIComponent(prefix)}`, { method: 'DELETE' });
    loadPrices();
  }

  function updateRowDirtyVisual(row, dirty) {
    const cell = row.querySelector('.prefix-cell');
    if (cell) cell.style.boxShadow = dirty ? 'inset 3px 0 0 0 #f59e0b' : 'none';
    const dot = row.querySelector('.dirty-dot');
    if (dot) dot.classList.toggle('hidden', !dirty);
  }

  function setChangesMessage(text, isError) {
    const el = document.getElementById('price-changes-message');
    if (!el) return;
    el.textContent = text || '';
    el.classList.toggle('text-red-300', !!isError);
    el.classList.toggle('text-gray-300', !isError);
  }

  /**
   * Show/hide the sticky "unsaved changes" bar and update its count to match
   * the current size of dirtyPrefixes.
   */
  function updateChangesBar() {
    const bar = document.getElementById('price-changes-bar');
    if (!bar) return;
    const count = dirtyPrefixes.size;
    const countEl = document.getElementById('price-changes-count');
    if (countEl) countEl.textContent = `${count} unsaved change${count === 1 ? '' : 's'}`;

    if (count > 0) {
      bar.classList.remove('hidden');
      requestAnimationFrame(() => bar.classList.remove('translate-y-2', 'opacity-0'));
    } else if (!bar.classList.contains('hidden')) {
      bar.classList.add('translate-y-2', 'opacity-0');
      setTimeout(() => bar.classList.add('hidden'), 200);
    }
  }

  function refreshRowDirtyState(row) {
    const prefix = row.dataset.prefix;
    const original = originalByPrefix[prefix];
    if (!original) return;
    const dirty = FIELD_NAMES.some((name) => row.querySelector(`[name="${name}"]`).value !== original[name]);
    dirtyPrefixes[dirty ? 'add' : 'delete'](prefix);
    updateRowDirtyVisual(row, dirty);
    setChangesMessage('', false);
    updateChangesBar();
  }

  function discardChanges() {
    dirtyPrefixes.forEach((prefix) => {
      const row = rowsByPrefix.get(prefix);
      if (!row) return;
      const original = originalByPrefix[prefix];
      FIELD_NAMES.forEach((name) => {
        row.querySelector(`[name="${name}"]`).value = original[name];
      });
      updateRowDirtyVisual(row, false);
    });
    dirtyPrefixes.clear();
    setChangesMessage('', false);
    updateChangesBar();
  }

  async function saveAllChanges() {
    if (dirtyPrefixes.size === 0) return;
    const saveBtn = document.getElementById('price-changes-save');
    const discardBtn = document.getElementById('price-changes-discard');
    if (saveBtn) saveBtn.disabled = true;
    if (discardBtn) discardBtn.disabled = true;
    setChangesMessage('Saving…', false);

    const prefixes = Array.from(dirtyPrefixes);
    const outcomes = await Promise.allSettled(prefixes.map(async (prefix) => {
      const row = rowsByPrefix.get(prefix);
      const res = await putPrice(prefix, readRowValues(row));
      if (!res.ok) throw new Error(await extractErrorMessage(res, `Failed to save ${prefix}.`));
      return { prefix, row };
    }));

    let failedCount = 0;
    let lastError = '';
    outcomes.forEach((outcome) => {
      if (outcome.status === 'fulfilled') {
        const { prefix, row } = outcome.value;
        originalByPrefix[prefix] = readRowValuesAsStrings(row);
        dirtyPrefixes.delete(prefix);
        updateRowDirtyVisual(row, false);
        const updatedCell = row.querySelector('.updated-cell');
        if (updatedCell) updatedCell.textContent = fmtTime(new Date().toISOString());
      } else {
        failedCount += 1;
        lastError = outcome.reason.message;
      }
    });

    if (saveBtn) saveBtn.disabled = false;
    if (discardBtn) discardBtn.disabled = false;

    if (failedCount === 0) {
      setChangesMessage(`Saved ${prefixes.length} change${prefixes.length === 1 ? '' : 's'}.`, false);
      setTimeout(updateChangesBar, 900);
    } else {
      setChangesMessage(`Saved ${prefixes.length - failedCount}, ${failedCount} failed: ${lastError}`, true);
      updateChangesBar();
    }
  }

  function readRowValuesAsStrings(row) {
    const values = {};
    FIELD_NAMES.forEach((name) => {
      values[name] = row.querySelector(`[name="${name}"]`).value;
    });
    return values;
  }

  async function loadPrices() {
    const res = await fetch('/api/prices');
    const prices = res.ok ? await res.json() : [];

    dirtyPrefixes.clear();
    rowsByPrefix.clear();
    Object.keys(originalByPrefix).forEach((key) => delete originalByPrefix[key]);
    updateChangesBar();
    setChangesMessage('', false);

    tbody.innerHTML = prices.length
      ? prices.map(buildRow).join('')
      : '<tr><td colspan="7" class="px-4 py-8 text-center text-gray-400">No model prices configured.</td></tr>';

    tbody.querySelectorAll('tr[data-prefix]').forEach((row) => {
      const prefix = row.dataset.prefix;
      rowsByPrefix.set(prefix, row);
      originalByPrefix[prefix] = readRowValuesAsStrings(row);
      const deleteBtn = row.querySelector('.delete-btn');
      if (deleteBtn) deleteBtn.addEventListener('click', () => deletePrice(prefix));
    });
  }

  tbody.addEventListener('input', (e) => {
    const row = e.target.closest('tr[data-prefix]');
    if (row) refreshRowDirtyState(row);
  });

  document.getElementById('price-changes-save')?.addEventListener('click', saveAllChanges);
  document.getElementById('price-changes-discard')?.addEventListener('click', discardChanges);

  const DIALOG_MESSAGE_STYLES = { error: ['text-red-700', 'bg-red-50', 'border-red-200'] };

  function setAddDialogMessage(type, text) {
    const el = document.getElementById('add-price-dialog-message');
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

  const addDialog = document.getElementById('add-price-dialog');
  const addForm = document.getElementById('add-price-form');
  if (addForm) {
    addForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const data = new FormData(addForm);
      const prefix = String(data.get('model_prefix') || '').trim();
      if (!prefix) return;
      const res = await putPrice(prefix, {
        input_per_m: parseFloat(data.get('input_per_m')),
        output_per_m: parseFloat(data.get('output_per_m')),
        cache_write_per_m: parseFloat(data.get('cache_write_per_m')),
        cache_read_per_m: parseFloat(data.get('cache_read_per_m')),
      });
      if (!res.ok) {
        setAddDialogMessage('error', await extractErrorMessage(res, 'Failed to add price.'));
        return;
      }
      addDialog?.close();
      loadPrices();
    });
  }
  addDialog?.addEventListener('close', () => {
    setAddDialogMessage(null, '');
    addForm?.reset();
  });

  loadPrices();
  initNavPolling();
})();
