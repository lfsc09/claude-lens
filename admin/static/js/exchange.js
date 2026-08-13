(() => {
  const id = window.location.pathname.split('/').filter(Boolean).pop();

  // prettyJSON mirrors admin's old server-side prettyJSON: a value that
  // isn't valid JSON (or is absent) renders unchanged, rather than erroring.
  function prettyJSON(s) {
    if (s == null) return '';
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return s;
    }
  }

  function section(title, bodyHtml) {
    return `<section class="mb-6">
      <h2 class="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">${title}</h2>
      ${bodyHtml}
      </section>`;
  }

  function render(row) {
    document.getElementById('page-title').textContent = `Exchange #${row.id} – claude-lens Admin`;

    const content = document.getElementById('exchange-content');
    let html = `<h1 class="text-xl font-semibold mb-6">Exchange #${row.id}</h1>
      <div class="bg-white rounded-lg border border-gray-200 p-5 mb-6 grid grid-cols-2 sm:grid-cols-3 gap-x-8 gap-y-4 text-sm">
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Session ID</p>
          <p class="mt-1 font-mono text-xs break-all">${esc(row.session_id)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Session name</p>
          <p class="mt-1">${esc(row.session_name || '—')}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Time</p>
          <p class="mt-1">${fmtTime(row.timestamp)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Path</p>
          <p class="mt-1 font-mono text-xs">${esc(row.path)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Input tokens</p>
          <p class="mt-1">${fmtInt(row.input_tokens)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Output tokens</p>
          <p class="mt-1">${fmtInt(row.output_tokens)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Cache creation tokens</p>
          <p class="mt-1">${fmtInt(row.cache_creation_tokens)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Cache read tokens</p>
          <p class="mt-1">${fmtInt(row.cache_read_tokens)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Model</p>
          <p class="mt-1 font-mono text-xs">${esc(row.model || '—')}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Cost</p>
          <p class="mt-1">${fmtCost(row.cost)}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 uppercase tracking-wide">Streaming</p>
          <p class="mt-1">${row.is_streaming ? 'Yes' : 'No'}</p>
        </div>
      </div>`;

    if (row.output_text) {
      html += section('Output', `<div class="bg-white rounded-lg border border-gray-200 p-4 text-sm whitespace-pre-wrap leading-relaxed">${esc(row.output_text)}</div>`);
    }
    if (row.input_messages) {
      html += section('Input messages', `<pre class="bg-white rounded-lg border border-gray-200 p-4 text-xs overflow-x-auto leading-relaxed">${esc(prettyJSON(row.input_messages))}</pre>`);
    }

    html += `<details class="mb-3 group">
      <summary class="cursor-pointer text-xs font-semibold text-gray-500 uppercase tracking-wide select-none">Raw request</summary>
      <pre class="mt-2 bg-white rounded-lg border border-gray-200 p-4 text-xs overflow-x-auto leading-relaxed">${esc(row.raw_request || '')}</pre>
      </details>
      <details class="group">
      <summary class="cursor-pointer text-xs font-semibold text-gray-500 uppercase tracking-wide select-none">Raw response</summary>
      <pre class="mt-2 bg-white rounded-lg border border-gray-200 p-4 text-xs overflow-x-auto leading-relaxed">${esc(row.raw_response || '')}</pre>
      </details>`;

    content.innerHTML = html;
  }

  function showNotFound() {
    document.getElementById('exchange-content').classList.add('hidden');
    document.getElementById('not-found-content').classList.remove('hidden');
    document.getElementById('not-found-message').textContent = id
      ? `Exchange #${id} was not found.`
      : 'Page not found.';
  }

  async function load() {
    if (!id || !/^\d+$/.test(id)) {
      showNotFound();
      return;
    }
    const res = await fetch(`/api/exchanges/${id}`);
    if (!res.ok) {
      showNotFound();
      return;
    }
    render(await res.json());
  }

  load();
  initNav();
})();
