package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// proxyIntegrationID looks up the seeded proxy integration of the given type.
func proxyIntegrationID(t *testing.T, s *Server, typ string) int64 {
	t.Helper()
	rec := doGet(t, s, "/api/proxy-integrations")
	var list []proxyIntegrationView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, p := range list {
		if p.Type == typ {
			return p.ID
		}
	}
	t.Fatalf("no %s integration in %s", typ, rec.Body.String())
	return 0
}

// activateProxy configures and activates the seeded integration of type typ
// through the API, returning its id.
func activateProxy(t *testing.T, s *Server, typ, baseURL, token string) int64 {
	t.Helper()
	id := proxyIntegrationID(t, s, typ)
	rec := doJSON(t, s, http.MethodPut, fmt.Sprintf("/api/proxy-integrations/%d", id), map[string]any{
		"base_url": baseURL, "auth_token": token, "price_sync_interval_minutes": 60,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, fmt.Sprintf("/api/proxy-integrations/%d/activate", id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate status = %d: %s", rec.Code, rec.Body.String())
	}
	return id
}

func TestProxyIntegrations_SeededInactiveAndTokenMasked(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doGet(t, s, "/api/proxy-integrations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw) != 2 {
		t.Fatalf("got %s, want the two seeded integrations", rec.Body.String())
	}
	for _, p := range raw {
		if p["active"] != false || p["has_auth_token"] != false {
			t.Errorf("%v: want inactive without a token", p)
		}
	}

	activateProxy(t, s, "litellm", "https://gw.example.test/", "sk-secret-1234")
	rec = doGet(t, s, "/api/proxy-integrations")
	if body := rec.Body.String(); strings.Contains(body, "sk-secret-1234") {
		t.Errorf("response leaks the auth token: %s", body)
	}
	var views []proxyIntegrationView
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	if v := views[0]; !v.Active || v.BaseURL != "https://gw.example.test" || v.AuthTokenHint != "…1234" || !v.HasAuthToken {
		t.Errorf("got %+v, want active, normalized base URL, hint …1234", v)
	}
}

func TestUpdateProxyIntegration_KeepsTokenWhenEmptyAndValidates(t *testing.T) {
	s, db := newTestServer(t)
	id := activateProxy(t, s, "bifrost", "https://bf.example.test", "tok-abcd")
	path := fmt.Sprintf("/api/proxy-integrations/%d", id)

	rec := doJSON(t, s, http.MethodPut, path, map[string]any{
		"base_url": "https://bf2.example.test", "auth_token": "", "price_sync_interval_minutes": 15,
		"custom_headers": map[string]string{"X-Team": "platform"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := db.GetProxyIntegration(context.Background(), id)
	if got.AuthToken != "tok-abcd" || got.BaseURL != "https://bf2.example.test" || got.PriceSyncIntervalMinutes != 15 || got.CustomHeaders["X-Team"] != "platform" {
		t.Errorf("got %+v", got)
	}

	for name, body := range map[string]map[string]any{
		"bad url":       {"base_url": "nope", "price_sync_interval_minutes": 1},
		"bad header":    {"base_url": "https://x.test", "price_sync_interval_minutes": 1, "custom_headers": map[string]string{"X Y": "v"}},
		"missing sync":  {"base_url": "https://x.test"},
		"negative sync": {"base_url": "https://x.test", "price_sync_interval_minutes": -1},
	} {
		if rec := doJSON(t, s, http.MethodPut, path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestUpdateProxyIntegration_RequiresATokenOnFirstSave(t *testing.T) {
	s, _ := newTestServer(t)
	id := proxyIntegrationID(t, s, "litellm")
	rec := doJSON(t, s, http.MethodPut, fmt.Sprintf("/api/proxy-integrations/%d", id), map[string]any{
		"base_url": "https://x.test", "price_sync_interval_minutes": 60,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without any token", rec.Code)
	}
}

func TestActivateProxyIntegration_IncompleteIsRejectedAndOnlyOneStaysActive(t *testing.T) {
	s, _ := newTestServer(t)
	litellmID := proxyIntegrationID(t, s, "litellm")
	if rec := doJSON(t, s, http.MethodPost, fmt.Sprintf("/api/proxy-integrations/%d/activate", litellmID), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("activating an unconfigured integration: status = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/api/proxy-integrations/9999/activate", nil); rec.Code != http.StatusNotFound {
		t.Errorf("activating a missing integration: status = %d, want 404", rec.Code)
	}

	activateProxy(t, s, "litellm", "https://a.test", "t1")
	bifrostID := activateProxy(t, s, "bifrost", "https://b.test", "t2")

	var views []proxyIntegrationView
	_ = json.Unmarshal(doGet(t, s, "/api/proxy-integrations").Body.Bytes(), &views)
	if views[0].Active || !views[1].Active || views[1].ID != bifrostID {
		t.Errorf("got %+v, want only bifrost active", views)
	}

	if rec := doJSON(t, s, http.MethodPost, fmt.Sprintf("/api/proxy-integrations/%d/deactivate", bifrostID), nil); rec.Code != http.StatusOK {
		t.Fatalf("deactivate status = %d", rec.Code)
	}
	_ = json.Unmarshal(doGet(t, s, "/api/proxy-integrations").Body.Bytes(), &views)
	if views[0].Active || views[1].Active {
		t.Errorf("got %+v, want none active", views)
	}
}

func TestSyncProxyPrices_RequiresTheActiveIntegration(t *testing.T) {
	s, _ := newTestServer(t)
	id := proxyIntegrationID(t, s, "litellm")
	rec := doJSON(t, s, http.MethodPost, fmt.Sprintf("/api/proxy-integrations/%d/sync-prices", id), nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for an inactive integration", rec.Code)
	}
}

func createNotification(t *testing.T, s *Server, name, webhook string) int64 {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/api/notification-integrations", map[string]any{
		"name": name, "type": "slack", "webhook_url": webhook,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var v notificationIntegrationView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v.ID
}

func TestNotificationIntegrations_CRUDMaskingAndToggle(t *testing.T) {
	s, _ := newTestServer(t)
	id := createNotification(t, s, "ops", "https://hooks.slack.test/services/T000/B000/abcd1234")

	rec := doGet(t, s, "/api/notification-integrations")
	if strings.Contains(rec.Body.String(), "T000/B000") {
		t.Errorf("response leaks the webhook URL: %s", rec.Body.String())
	}
	var views []notificationIntegrationView
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	if len(views) != 1 || !views[0].Active || views[0].WebhookHint != "hooks.slack.test/…1234" {
		t.Fatalf("got %+v", views)
	}

	path := fmt.Sprintf("/api/notification-integrations/%d", id)
	rec = doJSON(t, s, http.MethodPut, path, map[string]any{"name": "ops-renamed", "type": "slack"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &views[0])
	if views[0].Name != "ops-renamed" || views[0].WebhookHint != "hooks.slack.test/…1234" {
		t.Errorf("got %+v, want renamed with the webhook kept", views[0])
	}

	if rec := doJSON(t, s, http.MethodPost, path+"/deactivate", nil); rec.Code != http.StatusOK {
		t.Fatalf("deactivate status = %d", rec.Code)
	}
	_ = json.Unmarshal(doGet(t, s, "/api/notification-integrations").Body.Bytes(), &views)
	if views[0].Active {
		t.Error("still active after deactivate")
	}

	if rec := doJSON(t, s, http.MethodDelete, path, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	_ = json.Unmarshal(doGet(t, s, "/api/notification-integrations").Body.Bytes(), &views)
	if len(views) != 0 {
		t.Errorf("got %+v after delete, want none", views)
	}
}

func TestCreateNotificationIntegration_Validation(t *testing.T) {
	s, _ := newTestServer(t)
	createNotification(t, s, "ops", "https://hooks.example.test/a")

	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"missing name":    {map[string]any{"type": "slack", "webhook_url": "https://x.test/1"}, http.StatusBadRequest},
		"bad type":        {map[string]any{"name": "n", "type": "email", "webhook_url": "https://x.test/1"}, http.StatusBadRequest},
		"missing webhook": {map[string]any{"name": "n", "type": "slack"}, http.StatusBadRequest},
		"http webhook":    {map[string]any{"name": "n", "type": "slack", "webhook_url": "http://x.test/1"}, http.StatusBadRequest},
		"duplicate url":   {map[string]any{"name": "n", "type": "slack", "webhook_url": "https://hooks.example.test/a"}, http.StatusConflict},
	} {
		if rec := doJSON(t, s, http.MethodPost, "/api/notification-integrations", tc.body); rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func limiterBody(notifications ...map[string]any) map[string]any {
	return map[string]any{
		"session_id": "s1", "limit_amount": 10, "refresh_value": 60, "refresh_unit": "minutes",
		"notifications": notifications,
	}
}

func TestLimiterNotifications_CreateUpdateAndValidate(t *testing.T) {
	s, _ := newTestServer(t)
	a := createNotification(t, s, "a", "https://hooks.example.test/a")
	b := createNotification(t, s, "b", "https://hooks.example.test/b")

	rec := doJSON(t, s, http.MethodPost, "/api/limiters", limiterBody(
		map[string]any{"notification_id": a, "threshold_pct": 80},
		map[string]any{"notification_id": b, "request_cost_usd": 1.5},
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID            int64 `json:"id"`
		Notifications []struct {
			NotificationID int64    `json:"notification_id"`
			ThresholdPct   *int     `json:"threshold_pct"`
			RequestCostUSD *float64 `json:"request_cost_usd"`
		} `json:"notifications"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if len(created.Notifications) != 2 || *created.Notifications[0].ThresholdPct != 80 || *created.Notifications[1].RequestCostUSD != 1.5 {
		t.Fatalf("got %s", rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, fmt.Sprintf("/api/limiters/%d", created.ID), limiterBody(
		map[string]any{"notification_id": b, "threshold_pct": 50},
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", rec.Code, rec.Body.String())
	}
	var list []struct {
		Notifications []struct {
			NotificationID int64 `json:"notification_id"`
		} `json:"notifications"`
	}
	_ = json.Unmarshal(doGet(t, s, "/api/limiters").Body.Bytes(), &list)
	if len(list) != 1 || len(list[0].Notifications) != 1 || list[0].Notifications[0].NotificationID != b {
		t.Errorf("got %+v, want links fully replaced by the update", list)
	}

	for name, notifications := range map[string][]map[string]any{
		"no rule":             {{"notification_id": a}},
		"bad threshold":       {{"notification_id": a, "threshold_pct": 101}},
		"bad request cost":    {{"notification_id": a, "request_cost_usd": 0}},
		"duplicate link":      {{"notification_id": a, "threshold_pct": 5}, {"notification_id": a, "threshold_pct": 6}},
		"unknown integration": {{"notification_id": 9999, "threshold_pct": 5}},
	} {
		body := limiterBody(notifications...)
		body["session_id"] = "other"
		if rec := doJSON(t, s, http.MethodPost, "/api/limiters", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}
