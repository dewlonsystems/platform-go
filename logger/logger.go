// Package logger provides a single, shared *slog.Logger for the whole
// application. It has no dependency on any other internal package (not
// even errors or config) specifically so that every other package can
// depend on it without risk of an import cycle.
package logger

import (
	"log/slog"
	"os"
)

// Log is the shared application logger. It's safe for concurrent use, as
// all slog.Logger methods are. Replace it at startup via Init if you want
// different output (e.g. JSON in production).
//
// Init reassigns this variable and is only safe to call once, at startup,
// before any other goroutine has started logging. Calling it concurrently
// with logging elsewhere is a data race on Log itself.
var Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

// Init reconfigures Log for the given environment and level.
// env == "production" gets JSON output (for log aggregators); anything
// else gets human-readable text. level is one of "debug", "info", "warn", "error".
func Init(env, level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	Log = slog.New(handler)
}
