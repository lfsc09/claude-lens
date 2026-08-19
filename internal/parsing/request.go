// Package parsing extracts the fields claude-lens stores from Anthropic API
// request/response bodies. It is pure data transformation — no I/O.
package parsing

import "encoding/json"

type requestBody struct {
	Messages json.RawMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Model    string          `json:"model"`
}

// ExtractRequestFields extracts the messages array (as its original raw JSON
// text), the streaming flag, and the model name from a POST request body.
// A body that isn't a JSON object (or isn't valid JSON at all) yields the
// zero values instead of an error, so a malformed body never blocks the
// request from being proxied.
func ExtractRequestFields(rawBody []byte) (inputMessages *string, isStreaming bool, model *string) {
	var body requestBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return nil, false, nil
	}

	if len(body.Messages) > 0 && string(body.Messages) != "null" {
		s := string(body.Messages)
		inputMessages = &s
	}
	if body.Model != "" {
		m := body.Model
		model = &m
	}
	return inputMessages, body.Stream, model
}
