package parsing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens              *int `json:"input_tokens"`
			CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Usage struct {
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

// parseSSEResponse parses a streaming (text/event-stream) response body,
// assembling text_delta chunks and pulling token usage out of the
// message_start (input + cache tokens) / message_delta (output tokens)
// events, matching Anthropic's SSE event shapes for the Messages API.
func parseSSEResponse(raw []byte) (outputText *string, usage Usage) {
	var text strings.Builder

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			usage.InputTokens = event.Message.Usage.InputTokens
			usage.CacheCreationTokens = event.Message.Usage.CacheCreationInputTokens
			usage.CacheReadTokens = event.Message.Usage.CacheReadInputTokens
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta":
			usage.OutputTokens = event.Usage.OutputTokens
		}
	}

	if text.Len() > 0 {
		s := text.String()
		outputText = &s
	}
	return outputText, usage
}
