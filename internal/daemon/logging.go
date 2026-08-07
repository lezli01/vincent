package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// newLogger builds the daemon logger (phase 1 decision): slog TextHandler
// over a size-rotated {data_dir}/logs/daemon.log, mirrored to stderr in
// foreground mode. The returned LevelVar starts at info and is re-set from
// config at startup and on every hot-reload. close flushes the log file.
func newLogger(logsDir string, foreground bool) (log *slog.Logger, level *slog.LevelVar, closeLog func() error) {
	lj := &lumberjack.Logger{
		Filename:   filepath.Join(logsDir, "daemon.log"),
		MaxSize:    10, // MB
		MaxBackups: 3,
	}
	var w io.Writer = lj
	if foreground {
		w = io.MultiWriter(os.Stderr, lj)
	}
	level = new(slog.LevelVar)
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h), level, lj.Close
}

// parseLevel maps a validated config log_level to its slog level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}

// LogTail returns up to n trailing lines of the daemon log, for surfacing
// startup failures from `daemon start`.
func LogTail(dataDir string, n int) string {
	b, err := os.ReadFile(filepath.Join(dataDir, "logs", "daemon.log"))
	if err != nil {
		return fmt.Sprintf("(no daemon log: %v)", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
