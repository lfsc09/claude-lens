// Package logging configures the process-wide structured logger.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/lfsc09/claude-lens/internal/config"
)

// Setup wires slog's default logger to write to both stderr and a rotating
// file under cfg.LogDir (5MB per file, 3 backups — matches the old Python
// RotatingFileHandler settings).
func Setup(cfg config.Config) {
	fileWriter := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.LogDir, "claude-lens.log"),
		MaxSize:    5, // MB
		MaxBackups: 3,
	}
	w := io.MultiWriter(os.Stderr, fileWriter)

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
}
