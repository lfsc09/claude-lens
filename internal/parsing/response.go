package parsing

import "encoding/json"

type responseBody struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

// ExtractResponseFields extracts the assembled output text and token counts
// from an upstream Anthropic API response. Streaming and non-streaming
// responses have different shapes (SSE event stream vs. a single JSON
// object), so isStreaming picks which one to parse. Any decode failure
// yields all-nil results rather than an error, matching the old Python
// version's behavior of never letting a malformed upstream body break
// storage of the exchange.
func ExtractResponseFields(rawResponse []byte, isStreaming bool) (outputText *string, inputTokens, outputTokens *int) {
	if isStreaming {
		return parseSSEResponse(rawResponse)
	}

	var body responseBody
	if err := json.Unmarshal(rawResponse, &body); err != nil {
		return nil, nil, nil
	}

	for _, block := range body.Content {
		if block.Type == "text" {
			t := block.Text
			outputText = &t
			break
		}
	}
	return outputText, body.Usage.InputTokens, body.Usage.OutputTokens
}
