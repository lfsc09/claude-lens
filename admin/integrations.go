package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricesync"
	"github.com/lfsc09/claude-lens/internal/upstream"
)

const secretHintLen = 4

// secretHint returns the last few characters of secret, enough for an admin
// to recognize which credential is stored without exposing it.
func secretHint(secret string) string {
	if len(secret) <= secretHintLen {
		return "…"
	}
	return "…" + secret[len(secret)-secretHintLen:]
}

// proxyIntegrationView is a ProxyIntegration with its auth token masked.
type proxyIntegrationView struct {
	database.ProxyIntegration
	HasAuthToken  bool   `json:"has_auth_token"`
	AuthTokenHint string `json:"auth_token_hint"`
}

func newProxyIntegrationView(p database.ProxyIntegration) proxyIntegrationView {
	v := proxyIntegrationView{ProxyIntegration: p, HasAuthToken: p.AuthToken != ""}
	if v.HasAuthToken {
		v.AuthTokenHint = secretHint(p.AuthToken)
	}
	return v
}

// notificationIntegrationView is a NotificationIntegration with its webhook
// URL masked down to its host and last characters.
type notificationIntegrationView struct {
	database.NotificationIntegration
	WebhookHint string `json:"webhook_hint"`
}

func newNotificationIntegrationView(n database.NotificationIntegration) notificationIntegrationView {
	hint := secretHint(n.WebhookURL)
	if u, err := url.Parse(n.WebhookURL); err == nil {
		hint = u.Host + "/" + hint
	}
	return notificationIntegrationView{NotificationIntegration: n, WebhookHint: hint}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

func (h *handlers) listProxyIntegrations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.ListProxyIntegrations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]proxyIntegrationView, 0, len(rows))
	for _, p := range rows {
		views = append(views, newProxyIntegrationView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

type updateProxyIntegrationRequest struct {
	BaseURL                  string            `json:"base_url"`
	AuthToken                string            `json:"auth_token"`
	CustomHeaders            map[string]string `json:"custom_headers"`
	PriceSyncIntervalMinutes *int              `json:"price_sync_interval_minutes"`
}

// updateProxyIntegration replaces a proxy integration's base URL, custom
// headers and sync interval. An empty auth_token keeps the stored one, so
// the masked token never has to round-trip through the client.
func (h *handlers) updateProxyIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateProxyIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.db.GetProxyIntegration(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	baseURL, err := upstream.NormalizeBaseURL(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(req.AuthToken)
	if token == "" {
		token = existing.AuthToken
	}
	if token == "" {
		writeError(w, http.StatusBadRequest, "auth_token is required")
		return
	}
	if err := upstream.ValidateHeaders(req.CustomHeaders); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PriceSyncIntervalMinutes == nil || *req.PriceSyncIntervalMinutes < 0 {
		writeError(w, http.StatusBadRequest, "price_sync_interval_minutes is required and must be >= 0")
		return
	}

	existing.BaseURL = baseURL
	existing.AuthToken = token
	existing.CustomHeaders = req.CustomHeaders
	if existing.CustomHeaders == nil {
		existing.CustomHeaders = map[string]string{}
	}
	existing.PriceSyncIntervalMinutes = *req.PriceSyncIntervalMinutes
	existing.UpdatedAt = float64(time.Now().Unix())

	if err := h.db.UpdateProxyIntegration(r.Context(), *existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.respondProxyIntegration(w, r, id)
}

// activateProxyIntegration makes id the single active proxy integration,
// deactivating any other.
func (h *handlers) activateProxyIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := h.db.ActivateProxyIntegration(r.Context(), id, float64(time.Now().Unix()))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, database.ErrProxyIntegrationIncomplete):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.respondProxyIntegration(w, r, id)
}

// deactivateProxyIntegration turns id off; with none active, requests go
// directly to the Anthropic API.
func (h *handlers) deactivateProxyIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.db.DeactivateProxyIntegration(r.Context(), id, float64(time.Now().Unix())); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.respondProxyIntegration(w, r, id)
}

// respondProxyIntegration applies the stored integration state to the live
// proxy and writes the integration's masked view.
func (h *handlers) respondProxyIntegration(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.registry.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, err := h.db.GetProxyIntegration(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, newProxyIntegrationView(*p))
}

// syncProxyPrices triggers an on-demand pricesync.Sync against the active
// integration — the same sync also run periodically in the background — and
// reports what it did so the admin UI can show a meaningful summary.
//
// 409 means id isn't the active integration. 502 specifically means the
// gateway can't serve prices (see pricesync.ErrUpstreamUnavailable), which
// the Prices page's JS uses to grey out the manual sync button — anything
// else (e.g. a DB write error) isn't a capability signal, so it stays a
// plain 500 and the button stays usable for an immediate retry.
func (h *handlers) syncProxyPrices(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	integration, err := h.db.GetProxyIntegration(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if integration == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !integration.Active {
		writeError(w, http.StatusConflict, "only the active proxy integration can sync prices")
		return
	}

	result, err := pricesync.Sync(r.Context(), h.db, h.est, h.fetchers, *integration)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, pricesync.ErrUpstreamUnavailable) {
			status = http.StatusBadGateway
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handlers) listNotificationIntegrations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.ListNotificationIntegrations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]notificationIntegrationView, 0, len(rows))
	for _, n := range rows {
		views = append(views, newNotificationIntegrationView(n))
	}
	writeJSON(w, http.StatusOK, views)
}

type notificationIntegrationRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	WebhookURL string `json:"webhook_url"`
}

// validateNotificationIntegrationRequest checks the fields shared by create
// and update (webhook_url is checked separately, since update may omit it),
// returning a client-facing message or "".
func validateNotificationIntegrationRequest(req notificationIntegrationRequest) string {
	if strings.TrimSpace(req.Name) == "" {
		return "name is required"
	}
	if req.Type != database.NotificationTypeSlack {
		return "type must be slack"
	}
	if req.WebhookURL != "" && !strings.HasPrefix(req.WebhookURL, "https://") {
		return "webhook_url must be a https:// URL"
	}
	return ""
}

func (h *handlers) createNotificationIntegration(w http.ResponseWriter, r *http.Request) {
	var req notificationIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if msg := validateNotificationIntegrationRequest(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if req.WebhookURL == "" {
		writeError(w, http.StatusBadRequest, "webhook_url is required")
		return
	}

	now := float64(time.Now().Unix())
	n := database.NotificationIntegration{
		Type: req.Type, Name: strings.TrimSpace(req.Name), WebhookURL: req.WebhookURL,
		Active: true, CreatedAt: now, UpdatedAt: now,
	}
	id, err := h.db.CreateNotificationIntegration(r.Context(), n)
	if err != nil {
		writeNotificationWriteError(w, err)
		return
	}
	n.ID = id
	writeJSON(w, http.StatusOK, newNotificationIntegrationView(n))
}

// updateNotificationIntegration replaces an integration's name, type and
// webhook URL. An empty webhook_url keeps the stored one.
func (h *handlers) updateNotificationIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req notificationIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if msg := validateNotificationIntegrationRequest(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	existing, err := h.db.GetNotificationIntegration(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	existing.Type = req.Type
	existing.Name = strings.TrimSpace(req.Name)
	if req.WebhookURL != "" {
		existing.WebhookURL = req.WebhookURL
	}
	existing.UpdatedAt = float64(time.Now().Unix())
	if err := h.db.UpdateNotificationIntegration(r.Context(), *existing); err != nil {
		writeNotificationWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newNotificationIntegrationView(*existing))
}

func writeNotificationWriteError(w http.ResponseWriter, err error) {
	if isUniqueConstraintErr(err) {
		writeError(w, http.StatusConflict, "a notification integration with this webhook URL already exists")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// setNotificationIntegrationActive returns a handler that turns an
// integration on or off without touching its limiter links.
func (h *handlers) setNotificationIntegrationActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := h.db.SetNotificationIntegrationActive(r.Context(), id, active, float64(time.Now().Unix())); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, err := h.db.GetNotificationIntegration(r.Context(), id)
		if err != nil || n == nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, newNotificationIntegrationView(*n))
	}
}

func (h *handlers) deleteNotificationIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.db.DeleteNotificationIntegration(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.limitersFresh.Bump()
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": id})
}
