package admin

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/files"
)

// aboutResponse is a point-in-time snapshot: the About page fetches it once
// on load and never refreshes automatically, so the client always re-fetches
// (via reload) rather than the server pushing updates.
type aboutResponse struct {
	Version      string               `json:"version"`
	DBPath       string               `json:"db_path"`
	Tables       []database.TableStat `json:"tables"`
	LogPath      string               `json:"log_path"`
	LogSizeBytes int64                `json:"log_size_bytes"`
	LogTail      []string             `json:"log_tail"`
}

const logTailLines = 50

func (h *handlers) about(w http.ResponseWriter, r *http.Request) {
	tables, err := h.db.TableStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tables == nil {
		tables = []database.TableStat{}
	}

	var logSize int64
	if info, err := os.Stat(h.logPath); err == nil {
		logSize = info.Size()
	}

	tail, err := files.TailLines(h.logPath, logTailLines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tail == nil {
		tail = []string{}
	}

	writeJSON(w, http.StatusOK, aboutResponse{
		Version:      h.version,
		DBPath:       absPath(h.dbPath),
		Tables:       tables,
		LogPath:      absPath(h.logPath),
		LogSizeBytes: logSize,
		LogTail:      tail,
	})
}

// absPath resolves path to an absolute one for display purposes, falling
// back to the original value if it can't be resolved (e.g. an unreadable
// working directory).
func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
