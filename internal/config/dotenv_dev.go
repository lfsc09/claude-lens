//go:build dev

package config

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnv reads a .env file from the working directory, if present, and
// sets any variable not already present in the real environment. Real
// environment variables always take precedence. This file only exists in
// binaries built with -tags dev; release builds use the no-op in
// dotenv_release.go instead.
func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, value)
	}
}
