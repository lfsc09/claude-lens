package database

import (
	"context"
	"testing"

	"github.com/lfsc09/claude-lens/internal/priceprovider"
)

func TestUpsertPricesFromSync_CreatesWhenAbsent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	created, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "brand-new-model", InputPerM: 2.20, OutputPerM: 11.00, CacheWritePerM: 2.75, CacheReadPerM: 0.22}}, 1)
	if err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}
	if created != 1 {
		t.Fatal("expected created=1 for a prefix with no existing row")
	}

	p, err := db.GetPriceByPrefix(ctx, "brand-new-model")
	if err != nil {
		t.Fatalf("GetPriceByPrefix: %v", err)
	}
	if p == nil {
		t.Fatal("expected a new row for brand-new-model")
	}
	if p.InputPerM != 2.20 || p.OutputPerM != 11.00 {
		t.Errorf("got (input=%v, output=%v), want (2.20, 11.00)", p.InputPerM, p.OutputPerM)
	}
}

func TestUpsertPricesFromSync_OverwritesBaseRatesOnExistingRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	id, err := db.CreatePrice(ctx, Price{Prefix: "sync-model", InputPerM: 3.00, OutputPerM: 15.00, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("CreatePrice: %v", err)
	}

	created, updated, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "sync-model", InputPerM: 2.20, OutputPerM: 11.00, CacheWritePerM: 2.75, CacheReadPerM: 0.22}}, 2)
	if err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}
	if created != 0 || updated != 1 {
		t.Fatalf("created, updated = %d, %d, want 0, 1 when a row already exists", created, updated)
	}

	p, err := db.GetPrice(ctx, id)
	if err != nil || p == nil {
		t.Fatalf("GetPrice: %v", err)
	}
	if p.InputPerM != 2.20 || p.OutputPerM != 11.00 {
		t.Errorf("got (input=%v, output=%v), want (2.20, 11.00)", p.InputPerM, p.OutputPerM)
	}
}

// TestUpsertPricesFromSync_PreservesManualAbove200kOverride verifies that a
// sync which doesn't report an above-200k tier for a model leaves a
// manually configured override in place, rather than clearing it.
func TestUpsertPricesFromSync_PreservesManualAbove200kOverride(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	manualOverride := 20.0
	id, err := db.CreatePrice(ctx, Price{
		Prefix: "manual-tier-model", InputPerM: 3.00, OutputPerM: 15.00,
		InputPerMAbove200k: &manualOverride, CreatedAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("CreatePrice: %v", err)
	}

	if _, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "manual-tier-model", InputPerM: 2.20, OutputPerM: 11.00, CacheWritePerM: 2.75, CacheReadPerM: 0.22}}, 2); err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}

	p, err := db.GetPrice(ctx, id)
	if err != nil || p == nil {
		t.Fatalf("GetPrice: %v", err)
	}
	if p.InputPerMAbove200k == nil || *p.InputPerMAbove200k != manualOverride {
		t.Errorf("InputPerMAbove200k = %v, want the preserved manual override %v", p.InputPerMAbove200k, manualOverride)
	}
}

// TestUpsertPricesFromSync_OverwritesAbove200kWhenSyncReportsIt verifies that
// when the sync does supply a non-nil above-200k rate, it overwrites
// whatever was there before.
func TestUpsertPricesFromSync_OverwritesAbove200kWhenSyncReportsIt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	oldOverride := 20.0
	id, err := db.CreatePrice(ctx, Price{
		Prefix: "reported-tier-model", InputPerM: 3.00, OutputPerM: 15.00,
		InputPerMAbove200k: &oldOverride, CreatedAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("CreatePrice: %v", err)
	}

	newOverride := 30.0
	if _, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "reported-tier-model", InputPerM: 2.20, OutputPerM: 11.00, CacheWritePerM: 2.75, CacheReadPerM: 0.22, InputPerMAbove200k: &newOverride}}, 2); err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}

	p, err := db.GetPrice(ctx, id)
	if err != nil || p == nil {
		t.Fatalf("GetPrice: %v", err)
	}
	if p.InputPerMAbove200k == nil || *p.InputPerMAbove200k != newOverride {
		t.Errorf("InputPerMAbove200k = %v, want the newly synced override %v", p.InputPerMAbove200k, newOverride)
	}
}

func TestUpsertPricesFromSync_CountsCreatedAndUpdatedAcrossBatch(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.CreatePrice(ctx, Price{Prefix: "known", InputPerM: 1, OutputPerM: 2, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("CreatePrice: %v", err)
	}
	created, updated, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{
		{ModelName: "known", InputPerM: 3, OutputPerM: 4},
		{ModelName: "fresh-a", InputPerM: 3, OutputPerM: 4},
		{ModelName: "fresh-b", InputPerM: 3, OutputPerM: 4},
	}, 5)
	if err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}
	if created != 2 || updated != 1 {
		t.Errorf("created, updated = %d, %d, want 2, 1", created, updated)
	}
}

func TestUpsertPricesFromSync_StampsSyncedAtEvenWhenRatesUnchanged(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	price := priceprovider.ModelPrice{ModelName: "claude-sonnet-5", InputPerM: 3, OutputPerM: 15}

	before, err := db.GetPriceByPrefix(ctx, "claude-sonnet-5")
	if err != nil || before == nil {
		t.Fatalf("GetPriceByPrefix: %v, %v", before, err)
	}
	if before.SyncedAt != nil {
		t.Fatalf("seeded row SyncedAt = %v, want nil", *before.SyncedAt)
	}

	for _, now := range []float64{100, 200} {
		if _, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{price}, now); err != nil {
			t.Fatalf("UpsertPricesFromSync: %v", err)
		}
		got, err := db.GetPriceByPrefix(ctx, "claude-sonnet-5")
		if err != nil || got == nil {
			t.Fatalf("GetPriceByPrefix: %v, %v", got, err)
		}
		if got.SyncedAt == nil || *got.SyncedAt != now {
			t.Errorf("SyncedAt = %v, want %v after a sync that changed no rate", got.SyncedAt, now)
		}
	}
}

func TestUpsertPricesFromSync_StampsSyncedAtOnCreatedRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "brand-new", InputPerM: 1, OutputPerM: 2}}, 50); err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}
	got, err := db.GetPriceByPrefix(ctx, "brand-new")
	if err != nil || got == nil {
		t.Fatalf("GetPriceByPrefix: %v, %v", got, err)
	}
	if got.SyncedAt == nil || *got.SyncedAt != 50 {
		t.Errorf("SyncedAt = %v, want 50", got.SyncedAt)
	}
}

func TestManualPriceWritesLeaveSyncedAtAlone(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, _, err := db.UpsertPricesFromSync(ctx, []priceprovider.ModelPrice{{ModelName: "claude-sonnet-5", InputPerM: 3, OutputPerM: 15}}, 100); err != nil {
		t.Fatalf("UpsertPricesFromSync: %v", err)
	}
	p, err := db.GetPriceByPrefix(ctx, "claude-sonnet-5")
	if err != nil || p == nil {
		t.Fatalf("GetPriceByPrefix: %v, %v", p, err)
	}
	p.InputPerM = 9
	p.UpdatedAt = 300
	if err := db.UpdatePrice(ctx, p.ID, *p); err != nil {
		t.Fatalf("UpdatePrice: %v", err)
	}
	got, err := db.GetPrice(ctx, p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrice: %v, %v", got, err)
	}
	if got.SyncedAt == nil || *got.SyncedAt != 100 {
		t.Errorf("SyncedAt = %v, want 100 untouched by a manual update", got.SyncedAt)
	}
}
