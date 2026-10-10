import { esc, extractErrorMessage, fmtTime, initNavPolling, postJSON, putJSON, withPending } from './app.js';

'use strict';

(() => {
  const cardsEl = document.getElementById('proxy-cards');
  const modeNote = document.getElementById('proxy-mode-note');
  const integrationsById = new Map();

  const FIELD_CLASS = 'w-full text-sm px-3 py-1.5 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500';
  const LABEL_CLASS = 'block text-xs text-fg-muted uppercase tracking-wide mb-1';
  const BUTTON_CLASS = 'flex gap-2 items-center text-sm px-3 py-1.5 bg-surface border border-line-strong rounded-md hover:bg-surface-hover disabled:opacity-50 disabled:cursor-not-allowed';

  /**
   * Renders one custom-header key/value input row.
   * @param {string} [key=''] - Header name.
   * @param {string} [value=''] - Header value.
   * @returns {string} Row markup.
   */
  function headerRow(key = '', value = '') {
    return `<div class="header-row grid grid-cols-12 items-center gap-2">
      <input type="text" value="${esc(key)}" placeholder="Header name" aria-label="Header name" class="header-key col-span-5 ${FIELD_CLASS}">
      <input type="text" value="${esc(value)}" placeholder="Value" aria-label="Header value" class="header-value col-span-6 ${FIELD_CLASS}">
      <button type="button" class="remove-header-btn col-span-1 text-fg-subtle hover:text-red-600 dark:hover:text-red-400" aria-label="Remove header">✕</button>
    </div>`;
  }

  /**
   * Describes the integration's last price-sync outcome.
   * @param {object} p - Proxy integration from the API.
   * @returns {string} Status line text.
   */
  function syncStatus(p) {
    if (p.price_sync_error) return `Last price sync failed: ${p.price_sync_error}`;
    if (p.price_synced_at) return `Prices last synced ${fmtTime(p.price_synced_at)}.`;
    return 'Prices not synced yet.';
  }

  /**
   * Renders one proxy integration card with its edit form.
   * @param {object} p - Proxy integration from the API.
   * @returns {string} Card markup.
   */
  function buildCard(p) {
    const headers = Object.entries(p.custom_headers ?? {});
    const badge = p.active
      ? '<span class="text-emerald-700 dark:text-emerald-400 text-xs font-medium px-1.5 py-0.5 bg-emerald-50 dark:bg-emerald-500/15 rounded">Active</span>'
      : '<span class="text-fg-subtle text-xs font-medium px-1.5 py-0.5 bg-surface-hover rounded">Inactive</span>';
    const tokenPlaceholder = p.has_auth_token ? `Stored (${p.auth_token_hint}) — leave blank to keep` : 'Auth token';
    return `<article class="proxy-card p-5 bg-surface rounded-lg border border-line" data-id="${p.id}" aria-label="${esc(p.name)} integration">
      <form class="proxy-form flex flex-col gap-4">
        <div class="flex items-center justify-between gap-3">
          <h2 class="flex items-center gap-2 text-base font-semibold">${esc(p.name)} ${badge}</h2>
          <button type="button" class="toggle-active-btn ${BUTTON_CLASS}">${p.active ? 'Deactivate' : 'Activate'}</button>
        </div>
        <div>
          <label class="${LABEL_CLASS}">Base URL
            <input type="url" name="base_url" required value="${esc(p.base_url)}" placeholder="http://localhost:4000" class="normal-case tracking-normal mt-1 ${FIELD_CLASS}">
          </label>
        </div>
        <div>
          <label class="${LABEL_CLASS}">Auth token
            <input type="password" name="auth_token" autocomplete="off" placeholder="${esc(tokenPlaceholder)}" class="normal-case tracking-normal mt-1 ${FIELD_CLASS}">
          </label>
        </div>
        <fieldset class="flex flex-col gap-2">
          <legend class="${LABEL_CLASS}">Custom headers</legend>
          <div class="headers flex flex-col gap-2">${headers.map(([k, v]) => headerRow(k, v)).join('')}</div>
          <button type="button" class="add-header-btn self-start ${BUTTON_CLASS}">Add header</button>
        </fieldset>
        <div class="flex items-center gap-2">
          <label class="flex items-center gap-2 text-xs text-fg-muted uppercase tracking-wide">Sync prices every
            <input type="number" name="price_sync_interval_minutes" min="0" step="1" required value="${p.price_sync_interval_minutes}" class="w-20 normal-case tracking-normal ${FIELD_CLASS}">
          </label>
          <span class="text-xs text-fg-muted">min (0 = off)</span>
        </div>
        <p class="${p.price_sync_error ? 'text-red-600 dark:text-red-400' : 'text-fg-muted'} text-xs">${esc(syncStatus(p))}</p>
        <p class="card-message hidden text-xs text-red-700 dark:text-red-400" role="alert"></p>
        <div class="flex justify-end">
          <button type="submit" class="save-btn text-white text-sm px-3 py-1.5 bg-emerald-600 rounded-md hover:bg-emerald-700 flex gap-2 items-center disabled:opacity-50 disabled:cursor-not-allowed">Save</button>
        </div>
      </form>
    </article>`;
  }

  async function loadIntegrations() {
    const res = await fetch('/api/proxy-integrations');
    const integrations = res.ok ? await res.json() : [];
    integrationsById.clear();
    integrations.forEach((p) => integrationsById.set(String(p.id), p));
    const active = integrations.find((p) => p.active);
    modeNote.textContent = active
      ? `Requests are routed through ${active.name}.`
      : 'No proxy is active — requests go directly to the Anthropic API.';
    cardsEl.innerHTML = integrations.length
      ? integrations.map(buildCard).join('')
      : '<p class="text-center text-fg-subtle py-8">No proxy integrations available.</p>';
  }

  /**
   * Shows an error under a card's form, or hides it when text is empty.
   * @param {HTMLElement} card - The card element.
   * @param {string} text - Error message.
   */
  function setCardMessage(card, text) {
    const el = card.querySelector('.card-message');
    el.textContent = text;
    el.classList.toggle('hidden', !text);
  }

  /**
   * Reads a card's form into an update payload.
   * @param {HTMLFormElement} formEl - The card's form.
   * @returns {object} Request body for PUT /api/proxy-integrations/{id}.
   */
  function readForm(formEl) {
    const data = new FormData(formEl);
    const customHeaders = {};
    formEl.querySelectorAll('.header-row').forEach((row) => {
      const key = row.querySelector('.header-key').value.trim();
      if (key) customHeaders[key] = row.querySelector('.header-value').value;
    });
    return {
      base_url: String(data.get('base_url')).trim(),
      auth_token: String(data.get('auth_token')).trim(),
      custom_headers: customHeaders,
      price_sync_interval_minutes: parseInt(data.get('price_sync_interval_minutes'), 10),
    };
  }

  cardsEl.addEventListener('submit', async (e) => {
    e.preventDefault();
    const card = e.target.closest('.proxy-card');
    const btn = card.querySelector('.save-btn');
    setCardMessage(card, '');
    await withPending(btn, async () => {
      const res = await putJSON(`/api/proxy-integrations/${card.dataset.id}`, readForm(e.target));
      if (!res.ok) {
        setCardMessage(card, await extractErrorMessage(res, 'Failed to save integration.'));
        return;
      }
      loadIntegrations();
    });
  });

  cardsEl.addEventListener('click', async (e) => {
    const card = e.target.closest('.proxy-card');
    if (!card) return;
    if (e.target.closest('.add-header-btn')) {
      card.querySelector('.headers').insertAdjacentHTML('beforeend', headerRow());
    } else if (e.target.closest('.remove-header-btn')) {
      e.target.closest('.header-row').remove();
    } else if (e.target.closest('.toggle-active-btn')) {
      const p = integrationsById.get(card.dataset.id);
      const btn = e.target.closest('.toggle-active-btn');
      setCardMessage(card, '');
      await withPending(btn, async () => {
        const res = await postJSON(`/api/proxy-integrations/${p.id}/${p.active ? 'deactivate' : 'activate'}`, {});
        if (!res.ok) {
          setCardMessage(card, await extractErrorMessage(res, 'Failed to change integration status.'));
          return;
        }
        loadIntegrations();
      });
    }
  });

  loadIntegrations();
  initNavPolling();
})();
