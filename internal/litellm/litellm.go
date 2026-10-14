// Package litellm fetches per-model pricing from a LiteLLM proxy's
// /model/info endpoint.
package litellm

import (
	"context"
	"net/http"

	"github.com/lfsc09/claude-lens/internal/priceprovider"
)

// Client fetches model pricing from a LiteLLM proxy with a bounded timeout.
type Client struct {
	http *http.Client
}

// NewClient builds a Client with a bounded request timeout.
func NewClient() *Client {
	return &Client{http: priceprovider.NewHTTPClient()}
}

type modelInfoResponse struct {
	Data []struct {
		ModelName string `json:"model_name"`
		ModelInfo struct {
			InputCostPerToken                    float64  `json:"input_cost_per_token"`
			OutputCostPerToken                   float64  `json:"output_cost_per_token"`
			CacheCreationInputTokenCost          float64  `json:"cache_creation_input_token_cost"`
			CacheReadInputTokenCost              float64  `json:"cache_read_input_token_cost"`
			InputCostPerTokenAbove200k           *float64 `json:"input_cost_per_token_above_200k_tokens"`
			OutputCostPerTokenAbove200k          *float64 `json:"output_cost_per_token_above_200k_tokens"`
			CacheCreationInputTokenCostAbove200k *float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
			CacheReadInputTokenCostAbove200k     *float64 `json:"cache_read_input_token_cost_above_200k_tokens"`
		} `json:"model_info"`
	} `json:"data"`
}

// FetchModelPrices queries baseURL's /model/info endpoint and returns
// per-million-token rates for every model it reports with an input and
// output price. A non-200 answer is returned as a *priceprovider.StatusError
// noting the upstream may not be a LiteLLM proxy.
func (c *Client) FetchModelPrices(ctx context.Context, baseURL, authToken string) ([]priceprovider.ModelPrice, error) {
	var parsed modelInfoResponse
	if err := priceprovider.GetJSON(ctx, c.http, baseURL+"/model/info", authToken, &parsed); err != nil {
		return nil, priceprovider.WrapStatusError(err, "a LiteLLM proxy")
	}

	prices := make([]priceprovider.ModelPrice, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ModelName == "" {
			continue
		}
		price := priceprovider.ModelPrice{
			ModelName:               m.ModelName,
			InputPerM:               priceprovider.PerM(m.ModelInfo.InputCostPerToken),
			OutputPerM:              priceprovider.PerM(m.ModelInfo.OutputCostPerToken),
			CacheWritePerM:          priceprovider.PerM(m.ModelInfo.CacheCreationInputTokenCost),
			CacheReadPerM:           priceprovider.PerM(m.ModelInfo.CacheReadInputTokenCost),
			InputPerMAbove200k:      priceprovider.PerMPtr(m.ModelInfo.InputCostPerTokenAbove200k),
			OutputPerMAbove200k:     priceprovider.PerMPtr(m.ModelInfo.OutputCostPerTokenAbove200k),
			CacheWritePerMAbove200k: priceprovider.PerMPtr(m.ModelInfo.CacheCreationInputTokenCostAbove200k),
			CacheReadPerMAbove200k:  priceprovider.PerMPtr(m.ModelInfo.CacheReadInputTokenCostAbove200k),
		}
		if price.Priced() {
			prices = append(prices, price)
		}
	}
	return prices, nil
}
