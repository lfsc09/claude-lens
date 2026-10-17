'use strict';

import { badge, copyTextToClipboard, downloadTextFile, esc, estimateBytes, fmtBytes, fmtCost, fmtInt, initNavPolling, jsonViewerTheme } from './app.js';

(() => {
  const exchangeId = window.location.pathname.split('/').filter(Boolean).pop();
  let derivedExchange = null;

  window.addEventListener('themechange', () => {
    for (const id of ['request-json-viewer', 'response-json-viewer']) {
      document.getElementById(id)?.setAttribute('theme', jsonViewerTheme());
    }
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
   * Merges a streamed usage object into the message's usage. Non-zero numbers
   * always replace the stored value; a zero only fills a counter that is still
   * unset, so gateways that zero early events do not mask the real counts.
   * @param {object} message - Message being assembled.
   * @param {?object} usage - Usage object carried by an event.
   */
  function mergeUsage(message, usage) {
    if (!usage) return;
    message.usage ??= {};
    for (const [key, value] of Object.entries(usage)) {
      if (value == null) continue;
      if (value === 0 && typeof message.usage[key] === 'number') continue;
      message.usage[key] = value;
    }
  }

  /**
   * Applies a content_block_delta to the block at its index.
   * @param {object} block - Content block receiving the delta.
   * @param {object} delta - The event's delta payload.
   * @param {Map<number, string>} partialJson - Accumulated tool input JSON per block index.
   * @param {number} index - Block index of the delta.
   */
  function applyDelta(block, delta, partialJson, index) {
    switch (delta.type) {
      case 'text_delta':
        block.text = (block.text ?? '') + delta.text;
        break;
      case 'thinking_delta':
        block.thinking = (block.thinking ?? '') + delta.thinking;
        break;
      case 'signature_delta':
        block.signature = (block.signature ?? '') + delta.signature;
        break;
      case 'citations_delta':
        (block.citations ??= []).push(delta.citation);
        break;
      case 'input_json_delta':
        partialJson.set(index, (partialJson.get(index) ?? '') + delta.partial_json);
        break;
    }
  }

  /**
   * Stores a tool block's accumulated input JSON on the block: parsed as
   * `input` when complete, otherwise kept verbatim as `input_json`.
   * @param {object} block - Tool-use content block.
   * @param {string} json - Accumulated partial_json text.
   */
  function finalizeToolInput(block, json) {
    try {
      block.input = json ? JSON.parse(json) : {};
    } catch {
      block.input_json = json;
    }
  }

  /**
   * Parses the `data:` payloads of a server-sent-event body, skipping lines
   * that are not valid JSON.
   * @param {string} raw - Raw text/event-stream body.
   * @returns {object[]} Decoded events in stream order.
   */
  function parseSseEvents(raw) {
    const events = [];
    for (const line of raw.split('\n')) {
      if (!line.startsWith('data:')) continue;
      const data = line.slice(5).trim();
      if (data === '[DONE]') break;
      try {
        events.push(JSON.parse(data));
      } catch {
        // Malformed lines are skipped so one bad event does not hide the rest.
      }
    }
    return events;
  }

  /**
   * Folds a raw upstream response into a single Anthropic Messages API object.
   * A non-streaming body is parsed as-is; a streaming body has its content
   * blocks assembled (text, thinking, tool input JSON) and its usage merged
   * across message_start, message_delta and message_stop. Tool input of a
   * block the stream never closed is still finalized from what arrived.
   * @param {string} raw - Raw response body as stored.
   * @param {boolean} isStreaming - Whether the body is a text/event-stream.
   * @returns {?object} The assembled message, or null when nothing could be parsed.
   */
  function assembleMessage(raw, isStreaming) {
    if (!raw) return null;
    if (!isStreaming) {
      try {
        return JSON.parse(raw);
      } catch {
        return null;
      }
    }

    const events = parseSseEvents(raw);
    if (!events.length) return null;

    const message = { content: [] };
    const partialJson = new Map();
    for (const event of events) {
      switch (event.type) {
        case 'message_start':
          Object.assign(message, structuredClone(event.message));
          message.content ??= [];
          break;
        case 'content_block_start':
          message.content[event.index] = structuredClone(event.content_block);
          break;
        case 'content_block_delta': {
          const block = message.content[event.index];
          if (block) applyDelta(block, event.delta, partialJson, event.index);
          break;
        }
        case 'content_block_stop': {
          const block = message.content[event.index];
          if (block && partialJson.has(event.index)) finalizeToolInput(block, partialJson.get(event.index));
          partialJson.delete(event.index);
          break;
        }
        case 'message_delta':
          Object.assign(message, event.delta);
          mergeUsage(message, event.usage);
          break;
        case 'message_stop':
          mergeUsage(message, event.usage);
          break;
        case 'error':
          message.error = event.error;
          break;
      }
    }
    for (const [index, json] of partialJson) {
      if (message.content[index]) finalizeToolInput(message.content[index], json);
    }
    return message;
  }

  /**
   * Computes display-only fields (payload byte sizes, total request tokens,
   * the response assembled into a single message object) from the raw
   * exchange record and attaches them to it.
   * @param {object} exchange - The exchange record as returned by the API, or a falsy value.
   * @returns {object} The same exchange object, with derived fields attached.
   */
  function deriveExchange(exchange) {
    if (!exchange) return exchange;
    exchange.raw_request_bytes = estimateBytes(exchange.raw_request);
    exchange.raw_request_tokens = exchange.input_tokens + exchange.cache_creation_tokens + exchange.cache_read_tokens;
    exchange.raw_response_bytes = estimateBytes(exchange.raw_response);
    exchange.output_bytes = estimateBytes(exchange.output_text);
    exchange.response_message = assembleMessage(exchange.raw_response, exchange.is_streaming);
    return exchange;
  }

  /**
   * Renders one Request/Response tab button, selected when it is the first
   * tab with content and disabled when its payload is empty.
   * @param {string} name - Tab key: "request" | "response".
   * @param {string} label - Visible tab label.
   * @param {string} payload - Raw payload the tab reveals.
   * @param {?string} selected - Key of the initially selected tab.
   * @returns {string} Button markup.
   */
  function tabButton(name, label, payload, selected) {
    const active = name === selected;
    return `<button type="button" role="tab" id="${name}-tab" aria-selected="${active}" aria-controls="${name}-panel" class="flex-1 uppercase font-medium tracking-wide px-3 py-2 border rounded ${active ? 'bg-fg text-canvas' : 'bg-surface-active hover:bg-surface-strong'}" trigger-content-e="${name}" ${payload ? '' : 'disabled'}>${label}</button>`;
  }

  /**
   * Renders one button of the Response view toggle (assembled raw message or
   * output text).
   * @param {string} view - View key: "raw" | "output".
   * @param {string} label - Visible button label.
   * @param {string} selected - Key of the initially selected view.
   * @param {boolean} disabled - Whether the view is unavailable.
   * @returns {string} Button markup.
   */
  function responseViewButton(view, label, selected, disabled) {
    const active = view === selected;
    return `<button type="button" aria-pressed="${active}" trigger-response-view="${view}" class="text-xs uppercase font-medium tracking-wide px-2 py-1 rounded border disabled:opacity-50 disabled:cursor-not-allowed ${active ? 'bg-fg text-canvas' : 'bg-surface-active enabled:hover:bg-surface-strong'}" ${disabled ? 'disabled' : ''}>${label}</button>`;
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
   * frozen price snapshot (only those the price config set). Whether the tier actually applied to this exchange
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
          ${applied ? badge('amber', 'applied', 'rounded text-xs align-middle ml-1.5') : ''}
        </td>
      </tr>`).join('');
  }

  function render(exchange) {
    document.getElementById('page-title').textContent = `Exchange #${exchange.id} – claude-lens Admin`;

    const content = document.getElementById('exchange-content');
    const firstTab = exchange.raw_request ? 'request' : exchange.raw_response ? 'response' : null;
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
      ${firstTab ? `
      <section class="flex flex-col gap-4">
        <div class="flex w-full gap-0.5 rounded-lg shadow-xs *:text-sm" role="tablist">
          ${tabButton('request', 'Request', exchange.raw_request, firstTab)}
          ${tabButton('response', 'Response', exchange.raw_response, firstTab)}
        </div>
      </section>
      ` : `
      <section class="text-sm text-fg-muted p-4 bg-surface rounded-lg border border-line border-dashed" role="status">
        No raw payload was saved for this exchange. Enable "Save raw payloads" in Settings to keep request and response bodies of new exchanges.
      </section>
      `}
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
      const rawView = Boolean(exchange.response_message);
      html += `
        <section class="${firstTab === 'response' ? '' : 'hidden '}flex flex-col gap-0.5" id="response-panel" role="tabpanel" aria-labelledby="response-tab" e-content-id="response">
          <div class="flex items-center justify-between p-4 bg-surface rounded-t-lg border border-line">
            <div class="flex items-center gap-2">
              <h2 class="font-semibold uppercase tracking-wide">Response</h2>
              <button type="button" id="response-copy-raw" class="text-xs uppercase font-medium tracking-wide px-2 py-1 rounded bg-surface-active hover:bg-surface-strong">Copy Raw</button>
              <button type="button" id="response-download-raw" class="text-xs uppercase font-medium tracking-wide px-2 py-1 rounded bg-surface-active hover:bg-surface-strong">Download Raw</button>
              <div class="flex gap-0.5" role="group" aria-label="Response view">
                ${responseViewButton('raw', 'Raw', rawView ? 'raw' : 'output', !rawView)}
                ${responseViewButton('output', 'Output text', rawView ? 'raw' : 'output', !exchange.output_text?.trim())}
              </div>
            </div>
            <div class="flex flex-col items-end gap-2">
              <span class="text-xs text-fg font-mono">${fmtInt(exchange.output_tokens)} tokens</span>
              <span class="text-xs text-fg-muted font-mono" id="response-bytes">${fmtBytes(rawView ? exchange.raw_response_bytes : exchange.output_bytes)}</span>
            </div>
          </div>
          <div class="${rawView ? '' : 'hidden '}p-4 bg-surface rounded-b-lg border border-line" id="response-raw-view">
            <andypf-json-viewer
              id="response-json-viewer"
              indent="4"
              expanded="3"
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
          <div class="${rawView ? 'hidden ' : ''}max-h-[750px] overflow-y-auto text-xs text-fg font-mono whitespace-pre-wrap leading-relaxed p-4 bg-surface rounded-b-lg border border-line" id="response-output-view">${esc(exchange.output_text)}</div>
        </section>
      `;
    }

    content.innerHTML = html;

    if (exchange.raw_request) {
      const requestJsonViewer = document.getElementById('request-json-viewer');
      requestJsonViewer.data = exchange.raw_request;
    }
    if (exchange.response_message) {
      document.getElementById('response-json-viewer').data = exchange.response_message;
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

  document.addEventListener('click', (e) => {
    const trigger = e.target.closest('[trigger-response-view]');
    if (!trigger) return;
    const view = trigger.getAttribute('trigger-response-view');
    for (const button of document.querySelectorAll('[trigger-response-view]')) {
      const active = button === trigger;
      button.setAttribute('aria-pressed', active);
      button.classList.toggle('bg-fg', active);
      button.classList.toggle('text-canvas', active);
      button.classList.toggle('bg-surface-active', !active);
      button.classList.toggle('enabled:hover:bg-surface-strong', !active);
    }
    document.getElementById('response-raw-view').classList.toggle('hidden', view !== 'raw');
    document.getElementById('response-output-view').classList.toggle('hidden', view !== 'output');
    document.getElementById('response-bytes').textContent = fmtBytes(view === 'raw' ? derivedExchange.raw_response_bytes : derivedExchange.output_bytes);
  });

  document.addEventListener('click', async (e) => {
    const trigger = e.target.closest('#response-copy-raw');
    if (!trigger || !derivedExchange?.raw_response) return;
    await copyTextToClipboard(trigger, derivedExchange.raw_response, 'Copy Raw');
  });

  document.addEventListener('click', (e) => {
    const trigger = e.target.closest('#response-download-raw');
    if (!trigger || !derivedExchange?.raw_response) return;
    downloadTextFile(`exchange-${derivedExchange.id}-response.txt`, derivedExchange.raw_response, 'text/plain');
  });

  load();
  initNavPolling();
})();
