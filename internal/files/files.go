package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TryDeleteClaudeSession attempts to delete the Claude session files and folders for the given session ID.
// It returns the number of deleted files or directories and an error, if any.
func TryDeleteClaudeSession(sessionID string) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("session_id is required")
	}

	claudeDir, err := getDefaultClaudeProjectsDir()
	if err != nil {
		return 0, err
	}

	deletedCount := 0
	// Walk ~/.claude and delete any files or folders belonging to sessionID. Matches are either
	// an exact name (e.g. tasks/<sessionID>/, file-history/<sessionID>/, session-env/<sessionID>)
	// or the sessionID as a filename stem (e.g. projects/<project>/<sessionID>.jsonl).
	err = filepath.Walk(claudeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if name == sessionID || strings.HasPrefix(name, sessionID+".") {
			if info.IsDir() {
				err = os.RemoveAll(path)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				return fmt.Errorf("failed to delete %s: %w", path, err)
			}
			deletedCount++
			if info.IsDir() {
				// The directory is gone; don't let Walk try to descend into it.
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return deletedCount, err
	}

	if deletedCount == 0 {
		return 0, fmt.Errorf("no files or directories found for session_id: %s", sessionID)
	}

	return deletedCount, nil
}

// getDefaultClaudeProjectsDir returns the default directory where Claude session files are stored.
func getDefaultClaudeProjectsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		return "", fmt.Errorf("claude directory does not exist at %s", claudeDir)
	}

	return claudeDir + string(os.PathSeparator), nil
}
