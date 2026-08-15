(() => {
  const tbody = document.getElementById('prices-tbody');

  function buildRow(price, index) {
    const formID = `price-form-${index}`;
    return `<tr class="hover:bg-gray-50" data-prefix="${esc(price.model_prefix)}">
      <td class="px-4 py-2 font-mono text-xs">${esc(price.model_prefix)}</td>
      <td class="px-4 py-2 text-right">
        <input form="${formID}" type="number" step="0.01" min="0" name="input_per_m" value="${price.input_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input form="${formID}" type="number" step="0.01" min="0" name="output_per_m" value="${price.output_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input form="${formID}" type="number" step="0.01" min="0" name="cache_write_per_m" value="${price.cache_write_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-right">
        <input form="${formID}" type="number" step="0.01" min="0" name="cache_read_per_m" value="${price.cache_read_per_m}"
          class="border border-gray-300 rounded px-2 py-1 text-sm w-24 text-right focus:outline-none focus:ring-1 focus:ring-emerald-500">
      </td>
      <td class="px-4 py-2 text-gray-500 whitespace-nowrap">${fmtTime(price.updated_at)}</td>
      <td class="px-4 py-2 text-right whitespace-nowrap">
        <form id="${formID}" class="hidden"></form>
        <button form="${formID}" type="submit" class="save-btn text-sm text-emerald-600 hover:underline mr-3">Save</button>
        <button type="button" class="delete-btn text-sm text-red-500 hover:text-red-700 hover:underline">Delete</button>
      </td>
      </tr>`;
  }

  async function savePrice(prefix, row) {
    const val = (name) => parseFloat(row.querySelector(`[name="${name}"]`).value);
    const res = await fetch(`/api/prices/${encodeURIComponent(prefix)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        input_per_m: val('input_per_m'),
        output_per_m: val('output_per_m'),
        cache_write_per_m: val('cache_write_per_m'),
        cache_read_per_m: val('cache_read_per_m'),
      }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      alert(body.error || 'Failed to save price.');
      return;
    }
    loadPrices();
  }

  async function deletePrice(prefix) {
    if (!confirm(`Delete price for ${prefix}?`)) return;
    await fetch(`/api/prices/${encodeURIComponent(prefix)}`, { method: 'DELETE' });
    loadPrices();
  }

  async function loadPrices() {
    const res = await fetch('/api/prices');
    const prices = res.ok ? await res.json() : [];

    tbody.innerHTML = prices.length
      ? prices.map(buildRow).join('')
      : '<tr><td colspan="7" class="px-4 py-8 text-center text-gray-400">No model prices configured.</td></tr>';

    tbody.querySelectorAll('tr[data-prefix]').forEach((row) => {
      const prefix = row.dataset.prefix;
      const form = row.querySelector('form');
      if (form) {
        form.addEventListener('submit', (e) => {
          e.preventDefault();
          savePrice(prefix, row);
        });
      }
      const deleteBtn = row.querySelector('.delete-btn');
      if (deleteBtn) {
        deleteBtn.addEventListener('click', () => deletePrice(prefix));
      }
    });
  }

  const addForm = document.getElementById('add-price-form');
  if (addForm) {
    addForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const data = new FormData(addForm);
      const prefix = String(data.get('model_prefix') || '').trim();
      if (!prefix) return;
      const res = await fetch(`/api/prices/${encodeURIComponent(prefix)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          input_per_m: parseFloat(data.get('input_per_m')),
          output_per_m: parseFloat(data.get('output_per_m')),
          cache_write_per_m: parseFloat(data.get('cache_write_per_m')),
          cache_read_per_m: parseFloat(data.get('cache_read_per_m')),
        }),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        alert(body.error || 'Failed to add price.');
        return;
      }
      addForm.reset();
      loadPrices();
    });
  }

  loadPrices();
  initNav();
})();
