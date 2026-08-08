//go:build !dev

package config

// loadDotEnv is a no-op in release builds — only real OS environment
// variables are ever read. See dotenv_dev.go for the -tags dev behavior.
func loadDotEnv() {}
