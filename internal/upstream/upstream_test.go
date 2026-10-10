package upstream

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lfsc09/claude-lens/internal/database"
)

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRegistry_FallsBackToAnthropicWhenNoIntegrationIsActive(t *testing.T) {
	reg, err := NewRegistry(context.Background(), openTestDB(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	got := reg.Current()
	if got.BaseURL.String() != AnthropicBaseURL || got.AuthToken != "" || len(got.Headers) != 0 || got.IntegrationID != 0 {
		t.Errorf("Current() = %+v, want direct Anthropic with no overrides", got)
	}
}

func TestRegistry_RefreshFollowsTheActiveIntegration(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	reg, err := NewRegistry(ctx, db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	list, _ := db.ListProxyIntegrations(ctx)
	p := list[1]
	p.BaseURL, p.AuthToken, p.CustomHeaders = "https://gw.example.test/anthropic", "tok", map[string]string{"X-Team": "a"}
	if err := db.UpdateProxyIntegration(ctx, p); err != nil {
		t.Fatalf("UpdateProxyIntegration: %v", err)
	}
	if err := db.ActivateProxyIntegration(ctx, p.ID, 1); err != nil {
		t.Fatalf("ActivateProxyIntegration: %v", err)
	}
	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	got := reg.Current()
	if got.Type != database.ProxyTypeBifrost || got.BaseURL.Host != "gw.example.test" || got.BaseURL.Path != "/anthropic" ||
		got.AuthToken != "tok" || got.Headers["X-Team"] != "a" {
		t.Errorf("Current() = %+v, want the activated Bifrost integration", got)
	}
	select {
	case <-reg.Changed():
	default:
		t.Error("Refresh didn't signal Changed()")
	}

	if err := db.DeactivateProxyIntegration(ctx, p.ID, 2); err != nil {
		t.Fatalf("DeactivateProxyIntegration: %v", err)
	}
	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := reg.Current(); got.IntegrationID != 0 || got.BaseURL.String() != AnthropicBaseURL {
		t.Errorf("Current() = %+v, want direct Anthropic after deactivation", got)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "https://gw.example.test/", want: "https://gw.example.test"},
		{in: " http://localhost:4000/anthropic/ ", want: "http://localhost:4000/anthropic"},
		{in: "gw.example.test", wantErr: true},
		{in: "ftp://gw.example.test", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeBaseURL(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestValidateHeaders(t *testing.T) {
	if err := ValidateHeaders(map[string]string{"X-Team": "platform"}); err != nil {
		t.Errorf("valid headers rejected: %v", err)
	}
	for name, headers := range map[string]map[string]string{
		"empty name":  {"": "v"},
		"bad name":    {"X Team": "v"},
		"empty value": {"X-Team": "  "},
		"newline":     {"X-Team": "a\nb"},
	} {
		if err := ValidateHeaders(headers); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
