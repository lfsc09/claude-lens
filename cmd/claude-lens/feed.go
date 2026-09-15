package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
)

// feedTables maps a --table value to the admin API endpoint that creates and
// lists rows there. Add an entry here to make --feed support another table.
var feedTables = map[string]string{
	"limiters":     "/api/limiters",
	"model_prices": "/api/prices",
}

// runFeed inserts or updates a single row in table via the admin API, so
// that a running claude-lens instance can be seeded from the command line
// without going through its Admin UI. With match empty, row is posted as-is
// to create a new row. With match set to a JSON object of column:value
// pairs, runFeed first looks up rows in table whose fields equal every pair
// in match: no match still creates row, exactly one match updates it with
// row, and more than one match is an error. All field validation for row
// lives in the admin handler that receives it; runFeed only checks that
// table is known and row/match are syntactically valid JSON before making
// requests.
func runFeed(ctx context.Context, cfg config.Config, table, row, match string) error {
	endpoint, ok := feedTables[table]
	if !ok {
		names := make([]string, 0, len(feedTables))
		for name := range feedTables {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown table %q, must be one of: %s", table, strings.Join(names, ", "))
	}
	if !json.Valid([]byte(row)) {
		return fmt.Errorf("--row is not valid JSON")
	}

	base := "http://" + dialAddr(cfg.AdminAddr) + endpoint
	client := &http.Client{Timeout: 5 * time.Second}

	if match != "" {
		var matchFields map[string]any
		if err := json.Unmarshal([]byte(match), &matchFields); err != nil {
			return fmt.Errorf("--match is not a JSON object: %w", err)
		}
		if len(matchFields) == 0 {
			return fmt.Errorf("--match must specify at least one column:value pair")
		}

		id, found, err := findMatch(ctx, client, base, matchFields)
		if err != nil {
			return err
		}
		if found {
			body, err := sendRequest(ctx, client, http.MethodPut, fmt.Sprintf("%s/%d", base, id), []byte(row))
			if err != nil {
				return err
			}
			fmt.Println(string(body))
			return nil
		}
	}

	body, err := sendRequest(ctx, client, http.MethodPost, base, []byte(row))
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

// findMatch fetches the rows listed at base and returns the id of the
// single row whose fields equal every column:value pair in match. It
// reports found as false when no row matches, and errors when more than
// one row matches so the caller never guesses which row to update.
func findMatch(ctx context.Context, client *http.Client, base string, match map[string]any) (id int64, found bool, err error) {
	body, err := sendRequest(ctx, client, http.MethodGet, base, nil)
	if err != nil {
		return 0, false, fmt.Errorf("look up rows for --match: %w", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		return 0, false, fmt.Errorf("decode rows for --match: %w", err)
	}

	matches := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if rowMatches(r, match) {
			matches = append(matches, r)
		}
	}

	switch len(matches) {
	case 0:
		return 0, false, nil
	case 1:
		rowID, ok := matches[0]["id"].(float64)
		if !ok {
			return 0, false, fmt.Errorf("matched row has no numeric id field")
		}
		return int64(rowID), true, nil
	default:
		return 0, false, fmt.Errorf("--match matched %d rows, refine --match to a single row", len(matches))
	}
}

// rowMatches reports whether row contains every column:value pair in match.
func rowMatches(row, match map[string]any) bool {
	for col, val := range match {
		if !reflect.DeepEqual(row[col], val) {
			return false
		}
	}
	return true
}

// sendRequest sends an HTTP request with method to url, with body as the
// JSON payload when non-nil, and returns the response body on a 2xx status
// or an error describing the failure otherwise.
func sendRequest(ctx context.Context, client *http.Client, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", method, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach claude-lens admin API at %s (is the service running?): %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read admin API response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, nil
	}
	return nil, fmt.Errorf("admin API rejected the request (status %d): %s", resp.StatusCode, string(respBody))
}

// dialAddr turns a Config.AdminAddr bind address (e.g. ":7802" or
// "0.0.0.0:7802") into an address a client on the same host can dial.
func dialAddr(bindAddr string) string {
	switch {
	case strings.HasPrefix(bindAddr, ":"):
		return "localhost" + bindAddr
	case strings.HasPrefix(bindAddr, "0.0.0.0:"):
		return "localhost" + strings.TrimPrefix(bindAddr, "0.0.0.0")
	default:
		return bindAddr
	}
}
