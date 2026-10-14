package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func configureProxyIntegration(t *testing.T, db *DB, typ string) ProxyIntegration {
	t.Helper()
	ctx := context.Background()
	list, err := db.ListProxyIntegrations(ctx)
	if err != nil {
		t.Fatalf("ListProxyIntegrations: %v", err)
	}
	for _, p := range list {
		if p.Type == typ {
			p.BaseURL = "https://" + typ + ".example.test"
			p.AuthToken = "token-" + typ
			p.CustomHeaders = map[string]string{"X-Team": "platform"}
			if err := db.UpdateProxyIntegration(ctx, p); err != nil {
				t.Fatalf("UpdateProxyIntegration: %v", err)
			}
			return p
		}
	}
	t.Fatalf("no seeded %s integration", typ)
	return ProxyIntegration{}
}

func TestSeedProxyIntegrations_CreatesInactiveUnconfiguredRows(t *testing.T) {
	db := openTestDB(t)

	list, err := db.ListProxyIntegrations(context.Background())
	if err != nil {
		t.Fatalf("ListProxyIntegrations: %v", err)
	}
	if len(list) != 2 || list[0].Type != ProxyTypeLiteLLM || list[1].Type != ProxyTypeBifrost {
		t.Fatalf("got %+v, want seeded litellm and bifrost rows", list)
	}
	for _, p := range list {
		if p.Active || p.BaseURL != "" || p.AuthToken != "" || len(p.CustomHeaders) != 0 {
			t.Errorf("%s seeded as %+v, want inactive and empty", p.Type, p)
		}
		if p.PriceSyncIntervalMinutes != defaultPriceSyncIntervalMinutes {
			t.Errorf("%s interval = %d, want %d", p.Type, p.PriceSyncIntervalMinutes, defaultPriceSyncIntervalMinutes)
		}
	}
}

func TestActivateProxyIntegration_RequiresBaseURLAndToken(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	list, _ := db.ListProxyIntegrations(ctx)
	if err := db.ActivateProxyIntegration(ctx, list[0].ID, 1); !errors.Is(err, ErrProxyIntegrationIncomplete) {
		t.Errorf("err = %v, want ErrProxyIntegrationIncomplete", err)
	}
	if err := db.ActivateProxyIntegration(ctx, 9999, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestActivateProxyIntegration_KeepsASingleActiveRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	litellm := configureProxyIntegration(t, db, ProxyTypeLiteLLM)
	bifrost := configureProxyIntegration(t, db, ProxyTypeBifrost)

	if err := db.ActivateProxyIntegration(ctx, litellm.ID, 1); err != nil {
		t.Fatalf("activate litellm: %v", err)
	}
	if err := db.ActivateProxyIntegration(ctx, bifrost.ID, 2); err != nil {
		t.Fatalf("activate bifrost: %v", err)
	}

	active, err := db.GetActiveProxyIntegration(ctx)
	if err != nil || active == nil || active.ID != bifrost.ID {
		t.Fatalf("active = %+v, err = %v, want bifrost", active, err)
	}
	other, _ := db.GetProxyIntegration(ctx, litellm.ID)
	if other.Active {
		t.Error("litellm still active after activating bifrost")
	}
	if active.CustomHeaders["X-Team"] != "platform" {
		t.Errorf("custom headers = %v, want round-tripped X-Team", active.CustomHeaders)
	}

	if err := db.DeactivateProxyIntegration(ctx, bifrost.ID, 3); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if active, _ := db.GetActiveProxyIntegration(ctx); active != nil {
		t.Errorf("active = %+v, want none", active)
	}
}

func TestActivateProxyIntegration_MakesPriceSyncDue(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := configureProxyIntegration(t, db, ProxyTypeLiteLLM)

	if err := db.MarkPriceSyncFailed(ctx, p.ID, "boom", 500); err != nil {
		t.Fatalf("MarkPriceSyncFailed: %v", err)
	}
	if err := db.ActivateProxyIntegration(ctx, p.ID, 600); err != nil {
		t.Fatalf("activate: %v", err)
	}

	got, _ := db.GetProxyIntegration(ctx, p.ID)
	if got.PriceSyncAttemptedAt != 0 {
		t.Errorf("PriceSyncAttemptedAt = %v, want 0 so RunLoop syncs right away", got.PriceSyncAttemptedAt)
	}
}

func TestMarkPriceSynced_ClearsPreviousFailure(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := configureProxyIntegration(t, db, ProxyTypeBifrost)

	_ = db.MarkPriceSyncFailed(ctx, p.ID, "boom", 100)
	got, _ := db.GetProxyIntegration(ctx, p.ID)
	if got.PriceSyncError != "boom" || got.PriceSyncedAt != 0 || got.PriceSyncAttemptedAt != 100 {
		t.Fatalf("after failure got %+v", got)
	}

	_ = db.MarkPriceSynced(ctx, p.ID, 200)
	got, _ = db.GetProxyIntegration(ctx, p.ID)
	if got.PriceSyncError != "" || got.PriceSyncedAt != 200 || got.PriceSyncAttemptedAt != 200 {
		t.Errorf("after success got %+v", got)
	}
}
