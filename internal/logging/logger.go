package logging

import (
	"log/slog"
	"os"
)

// New returns a production-friendly JSON logger writing to stdout unless
// LOG_FORMAT=console is provided to prefer a human-readable output.
func New() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if format := os.Getenv("LOG_FORMAT"); format == "console" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
