'use strict';

import { badge, errorRow, esc, getJSON, initEntityDialog, initNavPolling, runAction, switchToggle } from './app.js';

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
  const webhookCurrent = document.getElementById('notification-webhook-current');
  const WEBHOOK_BY_TYPE = {
    slack: {
      placeholder: 'https://hooks.slack.com/services/…',
      hint: 'Create one in Slack: <a href="https://api.slack.com/messaging/webhooks" target="_blank" rel="noopener noreferrer" class="underline hover:text-fg">Incoming Webhooks</a>. Alerts post to the channel it was created for.',
    },
  };
  const integrationsById = new Map();

  /**
   * Renders one integration as a table row.
   * @param {object} n - Notification integration from the API.
   * @returns {string} Row markup.
   */
  function buildRow(n) {
    return `<tr class="hover:bg-surface-hover transition-colors" data-id="${n.id}">
      <td class="font-medium px-4 py-2">${esc(n.name)}</td>
      <td class="px-4 py-2">${badge('blue', esc(n.type))}</td>
      <td class="text-fg-muted font-mono text-xs px-4 py-2">${esc(n.webhook_hint)}</td>
      <td class="px-4 py-2">${switchToggle(n.active, 'Toggle integration active')}</td>
      <td class="text-right whitespace-nowrap px-4 py-2">
        <button type="button" command="show-modal" commandfor="notification-dialog" class="edit-btn text-sm text-fg-subtle mr-3 hover:text-fg">Edit</button>
        <button type="button" class="delete-btn text-sm text-fg-subtle hover:text-red-600 dark:hover:text-red-400">Delete</button>
      </td>
    </tr>`;
  }

  /** Fetches the integrations and renders them, or an error row when the fetch fails. */
  async function loadIntegrations() {
    let integrations;
    try {
      integrations = await getJSON('/api/notification-integrations');
    } catch (err) {
      tbody.innerHTML = errorRow(5, err.message);
      return;
    }
    integrationsById.clear();
    if (!integrations.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="text-center text-fg-subtle px-4 py-8">No notification integrations configured.</td></tr>';
      return;
    }
    integrations.forEach((n) => integrationsById.set(String(n.id), n));
    tbody.innerHTML = integrations.map(buildRow).join('');
  }

  /**
   * Flips an integration's active state and reloads the table.
   * @param {object} n - Notification integration from the API.
   */
  async function toggleActive(n) {
    const request = fetch(`/api/notification-integrations/${n.id}/${n.active ? 'deactivate' : 'activate'}`, { method: 'POST' });
    if (await runAction(request, 'Failed to change integration status.')) loadIntegrations();
  }

  /**
   * Deletes an integration after confirmation and reloads the table.
   * @param {object} n - Notification integration from the API.
   */
  async function deleteIntegration(n) {
    if (!confirm(`Delete "${n.name}"? It will be unlinked from every limiter.`)) return;
    if (await runAction(fetch(`/api/notification-integrations/${n.id}`, { method: 'DELETE' }), 'Failed to delete integration.')) loadIntegrations();
  }

  tbody.addEventListener('click', (e) => {
    const row = e.target.closest('tr[data-id]');
    const n = row && integrationsById.get(row.dataset.id);
    if (!n) return;
    if (e.target.closest('.toggle-active-btn')) toggleActive(n);
    else if (e.target.closest('.edit-btn')) startEdit(n);
    else if (e.target.closest('.delete-btn')) deleteIntegration(n);
  });

  const notificationDialog = initEntityDialog({
    dialog,
    form,
    titleEl: dialogTitle,
    submitBtn,
    messageId: 'notification-dialog-message',
    endpoint: '/api/notification-integrations',
    titles: { add: 'Add an integration', edit: 'Edit integration' },
    failures: { add: 'Failed to save integration.', edit: 'Failed to save integration.' },
    readPayload: () => ({
      name: nameInput.value.trim(),
      type: typeSelect.value,
      webhook_url: webhookInput.value.trim(),
    }),
    onReset: () => {
      webhookInput.required = true;
      webhookCurrent.classList.add('hidden');
      renderWebhookHelp();
    },
    onSaved: loadIntegrations,
  });

  /** Shows the webhook placeholder and hint that belong to the selected integration type. */
  function renderWebhookHelp() {
    const help = WEBHOOK_BY_TYPE[typeSelect.value];
    const editing = notificationDialog.editingId != null;
    webhookInput.placeholder = editing ? 'Paste a new URL to replace it' : (help?.placeholder ?? '');
    if (editing) webhookHint.textContent = 'Leave blank to keep the current URL.';
    else webhookHint.innerHTML = help?.hint ?? '';
  }

  /**
   * Fills the dialog with an integration for editing.
   * @param {object} n - Notification integration from the API.
   */
  function startEdit(n) {
    notificationDialog.beginEdit(n.id);
    nameInput.value = n.name;
    typeSelect.value = n.type;
    webhookInput.required = false;
    webhookCurrent.textContent = `Current: ${n.webhook_hint}`;
    webhookCurrent.classList.remove('hidden');
    renderWebhookHelp();
  }

  typeSelect.addEventListener('change', renderWebhookHelp);
  renderWebhookHelp();

  loadIntegrations();
  initNavPolling();
})();
