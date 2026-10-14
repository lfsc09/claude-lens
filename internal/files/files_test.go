package files

import (
	"os"
	"path/filepath"
	"testing"
)

const testSessionID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestTryDeleteClaudeSession_RemovesSessionFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcript := filepath.Join(home, ".claude", "projects", "app", testSessionID+".jsonl")
	taskDir := filepath.Join(home, ".claude", "tasks", testSessionID, "1.json")
	settings := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, transcript)
	writeFile(t, taskDir)
	writeFile(t, settings)

	deleted, err := TryDeleteClaudeSession(testSessionID)
	if err != nil {
		t.Fatalf("TryDeleteClaudeSession: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if exists(transcript) || exists(filepath.Dir(taskDir)) {
		t.Error("session files were not removed")
	}
	if !exists(settings) {
		t.Error("unrelated settings.json was removed")
	}
}

func TestTryDeleteClaudeSession_RejectsNonSessionIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".claude", "settings.json")
	projects := filepath.Join(home, ".claude", "projects", "app", "log.jsonl")
	writeFile(t, settings)
	writeFile(t, projects)

	for _, id := range []string{"", "settings", "projects", "..", ".", "default_session", "../projects", testSessionID + "x"} {
		if _, err := TryDeleteClaudeSession(id); err == nil {
			t.Errorf("TryDeleteClaudeSession(%q) succeeded, want error", id)
		}
	}
	if !exists(settings) || !exists(projects) {
		t.Error("files outside a session were removed")
	}
}
