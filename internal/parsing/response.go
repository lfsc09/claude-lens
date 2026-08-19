package parsing

import "encoding/json"

// Usage is the token accounting reported by Anthropic's Messages API for a
// single response: plain input/output tokens plus the two prompt-caching
// counters (cache writes and cache reads), which are priced separately from
// plain input tokens.
type Usage struct {
	InputTokens         *int
	OutputTokens        *int
	CacheCreationTokens *int
	CacheReadTokens     *int
}

type responseBody struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens              *int `json:"input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// ExtractResponseFields extracts the assembled output text and token usage
// from an upstream Anthropic API response. Streaming and non-streaming
// responses have different shapes (SSE event stream vs. a single JSON
// object), so isStreaming picks which one to parse. Any decode failure
// yields an all-nil Usage rather than an error, so a malformed upstream
// body never blocks storage of the exchange.
func ExtractResponseFields(rawResponse []byte, isStreaming bool) (outputText *string, usage Usage) {
	if isStreaming {
		return parseSSEResponse(rawResponse)
	}

	var body responseBody
	if err := json.Unmarshal(rawResponse, &body); err != nil {
		return nil, Usage{}
	}

	for _, block := range body.Content {
		if block.Type == "text" {
			t := block.Text
			outputText = &t
			break
		}
	}
	return outputText, Usage{
		InputTokens:         body.Usage.InputTokens,
		OutputTokens:        body.Usage.OutputTokens,
		CacheCreationTokens: body.Usage.CacheCreationInputTokens,
		CacheReadTokens:     body.Usage.CacheReadInputTokens,
	}
}
