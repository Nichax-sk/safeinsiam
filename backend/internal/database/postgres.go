package database

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

const (
	applicationName = "safeinsiam-api"

	maxConnLifetime   = time.Hour
	maxConnIdleTime   = 30 * time.Minute
	healthCheckPeriod = time.Minute

	connectAttempts   = 5
	pingTimeout       = 5 * time.Second
	retryInitialDelay = 500 * time.Millisecond
	retryMaxDelay     = 4 * time.Second
)

// NewPostgresPool creates a connection pool and waits until PostgreSQL
// answers a ping, retrying with backoff. It gives up after a bounded number
// of attempts or as soon as ctx is cancelled.
func NewPostgresPool(
	ctx context.Context,
	cfg config.Database,
	logger *slog.Logger,
) (*pgxpool.Pool, error) {
	poolConfig, err := buildPoolConfig(cfg)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	ping := func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()

		return pool.Ping(pingCtx)
	}

	onRetry := func(attempt int, wait time.Duration, err error) {
		logger.Warn("database not ready, retrying",
			"attempt", attempt,
			"max_attempts", connectAttempts,
			"retry_in", wait,
			"error", err,
		)
	}

	if err := retry(
		ctx, connectAttempts, retryInitialDelay, retryMaxDelay, ping, onRetry,
	); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	return pool, nil
}

// buildPoolConfig builds the pool configuration from a URL so that every
// value is escaped and pgx derives TLS settings from the real host.
func buildPoolConfig(cfg config.Database) (*pgxpool.Config, error) {
	connURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     "/" + cfg.Name,
		RawQuery: url.Values{"sslmode": {cfg.SSLMode}}.Encode(),
	}

	poolConfig, err := pgxpool.ParseConfig(connURL.String())
	if err != nil {
		// pgx redacts the password in parse errors.
		return nil, fmt.Errorf("parse database config: %w", err)
	}

	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = maxConnLifetime
	poolConfig.MaxConnIdleTime = maxConnIdleTime
	poolConfig.HealthCheckPeriod = healthCheckPeriod

	runtimeParams := poolConfig.ConnConfig.RuntimeParams
	runtimeParams["application_name"] = applicationName
	if cfg.StatementTimeout > 0 {
		runtimeParams["statement_timeout"] = strconv.FormatInt(
			cfg.StatementTimeout.Milliseconds(), 10,
		)
	}

	return poolConfig, nil
}

// retry calls fn up to attempts times, doubling the wait between attempts
// from delay up to maxDelay. It returns early when ctx is cancelled.
func retry(
	ctx context.Context,
	attempts int,
	delay, maxDelay time.Duration,
	fn func(context.Context) error,
	onRetry func(attempt int, wait time.Duration, err error),
) error {
	var err error

	for attempt := 1; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}

		if attempt >= attempts {
			return fmt.Errorf("after %d attempts: %w", attempt, err)
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("after %d attempts: %w", attempt, ctxErr)
		}

		onRetry(attempt, delay, err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("after %d attempts: %w", attempt, ctx.Err())
		case <-timer.C:
		}

		delay = min(delay*2, maxDelay)
	}
}
