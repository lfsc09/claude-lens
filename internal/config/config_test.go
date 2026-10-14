package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultsBindLoopback(t *testing.T) {
	t.Setenv("CLENS_PROXY_ADDR", "")
	t.Setenv("CLENS_ADMIN_ADDR", "")
	t.Setenv("CLENS_DATA_DIR", filepath.Join(t.TempDir(), "data"))
	t.Setenv("CLENS_LOG_DIR", filepath.Join(t.TempDir(), "logs"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !IsLoopbackAddr(cfg.ProxyAddr) || !IsLoopbackAddr(cfg.AdminAddr) {
		t.Errorf("default addresses must be loopback, got proxy=%q admin=%q", cfg.ProxyAddr, cfg.AdminAddr)
	}
}

func TestLoad_RestrictsDirectoriesToOwner(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	logDir := t.TempDir()
	if err := os.Chmod(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLENS_DATA_DIR", dataDir)
	t.Setenv("CLENS_LOG_DIR", logDir)

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, dir := range []string{dataDir, logDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("%s mode = %o, want 700", dir, mode)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7801": true,
		"[::1]:7801":     true,
		"localhost:7801": true,
		":7801":          false,
		"0.0.0.0:7801":   false,
		"[::]:7801":      false,
		"192.168.1.5:80": false,
		"7801":           false,
		"":               false,
	}
	for addr, want := range cases {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
