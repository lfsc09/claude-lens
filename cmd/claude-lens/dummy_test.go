package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
)

func TestNewDummyExchange(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	now := time.Now()
	earliest := float64(now.UnixNano())/1e9 - dummyHistoryDays*24*3600
	seen := map[string]bool{}

	for range 500 {
		e, err := newDummyExchange(rng, now, 3)
		if err != nil {
			t.Fatal(err)
		}
		seen[e.SessionID] = true

		if e.Timestamp < earliest || e.Timestamp > float64(now.UnixNano())/1e9 {
			t.Fatalf("timestamp %v outside the last %d days", e.Timestamp, dummyHistoryDays)
		}
		var price database.Price
		if err := json.Unmarshal([]byte(e.MatchedPrice), &price); err != nil || price.Prefix != e.Model {
			t.Fatalf("matched price %q is not a snapshot of model %q: %v", e.MatchedPrice, e.Model, err)
		}
	}

	if len(seen) != 3 {
		t.Errorf("distinct sessions = %d (%v), want 3", len(seen), seen)
	}
	for i := 1; i <= 3; i++ {
		if !seen[fmt.Sprintf("dummy-session-%d", i)] {
			t.Errorf("session dummy-session-%d never generated", i)
		}
	}
}

func TestTokenCost(t *testing.T) {
	if got := tokenCost(2_000_000, 3); math.Abs(got-6) > 1e-9 {
		t.Errorf("tokenCost = %v, want 6", got)
	}
}
