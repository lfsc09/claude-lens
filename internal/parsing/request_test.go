package parsing

import "testing"

func TestExtractRequestFields(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStreaming bool
		wantModel     string // "" means want nil
	}{
		{
			name:      "typical non-streaming request",
			body:      `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantModel: "claude-sonnet-5",
		},
		{
			name:          "streaming request",
			body:          `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantStreaming: true,
			wantModel:     "claude-opus-5",
		},
		{
			name:      "stream field absent defaults to false",
			body:      `{"model":"claude-sonnet-5","messages":[]}`,
			wantModel: "claude-sonnet-5",
		},
		{
			name:      "messages field absent",
			body:      `{"model":"claude-sonnet-5"}`,
			wantModel: "claude-sonnet-5",
		},
		{
			name: "empty model string treated as absent",
			body: `{"model":"","messages":[]}`,
		},
		{
			name: "model field absent",
			body: `{"messages":[]}`,
		},
		{
			name: "empty object",
			body: `{}`,
		},
		{
			name: "empty body",
			body: ``,
		},
		{
			name: "malformed JSON",
			body: `{"model": "claude-sonnet-5", "messages": [`,
		},
		{
			name: "top-level JSON array instead of object",
			body: `[1,2,3]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStreaming, gotModel := ExtractRequestFields([]byte(tt.body))

			if gotStreaming != tt.wantStreaming {
				t.Errorf("isStreaming = %v, want %v", gotStreaming, tt.wantStreaming)
			}

			if tt.wantModel == "" {
				if gotModel != nil {
					t.Errorf("model = %q, want nil", *gotModel)
				}
			} else if gotModel == nil || *gotModel != tt.wantModel {
				t.Errorf("model = %v, want %q", gotModel, tt.wantModel)
			}
		})
	}
}
