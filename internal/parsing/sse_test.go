package parsing

import "testing"

const fullSSEFixture = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5\",\"usage\":{\"input_tokens\":25,\"output_tokens\":1,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":50}}}\n" +
	"\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
	"\n" +
	"event: ping\n" +
	"data: {\"type\": \"ping\"}\n" +
	"\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n" +
	"\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n" +
	"\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
	"\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":15}}\n" +
	"\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n" +
	"\n"

func TestExtractResponseFields_Streaming_FullSequence(t *testing.T) {
	gotText, usage := ExtractResponseFields([]byte(fullSSEFixture), true)

	if gotText == nil || *gotText != "Hello world" {
		t.Errorf("outputText = %v, want %q", derefOrNil(gotText), "Hello world")
	}
	assertIntPtrEqual(t, "inputTokens", usage.InputTokens, intPtr(25))
	assertIntPtrEqual(t, "outputTokens", usage.OutputTokens, intPtr(15))
	assertIntPtrEqual(t, "cacheCreationTokens", usage.CacheCreationTokens, intPtr(200))
	assertIntPtrEqual(t, "cacheReadTokens", usage.CacheReadTokens, intPtr(50))
}

func TestExtractResponseFields_Streaming_StopsAtDoneMarker(t *testing.T) {
	raw := "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"before\"}}\n" +
		"data: [DONE]\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"after\"}}\n"

	gotText, _ := ExtractResponseFields([]byte(raw), true)
	if gotText == nil || *gotText != "before" {
		t.Errorf("outputText = %v, want %q (text after [DONE] must be ignored)", derefOrNil(gotText), "before")
	}
}

func TestExtractResponseFields_Streaming_SkipsMalformedEventLines(t *testing.T) {
	raw := "data: {this is not json}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n"

	gotText, _ := ExtractResponseFields([]byte(raw), true)
	if gotText == nil || *gotText != "ok" {
		t.Errorf("outputText = %v, want %q (malformed line should be skipped, not abort parsing)", derefOrNil(gotText), "ok")
	}
}

func TestExtractResponseFields_Streaming_NoTextYieldsNilOutputText(t *testing.T) {
	raw := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":0}}\n"

	gotText, usage := ExtractResponseFields([]byte(raw), true)
	if gotText != nil {
		t.Errorf("outputText = %q, want nil when no text_delta events occur", *gotText)
	}
	assertIntPtrEqual(t, "inputTokens", usage.InputTokens, intPtr(10))
	assertIntPtrEqual(t, "outputTokens", usage.OutputTokens, intPtr(0))
	assertIntPtrEqual(t, "cacheCreationTokens", usage.CacheCreationTokens, nil)
	assertIntPtrEqual(t, "cacheReadTokens", usage.CacheReadTokens, nil)
}

func TestExtractResponseFields_Streaming_IgnoresNonDataLines(t *testing.T) {
	raw := "id: 1\n" +
		"event: content_block_delta\n" +
		": this is a comment line\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n"

	gotText, _ := ExtractResponseFields([]byte(raw), true)
	if gotText == nil || *gotText != "hi" {
		t.Errorf("outputText = %v, want %q", derefOrNil(gotText), "hi")
	}
}
