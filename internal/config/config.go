// Package config loads claude-lens configuration from the process environment.
package config

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// privateDirMode is the permission of the data and log directories.
const privateDirMode = 0o700

// Config holds all runtime configuration for both the proxy and admin servers.
type Config struct {
	ProxyAddr string
	AdminAddr string

	DataDir string
	LogDir  string
	DBPath  string
}

// Load reads configuration from the OS environment (plus a dev-only .env file,
// see dotenv_dev.go / dotenv_release.go), applies defaults, and ensures the
// data/log directories exist with owner-only permissions.
func Load() (Config, error) {
	loadDotEnv()

	cfg := Config{
		ProxyAddr: getEnv("CLENS_PROXY_ADDR", "127.0.0.1:7801"),
		AdminAddr: getEnv("CLENS_ADMIN_ADDR", "127.0.0.1:7802"),

		DataDir: getEnv("CLENS_DATA_DIR", "data"),
		LogDir:  getEnv("CLENS_LOG_DIR", "logs"),
	}
	cfg.DBPath = filepath.Join(cfg.DataDir, "claude-lens.db")

	for _, dir := range [...]string{cfg.DataDir, cfg.LogDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

// ensurePrivateDir creates dir if missing and restricts it to its owner.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return err
	}
	return os.Chmod(dir, privateDirMode)
}

// IsLoopbackAddr reports whether a listen address binds only to a loopback
// interface. Addresses without a host (":7801") or with an unspecified host
// ("0.0.0.0") listen on every interface and are not loopback.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && IsLoopbackHost(host)
}

// IsLoopbackHost reports whether host, without a port, is "localhost" or a
// loopback IP address.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// getEnv returns the trimmed value of the environment variable name, or
// fallback when it is unset or blank.
func getEnv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
