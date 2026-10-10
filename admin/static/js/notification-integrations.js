import { esc, extractErrorMessage, initNavPolling, makeDialogMessage, postJSON, putJSON, withPending } from './app.js';

'use strict';

(() => {
  const tbody = document.getElementById('notifications-tbody');
  const dialog = document.getElementById('notification-dialog');
  const form = document.getElementById('notification-form');
  const dialogTitle = document.getElementById('notification-dialog-title');
  const submitBtn = document.getElementById('notification-dialog-submit');
  const nameInput = document.getElementById('notification-name');
  const typeSelect = document.getElementById('notification-type');
  const webhookInput = document.getElementById('notification-webhook-url');
  const webhookHint = document.getElementById('notification-webhook-hint');
  const integrationsById = new Map();
  let editingId = null;

  const setDialogMessage = makeDialogMessage('notification-dialog-message', {
    error: ['text-red-700 dark:text-red-400', 'bg-red-50 dark:bg-red-500/15', 'border-red-200 dark:border-red-800'],
  });

  /**
   * Renders the on/off switch for an integration.
   * @param {{id: number, active: boolean}} n - Notification integration.
   * @returns {string} Button markup.
   */
  function statusToggle(n) {
    return `<button type="button" class="toggle-active-btn inline-flex items-center relative h-5 w-9 rounded-full transition-colors ${n.active ? 'bg-emerald-600' : 'bg-surface-strong'}"
      aria-pressed="${n.active}" aria-label="Toggle integration active">
      <span class="inline-block h-3.5 w-3.5 transform rounded-full bg-surface transition-transform ${n.active ? 'translate-x-5' : 'translate-x-0.5'}"></span>
    </button>`;
  }

  /**
   * Renders one integration as a table row.
   * @param {object} n - Notification integration from the API.
   * @returns {string} Row markup.
   */
  function buildRow(n) {
    return `<tr class="hover:bg-surface-hover transition-colors" data-id="${n.id}">
      <td class="font-medium px-4 py-2">${esc(n.name)}</td>
      <td class="px-4 py-2"><span class="text-blue-700 dark:text-blue-400 font-medium px-1.5 py-0.5 bg-blue-50 dark:bg-blue-500/15 rounded">${esc(n.type)}</span></td>
      <td class="text-fg-muted font-mono text-xs px-4 py-2">${esc(n.webhook_hint)}</td>
      <td class="px-4 py-2">${statusToggle(n)}</td>
      <td class="text-right whitespace-nowrap px-4 py-2">
        <button type="button" command="show-modal" commandfor="notification-dialog" class="edit-btn text-sm text-fg-subtle mr-3 hover:text-fg">Edit</button>
        <button type="button" class="delete-btn text-sm text-fg-subtle hover:text-red-600 dark:hover:text-red-400">Delete</button>
      </td>
    </tr>`;
  }

  async function loadIntegrations() {
    const res = await fetch('/api/notification-integrations');
    const integrations = res.ok ? await res.json() : [];
    integrationsById.clear();
    if (!integrations.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="text-center text-fg-subtle px-4 py-8">No notification integrations configured.</td></tr>';
      return;
    }
    integrations.forEach((n) => integrationsById.set(String(n.id), n));
    tbody.innerHTML = integrations.map(buildRow).join('');
  }

  async function toggleActive(n) {
    await fetch(`/api/notification-integrations/${n.id}/${n.active ? 'deactivate' : 'activate'}`, { method: 'POST' });
    loadIntegrations();
  }

  async function deleteIntegration(n) {
    if (!confirm(`Delete "${n.name}"? It will be unlinked from every limiter.`)) return;
    await fetch(`/api/notification-integrations/${n.id}`, { method: 'DELETE' });
    loadIntegrations();
  }

  tbody.addEventListener('click', (e) => {
    const row = e.target.closest('tr[data-id]');
    const n = row && integrationsById.get(row.dataset.id);
    if (!n) return;
    if (e.target.closest('.toggle-active-btn')) toggleActive(n);
    else if (e.target.closest('.edit-btn')) startEdit(n);
    else if (e.target.closest('.delete-btn')) deleteIntegration(n);
  });

  function startEdit(n) {
    editingId = n.id;
    dialogTitle.textContent = 'Edit integration';
    submitBtn.textContent = 'Save changes';
    nameInput.value = n.name;
    typeSelect.value = n.type;
    webhookInput.required = false;
    webhookInput.placeholder = n.webhook_hint;
    webhookHint.textContent = 'Leave blank to keep the stored webhook URL.';
  }

  function resetDialog() {
    editingId = null;
    dialogTitle.textContent = 'Add an integration';
    submitBtn.textContent = 'Add';
    webhookInput.required = true;
    webhookInput.placeholder = 'https://hooks.slack.com/services/…';
    webhookHint.textContent = 'Slack incoming webhook that receives the alerts.';
    setDialogMessage(null, '');
    form.reset();
  }
  dialog.addEventListener('close', resetDialog);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const payload = {
      name: nameInput.value.trim(),
      type: typeSelect.value,
      webhook_url: webhookInput.value.trim(),
    };
    await withPending(submitBtn, async () => {
      const res = editingId
        ? await putJSON(`/api/notification-integrations/${editingId}`, payload)
        : await postJSON('/api/notification-integrations', payload);
      if (!res.ok) {
        setDialogMessage('error', await extractErrorMessage(res, 'Failed to save integration.'));
        return;
      }
      dialog.close();
      loadIntegrations();
    });
  });

  loadIntegrations();
  initNavPolling();
})();
