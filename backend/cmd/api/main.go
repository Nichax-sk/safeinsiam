package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Nichax-sk/safeinsiam/internal/app"
	"github.com/Nichax-sk/safeinsiam/internal/config"
	"github.com/Nichax-sk/safeinsiam/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("SafeInSiam API stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := logging.New(os.Stdout, cfg.Env, cfg.LogLevel)
	slog.SetDefault(logger)

	if cfg.EnvFile != "" {
		logger.Info("environment file loaded", "path", cfg.EnvFile)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// Restore default signal handling once shutdown begins, so a second
	// signal terminates the process immediately.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return app.Run(ctx, cfg, logger)
}
