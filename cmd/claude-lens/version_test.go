package main

import (
	"os"
	"strings"
	"testing"
)

// TestDeployWorkflowUsesCorrectLdflagsSymbol guards against a real
// regression hit while wiring this up: for a package named main,
// -ldflags "-X ...Version=..." must target the symbol as "main.Version",
// not the module's full import path (e.g.
// "github.com/lfsc09/claude-lens/cmd/claude-lens.Version"). Both look
// equally plausible, but the linker gives no error or warning on a
// mismatched -X target — it just silently leaves Version at its "dev"
// source default, so a wrong path here fails quietly rather than loudly.
// See version.go's comment for the full explanation.
func TestDeployWorkflowUsesCorrectLdflagsSymbol(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/deploy.yaml")
	if err != nil {
		t.Skipf("deploy.yaml not found: %v", err)
	}
	if !strings.Contains(string(b), "-X main.Version=") {
		t.Error(`deploy.yaml must inject the version via "-X main.Version=...", not the module's import path`)
	}
}
