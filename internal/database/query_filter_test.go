package database

import (
	"reflect"
	"testing"
)

func TestCompileExchangeFilter_Valid(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantWhere string
		wantArgs  []any
	}{
		{"empty query", "", "", nil},
		{"blank query", "   ", "", nil},
		{"text equals", `model = sonnet`, "model = ?", []any{"sonnet"}},
		{"text like", `model like sonnet`, `model LIKE ? ESCAPE '\'`, []any{"%sonnet%"}},
		{"quoted value with spaces", `path = "/v1/messages"`, "path = ?", []any{"/v1/messages"}},
		{"number gt", `cost > 0.5`, "cost > ?", []any{0.5}},
		{"number no spaces", `cost>=0.5`, "cost >= ?", []any{0.5}},
		{"negative number", `id != -1`, "id != ?", []any{-1.0}},
		{"session maps to two columns", `session = "abc"`, "(session_id = ? OR session_name = ?)", []any{"abc", "abc"}},
		{"session like maps to two columns", `session like abc`, `(session_id LIKE ? ESCAPE '\' OR session_name LIKE ? ESCAPE '\')`, []any{"%abc%", "%abc%"}},
		{"bool true", `stream = true`, "is_streaming = ?", []any{1}},
		{"bool false", `stream = false`, "is_streaming = ?", []any{0}},
		{"date", `date >= 2026-08-01`, "timestamp >= ?", nil}, // arg checked separately (depends on local tz)
		{
			"and",
			`model = sonnet AND cost > 0.5`,
			"model = ? AND cost > ?",
			[]any{"sonnet", 0.5},
		},
		{
			"or",
			`model = sonnet OR cost > 0.5`,
			"model = ? OR cost > ?",
			[]any{"sonnet", 0.5},
		},
		{
			"and binds tighter than or, no parens",
			`model = sonnet AND cost > 0.5 OR id = 1`,
			"(model = ? AND cost > ?) OR id = ?",
			[]any{"sonnet", 0.5, 1.0},
		},
		{
			"parens override precedence",
			`(model = sonnet OR cost > 0.5) AND id = 1`,
			"(model = ? OR cost > ?) AND id = ?",
			[]any{"sonnet", 0.5, 1.0},
		},
		{
			"nested parens",
			`model = sonnet AND (cost > 0.5 OR (id = 1 AND stream = true))`,
			"model = ? AND (cost > ? OR (id = ? AND is_streaming = ?))",
			[]any{"sonnet", 0.5, 1.0, 1},
		},
		{
			"cache_tokens computed expr",
			`cache_tokens > 100`,
			cacheTokensExpr + " > ?",
			[]any{100.0},
		},
		{
			"total_tokens computed expr",
			`total_tokens > 100`,
			totalTokensExpr + " > ?",
			[]any{100.0},
		},
		{"case-insensitive keywords and field/op", `MODEL LIKE sonnet AND COST > 0.5`, `model LIKE ? ESCAPE '\' AND cost > ?`, []any{"%sonnet%", 0.5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args, err := CompileExchangeFilter(tt.query)
			if err != nil {
				t.Fatalf("CompileExchangeFilter(%q): unexpected error: %v", tt.query, err)
			}
			if where != tt.wantWhere {
				t.Errorf("where = %q, want %q", where, tt.wantWhere)
			}
			if tt.wantArgs != nil && !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tt.wantArgs)
			}
		})
	}
}

func TestCompileExchangeFilter_DateValue(t *testing.T) {
	_, args, err := CompileExchangeFilter(`date >= 2026-08-01`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("got %d args, want 1", len(args))
	}
	v, ok := args[0].(float64)
	if !ok || v <= 0 {
		t.Fatalf("date arg = %#v, want a positive unix timestamp", args[0])
	}
}

func TestCompileExchangeFilter_Errors(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"unknown field", `bogus_field = 1`},
		{"number op on text field", `model > sonnet`},
		{"text op on number field", `cost like 0.5`},
		{"like op on bool field", `stream like true`},
		{"malformed number", `cost > notanumber`},
		{"malformed date", `date > notadate`},
		{"malformed bool", `stream = maybe`},
		{"unbalanced open paren", `(model = sonnet AND cost > 0.5`},
		{"unbalanced close paren", `model = sonnet) AND cost > 0.5`},
		{"empty parens", `()`},
		{"trailing garbage", `model = sonnet AND`},
		{"missing operator", `model sonnet`},
		{"missing value", `model =`},
		{"unterminated string", `model = "sonnet`},
		{"dangling and/or with no condition", `AND model = sonnet`},
		{"double operator", `cost >= => 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := CompileExchangeFilter(tt.query)
			if err == nil {
				t.Fatalf("CompileExchangeFilter(%q): expected error, got nil", tt.query)
			}
		})
	}
}

func TestExtractExactSession(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		wantID string
		wantOK bool
	}{
		{"exact session equals", `session = "abc-123"`, "abc-123", true},
		{"unquoted value", `session = abc-123`, "abc-123", true},
		{"session like is not exact", `session like abc`, "", false},
		{"session anded with something else", `session = "abc" AND cost > 1`, "", false},
		{"session ored with something else", `session = "abc" OR cost > 1`, "", false},
		{"wrong field", `model = "abc"`, "", false},
		{"empty query", ``, "", false},
		{"malformed query", `session = `, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := ExtractExactSession(tt.query)
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("ExtractExactSession(%q) = (%q, %v), want (%q, %v)", tt.query, id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
