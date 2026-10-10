import { esc, extractErrorMessage, fmtCost, fmtCountdown, fmtTime, initNav, makeDialogMessage, pad, postJSON, progressBar, putJSON, fmtSessionId, fmtActivePeriod, withPending } from './app.js';

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
  const notificationsEl = document.getElementById('limiter-notifications');
  const notificationsEmptyEl = document.getElementById('limiter-notifications-empty');
  const addNotificationBtn = document.getElementById('add-limiter-notification-btn');
  let notificationIntegrations = [];

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

  function scopeBadge(l) {
    const badge = !l.session_id
      ? '<span class="text-blue-700 dark:text-blue-400 font-medium px-1.5 py-0.5 bg-blue-50 dark:bg-blue-500/15 rounded">Global</span>'
      : `<span class="text-emerald-700 dark:text-emerald-400 font-medium px-1.5 py-0.5 bg-emerald-50 dark:bg-emerald-500/15 rounded">${esc(fmtSessionId(l.session_id))}</span>`;
    return `${badge}${alertBadge(l)}`;
  }

  // Bell indicator shown next to the scope badge when the limiter has
  // notification links, with each link's trigger in a tooltip.
  function alertBadge(l) {
    if (!l.notifications?.length) return '';
    const lines = l.notifications.map((n) => {
      const triggers = [];
      if (n.threshold_pct) triggers.push(`at ${n.threshold_pct}% of budget${n.alert_sent ? ' (already sent this window)' : ''}`);
      if (n.request_cost_usd) triggers.push(`on a single request over ${fmtCost(n.request_cost_usd)}`);
      return `${integrationName(n.notification_id)}: ${triggers.join(', ')}`;
    });
    const tip = `Alerts:\n${lines.join('\n')}`;
    return ` <span data-tip="${esc(tip)}" class="text-amber-500 cursor-default" aria-label="${esc(tip)}">🔔</span>`;
  }

  function integrationName(id) {
    return notificationIntegrations.find((n) => n.id === id)?.name ?? `#${id}`;
  }

  // ── Notification link rows ──────────────────────────────────────────
  function notificationRow(link = {}) {
    const options = notificationIntegrations.map((n) => {
      const label = n.active ? n.name : `${n.name} (inactive)`;
      return `<option value="${n.id}" ${n.id === link.notification_id ? 'selected' : ''}>${esc(label)}</option>`;
    }).join('');
    const field = 'w-full text-sm px-2 py-1.5 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500';
    return `<div class="notification-row grid grid-cols-12 items-center gap-2">
      <select class="notification-id col-span-5 ${field}" aria-label="Notification integration">${options}</select>
      <input type="number" step="1" min="1" max="100" placeholder="% budget" value="${link.threshold_pct ?? ''}" class="notification-threshold col-span-3 ${field}" aria-label="Alert at percent of budget">
      <input type="number" step="0.01" min="0.01" placeholder="$ request" value="${link.request_cost_usd ?? ''}" class="notification-cost col-span-3 ${field}" aria-label="Alert on a single request over dollars">
      <button type="button" class="remove-notification-btn col-span-1 text-fg-subtle hover:text-red-600 dark:hover:text-red-400" aria-label="Remove notification">✕</button>
    </div>`;
  }

  function renderNotificationRows(links) {
    notificationsEl.innerHTML = links.map(notificationRow).join('');
    notificationsEmptyEl.classList.toggle('hidden', notificationIntegrations.length > 0);
    addNotificationBtn.disabled = notificationIntegrations.length === 0;
  }

  function readNotificationRows() {
    return [...notificationsEl.querySelectorAll('.notification-row')].map((row) => {
      const threshold = row.querySelector('.notification-threshold').value.trim();
      const cost = row.querySelector('.notification-cost').value.trim();
      return {
        notification_id: parseInt(row.querySelector('.notification-id').value, 10),
        threshold_pct: threshold ? parseInt(threshold, 10) : null,
        request_cost_usd: cost ? parseFloat(cost) : null,
      };
    });
  }

  async function loadNotificationIntegrations() {
    const res = await fetch('/api/notification-integrations');
    notificationIntegrations = res.ok ? await res.json() : [];
  }

  addNotificationBtn.addEventListener('click', () => {
    notificationsEl.insertAdjacentHTML('beforeend', notificationRow({ notification_id: notificationIntegrations.find((n) => n.active)?.id }));
  });
  notificationsEl.addEventListener('click', (e) => {
    e.target.closest('.remove-notification-btn')?.closest('.notification-row').remove();
  });

  function statusToggle(l) {
    return `<button type="button" class="toggle-active-btn inline-flex items-center relative h-5 w-9 rounded-full transition-colors ${l.is_active ? 'bg-emerald-600' : 'bg-surface-strong'}"
      data-id="${l.id}" data-active="${l.is_active}" aria-pressed="${l.is_active}" aria-label="Toggle limiter active">
      <span class="inline-block h-3.5 w-3.5 transform rounded-full bg-surface transition-transform ${l.is_active ? 'translate-x-5' : 'translate-x-0.5'}"></span>
    </button>`;
  }

  function buildRow(l) {
    return `<tr class="hover:bg-surface-hover transition-colors" data-id="${l.id}">
      <td class="px-4 py-2">${scopeBadge(l)}</td>
      <td class="px-4 py-2">${fmtCost(l.limit_amount)}</td>
      <td class="w-40 px-4 py-2">${progressBar(l)}</td>
      <td class="whitespace-nowrap px-4 py-2">
        <div>${esc(fmtRefresh(l))}</div>
        <div class="text-xs text-fg-subtle" data-countdown="${l.id}">${esc(fmtCountdown(l.within_active_period && l.is_active ? l.next_refresh_at : null))}</div>
      </td>
      <td class="whitespace-nowrap px-4 py-2">${fmtActivePeriod(l)}</td>
      <td class="px-4 py-2">${statusToggle(l)}</td>
      <td class="text-fg-muted whitespace-nowrap px-4 py-2">${fmtTime(l.updated_at)}</td>
      <td class="text-right whitespace-nowrap px-4 py-2">
        <button type="button" command="show-modal" commandfor="limiter-dialog" class="edit-btn text-sm text-fg-subtle mr-3 hover:text-fg">Edit</button>
        <button type="button" class="delete-btn text-sm text-fg-subtle hover:text-red-600 dark:hover:text-red-400">Delete</button>
      </td>
    </tr>`;
  }

  async function loadLimiters() {
    const res = await fetch('/api/limiters');
    const limiters = res.ok ? await res.json() : [];

    limitersById.clear();
    if (!limiters.length) {
      tbody.innerHTML = '<tr><td colspan="8" class="text-center text-fg-subtle px-4 py-8">No limiters configured.</td></tr>';
      return;
    }

    limiters.forEach((l) => limitersById.set(String(l.id), l));
    tbody.innerHTML = limiters.map(buildRow).join('');
  }

  // Recomputes just the countdown text every 30s from already-fetched data,
  // so it visibly ticks down between actual data changes without a refetch
  // or a full-table re-render.
  function tickCountdowns() {
    tbody.querySelectorAll('[data-countdown]').forEach((el) => {
      const l = limitersById.get(el.dataset.countdown);
      if (l) el.textContent = fmtCountdown(l.next_refresh_at);
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
  const setDialogMessage = makeDialogMessage('limiter-dialog-message', {
    error: ['text-red-700 dark:text-red-400', 'bg-red-50 dark:bg-red-500/15', 'border-red-200 dark:border-red-800'],
  });

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

    renderNotificationRows(l.notifications ?? []);
  }

  function resetDialog() {
    editingId = null;
    dialogTitle.textContent = 'Add a limiter';
    submitBtn.textContent = 'Add';
    setDialogMessage(null, '');
    form.reset();
    renderNotificationRows([]);
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
      notifications: readNotificationRows(),
    };

    await withPending(submitBtn, async () => {
      const res = editingId ? await putJSON(`/api/limiters/${editingId}`, payload) : await postJSON('/api/limiters', payload);
      if (!res.ok) {
        setDialogMessage('error', await extractErrorMessage(res, editingId ? 'Failed to update limiter.' : 'Failed to add limiter.'));
        return;
      }
      dialog.close();
      loadLimiters();
    });
  });

  updateAlignedAvailability();
  updateActivePeriodInputs();
  renderNotificationRows([]);
  loadNotificationIntegrations().then(loadLimiters);
  initNav(null, { onLimitersChanged: loadLimiters });
  setInterval(tickCountdowns, 30000);
})();
