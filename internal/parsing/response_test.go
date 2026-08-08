package parsing

import "testing"

func TestExtractResponseFields_NonStreaming(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantText        string // "" means want nil
		wantInputToks   *int
		wantOutputToks  *int
		wantCacheCreate *int
		wantCacheRead   *int
	}{
		{
			name:           "typical response",
			body:           `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello world"}],"model":"claude-sonnet-5","usage":{"input_tokens":25,"output_tokens":15}}`,
			wantText:       "Hello world",
			wantInputToks:  intPtr(25),
			wantOutputToks: intPtr(15),
		},
		{
			name:           "text block is not first content block",
			body:           `{"content":[{"type":"tool_use","id":"t1","name":"foo","input":{}},{"type":"text","text":"final answer"}],"usage":{"input_tokens":5,"output_tokens":8}}`,
			wantText:       "final answer",
			wantInputToks:  intPtr(5),
			wantOutputToks: intPtr(8),
		},
		{
			name:           "no text block present",
			body:           `{"content":[{"type":"tool_use","id":"t1","name":"foo","input":{}}],"usage":{"input_tokens":5,"output_tokens":8}}`,
			wantText:       "",
			wantInputToks:  intPtr(5),
			wantOutputToks: intPtr(8),
		},
		{
			name:           "empty content array",
			body:           `{"content":[],"usage":{"input_tokens":1,"output_tokens":2}}`,
			wantText:       "",
			wantInputToks:  intPtr(1),
			wantOutputToks: intPtr(2),
		},
		{
			name:           "missing usage",
			body:           `{"content":[{"type":"text","text":"hi"}]}`,
			wantText:       "hi",
			wantInputToks:  nil,
			wantOutputToks: nil,
		},
		{
			name:           "malformed JSON",
			body:           `{"content": [`,
			wantText:       "",
			wantInputToks:  nil,
			wantOutputToks: nil,
		},
		{
			name:            "cache creation and read tokens present",
			body:            `{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":200,"cache_read_input_tokens":50}}`,
			wantText:        "hi",
			wantInputToks:   intPtr(10),
			wantOutputToks:  intPtr(5),
			wantCacheCreate: intPtr(200),
			wantCacheRead:   intPtr(50),
		},
		{
			name:            "cache fields absent stay nil, not zero",
			body:            `{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}`,
			wantText:        "hi",
			wantInputToks:   intPtr(10),
			wantOutputToks:  intPtr(5),
			wantCacheCreate: nil,
			wantCacheRead:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, usage := ExtractResponseFields([]byte(tt.body), false)

			if tt.wantText == "" {
				if gotText != nil {
					t.Errorf("outputText = %q, want nil", *gotText)
				}
			} else if gotText == nil || *gotText != tt.wantText {
				t.Errorf("outputText = %v, want %q", derefOrNil(gotText), tt.wantText)
			}

			assertIntPtrEqual(t, "inputTokens", usage.InputTokens, tt.wantInputToks)
			assertIntPtrEqual(t, "outputTokens", usage.OutputTokens, tt.wantOutputToks)
			assertIntPtrEqual(t, "cacheCreationTokens", usage.CacheCreationTokens, tt.wantCacheCreate)
			assertIntPtrEqual(t, "cacheReadTokens", usage.CacheReadTokens, tt.wantCacheRead)
		})
	}
}

func intPtr(i int) *int { return &i }

func assertIntPtrEqual(t *testing.T, field string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", field, *want)
	case want != nil && got != nil && *want != *got:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}
