'use strict';

import { badge, errorRow, esc, extractErrorMessage, fmtActivePeriod, fmtCost, fmtCountdown, fmtSessionId, getJSON, initNav, makeDialogMessage, notifyError, pad, postJSON, progressBar, putJSON, runAction, switchToggle, timeAgoHtml, withPending } from './app.js';

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

  /**
   * Fills a select with one option per hour of the day.
   * @param {HTMLSelectElement} select - Select to populate.
   */
  function populateHourSelect(select) {
    select.innerHTML = Array.from({ length: 24 }, (_, h) => `<option value="${h}">${pad(h)}:00</option>`).join('');
  }
  populateHourSelect(startHourSelect);
  populateHourSelect(endHourSelect);
  startHourSelect.value = '0';
  endHourSelect.value = '23';

  /**
   * Singularizes a refresh unit for a value of 1.
   * @param {number} value - Refresh interval length.
   * @param {string} unit - Plural unit name.
   * @returns {string} Unit label.
   */
  function unitLabel(value, unit) {
    return value === 1 ? unit.slice(0, -1) : unit;
  }

  /**
   * Describes a limiter's refresh interval, e.g. "1 hour (aligned)".
   * @param {object} l - Limiter from the API.
   * @returns {string} Interval text.
   */
  function fmtRefresh(l) {
    const base = `${l.refresh_value} ${unitLabel(l.refresh_value, l.refresh_unit)}`;
    return l.refresh_aligned ? `${base} (aligned)` : base;
  }

  /**
   * Renders a limiter's scope label followed by its alert indicator.
   * @param {object} l - Limiter from the API.
   * @returns {string} Badge markup.
   */
  function scopeBadge(l) {
    const scope = l.session_id ? badge('emerald', esc(fmtSessionId(l.session_id))) : badge('blue', 'Global');
    return `${scope}${alertBadge(l)}`;
  }

  /**
   * Renders the bell shown next to the scope label when a limiter has
   * notification links, listing each link's trigger in a tooltip.
   * @param {object} l - Limiter from the API.
   * @returns {string} Bell markup, or '' when the limiter has no links.
   */
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

  /**
   * Looks up a notification integration's display name.
   * @param {number} id - Notification integration id.
   * @returns {string} Its name, or "#id" when unknown.
   */
  function integrationName(id) {
    return notificationIntegrations.find((n) => n.id === id)?.name ?? `#${id}`;
  }

  // ── Notification link rows ──────────────────────────────────────────
  const NOTIFICATION_LABEL = 'block text-xs text-fg-muted uppercase tracking-wide';
  const NOTIFICATION_FIELD = 'w-full text-sm normal-case tracking-normal mt-1 px-2 py-1.5 border border-line-strong rounded focus:outline-none focus:ring-1 focus:ring-emerald-500';

  /**
   * Renders one notification link as a card with labelled fields.
   * @param {{notification_id?: number, threshold_pct?: ?number, request_cost_usd?: ?number}} [link] - Link to prefill.
   * @returns {string} Row markup.
   */
  function notificationRow(link = {}) {
    const options = notificationIntegrations.map((n) => {
      const label = n.active ? n.name : `${n.name} (inactive)`;
      return `<option value="${n.id}" ${n.id === link.notification_id ? 'selected' : ''}>${esc(label)}</option>`;
    }).join('');
    return `<div class="notification-row flex flex-col gap-3 p-3 bg-surface-hover border border-line rounded-md">
      <div class="flex items-end gap-2">
        <label class="${NOTIFICATION_LABEL} flex-1 min-w-0">Integration
          <select class="notification-id ${NOTIFICATION_FIELD}">${options}</select>
        </label>
        <button type="button" class="remove-notification-btn shrink-0 text-sm text-fg-subtle px-2 py-1.5 hover:text-red-600 dark:hover:text-red-400" aria-label="Remove notification">Remove</button>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <label class="${NOTIFICATION_LABEL}">At % of budget
          <input type="number" step="1" min="1" max="100" placeholder="80" value="${link.threshold_pct ?? ''}" class="notification-threshold ${NOTIFICATION_FIELD}">
        </label>
        <label class="${NOTIFICATION_LABEL}">On request over ($)
          <input type="number" step="0.01" min="0.01" placeholder="5.00" value="${link.request_cost_usd ?? ''}" class="notification-cost ${NOTIFICATION_FIELD}">
        </label>
      </div>
    </div>`;
  }

  /** Hides, in each row's select, the integrations already chosen in other rows, and disables "Add" once every integration is linked. */
  function syncNotificationOptions() {
    const selects = [...notificationsEl.querySelectorAll('.notification-id')];
    const chosen = selects.map((select) => select.value);
    selects.forEach((select) => {
      const takenElsewhere = new Set(chosen.filter((_, i) => selects[i] !== select));
      select.querySelectorAll('option').forEach((option) => {
        option.hidden = option.disabled = takenElsewhere.has(option.value);
      });
    });
    notificationsEmptyEl.classList.toggle('hidden', selects.length > 0);
    notificationsEmptyEl.innerHTML = notificationIntegrations.length
      ? 'No alerts — this limiter won\'t notify anyone.'
      : 'No notification integrations yet. <a href="/integrations/notifications" class="underline hover:text-fg">Create one</a> first.';
    addNotificationBtn.disabled = selects.length >= notificationIntegrations.length;
  }

  /**
   * Replaces the dialog's notification link rows.
   * @param {object[]} links - Links to render.
   */
  function renderNotificationRows(links) {
    notificationsEl.innerHTML = links.map(notificationRow).join('');
    syncNotificationOptions();
  }

  /**
   * Reads the dialog's notification link rows.
   * @returns {{notification_id: number, threshold_pct: ?number, request_cost_usd: ?number}[]} Links as sent to the API.
   */
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

  /** Reloads the notification integrations and re-renders the dialog's link rows, keeping what is already entered. */
  async function refreshNotificationIntegrations() {
    try {
      notificationIntegrations = await getJSON('/api/notification-integrations');
    } catch (err) {
      notifyError(err.message);
    }
    renderNotificationRows(readNotificationRows());
  }

  addNotificationBtn.addEventListener('click', () => {
    const linked = new Set(readNotificationRows().map((n) => n.notification_id));
    const available = notificationIntegrations.filter((n) => !linked.has(n.id));
    const next = available.find((n) => n.active) ?? available[0];
    notificationsEl.insertAdjacentHTML('beforeend', notificationRow({ notification_id: next?.id }));
    syncNotificationOptions();
  });
  notificationsEl.addEventListener('click', (e) => {
    e.target.closest('.remove-notification-btn')?.closest('.notification-row').remove();
    syncNotificationOptions();
  });
  notificationsEl.addEventListener('change', (e) => {
    if (e.target.matches('.notification-id')) syncNotificationOptions();
  });

  /**
   * Renders one limiter as a table row.
   * @param {object} l - Limiter from the API.
   * @returns {string} Row markup.
   */
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
      <td class="px-4 py-2">${switchToggle(l.is_active, 'Toggle limiter active')}</td>
      <td class="text-fg-muted whitespace-nowrap px-4 py-2">${timeAgoHtml(l.updated_at)}</td>
      <td class="text-right whitespace-nowrap px-4 py-2">
        <button type="button" command="show-modal" commandfor="limiter-dialog" class="edit-btn text-sm text-fg-subtle mr-3 hover:text-fg">Edit</button>
        <button type="button" class="delete-btn text-sm text-fg-subtle hover:text-red-600 dark:hover:text-red-400">Delete</button>
      </td>
    </tr>`;
  }

  /** Fetches the limiters and renders them, or an error row when the fetch fails. */
  async function loadLimiters() {
    let limiters;
    try {
      limiters = await getJSON('/api/limiters');
    } catch (err) {
      tbody.innerHTML = errorRow(8, err.message);
      return;
    }

    limitersById.clear();
    if (!limiters.length) {
      tbody.innerHTML = '<tr><td colspan="8" class="text-center text-fg-subtle px-4 py-8">No limiters configured.</td></tr>';
      return;
    }

    limiters.forEach((l) => limitersById.set(String(l.id), l));
    tbody.innerHTML = limiters.map(buildRow).join('');
  }

  /** Recomputes each row's refresh countdown from the already-fetched limiters. */
  function tickCountdowns() {
    tbody.querySelectorAll('[data-countdown]').forEach((el) => {
      const l = limitersById.get(el.dataset.countdown);
      if (l) el.textContent = fmtCountdown(l.next_refresh_at);
    });
  }

  /**
   * Flips a limiter's master switch and reloads the table.
   * @param {object} l - Limiter from the API.
   */
  async function toggleActive(l) {
    const request = fetch(`/api/limiters/${l.id}/active`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ is_active: !l.is_active }),
    });
    if (await runAction(request, 'Failed to change limiter status.')) loadLimiters();
  }

  /**
   * Deletes a limiter after confirmation and reloads the table.
   * @param {object} l - Limiter from the API.
   */
  async function deleteLimiter(l) {
    const scope = l.session_id ? `session ${l.session_id}` : 'global';
    if (!confirm(`Delete the ${scope} limiter (${fmtCost(l.limit_amount)})?`)) return;
    if (await runAction(fetch(`/api/limiters/${l.id}`, { method: 'DELETE' }), 'Failed to delete limiter.')) loadLimiters();
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
  /** Enables the aligned-refresh checkbox only for interval combinations with a calendar boundary. */
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
  /** Disables the hour selects while the limiter is always active. */
  function updateActivePeriodInputs() {
    const disabled = alwaysActiveCheckbox.checked;
    startHourSelect.disabled = disabled;
    endHourSelect.disabled = disabled;
  }
  alwaysActiveCheckbox.addEventListener('change', updateActivePeriodInputs);

  // ── Dialog message ──────────────────────────────────────────────────
  const setDialogMessage = makeDialogMessage('limiter-dialog-message');

  // ── Add / Edit dialog wiring ────────────────────────────────────────
  /**
   * Fills the dialog with a limiter for editing.
   * @param {object} l - Limiter from the API.
   */
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
    refreshNotificationIntegrations();
  }

  /** Returns the dialog to its empty "add" state. */
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
  document.getElementById('add-limiter-btn').addEventListener('click', refreshNotificationIntegrations);
  refreshNotificationIntegrations().then(loadLimiters);
  initNav(null, { onLimitersChanged: loadLimiters });
  setInterval(tickCountdowns, 30000);
})();
