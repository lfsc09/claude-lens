'use strict';

import { errorBlock, esc, extractErrorMessage, fmtCost, fmtTime, getJSON, initNavPolling, makeDialogMessage, notifyError, postJSON, putJSON, runAction, withPending } from './app.js';

(() => {
  const grid = document.getElementById('prices-grid');
  const pricesById = new Map();
  let editingId = null;

  const dialog = document.getElementById('price-dialog');
  const form = document.getElementById('price-form');
  const dialogTitle = document.getElementById('price-dialog-title');
  const submitBtn = document.getElementById('price-dialog-submit');
  const prefixInput = document.getElementById('price-model-prefix');

  const TIER_FIELDS = [
    ['input_per_m_above_200k', 'Input'],
    ['output_per_m_above_200k', 'Output'],
    ['cache_write_per_m_above_200k', 'Cache write'],
    ['cache_read_per_m_above_200k', 'Cache read'],
  ];

  /**
   * Renders the above-200k-token rates of a price, when it has any.
   * @param {object} p - Model price from the API.
   * @returns {string} Section markup, or '' when no tier is set.
   */
  function above200kSectionHtml(p) {
    const rows = TIER_FIELDS
      .filter(([field]) => p[field] != null)
      .map(([field, label]) => `<div class="flex justify-between"><span class="text-fg-muted">${label} $/M</span><span class="font-mono text-fg">${fmtCost(p[field])}</span></div>`)
      .join('');
    if (!rows) return '';
    return `<div class="pt-3 border-t border-line">
      <p class="text-xs font-medium uppercase tracking-wide text-amber-600 dark:text-amber-400 mb-1.5">Above 200k tokens</p>
      <div class="grid grid-cols-2 text-sm gap-y-1.5 gap-x-4">${rows}</div>
    </div>`;
  }

  /**
   * Renders one model price as a card.
   * @param {object} p - Model price from the API.
   * @returns {string} Card markup.
   */
  function buildCard(p) {
    return `<article class="flex flex-col gap-3 p-4 bg-surface rounded-lg border border-line" data-id="${p.id}">
      <div class="flex items-start justify-between gap-2">
        <p class="font-mono text-sm font-medium break-all">${esc(p.model_prefix)}</p>
        <div class="shrink-0 flex items-center gap-2">
          <button type="button" command="show-modal" commandfor="price-dialog" class="edit-btn text-sm text-fg-subtle hover:text-fg">Edit</button>
          <button type="button" class="delete-btn text-sm text-fg-subtle hover:text-red-600 dark:hover:text-red-400">Delete</button>
        </div>
      </div>
      <div class="pt-3 border-t border-line">
        <div class="grid grid-cols-2 text-sm gap-y-1.5 gap-x-4">
          <div class="flex justify-between"><span class="text-fg-muted">Input $/M</span><span class="font-mono text-fg">${fmtCost(p.input_per_m)}</span></div>
          <div class="flex justify-between"><span class="text-fg-muted">Output $/M</span><span class="font-mono text-fg">${fmtCost(p.output_per_m)}</span></div>
          <div class="flex justify-between"><span class="text-fg-muted">Cache write $/M</span><span class="font-mono text-fg">${fmtCost(p.cache_write_per_m)}</span></div>
          <div class="flex justify-between"><span class="text-fg-muted">Cache read $/M</span><span class="font-mono text-fg">${fmtCost(p.cache_read_per_m)}</span></div>
        </div>
      </div>
      ${above200kSectionHtml(p)}
      <div class="flex-1 flex items-end">
        <div class="flex justify-end w-full pt-3 border-t border-line">
          <p class="text-xs text-fg-subtle">Updated ${fmtTime(p.updated_at)}</p>
        </div>
      </div>
    </article>`;
  }

  /**
   * Deletes a model price after confirmation and reloads the grid.
   * @param {object} p - Model price from the API.
   */
  async function deletePrice(p) {
    if (!confirm(`Delete the price for ${p.model_prefix}?`)) return;
    if (await runAction(fetch(`/api/prices/${p.id}`, { method: 'DELETE' }), 'Failed to delete price.')) loadPrices();
  }

  /** Fetches the prices and renders them, or an error when the fetch fails. */
  async function loadPrices() {
    let prices;
    try {
      prices = await getJSON('/api/prices');
    } catch (err) {
      grid.innerHTML = errorBlock(err.message);
      return;
    }

    pricesById.clear();
    if (!prices.length) {
      grid.innerHTML = '<p class="col-span-full text-center text-fg-subtle py-8">No model prices configured.</p>';
      return;
    }

    prices.forEach((p) => pricesById.set(String(p.id), p));
    grid.innerHTML = prices.map(buildCard).join('');
  }

  grid.addEventListener('click', (e) => {
    const card = e.target.closest('article[data-id]');
    if (!card) return;
    const p = pricesById.get(card.dataset.id);
    if (!p) return;

    if (e.target.closest('.edit-btn')) {
      startEdit(p);
    } else if (e.target.closest('.delete-btn')) {
      deletePrice(p);
    }
  });

  // ── Add / Edit dialog wiring ────────────────────────────────────────
  const setDialogMessage = makeDialogMessage('price-dialog-message');

  /**
   * Fills the dialog with a price for editing.
   * @param {object} p - Model price from the API.
   */
  function startEdit(p) {
    editingId = p.id;
    dialogTitle.textContent = 'Edit model price';
    submitBtn.textContent = 'Save changes';

    prefixInput.value = p.model_prefix;
    prefixInput.disabled = true;
    form.querySelector('[name="input_per_m"]').value = p.input_per_m;
    form.querySelector('[name="output_per_m"]').value = p.output_per_m;
    form.querySelector('[name="cache_write_per_m"]').value = p.cache_write_per_m;
    form.querySelector('[name="cache_read_per_m"]').value = p.cache_read_per_m;
    TIER_FIELDS.forEach(([field]) => {
      form.querySelector(`[name="${field}"]`).value = p[field] ?? '';
    });
  }

  /** Returns the dialog to its empty "add" state. */
  function resetDialog() {
    editingId = null;
    dialogTitle.textContent = 'Add a model price';
    submitBtn.textContent = 'Add';
    prefixInput.disabled = false;
    setDialogMessage(null, '');
    form.reset();
  }
  dialog.addEventListener('close', resetDialog);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = new FormData(form);
    const prefix = String(data.get('model_prefix') || '').trim();
    if (!prefix) return;

    const payload = {
      model_prefix: prefix,
      input_per_m: parseFloat(data.get('input_per_m')),
      output_per_m: parseFloat(data.get('output_per_m')),
      cache_write_per_m: parseFloat(data.get('cache_write_per_m')) || 0,
      cache_read_per_m: parseFloat(data.get('cache_read_per_m')) || 0,
    };
    TIER_FIELDS.forEach(([field]) => {
      const raw = String(data.get(field) || '').trim();
      payload[field] = raw ? parseFloat(raw) : null;
    });

    await withPending(submitBtn, async () => {
      const res = editingId ? await putJSON(`/api/prices/${editingId}`, payload) : await postJSON('/api/prices', payload);
      if (!res.ok) {
        setDialogMessage('error', await extractErrorMessage(res, editingId ? 'Failed to update price.' : 'Failed to add price.'));
        return;
      }
      dialog.close();
      loadPrices();
    });
  });

  // ── Sync prices from the active proxy integration ────────────────────
  const syncBtn = document.getElementById('sync-prices-btn');
  const syncBtnWrap = document.getElementById('sync-btn-wrap');
  const syncMessageEl = document.getElementById('sync-message');

  /**
   * Shows a status line next to the sync button.
   * @param {string} text - Message to show; empty clears it.
   * @param {boolean} [isError=false] - Whether to style it as an error.
   */
  function setSyncMessage(text, isError = false) {
    syncMessageEl.textContent = text;
    syncMessageEl.classList.toggle('text-red-600', isError);
    syncMessageEl.classList.toggle('dark:text-red-400', isError);
    syncMessageEl.classList.toggle('text-fg-muted', !isError);
  }

  /**
   * Fetches the currently active proxy integration.
   * @returns {Promise<?object>} The active integration, or null when none is active.
   */
  async function loadActiveProxy() {
    try {
      const integrations = await getJSON('/api/proxy-integrations');
      return integrations.find((p) => p.active) ?? null;
    } catch (err) {
      notifyError(err.message);
      return null;
    }
  }

  /** Enables the sync button only while an active proxy integration can sync; otherwise its wrapper shows a tooltip with the reason. */
  async function refreshSyncAvailability() {
    const proxy = await loadActiveProxy();
    const lastSynced = proxy?.price_synced_at ? ` (last synced ${fmtTime(proxy.price_synced_at)})` : '';
    const reason = !proxy
      ? 'Price sync needs an active proxy integration.'
      : proxy.price_sync_error && `${proxy.name} sync unavailable: ${proxy.price_sync_error}${lastSynced}`;
    syncBtn.disabled = !!reason;
    if (reason) syncBtnWrap.dataset.tip = esc(reason);
    else syncBtnWrap.removeAttribute('data-tip');
    setSyncMessage(reason ? '' : `Syncing from ${proxy.name}${lastSynced}.`);
    return proxy;
  }

  syncBtn.addEventListener('click', async () => {
    const proxy = await loadActiveProxy();
    if (!proxy) {
      refreshSyncAvailability();
      return;
    }
    setSyncMessage('Syncing…');
    let outcome;
    await withPending(syncBtn, async () => {
      const res = await postJSON(`/api/proxy-integrations/${proxy.id}/sync-prices`, {});
      if (!res.ok) {
        outcome = { text: await extractErrorMessage(res, 'Sync failed.'), isError: true };
        return;
      }
      const data = await res.json();
      outcome = { text: `Synced ${data.models.length} model(s) from ${proxy.name}: ${data.created} created, ${data.updated} updated.`, isError: false };
      loadPrices();
    });
    await refreshSyncAvailability();
    if (!(outcome.isError && syncBtn.disabled)) setSyncMessage(outcome.text, outcome.isError);
  });

  loadPrices();
  refreshSyncAvailability();
  initNavPolling();
})();
