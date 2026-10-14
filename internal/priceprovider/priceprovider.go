// Package priceprovider defines the shared contract that every upstream
// price-sync client (internal/litellm, internal/bifrost) implements, so
// internal/pricesync can sync from whichever one is configured without
// depending on either concretely.
package priceprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	requestTimeout   = 10 * time.Second
	maxResponseBytes = 8 << 20
)

// ModelPrice is one model's USD-per-million-token rates, converted from the
// per-token rates an upstream reports. The four *Above200k fields are nil
// unless the upstream reports that tier for this model — see
// database.Price for how a nil override behaves.
type ModelPrice struct {
	ModelName               string
	InputPerM               float64
	OutputPerM              float64
	CacheWritePerM          float64
	CacheReadPerM           float64
	InputPerMAbove200k      *float64
	OutputPerMAbove200k     *float64
	CacheWritePerMAbove200k *float64
	CacheReadPerMAbove200k  *float64
}

// Priced reports whether the upstream reported both an input and an output
// rate, so a model it knows nothing about never overwrites a stored price.
func (p ModelPrice) Priced() bool {
	return p.InputPerM > 0 && p.OutputPerM > 0
}

// Fetcher fetches per-model pricing from an upstream gateway's own API.
type Fetcher interface {
	FetchModelPrices(ctx context.Context, baseURL, authToken string) ([]ModelPrice, error)
}

// Round6 rounds to 6 decimal places, clearing the float64 noise a per-token
// rate picks up when scaled by 1,000,000 (e.g. 0.0000033*1e6 rendering as
// 3.3000000000000003 instead of 3.3).
func Round6(f float64) float64 {
	const mult = 1e6
	return math.Round(f*mult) / mult
}

// PerM converts a per-token rate to a per-million-token rate.
func PerM(perToken float64) float64 {
	return Round6(perToken * 1_000_000)
}

// PerMPtr converts a per-token rate to a per-million-token rate, preserving
// nil (no override reported) instead of defaulting it to zero.
func PerMPtr(perToken *float64) *float64 {
	if perToken == nil {
		return nil
	}
	perM := PerM(*perToken)
	return &perM
}

// NewHTTPClient builds the HTTP client price fetchers use, with a bounded
// request timeout.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: requestTimeout}
}

// StatusError is returned by GetJSON when the upstream answers with a
// non-200 status.
type StatusError struct {
	URL  string
	Code int
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned status %d", e.URL, e.Code)
}

// WrapStatusError annotates a *StatusError with the kind of gateway that was
// expected to serve the route (e.g. "a LiteLLM proxy"); any other error is
// returned unchanged.
func WrapStatusError(err error, gateway string) error {
	if statusErr := (*StatusError)(nil); errors.As(err, &statusErr) {
		return fmt.Errorf("%w — is this upstream %s?", err, gateway)
	}
	return err
}

// GetJSON GETs url with an optional Bearer token and decodes the JSON body
// into v. It fails with a *StatusError on a non-200 status and refuses
// bodies larger than maxResponseBytes.
func GetJSON(ctx context.Context, client *http.Client, url, authToken string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &StatusError{URL: url, Code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("%s response exceeds %d bytes", url, maxResponseBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}
