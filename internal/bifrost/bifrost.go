// Package bifrost fetches per-model pricing from a Bifrost gateway's
// /v1/models endpoint.
package bifrost

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lfsc09/claude-lens/internal/priceprovider"
)

// Client fetches model pricing from a Bifrost gateway with a bounded
// timeout.
type Client struct {
	http *http.Client
}

// NewClient builds a Client with a bounded request timeout.
func NewClient() *Client {
	return &Client{http: priceprovider.NewHTTPClient()}
}

type listModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Prompt          string `json:"prompt"`
			Completion      string `json:"completion"`
			InputCacheWrite string `json:"input_cache_write"`
			InputCacheRead  string `json:"input_cache_read"`
		} `json:"pricing"`
	} `json:"data"`
}

const (
	anthropicVendorMarker = "anthropic."
	bedrockVersionSuffix  = "-v1:0"
)

// claudeModelName converts a Bifrost model id to the plain Claude model name
// Claude Code sends, dropping the leading "<provider>/", any
// "<region>.anthropic." vendor prefix and Bedrock's "-v1:0" version suffix:
// "bedrock/us.anthropic.claude-haiku-4-5-20251001-v1:0" and
// "anthropic/claude-haiku-4-5-20251001" both yield
// "claude-haiku-4-5-20251001". It reports false for any non-Claude model.
func claudeModelName(id string) (string, bool) {
	name := id
	if _, rest, found := strings.Cut(id, "/"); found {
		name = rest
	}
	if i := strings.LastIndex(name, anthropicVendorMarker); i >= 0 {
		name = name[i+len(anthropicVendorMarker):]
	}
	name = strings.TrimSuffix(name, bedrockVersionSuffix)
	if !strings.HasPrefix(name, "claude") {
		return "", false
	}
	return name, true
}

// FetchModelPrices queries the host root of baseURL for /v1/models and
// returns per-million-token rates for every Claude model it reports; when
// several providers report the same model the first listed wins, and
// non-Claude models are skipped. Any path on baseURL is discarded. Pricing
// fields are decimal strings and no above-200k tier is reported, so the
// *Above200k fields of every returned price are nil. A non-200 answer is
// returned as a *priceprovider.StatusError noting the upstream may not be a
// Bifrost gateway.
func (c *Client) FetchModelPrices(ctx context.Context, baseURL, authToken string) ([]priceprovider.ModelPrice, error) {
	hostRoot, err := hostRootURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", baseURL, err)
	}

	var parsed listModelsResponse
	if err := priceprovider.GetJSON(ctx, c.http, hostRoot+"/v1/models?page_size=1000", authToken, &parsed); err != nil {
		return nil, priceprovider.WrapStatusError(err, "a Bifrost gateway")
	}

	prices := make([]priceprovider.ModelPrice, 0, len(parsed.Data))
	seen := make(map[string]struct{}, len(parsed.Data))
	for _, m := range parsed.Data {
		modelName, ok := claudeModelName(m.ID)
		if !ok {
			continue
		}
		if _, dup := seen[modelName]; dup {
			continue
		}
		seen[modelName] = struct{}{}

		price, err := modelPrice(modelName, m.Pricing.Prompt, m.Pricing.Completion, m.Pricing.InputCacheWrite, m.Pricing.InputCacheRead)
		if err != nil {
			slog.Warn("skip bifrost model with unusable pricing", "model", m.ID, "error", err)
			continue
		}
		if price.Priced() {
			prices = append(prices, price)
		}
	}
	return prices, nil
}

// modelPrice converts a model's decimal-string per-token rates to a
// ModelPrice in USD per million tokens.
func modelPrice(name string, prompt, completion, cacheWrite, cacheRead string) (priceprovider.ModelPrice, error) {
	rates := [4]float64{}
	for i, raw := range [...]string{prompt, completion, cacheWrite, cacheRead} {
		rate, err := parsePerTokenRate(raw)
		if err != nil {
			return priceprovider.ModelPrice{}, err
		}
		rates[i] = priceprovider.PerM(rate)
	}
	return priceprovider.ModelPrice{
		ModelName:      name,
		InputPerM:      rates[0],
		OutputPerM:     rates[1],
		CacheWritePerM: rates[2],
		CacheReadPerM:  rates[3],
	}, nil
}

// parsePerTokenRate parses a Bifrost pricing field's decimal-string USD
// per-token rate, treating "" (a dimension the model doesn't bill for,
// e.g. no cache support) as 0 rather than an error.
func parsePerTokenRate(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// hostRootURL reduces rawURL to its scheme and host, discarding any path,
// query and fragment.
func hostRootURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}
