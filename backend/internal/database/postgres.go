package database

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
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

	// maxErrorTypeNames caps the Go type names logged for a retried error.
	maxErrorTypeNames = 8
)

// Operations reported in startup errors.
const (
	opParseConfig = "parse database config"
	opCreatePool  = "create database pool"
	opConnect     = "connect to postgres"
)

// NewPostgresPool creates a connection pool and waits until PostgreSQL
// answers a ping, retrying with backoff while IsRetryable reports the ping
// error as retryable. It gives up at the first error that isn't, after a
// bounded number of attempts, or as soon as ctx is cancelled.
//
// Every error it returns is a *startupError, whose text names only the
// operation, attempts, kind and SQLSTATE: never the driver's message, the
// host, user, database or password. The driver error stays available
// through errors.Is, errors.As and Classify.
func NewPostgresPool(
	ctx context.Context,
	cfg config.Database,
	logger *slog.Logger,
) (*pgxpool.Pool, error) {
	poolConfig, err := buildPoolConfig(cfg)
	if err != nil {
		return nil, newStartupError(opParseConfig, 0, err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, newStartupError(opCreatePool, 0, err)
	}

	ping := func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()

		return pool.Ping(pingCtx)
	}

	// The error's text can name the host, user and database and carry
	// server messages, so only its classification and type names are logged.
	onRetry := func(attempt int, wait time.Duration, err error) {
		class := Classify(err)
		attrs := []any{
			"attempt", attempt,
			"max_attempts", connectAttempts,
			"retry_in", wait,
			"kind", class.Kind.String(),
		}
		if class.SQLState != "" {
			attrs = append(attrs, "sqlstate", class.SQLState)
		}
		attrs = append(attrs, "error_type", errorTypeNames(err))
		logger.Warn("database not ready, retrying", attrs...)
	}

	attempts, err := retry(
		ctx, connectAttempts, retryInitialDelay, retryMaxDelay, ping, IsRetryable, onRetry,
	)
	if err != nil {
		pool.Close()
		return nil, newStartupError(opConnect, attempts, err)
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
		// The error's text holds the connection string; NewPostgresPool
		// wraps it in a startupError, which never shows that text.
		return nil, err
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
// from delay up to maxDelay. It stops early at the first error retryable
// rejects, or when ctx is cancelled. It returns the number of attempts made
// and the last error: fn's, or ctx's when ctx ended the retries.
func retry(
	ctx context.Context,
	attempts int,
	delay, maxDelay time.Duration,
	fn func(context.Context) error,
	retryable func(error) bool,
	onRetry func(attempt int, wait time.Duration, err error),
) (int, error) {
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return attempt, nil
		}

		if attempt >= attempts {
			return attempt, err
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return attempt, ctxErr
		}

		if !retryable(err) {
			return attempt, err
		}

		onRetry(attempt, delay, err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return attempt, ctx.Err()
		case <-timer.C:
		}

		delay = min(delay*2, maxDelay)
	}
}

// startupError is an error from NewPostgresPool. Its text is built only from
// the operation, the number of attempts, the error's kind and its SQLSTATE,
// never from the wrapped error's text, which can name the host, user and
// database and carry server messages. Unwrap keeps the wrapped error
// available to errors.Is, errors.As and Classify.
type startupError struct {
	op       string
	attempts int // 0 when no ping was attempted
	class    Classification
	err      error
}

func newStartupError(op string, attempts int, err error) *startupError {
	return &startupError{op: op, attempts: attempts, class: Classify(err), err: err}
}

// Error returns, for example,
// "connect to postgres: after 1 attempt: unavailable (sqlstate 28P01)".
func (e *startupError) Error() string {
	var b strings.Builder
	b.WriteString(e.op)
	b.WriteString(": ")
	if e.attempts > 0 {
		b.WriteString("after ")
		b.WriteString(strconv.Itoa(e.attempts))
		if e.attempts == 1 {
			b.WriteString(" attempt: ")
		} else {
			b.WriteString(" attempts: ")
		}
	}
	b.WriteString(e.class.Kind.String())
	if e.class.SQLState != "" {
		b.WriteString(" (sqlstate ")
		b.WriteString(e.class.SQLState)
		b.WriteString(")")
	}
	return b.String()
}

func (e *startupError) Unwrap() error {
	return e.err
}

// errorTypeNames returns the Go type names in err's chain, depth first, at
// most maxErrorTypeNames, joined with commas. Type names come from code, not
// data, so they are safe to log. It calls no method other than Unwrap, and
// returns "" for a chain walkChain rejects (a typed-nil error, a cycle or an
// oversized chain).
func errorTypeNames(err error) string {
	if err == nil || !walkChain(err) {
		return ""
	}

	var names []string
	var walk func(error)
	walk = func(err error) {
		if err == nil || len(names) >= maxErrorTypeNames {
			return
		}
		names = append(names, fmt.Sprintf("%T", err))

		switch unwrapper := err.(type) {
		case interface{ Unwrap() error }:
			walk(unwrapper.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range unwrapper.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)

	return strings.Join(names, ",")
}
