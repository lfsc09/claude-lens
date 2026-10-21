// Package parsing extracts the fields claude-lens stores from Anthropic API
// request/response bodies. It is pure data transformation — no I/O.
package parsing

import (
	"encoding/json"

	"github.com/lfsc09/claude-lens/internal/modelname"
)

// requestBody is the part of an API request body claude-lens reads.
type requestBody struct {
	Stream bool   `json:"stream"`
	Model  string `json:"model"`
}

// ExtractRequestFields extracts the streaming flag and the model name from a
// POST request body; the model is returned normalized (see
// modelname.Normalize). A body that isn't a JSON object (or isn't valid JSON at
// all) yields the zero values instead of an error, so a malformed body never
// blocks the request from being proxied.
func ExtractRequestFields(rawBody []byte) (isStreaming bool, model *string) {
	var body requestBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return false, nil
	}

	if body.Model != "" {
		m := modelname.Normalize(body.Model)
		model = &m
	}
	return body.Stream, model
}
