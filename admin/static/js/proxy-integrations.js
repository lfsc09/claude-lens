'use strict';

import { badge, errorBlock, esc, extractErrorMessage, getJSON, initNavPolling, postJSON, putJSON, timeAgoHtml, withPending } from './app.js';

(() => {
  const cardsEl = document.getElementById('proxy-cards');
  const modeNote = document.getElementById('proxy-mode-note');
  const integrationsById = new Map();

  const FIELD_BASE = 'text-sm px-3 py-1.5 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500';
  const FIELD_CLASS = `w-full ${FIELD_BASE}`;
  const LABEL_CLASS = 'block text-xs text-fg-muted uppercase tracking-wide mb-1';
  const BUTTON_CLASS = 'flex gap-2 items-center text-sm text-fg px-3 py-1.5 bg-surface border border-line-strong rounded-md hover:bg-surface-hover disabled:opacity-50 disabled:cursor-not-allowed';
  const CHECK_ICON = '<svg xmlns="http://www.w3.org/2000/svg" class="w-5 h-5 opacity-0 transition-opacity duration-150" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 13l4 4L19 7"/></svg>';
  const SAVED_FLASH_MS = 3000;
  const SAVED_FADE_MS = 300;
  const messageTimers = new WeakMap();
  const PRICE_ENDPOINTS = { litellm: '/model/info', bifrost: '/v1/models' };

  /**
   * Renders one custom-header key/value input row.
   * @param {string} [key=''] - Header name.
   * @param {string} [value=''] - Header value.
   * @returns {string} Row markup.
   */
  function headerRow(key = '', value = '') {
    return `<div class="header-row flex items-center gap-2">
      <input type="text" value="${esc(key)}" placeholder="Header name" aria-label="Header name" class="header-key flex-1 min-w-0 ${FIELD_CLASS}">
      <input type="text" value="${esc(value)}" placeholder="Value" aria-label="Header value" class="header-value flex-1 min-w-0 ${FIELD_CLASS}">
      <button type="button" class="remove-header-btn shrink-0 text-fg-subtle p-1.5 rounded hover:text-red-600 dark:hover:text-red-400" aria-label="Remove header">✕</button>
    </div>`;
  }

  /**
   * Describes the integration's last price-sync outcome.
   * @param {object} p - Proxy integration from the API.
   * @returns {string} Status line markup.
   */
  function syncStatus(p) {
    if (p.price_sync_error) return esc(`Last price sync failed: ${p.price_sync_error}`);
    if (p.price_synced_at) return `Prices last synced ${timeAgoHtml(p.price_synced_at)}`;
    return 'Prices not synced yet';
  }

  /**
   * Renders one proxy integration card with its edit form.
   * @param {object} p - Proxy integration from the API.
   * @returns {string} Card markup.
   */
  function buildCard(p) {
    const headers = Object.entries(p.custom_headers ?? {});
    const status = badge(p.active ? 'emerald' : 'muted', p.active ? 'Active' : 'Inactive', 'rounded-full text-xs');
    const tokenPlaceholder = p.has_auth_token ? `Stored (${p.auth_token_hint}) — leave blank to keep` : 'Paste the gateway token';
    const id = `proxy-${p.id}`;
    return `<article class="proxy-card ${p.active ? 'border-emerald-600' : 'border-line'} bg-surface rounded-lg border" data-id="${p.id}" aria-label="${esc(p.name)} integration">
      <form class="proxy-form">
        <header class="flex items-start justify-between gap-4 px-5 py-4 border-b border-line">
          <div>
            <h2 class="flex items-center gap-2 text-base font-semibold">${esc(p.name)} ${status}</h2>
            <p class="text-xs text-fg-subtle mt-0.5">Prices are read from <code>${PRICE_ENDPOINTS[p.type] ?? ''}</code> on the gateway.</p>
          </div>
          <button type="button" class="toggle-active-btn shrink-0 ${BUTTON_CLASS}">${p.active ? 'Deactivate' : 'Activate'}</button>
        </header>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-4 px-5 py-4">
          <div>
            <label for="${id}-base-url" class="${LABEL_CLASS}">Base URL</label>
            <input type="url" id="${id}-base-url" name="base_url" required value="${esc(p.base_url)}" placeholder="http://localhost:4000" class="${FIELD_CLASS}">
          </div>
          <div>
            <label for="${id}-token" class="${LABEL_CLASS}">Auth token</label>
            <input type="password" id="${id}-token" name="auth_token" autocomplete="off" placeholder="${esc(tokenPlaceholder)}" class="${FIELD_CLASS}">
          </div>
        </div>

        <fieldset class="px-5 pb-4">
          <legend class="${LABEL_CLASS}">Custom headers</legend>
          <div class="headers flex flex-col gap-2">${headers.map(([k, v]) => headerRow(k, v)).join('')}</div>
          <p class="no-headers ${headers.length ? 'hidden' : ''} text-xs text-fg-subtle">None — only the auth token is added to forwarded requests.</p>
          <button type="button" class="add-header-btn mt-2 ${BUTTON_CLASS}">Add header</button>
        </fieldset>

        <footer class="flex flex-wrap items-center justify-between gap-4 px-5 py-4 bg-surface-hover border-t border-line rounded-b-lg">
          <div class="flex flex-col gap-1">
            <label class="flex items-center gap-2 text-sm text-fg-muted">
              Sync prices every
              <input type="number" name="price_sync_interval_minutes" min="0" step="1" required value="${p.price_sync_interval_minutes}" aria-label="Price sync interval in minutes" class="w-20 ${FIELD_BASE}">
              min <span class="text-xs text-fg-subtle">(0 = off)</span>
            </label>
            ${p.active ? `<p class="${p.price_sync_error ? 'text-red-600 dark:text-red-400' : 'text-fg-subtle'} text-xs">${syncStatus(p)}</p>` : ''}
          </div>
          <div class="flex items-center gap-3">
            <p class="card-message hidden text-xs" role="alert"></p>
            <button type="submit" class="save-btn flex gap-2 items-center text-white text-sm px-4 py-1.5 bg-emerald-600 rounded-md hover:bg-emerald-700 disabled:opacity-50 disabled:cursor-not-allowed">Save</button>
          </div>
        </footer>
      </form>
    </article>`;
  }

  /**
   * Renders the banner stating whether requests go through a proxy or directly to Anthropic.
   * @param {?object} [active] - The active proxy integration, or null when requests go direct.
   * @param {boolean} [hidden=false] - Whether to hide the banner, e.g. when loading failed.
   */
  function renderModeNote(active, hidden = false) {
    modeNote.classList.toggle('hidden', hidden);
    if (hidden) return;
    const tone = active
      ? 'border-emerald-600 bg-emerald-50 dark:bg-emerald-950'
      : 'border-line-strong bg-surface';
    const dot = active ? 'bg-emerald-500 dark:bg-emerald-400' : 'bg-fg-subtle';
    const title = active ? `Routing through ${active.name}` : 'Direct to Anthropic';
    const detail = active
      ? `Requests are forwarded to <code>${esc(active.base_url)}</code>.`
      : 'No proxy is active — requests go directly to the Anthropic API.';
    modeNote.className = `flex items-start gap-3 max-w-2xl mt-4 px-4 py-3 border rounded-lg ${tone}`;
    modeNote.innerHTML = `<span class="relative flex shrink-0 w-2.5 h-2.5 mt-1.5" aria-hidden="true">
        ${active ? `<span class="absolute inline-flex w-full h-full rounded-full opacity-75 motion-safe:animate-ping ${dot}"></span>` : ''}
        <span class="relative inline-flex w-2.5 h-2.5 rounded-full ${dot}"></span>
      </span>
      <div>
        <p class="text-sm font-semibold text-fg">${esc(title)}</p>
        <p class="text-xs text-fg-muted mt-0.5">${detail}</p>
      </div>`;
  }

  /** Fetches the integrations and renders their cards, or an error when the fetch fails. */
  async function loadIntegrations() {
    let integrations;
    try {
      integrations = await getJSON('/api/proxy-integrations');
    } catch (err) {
      renderModeNote(null, true);
      cardsEl.innerHTML = errorBlock(err.message);
      return;
    }
    integrationsById.clear();
    integrations.forEach((p) => integrationsById.set(String(p.id), p));
    const active = integrations.find((p) => p.active);
    renderModeNote(active ?? null);
    cardsEl.innerHTML = integrations.length
      ? integrations.map(buildCard).join('')
      : '<p class="text-center text-fg-subtle py-8">No proxy integrations available.</p>';
  }

  /**
   * Fades in a green check beside a card's Save button, then fades it out after a short delay.
   * @param {HTMLElement} card - The card element.
   */
  function flashSaved(card) {
    const el = card.querySelector('.card-message');
    setCardMessage(card, 'Saved', false, CHECK_ICON);
    const icon = el.firstElementChild;
    // Reading layout commits the initial opacity so the fade-in transition runs.
    icon.getBoundingClientRect();
    icon.classList.remove('opacity-0');
    messageTimers.set(el, setTimeout(() => {
      icon.classList.add('opacity-0');
      messageTimers.set(el, setTimeout(() => setCardMessage(card, ''), SAVED_FADE_MS));
    }, SAVED_FLASH_MS));
  }

  /**
   * Shows a status line beside a card's Save button, or hides it when text is empty.
   * @param {HTMLElement} card - The card element.
   * @param {string} text - Message to show; also the accessible label when markup is given.
   * @param {boolean} [isError=true] - Whether to style and announce it as an error.
   * @param {?string} [markup=null] - HTML to render in place of the text.
   */
  function setCardMessage(card, text, isError = true, markup = null) {
    const el = card.querySelector('.card-message');
    clearTimeout(messageTimers.get(el));
    if (markup) {
      el.innerHTML = markup;
      el.setAttribute('aria-label', text);
    } else {
      el.textContent = text;
      el.removeAttribute('aria-label');
    }
    el.setAttribute('role', isError ? 'alert' : 'status');
    el.classList.toggle('hidden', !text);
    el.classList.toggle('text-red-700', isError);
    el.classList.toggle('dark:text-red-400', isError);
    el.classList.toggle('text-emerald-700', !isError);
    el.classList.toggle('dark:text-emerald-400', !isError);
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
      await loadIntegrations();
      const savedCard = cardsEl.querySelector(`.proxy-card[data-id="${card.dataset.id}"]`);
      if (savedCard) flashSaved(savedCard);
    });
  });

  cardsEl.addEventListener('click', async (e) => {
    const card = e.target.closest('.proxy-card');
    if (!card) return;
    if (e.target.closest('.add-header-btn')) {
      card.querySelector('.headers').insertAdjacentHTML('beforeend', headerRow());
      card.querySelector('.no-headers').classList.add('hidden');
    } else if (e.target.closest('.remove-header-btn')) {
      e.target.closest('.header-row').remove();
      card.querySelector('.no-headers').classList.toggle('hidden', card.querySelector('.header-row') !== null);
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
