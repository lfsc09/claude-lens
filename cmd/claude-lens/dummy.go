package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/database"
)

// dummyHistoryDays is how far back generated exchange timestamps reach.
const dummyHistoryDays = 30

// dummyPrices are the price rules dummy exchanges are costed with and whose
// snapshot is stored as their matched price.
var dummyPrices = []database.Price{
	{Prefix: "claude-opus-5-5", InputPerM: 15, OutputPerM: 75, CacheWritePerM: 18.75, CacheReadPerM: 1.5},
	{Prefix: "claude-sonnet-5-5", InputPerM: 3, OutputPerM: 15, CacheWritePerM: 3.75, CacheReadPerM: 0.3},
	{Prefix: "claude-haiku-4-5-20251001", InputPerM: 1, OutputPerM: 5, CacheWritePerM: 1.25, CacheReadPerM: 0.1},
}

// dummyExchange is the JSON body accepted by POST /api/exchanges.
type dummyExchange struct {
	SessionID           string  `json:"session_id"`
	Path                string  `json:"path"`
	Timestamp           float64 `json:"timestamp"`
	IsStreaming         bool    `json:"is_streaming"`
	InputMessages       string  `json:"input_messages"`
	RawRequest          string  `json:"raw_request"`
	RawResponse         string  `json:"raw_response"`
	OutputText          string  `json:"output_text"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	Model               string  `json:"model"`
	InputCost           float64 `json:"input_cost"`
	OutputCost          float64 `json:"output_cost"`
	CacheCreationCost   float64 `json:"cache_creation_cost"`
	CacheReadCost       float64 `json:"cache_read_cost"`
	MatchedPrice        string  `json:"matched_price"`
}

// newDummyExchange builds one random exchange belonging to a session among
// sessions distinct "dummy-session-N" ids, timestamped within the last
// dummyHistoryDays days before now.
func newDummyExchange(rng *rand.Rand, now time.Time, sessions int) (dummyExchange, error) {
	price := dummyPrices[rng.IntN(len(dummyPrices))]
	matchedPrice, err := json.Marshal(price)
	if err != nil {
		return dummyExchange{}, fmt.Errorf("encode matched price: %w", err)
	}
	e := dummyExchange{
		SessionID:           fmt.Sprintf("dummy-session-%d", 1+rng.IntN(sessions)),
		Path:                "/v1/messages",
		Timestamp:           float64(now.UnixNano())/1e9 - rng.Float64()*dummyHistoryDays*24*3600,
		IsStreaming:         rng.IntN(2) == 0,
		InputMessages:       `[{"role":"user","content":"Dummy prompt"}]`,
		RawRequest:          fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Dummy prompt"}]}`, price.Prefix),
		RawResponse:         `{"type":"message","role":"assistant","content":[{"type":"text","text":"Dummy response"}]}`,
		OutputText:          "Dummy response",
		InputTokens:         200 + rng.IntN(8000),
		OutputTokens:        50 + rng.IntN(2000),
		CacheCreationTokens: rng.IntN(4000),
		CacheReadTokens:     rng.IntN(20000),
		Model:               price.Prefix,
		MatchedPrice:        string(matchedPrice),
	}
	e.InputCost = tokenCost(e.InputTokens, price.InputPerM)
	e.OutputCost = tokenCost(e.OutputTokens, price.OutputPerM)
	e.CacheCreationCost = tokenCost(e.CacheCreationTokens, price.CacheWritePerM)
	e.CacheReadCost = tokenCost(e.CacheReadTokens, price.CacheReadPerM)
	return e, nil
}

// tokenCost returns the USD cost of tokens at perMillion USD per million.
func tokenCost(tokens int, perMillion float64) float64 {
	return float64(tokens) * perMillion / 1e6
}

// runDummy creates count random exchanges, spread over sessions sessions,
// through the running admin API. Each one is stored with its
// exchanges_ledger row, so dashboards, totals and limiters behave as with
// real proxy traffic.
func runDummy(ctx context.Context, cfg config.Config, count, sessions int) error {
	if count < 1 || sessions < 1 {
		return fmt.Errorf("--count and --sessions must be at least 1")
	}

	url := "http://" + dialAddr(cfg.AdminAddr) + "/api/exchanges"
	client := &http.Client{Timeout: 5 * time.Second}
	now := time.Now()
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))

	for range count {
		exchange, err := newDummyExchange(rng, now, sessions)
		if err != nil {
			return err
		}
		body, err := json.Marshal(exchange)
		if err != nil {
			return fmt.Errorf("encode dummy exchange: %w", err)
		}
		if _, err := sendRequest(ctx, client, http.MethodPost, url, body); err != nil {
			return err
		}
	}

	fmt.Printf("created %d exchanges across %d sessions\n", count, sessions)
	return nil
}
