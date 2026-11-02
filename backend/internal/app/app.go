package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nichax-sk/safeinsiam/internal/config"
	"github.com/Nichax-sk/safeinsiam/internal/database"
	"github.com/Nichax-sk/safeinsiam/internal/health"
	"github.com/Nichax-sk/safeinsiam/internal/httpapi"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 1 << 20
)

// Run connects to the database, serves HTTP until ctx is cancelled, then
// drains in-flight requests and closes the database pool, in that order.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	gin.SetMode(ginMode(cfg.Env))

	dbPool, err := database.NewPostgresPool(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer dbPool.Close()
	logger.Info("database connected")

	handler, err := newHandler(logger, cfg.HTTP, dbPool)
	if err != nil {
		return fmt.Errorf("configure router: %w", err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	addr := net.JoinHostPort(cfg.HTTP.Host, strconv.Itoa(cfg.HTTP.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	logger.Info("SafeInSiam API listening",
		"addr", listener.Addr().String(),
		"env", cfg.Env,
	)

	return serve(ctx, server, listener, cfg.HTTP.ShutdownTimeout, logger)
}

// newHandler builds the HTTP handler: the router, with successful health
// checks logged at Debug and the configured CORS origins and trusted
// proxies, plus the health endpoints. Its only error is NewRouter's.
func newHandler(logger *slog.Logger, httpCfg config.HTTP, db health.Pinger) (http.Handler, error) {
	router, err := httpapi.NewRouter(httpapi.Options{
		Logger:             logger,
		DebugRoutes:        health.Routes,
		CORSAllowedOrigins: httpCfg.CORSAllowedOrigins,
		TrustedProxies:     httpCfg.TrustedProxies,
	})
	if err != nil {
		return nil, err
	}
	health.Register(router.Engine, db)

	return router.Engine, nil
}

func ginMode(env config.Env) string {
	switch env {
	case config.EnvDevelopment:
		return gin.DebugMode
	case config.EnvTest:
		return gin.TestMode
	default:
		return gin.ReleaseMode
	}
}

// serve runs server on listener until ctx is cancelled or the server fails,
// then shuts it down gracefully within shutdownTimeout.
func serve(
	ctx context.Context,
	server *http.Server,
	listener net.Listener,
	shutdownTimeout time.Duration,
	logger *slog.Logger,
) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown started", "timeout", shutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		shutdownTimeout,
	)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown http server: %w", err)
	}

	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}

	logger.Info("shutdown complete")

	return nil
}
