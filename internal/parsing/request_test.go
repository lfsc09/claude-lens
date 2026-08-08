package parsing

import "testing"

func TestExtractRequestFields(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantMessages  string // "" means want nil
		wantStreaming bool
		wantModel     string // "" means want nil
	}{
		{
			name:          "typical non-streaming request",
			body:          `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantMessages:  `[{"role":"user","content":"hi"}]`,
			wantStreaming: false,
			wantModel:     "claude-sonnet-5",
		},
		{
			name:          "streaming request",
			body:          `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantMessages:  `[{"role":"user","content":"hi"}]`,
			wantStreaming: true,
			wantModel:     "claude-opus-5",
		},
		{
			name:          "stream field absent defaults to false",
			body:          `{"model":"claude-sonnet-5","messages":[]}`,
			wantMessages:  `[]`,
			wantStreaming: false,
			wantModel:     "claude-sonnet-5",
		},
		{
			name:          "messages field absent",
			body:          `{"model":"claude-sonnet-5"}`,
			wantMessages:  "",
			wantStreaming: false,
			wantModel:     "claude-sonnet-5",
		},
		{
			name:          "messages explicitly null",
			body:          `{"model":"claude-sonnet-5","messages":null}`,
			wantMessages:  "",
			wantStreaming: false,
			wantModel:     "claude-sonnet-5",
		},
		{
			name:          "empty model string treated as absent",
			body:          `{"model":"","messages":[]}`,
			wantMessages:  `[]`,
			wantStreaming: false,
			wantModel:     "",
		},
		{
			name:          "model field absent",
			body:          `{"messages":[]}`,
			wantMessages:  `[]`,
			wantStreaming: false,
			wantModel:     "",
		},
		{
			name:          "malformed JSON",
			body:          `{"model": "claude-sonnet-5", "messages": [`,
			wantMessages:  "",
			wantStreaming: false,
			wantModel:     "",
		},
		{
			name:          "top-level JSON array instead of object",
			body:          `[1,2,3]`,
			wantMessages:  "",
			wantStreaming: false,
			wantModel:     "",
		},
		{
			name:          "unicode content preserved without ASCII escaping",
			body:          `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"héllo wörld 日本語"}]}`,
			wantMessages:  `[{"role":"user","content":"héllo wörld 日本語"}]`,
			wantStreaming: false,
			wantModel:     "claude-sonnet-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMessages, gotStreaming, gotModel := ExtractRequestFields([]byte(tt.body))

			if tt.wantMessages == "" {
				if gotMessages != nil {
					t.Errorf("inputMessages = %q, want nil", *gotMessages)
				}
			} else {
				if gotMessages == nil || *gotMessages != tt.wantMessages {
					t.Errorf("inputMessages = %v, want %q", derefOrNil(gotMessages), tt.wantMessages)
				}
			}

			if gotStreaming != tt.wantStreaming {
				t.Errorf("isStreaming = %v, want %v", gotStreaming, tt.wantStreaming)
			}

			if tt.wantModel == "" {
				if gotModel != nil {
					t.Errorf("model = %q, want nil", *gotModel)
				}
			} else {
				if gotModel == nil || *gotModel != tt.wantModel {
					t.Errorf("model = %v, want %q", derefOrNil(gotModel), tt.wantModel)
				}
			}
		})
	}
}

func derefOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
