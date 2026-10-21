package modelname

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		id, want string
	}{
		{"claude-sonnet-5-5", "claude-sonnet-5-5"},
		{"us.anthropic.claude-sonnet-5-5", "claude-sonnet-5-5"},
		{"eu.anthropic.claude-opus-5", "claude-opus-5"},
		{"global.anthropic.claude-haiku-5-5", "claude-haiku-5-5"},
		{"anthropic.claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", "claude-haiku-4-5-20251001"},
		{"bedrock/us.anthropic.claude-haiku-4-5-20251001-v1:0", "claude-haiku-4-5-20251001"},
		{"anthropic/claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{"claude-sonnet-5-5[1m]", "claude-sonnet-5-5[1m]"},
		{"gpt-4o", "gpt-4o"},
		{"openai/gpt-4o", "gpt-4o"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.id); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestNormalize_IsIdempotent(t *testing.T) {
	for _, id := range []string{"bedrock/us.anthropic.claude-opus-5-v1:0", "claude-opus-5", "x/y/z"} {
		once := Normalize(id)
		if twice := Normalize(once); twice != once {
			t.Errorf("Normalize(Normalize(%q)) = %q, want %q", id, twice, once)
		}
	}
}
