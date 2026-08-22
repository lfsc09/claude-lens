import { esc, extractErrorMessage, fmtCost, fmtTime, initNavPolling, pad, fmtSessionId } from './app.js';

'use strict';

(() => {
  const tbody = document.getElementById('limiters-tbody');
  const limitersById = new Map();
  let editingId = null;

  const ALIGNED_COMBOS = { minutes: [60], hours: [1, 24], days: [1, 7], months: [1] };

  const dialog = document.getElementById('limiter-dialog');
  const form = document.getElementById('limiter-form');
  const dialogTitle = document.getElementById('limiter-dialog-title');
  const submitBtn = document.getElementById('limiter-dialog-submit');
  const sessionIDInput = document.getElementById('limiter-session-id');
  const limitAmountInput = document.getElementById('limiter-limit-amount');
  const refreshValueInput = document.getElementById('limiter-refresh-value');
  const refreshUnitSelect = document.getElementById('limiter-refresh-unit');
  const alignedCheckbox = document.getElementById('limiter-refresh-aligned');
  const alwaysActiveCheckbox = document.getElementById('limiter-always-active');
  const startHourSelect = document.getElementById('limiter-active-start-hour');
  const endHourSelect = document.getElementById('limiter-active-end-hour');

  function populateHourSelect(select) {
    select.innerHTML = Array.from({ length: 24 }, (_, h) => `<option value="${h}">${pad(h)}:00</option>`).join('');
  }
  populateHourSelect(startHourSelect);
  populateHourSelect(endHourSelect);
  startHourSelect.value = '0';
  endHourSelect.value = '23';

  function unitLabel(value, unit) {
    return value === 1 ? unit.slice(0, -1) : unit;
  }

  function fmtRefresh(l) {
    const base = `${l.refresh_value} ${unitLabel(l.refresh_value, l.refresh_unit)}`;
    return l.refresh_aligned ? `${base} (aligned)` : base;
  }

  function fmtActivePeriod(l) {
    if (l.active_start_hour == null) return 'Always';
    return `${pad(l.active_start_hour)}:00 – ${pad(l.active_end_hour)}:59`;
  }

  function scopeBadge(l) {
    if (!l.session_id) {
      return '<span class="px-1.5 py-0.5 bg-blue-50 text-blue-700 rounded font-medium">Global</span>';
    }
    return `<span class="px-1.5 py-0.5 bg-emerald-50 text-emerald-700 rounded font-medium">${esc(fmtSessionId(l.session_id))}</span>`;
  }

  function progressBar(l) {
    const pct = l.limit_amount > 0 ? Math.min(100, (l.current_cost / l.limit_amount) * 100) : 0;
    const barColor = pct >= 100 ? 'bg-red-500' : pct >= 80 ? 'bg-amber-500' : 'bg-emerald-500';
    return `<div class="w-32">
      <div class="h-1.5 w-full bg-gray-200 rounded-full overflow-hidden">
        <div class="h-full ${barColor}" style="width:${pct}%"></div>
      </div>
      <div class="text-xs text-gray-500 mt-1">${fmtCost(l.current_cost)} of ${fmtCost(l.limit_amount)}</div>
    </div>`;
  }

  function statusToggle(l) {
    return `<button type="button" class="toggle-active-btn relative inline-flex h-5 w-9 items-center rounded-full transition-colors ${l.is_active ? 'bg-emerald-600' : 'bg-gray-300'}"
      data-id="${l.id}" data-active="${l.is_active}" aria-pressed="${l.is_active}" aria-label="Toggle limiter active">
      <span class="inline-block h-3.5 w-3.5 transform rounded-full bg-white transition-transform ${l.is_active ? 'translate-x-5' : 'translate-x-0.5'}"></span>
    </button>`;
  }

  function buildRow(l) {
    return `<tr class="hover:bg-gray-50 transition-colors" data-id="${l.id}">
      <td class="px-4 py-2">${scopeBadge(l)}</td>
      <td class="px-4 py-2">${fmtCost(l.limit_amount)}</td>
      <td class="px-4 py-2">${progressBar(l)}</td>
      <td class="px-4 py-2 whitespace-nowrap">${esc(fmtRefresh(l))}</td>
      <td class="px-4 py-2 whitespace-nowrap">${esc(fmtActivePeriod(l))}</td>
      <td class="px-4 py-2">${statusToggle(l)}</td>
      <td class="px-4 py-2 text-gray-500 whitespace-nowrap">${fmtTime(l.updated_at)}</td>
      <td class="px-4 py-2 text-right whitespace-nowrap">
        <button type="button" command="show-modal" commandfor="limiter-dialog" class="edit-btn text-sm text-gray-400 hover:text-gray-700 mr-3">Edit</button>
        <button type="button" class="delete-btn text-sm text-gray-400 hover:text-red-600">Delete</button>
      </td>
    </tr>`;
  }

  async function loadLimiters() {
    const res = await fetch('/api/limiters');
    const limiters = res.ok ? await res.json() : [];

    limitersById.clear();
    if (!limiters.length) {
      tbody.innerHTML = '<tr><td colspan="8" class="px-4 py-8 text-center text-gray-400">No limiters configured.</td></tr>';
      return;
    }

    limiters.forEach((l) => limitersById.set(String(l.id), l));
    tbody.innerHTML = limiters.map(buildRow).join('');
  }

  function createLimiter(payload) {
    return fetch('/api/limiters', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  function putLimiter(id, payload) {
    return fetch(`/api/limiters/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  async function toggleActive(l) {
    await fetch(`/api/limiters/${l.id}/active`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ is_active: !l.is_active }),
    });
    loadLimiters();
  }

  async function deleteLimiter(l) {
    const scope = l.session_id ? `session ${l.session_id}` : 'global';
    if (!confirm(`Delete the ${scope} limiter (${fmtCost(l.limit_amount)})?`)) return;
    await fetch(`/api/limiters/${l.id}`, { method: 'DELETE' });
    loadLimiters();
  }

  tbody.addEventListener('click', (e) => {
    const row = e.target.closest('tr[data-id]');
    if (!row) return;
    const l = limitersById.get(row.dataset.id);
    if (!l) return;

    if (e.target.closest('.toggle-active-btn')) {
      toggleActive(l);
    } else if (e.target.closest('.edit-btn')) {
      startEdit(l);
    } else if (e.target.closest('.delete-btn')) {
      deleteLimiter(l);
    }
  });

  // ── Aligned-refresh availability ────────────────────────────────────
  function updateAlignedAvailability() {
    const value = parseInt(refreshValueInput.value, 10);
    const unit = refreshUnitSelect.value;
    const supported = (ALIGNED_COMBOS[unit] || []).includes(value);
    alignedCheckbox.disabled = !supported;
    if (!supported) alignedCheckbox.checked = false;
  }
  refreshValueInput.addEventListener('input', updateAlignedAvailability);
  refreshUnitSelect.addEventListener('change', updateAlignedAvailability);

  // ── Always-active toggle ────────────────────────────────────────────
  function updateActivePeriodInputs() {
    const disabled = alwaysActiveCheckbox.checked;
    startHourSelect.disabled = disabled;
    endHourSelect.disabled = disabled;
  }
  alwaysActiveCheckbox.addEventListener('change', updateActivePeriodInputs);

  // ── Dialog message ──────────────────────────────────────────────────
  const DIALOG_MESSAGE_STYLES = { error: ['text-red-700', 'bg-red-50', 'border-red-200'] };

  function setDialogMessage(type, text) {
    const el = document.getElementById('limiter-dialog-message');
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

  // ── Add / Edit dialog wiring ────────────────────────────────────────
  function startEdit(l) {
    editingId = l.id;
    dialogTitle.textContent = 'Edit limiter';
    submitBtn.textContent = 'Save changes';

    sessionIDInput.value = l.session_id;
    limitAmountInput.value = l.limit_amount;
    refreshValueInput.value = l.refresh_value;
    refreshUnitSelect.value = l.refresh_unit;
    updateAlignedAvailability();
    alignedCheckbox.checked = l.refresh_aligned && !alignedCheckbox.disabled;

    alwaysActiveCheckbox.checked = l.active_start_hour == null;
    startHourSelect.value = String(l.active_start_hour ?? 0);
    endHourSelect.value = String(l.active_end_hour ?? 23);
    updateActivePeriodInputs();
  }

  function resetDialog() {
    editingId = null;
    dialogTitle.textContent = 'Add a limiter';
    submitBtn.textContent = 'Add';
    setDialogMessage(null, '');
    form.reset();
    updateAlignedAvailability();
    updateActivePeriodInputs();
  }
  dialog.addEventListener('close', resetDialog);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = new FormData(form);
    const alwaysActive = alwaysActiveCheckbox.checked;

    const payload = {
      session_id: String(data.get('session_id') || '').trim(),
      limit_amount: parseFloat(data.get('limit_amount')),
      refresh_value: parseInt(data.get('refresh_value'), 10),
      refresh_unit: String(data.get('refresh_unit')),
      refresh_aligned: alignedCheckbox.checked && !alignedCheckbox.disabled,
      active_start_hour: alwaysActive ? null : parseInt(data.get('active_start_hour'), 10),
      active_end_hour: alwaysActive ? null : parseInt(data.get('active_end_hour'), 10),
    };

    const res = editingId ? await putLimiter(editingId, payload) : await createLimiter(payload);
    if (!res.ok) {
      setDialogMessage('error', await extractErrorMessage(res, editingId ? 'Failed to update limiter.' : 'Failed to add limiter.'));
      return;
    }
    dialog.close();
    loadLimiters();
  });

  updateAlignedAvailability();
  updateActivePeriodInputs();
  loadLimiters();
  initNavPolling();
})();
