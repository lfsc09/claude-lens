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
			InputTokens *int `json:"input_tokens"`
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
// assembling text_delta chunks and pulling token counts out of the
// message_start / message_delta events, matching Anthropic's SSE event
// shapes for the Messages API.
func parseSSEResponse(raw []byte) (outputText *string, inputTokens, outputTokens *int) {
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
			inputTokens = event.Message.Usage.InputTokens
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta":
			outputTokens = event.Usage.OutputTokens
		}
	}

	if text.Len() > 0 {
		s := text.String()
		outputText = &s
	}
	return outputText, inputTokens, outputTokens
}
