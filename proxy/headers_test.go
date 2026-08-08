package proxy

import (
	"net/http"
	"reflect"
	"testing"
)

func TestParseCustomHeaders(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", raw: "", want: map[string]string{}},
		{name: "single header", raw: "X-Api-Key: abc123", want: map[string]string{"X-Api-Key": "abc123"}},
		{
			name: "multiple headers with blank lines",
			raw:  "X-Api-Key: abc123\n\nX-Team: platform\n",
			want: map[string]string{"X-Api-Key": "abc123", "X-Team": "platform"},
		},
		{
			name: "value containing a colon is preserved",
			raw:  "X-Custom: value:with:colons",
			want: map[string]string{"X-Custom": "value:with:colons"},
		},
		{name: "missing colon is an error", raw: "not-a-header-line", wantErr: true},
		{name: "empty name is an error", raw: ": value", wantErr: true},
		{name: "empty value is an error", raw: "X-Api-Key:", wantErr: true},
		{name: "whitespace-only value is an error", raw: "X-Api-Key:    ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCustomHeaders(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCustomHeaders(%q) = %v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCustomHeaders(%q) unexpected error: %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCustomHeaders(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSetHeader_ReplacesCaseInsensitively(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "old-value")
	SetHeader(h, "authorization", "new-value")

	if got := h.Get("Authorization"); got != "new-value" {
		t.Errorf("Authorization = %q, want %q", got, "new-value")
	}
	if len(h["Authorization"]) != 1 {
		t.Errorf("expected exactly one Authorization value, got %v", h["Authorization"])
	}
}

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
