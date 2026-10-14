package proxy

import "testing"

func TestFormatAuthHeader(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "raw key gets Bearer prefix", token: "sk-abc123", want: "Bearer sk-abc123"},
		{name: "already has Bearer scheme", token: "Bearer sk-abc123", want: "Bearer sk-abc123"},
		{name: "already has custom scheme", token: "Token sk-abc123", want: "Token sk-abc123"},
		{name: "surrounding whitespace trimmed", token: "  sk-abc123  ", want: "Bearer sk-abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatAuthHeader(tt.token); got != tt.want {
				t.Errorf("FormatAuthHeader(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

func TestSanitizeSessionID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "already clean", input: "abc-123_XYZ", want: "abc-123_XYZ"},
		{name: "spaces become underscores", input: "my session name", want: "my_session_name"},
		{name: "leading/trailing junk trimmed", input: "___abc___", want: "abc"},
		{name: "empty string falls back to default", input: "", want: "default_session"},
		{name: "only disallowed characters falls back to default", input: "!!!", want: "default_session"},
		{name: "unicode characters replaced", input: "sessão-日本語", want: "sess_o-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeSessionID(tt.input); got != tt.want {
				t.Errorf("SanitizeSessionID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
