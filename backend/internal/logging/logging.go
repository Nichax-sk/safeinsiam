package logging

import (
	"io"
	"log/slog"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

// New returns a logger that writes human-readable text in development and
// test, and JSON in staging and production.
func New(w io.Writer, env config.Env, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	switch env {
	case config.EnvStaging, config.EnvProduction:
		return slog.New(slog.NewJSONHandler(w, opts))
	default:
		return slog.New(slog.NewTextHandler(w, opts))
	}
}
