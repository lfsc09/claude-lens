// Package config loads claude-lens configuration from the process environment.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for both the proxy and admin servers.
type Config struct {
	ProxyAddr string
	AdminAddr string

	AnthropicBaseURL       string
	AnthropicAuthToken     string
	AnthropicCustomHeaders string

	DataDir string
	LogDir  string
	DBPath  string

	// SlackWebhookURL is the default Slack incoming-webhook target for cost
	// alerts: always used for the single-request spike alert, and used as a
	// fallback for a limiter's budget alert when it has no webhook of its
	// own configured.
	SlackWebhookURL string
	// AlertRequestCostUSD triggers a Slack alert whenever a single exchange
	// costs at least this much. Zero (the default) disables the alert.
	AlertRequestCostUSD float64
}

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// Load reads configuration from the OS environment (plus a dev-only .env file,
// see dotenv_dev.go / dotenv_release.go), applies defaults, and ensures the
// data/log directories exist.
func Load() (Config, error) {
	loadDotEnv()

	cfg := Config{
		ProxyAddr: getEnv("CLENS_PROXY_ADDR", ":7801"),
		AdminAddr: getEnv("CLENS_ADMIN_ADDR", ":7802"),

		AnthropicBaseURL:       strings.TrimRight(getEnv("CLENS_PROXY_BASE_URL", defaultAnthropicBaseURL), "/"),
		AnthropicAuthToken:     getEnv("CLENS_PROXY_AUTH_TOKEN", ""),
		AnthropicCustomHeaders: getEnv("CLENS_PROXY_CUSTOM_HEADERS", ""),

		DataDir: getEnv("CLENS_DATA_DIR", "data"),
		LogDir:  getEnv("CLENS_LOG_DIR", "logs"),

		SlackWebhookURL:     getEnv("CLENS_SLACK_WEBHOOK_URL", ""),
		AlertRequestCostUSD: getEnvFloat("CLENS_ALERT_REQUEST_COST_USD", 0),
	}
	cfg.DBPath = filepath.Join(cfg.DataDir, "claude-lens.db")

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func getEnv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func getEnvFloat(name string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}
