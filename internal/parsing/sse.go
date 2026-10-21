package parsing

import (
	"bytes"
	"encoding/json"
	"strings"
)

// sseEvent is the part of a streaming API event claude-lens reads.
type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage usageFields `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Usage usageFields `json:"usage"`
}

// parseSSEResponse parses a streaming (text/event-stream) response body,
// assembling text_delta chunks and merging token usage across the
// message_start, message_delta and message_stop events. The text is
// assembled only when wantText is set.
func parseSSEResponse(raw []byte, wantText bool) (outputText *string, usage Usage) {
	var text strings.Builder

	for line := range bytes.Lines(raw) {
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if string(data) == "[DONE]" {
			break
		}

		var event sseEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			usage.merge(event.Message.Usage)
		case "content_block_delta":
			if wantText && event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta", "message_stop":
			usage.merge(event.Usage)
		}
	}

	if text.Len() > 0 {
		s := text.String()
		outputText = &s
	}
	return outputText, usage.reliable()
}
