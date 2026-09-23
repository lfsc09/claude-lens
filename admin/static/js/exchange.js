import { copyTextToClipboard, downloadTextFile, esc, estimateBytes, fmtBytes, fmtCost, fmtInt, initNavPolling, jsonViewerTheme, ruleText } from './app.js';

'use strict';

(() => {
  const exchangeId = window.location.pathname.split('/').filter(Boolean).pop();
  let derivedExchange = null;

  window.addEventListener('themechange', () => {
    const viewer = document.getElementById('request-json-viewer');
    if (viewer) viewer.setAttribute('theme', jsonViewerTheme());
  });

  /**
   * Goes back to the previous page if the referrer is same-origin, otherwise
   * does nothing.
   * @param {MouseEvent} e - Click event on the back link.
   */
  function goBackToExchanges(e) {
    const referrer = document.referrer;
    const sameOriginReferrer = referrer && new URL(referrer).origin === window.location.origin;
    if (!sameOriginReferrer) return;
    e.preventDefault();
    if (window.history.length > 1) window.history.back();
    else window.location.href = referrer;
  }
  const backLink = document.getElementById('back-link');
  if (backLink) backLink.addEventListener('click', goBackToExchanges);
  const notFoundBackLink = document.getElementById('not-found-back-link');
  if (notFoundBackLink) notFoundBackLink.addEventListener('click', goBackToExchanges);

  /**
   * Computes display-only fields (payload byte sizes, total request tokens)
   * from the raw exchange record and attaches them to it.
   * @param {object} exchange - The exchange record as returned by the API, or a falsy value.
   * @returns {object} The same exchange object, with derived fields attached.
   */
  function deriveExchange(exchange) {
    if (!exchange) return exchange;
    exchange.raw_request_bytes = estimateBytes(exchange.raw_request);
    exchange.raw_request_tokens = exchange.input_tokens + exchange.cache_creation_tokens + exchange.cache_read_tokens;
    exchange.raw_response_bytes = estimateBytes(exchange.raw_response);
    exchange.output_bytes = estimateBytes(exchange.output_text);
    return exchange;
  }

  const ABOVE_200K_TOKENS = 200_000;
  const ABOVE_200K_FIELDS = [
    ['input_per_m_above_200k', 'Input $/M (above 200k)'],
    ['output_per_m_above_200k', 'Output $/M (above 200k)'],
    ['cache_write_per_m_above_200k', 'Cache write $/M (above 200k)'],
    ['cache_read_per_m_above_200k', 'Cache read $/M (above 200k)'],
  ];

  /**
   * Renders a row for each above-200k override present on the exchange's
   * frozen price snapshot (absent on pre-migration snapshots, which never
   * had tier columns). Whether the tier actually applied to this exchange
   * is derived from its own token counts rather than stored on the
   * snapshot, since the 200k threshold is a fixed constant.
   * @param {object} exchange - The exchange record, including `matched_price` and `raw_request_tokens`.
   * @returns {string} HTML for the above-200k table rows, or an empty string when no override is set.
   */
  function above200kRowsHtml(exchange) {
    const price = exchange.matched_price;
    const rows = ABOVE_200K_FIELDS.filter(([field]) => price[field] != null);
    if (!rows.length) return '';
    const applied = exchange.raw_request_tokens > ABOVE_200K_TOKENS;
    return rows.map(([field, label]) => `
      <tr class="*:p-3">
        <td class="font-medium uppercase tracking-wide text-fg-muted">${esc(label)}</td>
        <td class="font-mono text-fg break-all text-right">
          ${fmtCost(price[field])}
          ${applied ? '<span class="text-xs font-medium align-middle ml-1.5 px-1.5 py-0.5 bg-amber-100 dark:bg-amber-500/20 text-amber-700 dark:text-amber-400 rounded">applied</span>' : ''}
        </td>
      </tr>`).join('');
  }

  function render(exchange) {
    document.getElementById('page-title').textContent = `Exchange #${exchange.id} – claude-lens Admin`;

    const content = document.getElementById('exchange-content');
    let html = `
      <h1 class="text-xl font-semibold">Exchange #${exchange.id}</h1>
      <section class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="overflow-x-auto bg-surface rounded-lg border border-line">
          <table class="w-full text-xs">
            <tbody class="divide-y divide-line">
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Session ID</td>
                <td class="font-mono text-fg break-all text-right">${esc(exchange.session_id)}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Model</td>
                <td class="font-mono text-fg text-right">${esc(exchange.model || '—')}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Was Streaming</td>
                <td class="font-mono text-fg break-all text-right">${exchange.is_streaming ? 'Yes' : 'No'}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Total Cost</td>
                <td class="font-mono text-fg break-all text-right">${fmtCost(exchange.cost)}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Context size</td>
                <td class="font-mono text-fg break-all text-right">${fmtInt(exchange.raw_request_tokens)}</td>
              </tr>
              <tr class="*:p-3 *:border-b-0">
                <td class="font-medium uppercase tracking-wide text-fg-muted !px-8">Input tokens</td>
                <td class="font-mono text-fg break-all text-right">${fmtInt(exchange.input_tokens)}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted !px-8">Cache creation tokens</td>
                <td class="font-mono text-fg break-all text-right">${fmtInt(exchange.cache_creation_tokens)}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted !px-8">Cache read tokens</td>
                <td class="font-mono text-fg break-all text-right">${fmtInt(exchange.cache_read_tokens)}</td>
              </tr>
              <tr class="*:p-3">
                <td class="font-medium uppercase tracking-wide text-fg-muted">Output tokens</td>
                <td class="font-mono text-fg break-all text-right">${fmtInt(exchange.output_tokens)}</td>
              </tr>
            </tbody>
          </table>
        </div>
        ${exchange.matched_price ? `
          <div class="overflow-x-auto bg-surface rounded-lg border border-line">
            <table class="w-full text-xs">
              <tbody class="divide-y divide-line">
                <tr class="*:p-3">
                  <td colspan="2" class="bg-surface-hover">
                    <div class="flex items-center gap-1.5">
                      <p class="font-medium uppercase tracking-wide text-fg-muted">Matched price</p>
                      <span class="text-xs text-fg-subtle" data-tip="Captured when this exchange was saved — a permanent snapshot of what was actually charged, even if the price is edited or deleted later.">ⓘ</span>
                    </div>
                  </td>
                </tr>
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Prefix</td>
                  <td class="font-mono text-fg break-all text-right">${esc(exchange.matched_price.model_prefix)}</td>
                </tr>
                ${exchange.matched_price.rule != null ? `
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Rule</td>
                  <td class="font-mono text-fg break-all text-right">${esc(ruleText(exchange.matched_price))}</td>
                </tr>
                ` : ``}
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Input $/M</td>
                  <td class="font-mono text-fg break-all text-right">${fmtCost(exchange.matched_price.input_per_m)}</td>
                </tr>
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Output $/M</td>
                  <td class="font-mono text-fg break-all text-right">${fmtCost(exchange.matched_price.output_per_m)}</td>
                </tr>
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Cache write $/M</td>
                  <td class="font-mono text-fg break-all text-right">${fmtCost(exchange.matched_price.cache_write_per_m)}</td>
                </tr>
                <tr class="*:p-3">
                  <td class="font-medium uppercase tracking-wide text-fg-muted">Cache read $/M</td>
                  <td class="font-mono text-fg break-all text-right">${fmtCost(exchange.matched_price.cache_read_per_m)}</td>
                </tr>
                ${above200kRowsHtml(exchange)}
              </tbody>
            </table>
          </div>
        ` : ``}
      </section>
      <section class="flex flex-col gap-4">
        <div class="flex w-full gap-0.5 rounded-lg shadow-xs *:text-sm" role="tablist">
          <button type="button" role="tab" id="request-tab" aria-selected="${exchange.raw_request ? 'true' : 'false'}" aria-controls="request-panel" class="flex-1 uppercase font-medium tracking-wide px-3 py-2 border rounded ${exchange.raw_request ? 'bg-fg text-canvas' : 'bg-surface-active hover:bg-surface-strong'}" trigger-content-e="request" ${exchange.raw_request ? '' : 'disabled'}>Request</button>
          <button type="button" role="tab" id="response-tab" aria-selected="false" aria-controls="response-panel" class="flex-1 uppercase font-medium tracking-wide px-3 py-2 border rounded bg-surface-active hover:bg-surface-strong" trigger-content-e="response" ${exchange.raw_response ? '' : 'disabled'}>Response</button>
        </div>
      </section>
    `;

    if (exchange.raw_request) {
      html += `
        <section class="flex flex-col gap-0.5" id="request-panel" role="tabpanel" aria-labelledby="request-tab" e-content-id="request">
          <div class="flex items-center justify-between p-4 bg-surface rounded-t-lg border border-line">
            <div class="flex items-center gap-2">
              <h2 class="font-semibold uppercase tracking-wide">Request</h2>
              <button type="button" id="request-copy-raw" class="text-xs uppercase font-medium tracking-wide px-2 py-1 rounded bg-surface-active hover:bg-surface-strong">Copy Raw</button>
              <button type="button" id="request-download-raw" class="text-xs uppercase font-medium tracking-wide px-2 py-1 rounded bg-surface-active hover:bg-surface-strong">Download Raw</button>
            </div>
            <div class="flex flex-col items-end gap-2">
              <span class="text-xs text-fg font-mono">${fmtInt(exchange.raw_request_tokens)} tokens</span>
              <span class="text-xs text-fg-muted font-mono">${fmtBytes(exchange.raw_request_bytes)}</span>
            </div>
          </div>
          <div class="p-4 bg-surface rounded-b-lg border border-line">
            <andypf-json-viewer
              id="request-json-viewer"
              indent="4"
              expanded="2"
              theme="${jsonViewerTheme()}"
              show-data-types="false"
              show-toolbar="true"
              expand-icon-type="square"
              show-copy="false"
              show-size="true"
              preserve-expanded="false"
              expand-empty="false"
              copy-with-key="false"
            ></andypf-json-viewer>
          </div>
        </section>
      `;
    }

    if (exchange.raw_response) {
      html += `
        <section class="hidden flex flex-col gap-0.5" id="response-panel" role="tabpanel" aria-labelledby="response-tab" e-content-id="response">
          <div class="flex items-center justify-between p-4 bg-surface rounded-t-lg border border-line">
            <h2 class="font-semibold uppercase tracking-wide">Response</h2>
            <div class="flex flex-col items-end gap-2">
              <span class="text-xs text-fg font-mono">${fmtInt(exchange.output_tokens)} tokens</span>
              <span class="text-xs text-fg-muted font-mono">${fmtBytes(exchange.output_bytes)}</span>
            </div>
          </div>
          <div class="max-h-[750px] overflow-y-auto text-xs text-fg font-mono whitespace-pre-wrap leading-relaxed p-4 bg-surface rounded-b-lg border border-line">${esc(exchange.output_text)}</div>
        </section>
      `;
    }

    content.innerHTML = html;

    if (exchange.raw_request) {
      const requestJsonViewer = document.getElementById('request-json-viewer');
      requestJsonViewer.data = exchange.raw_request;
    }
  }

  function showNotFound() {
    document.getElementById('exchange-content').classList.add('hidden');
    document.getElementById('not-found-content').classList.remove('hidden');
    document.getElementById('not-found-message').textContent = exchangeId
      ? `Exchange #${exchangeId} was not found.`
      : 'Page not found.';
  }

  async function load() {
    if (!exchangeId || !/^\d+$/.test(exchangeId)) {
      showNotFound();
      return;
    }

    const res = await fetch(`/api/exchanges/${exchangeId}`);
    if (!res.ok) {
      showNotFound();
      return;
    }

    derivedExchange = deriveExchange(await res.json());

    render(derivedExchange);
  }

  document.addEventListener('click', (e) => {
    const trigger = e.target.closest('[trigger-content-e]');
    if (!trigger) return;
    const triggeredId = trigger.getAttribute('trigger-content-e');
    const tabs = document.querySelectorAll('[role="tab"]');
    for (const tab of tabs) {
      const active = tab.getAttribute('trigger-content-e') === triggeredId;
      tab.setAttribute('aria-selected', active);
      tab.classList.toggle('bg-fg', active);
      tab.classList.toggle('text-canvas', active);
      tab.classList.toggle('bg-surface-active', !active);
      tab.classList.toggle('hover:bg-surface-strong', !active);
    }
    const eContents = document.querySelectorAll('[e-content-id]');
    for (const eContent of eContents) {
      eContent.classList.toggle('hidden', eContent.getAttribute('e-content-id') !== triggeredId);
    }
  });

  document.addEventListener('click', async (e) => {
    const trigger = e.target.closest('#request-copy-raw');
    if (!trigger || !derivedExchange?.raw_request) return;
    await copyTextToClipboard(trigger, derivedExchange.raw_request, 'Copy Raw');
  });

  document.addEventListener('click', (e) => {
    const trigger = e.target.closest('#request-download-raw');
    if (!trigger || !derivedExchange?.raw_request) return;
    let content = derivedExchange.raw_request;
    try {
      content = JSON.stringify(JSON.parse(content), null, 4);
    } catch {
      // raw_request wasn't valid JSON; fall back to the original text.
    }
    downloadTextFile(`exchange-${derivedExchange.id}-request.json`, content);
  });

  load();
  initNavPolling();
})();
