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
// message_start, message_delta and message_stop events. Gateways distribute
// the counters differently (Anthropic puts input and cache counts in
// message_start, Bifrost only in message_delta, LiteLLM repeats them in
// message_stop), so no single event is authoritative. The text is assembled
// only when wantText is set.
func parseSSEResponse(raw []byte, wantText bool) (outputText *string, usage Usage) {
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
