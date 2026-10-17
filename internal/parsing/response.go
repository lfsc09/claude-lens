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

// usageFields is the wire shape of a usage object, shared by the
// non-streaming body and every SSE event that carries usage.
type usageFields struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

type responseBody struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage usageFields `json:"usage"`
}

// Empty reports whether no token counter carries information, i.e. every
// counter is nil or zero.
func (u Usage) Empty() bool {
	for _, count := range [...]*int{u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens} {
		if count != nil && *count != 0 {
			return false
		}
	}
	return true
}

// merge folds a usage object into u counter by counter. A non-zero value
// replaces the stored one; a zero is stored only while the counter is unset.
func (u *Usage) merge(f usageFields) {
	mergeCount(&u.InputTokens, f.InputTokens)
	mergeCount(&u.OutputTokens, f.OutputTokens)
	mergeCount(&u.CacheCreationTokens, f.CacheCreationInputTokens)
	mergeCount(&u.CacheReadTokens, f.CacheReadInputTokens)
}

func mergeCount(dst **int, src *int) {
	if src != nil && (*src != 0 || *dst == nil) {
		*dst = src
	}
}

// reliable returns u, or an all-nil Usage when every counter is zero or nil.
func (u Usage) reliable() Usage {
	if u.Empty() {
		return Usage{}
	}
	return u
}

// ExtractResponseFields extracts the assembled output text and token usage
// from an upstream Anthropic API response. Streaming and non-streaming
// responses have different shapes (SSE event stream vs. a single JSON
// object), so isStreaming picks which one to parse. Any decode failure, or a
// response whose counters are all zero, yields an all-nil Usage rather than
// an error, so a malformed upstream body never blocks storage of the
// exchange.
func ExtractResponseFields(rawResponse []byte, isStreaming bool) (outputText *string, usage Usage) {
	return parseResponse(rawResponse, isStreaming, true)
}

// ExtractUsage extracts only the token usage from an upstream Anthropic API
// response, skipping output text assembly. Failure behavior matches
// ExtractResponseFields.
func ExtractUsage(rawResponse []byte, isStreaming bool) Usage {
	_, usage := parseResponse(rawResponse, isStreaming, false)
	return usage
}

// parseResponse decodes a streaming or non-streaming response, assembling
// the output text only when wantText is set.
func parseResponse(rawResponse []byte, isStreaming, wantText bool) (outputText *string, usage Usage) {
	if isStreaming {
		return parseSSEResponse(rawResponse, wantText)
	}

	var body responseBody
	if err := json.Unmarshal(rawResponse, &body); err != nil {
		return nil, Usage{}
	}

	for _, block := range body.Content {
		if wantText && block.Type == "text" {
			t := block.Text
			outputText = &t
			break
		}
	}
	usage.merge(body.Usage)
	return outputText, usage.reliable()
}
