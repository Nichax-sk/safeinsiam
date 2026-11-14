package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

func testDatabaseConfig() config.Database {
	return config.Database{
		Host:             "db.example.internal",
		Port:             6543,
		Name:             "app_db",
		User:             "app_user",
		Password:         `p@ss w'o"r=d/%?#`,
		SSLMode:          "disable",
		MaxConns:         7,
		MinConns:         3,
		StatementTimeout: 30 * time.Second,
	}
}

func TestBuildPoolConfig(t *testing.T) {
	cfg := testDatabaseConfig()

	poolConfig, err := buildPoolConfig(cfg)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	conn := poolConfig.ConnConfig
	if conn.Host != cfg.Host || conn.Port != uint16(cfg.Port) ||
		conn.Database != cfg.Name || conn.User != cfg.User {
		t.Errorf("connection target = %s:%d/%s user=%s",
			conn.Host, conn.Port, conn.Database, conn.User)
	}
	if conn.Password != cfg.Password {
		t.Errorf("password did not round-trip through the URL")
	}
	if conn.TLSConfig != nil {
		t.Errorf("TLSConfig should be nil for sslmode=disable")
	}

	if got := conn.RuntimeParams["application_name"]; got != applicationName {
		t.Errorf("application_name = %q, want %q", got, applicationName)
	}
	if got := conn.RuntimeParams["statement_timeout"]; got != "30000" {
		t.Errorf("statement_timeout = %q, want 30000", got)
	}

	if poolConfig.MaxConns != 7 || poolConfig.MinConns != 3 {
		t.Errorf("pool size = max %d min %d, want max 7 min 3",
			poolConfig.MaxConns, poolConfig.MinConns)
	}
	if poolConfig.MaxConnLifetime != maxConnLifetime ||
		poolConfig.MaxConnIdleTime != maxConnIdleTime ||
		poolConfig.HealthCheckPeriod != healthCheckPeriod {
		t.Errorf("pool lifetimes not applied")
	}
}

func TestBuildPoolConfigVerifyFullUsesHostForTLS(t *testing.T) {
	cfg := testDatabaseConfig()
	cfg.SSLMode = "verify-full"

	poolConfig, err := buildPoolConfig(cfg)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	tlsConfig := poolConfig.ConnConfig.TLSConfig
	if tlsConfig == nil {
		t.Fatal("TLSConfig is nil for sslmode=verify-full")
	}
	if tlsConfig.ServerName != cfg.Host {
		t.Errorf("TLS ServerName = %q, want %q", tlsConfig.ServerName, cfg.Host)
	}
}

func TestBuildPoolConfigWithoutStatementTimeout(t *testing.T) {
	cfg := testDatabaseConfig()
	cfg.StatementTimeout = 0

	poolConfig, err := buildPoolConfig(cfg)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	if _, ok := poolConfig.ConnConfig.RuntimeParams["statement_timeout"]; ok {
		t.Error("statement_timeout set although the timeout is 0")
	}
}

func alwaysRetryable(error) bool { return true }

func TestRetrySucceedsAfterFailures(t *testing.T) {
	calls, retries := 0, 0

	attempts, err := retry(context.Background(), 5, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not ready")
			}
			return nil
		},
		alwaysRetryable,
		func(int, time.Duration, error) { retries++ },
	)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if calls != 3 || retries != 2 || attempts != 3 {
		t.Errorf("calls = %d, retries = %d, attempts = %d, want 3, 2 and 3", calls, retries, attempts)
	}
}

func TestRetryReturnsLastErrorAfterMaxAttempts(t *testing.T) {
	lastErr := errors.New("still down")
	calls := 0

	attempts, err := retry(context.Background(), 4, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			return lastErr
		},
		alwaysRetryable,
		func(int, time.Duration, error) {},
	)
	if !errors.Is(err, lastErr) {
		t.Fatalf("retry error = %v, want %v", err, lastErr)
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
	if attempts != 4 {
		t.Errorf("attempts = %d, want 4", attempts)
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	_, err := retry(ctx, 5, time.Hour, time.Hour,
		func(context.Context) error { return errors.New("not ready") },
		alwaysRetryable,
		func(int, time.Duration, error) { cancel() },
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("retry took %v after cancellation", elapsed)
	}
}

func TestRetryCapsDelay(t *testing.T) {
	var waits []time.Duration

	_, _ = retry(context.Background(), 6, time.Microsecond, 4*time.Microsecond,
		func(context.Context) error { return errors.New("not ready") },
		alwaysRetryable,
		func(_ int, wait time.Duration, _ error) { waits = append(waits, wait) },
	)

	want := []time.Duration{
		time.Microsecond,
		2 * time.Microsecond,
		4 * time.Microsecond,
		4 * time.Microsecond,
		4 * time.Microsecond,
	}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("waits = %v, want %v", waits, want)
			break
		}
	}
}

func TestRetryStopsOnNonRetryableError(t *testing.T) {
	permanent := errors.New("permanent")
	calls, retries := 0, 0

	attempts, err := retry(context.Background(), 5, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			return permanent
		},
		func(err error) bool { return !errors.Is(err, permanent) },
		func(int, time.Duration, error) { retries++ },
	)
	if !errors.Is(err, permanent) {
		t.Fatalf("retry error = %v, want %v", err, permanent)
	}
	if calls != 1 || retries != 0 || attempts != 1 {
		t.Errorf("calls = %d, retries = %d, attempts = %d, want 1, 0 and 1", calls, retries, attempts)
	}
}

func TestRetryStopsWhenErrorBecomesNonRetryable(t *testing.T) {
	transient, permanent := errors.New("transient"), errors.New("permanent")
	calls, retries := 0, 0

	attempts, err := retry(context.Background(), 5, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			if calls == 1 {
				return transient
			}
			return permanent
		},
		func(err error) bool { return errors.Is(err, transient) },
		func(int, time.Duration, error) { retries++ },
	)
	if !errors.Is(err, permanent) {
		t.Fatalf("retry error = %v, want %v", err, permanent)
	}
	if calls != 2 || retries != 1 || attempts != 2 {
		t.Errorf("calls = %d, retries = %d, attempts = %d, want 2, 1 and 2", calls, retries, attempts)
	}
}

func TestRetryRetriesRetryableErrors(t *testing.T) {
	calls, retries := 0, 0

	attempts, _ := retry(context.Background(), 5, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			return errors.New("transient")
		},
		alwaysRetryable,
		func(int, time.Duration, error) { retries++ },
	)
	if calls != 5 || retries != 4 || attempts != 5 {
		t.Errorf("calls = %d, retries = %d, attempts = %d, want 5, 4 and 5", calls, retries, attempts)
	}
}

func TestRetryPolicyConstants(t *testing.T) {
	if connectAttempts != 5 || retryInitialDelay != 500*time.Millisecond || retryMaxDelay != 4*time.Second {
		t.Errorf("retry policy = %d attempts, %v to %v; want 5 attempts, 500ms to 4s",
			connectAttempts, retryInitialDelay, retryMaxDelay)
	}
}

// Sentinels for the disclosure tests: any of these in a log or error text
// is a leak.
const (
	sentinelUser     = "sentinel_user"
	sentinelDatabase = "sentinel_db"
	sentinelPassword = "sentinel_password"
)

// forbiddenStartupText must never appear in startup logs or error text.
var forbiddenStartupText = []string{"sentinel", "127.0.0.1", "SELECT"}

// sentinelConfig points at a fake server on port with sentinel credentials.
func sentinelConfig(port int) config.Database {
	return config.Database{
		Host:     "127.0.0.1",
		Port:     port,
		Name:     sentinelDatabase,
		User:     sentinelUser,
		Password: sentinelPassword,
		SSLMode:  "disable",
		MaxConns: 2,
	}
}

// sentinelFatal is a FATAL error whose every text field holds sentinels.
func sentinelFatal(code string) *pgproto3.ErrorResponse {
	return &pgproto3.ErrorResponse{
		Severity:      "FATAL",
		Code:          code,
		Message:       "sentinel_message for user " + sentinelUser + " on " + sentinelDatabase,
		Detail:        "sentinel_detail",
		Hint:          "sentinel_hint",
		Where:         "sentinel_where",
		InternalQuery: "SELECT sentinel_internal_query",
	}
}

// startupLogger logs JSON at Debug level into buf, without timestamps.
func startupLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// retryLogs returns the "database not ready, retrying" entries in buf.
func retryLogs(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if entry["msg"] == "database not ready, retrying" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// assertNoDisclosure checks the retry logs, the error's text, the text
// app.Run wraps it in, and the line main logs for it.
func assertNoDisclosure(t *testing.T, err error, logs *bytes.Buffer) {
	t.Helper()

	appErr := fmt.Errorf("initialize database: %w", err)

	var jsonLine, textLine bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonLine, nil)).Error("SafeInSiam API stopped with error", "error", appErr)
	slog.New(slog.NewTextHandler(&textLine, nil)).Error("SafeInSiam API stopped with error", "error", appErr)

	for where, text := range map[string]string{
		"retry log":       logs.String(),
		"error":           err.Error(),
		"app error":       appErr.Error(),
		"%v":              fmt.Sprintf("%v", appErr),
		"%+v":             fmt.Sprintf("%+v", appErr),
		"main log (json)": jsonLine.String(),
		"main log (text)": textLine.String(),
	} {
		for _, forbidden := range forbiddenStartupText {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains %q: %s", where, forbidden, text)
			}
		}
	}
}

func TestNewPostgresPoolStopsOnNonRetryableStartupError(t *testing.T) {
	server := newFakePostgres(t, startupFatal(sentinelFatal("28P01")))
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	pool, err := NewPostgresPool(ctx, sentinelConfig(server.port), startupLogger(&logs))
	elapsed := time.Since(start)

	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool")
	}
	if got := len(retryLogs(t, &logs)); got != 0 {
		t.Errorf("logged %d retries, want 0 for a non-retryable error", got)
	}
	if accepted, _, _ := server.state(); accepted != 1 {
		t.Errorf("server accepted %d connections, want 1 attempt", accepted)
	}
	if elapsed > 2*time.Second {
		t.Errorf("NewPostgresPool took %v; it should stop at the first attempt", elapsed)
	}

	if want := "connect to postgres: after 1 attempt: unavailable (sqlstate 28P01)"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	var connectErr *pgconn.ConnectError
	var pgErr *pgconn.PgError
	if !errors.As(err, &connectErr) || !errors.As(err, &pgErr) || pgErr.Code != "28P01" {
		t.Error("the driver's ConnectError and PgError are not reachable through Unwrap")
	}
	if got, want := Classify(err), (Classification{Kind: KindUnavailable, SQLState: "28P01"}); got != want {
		t.Errorf("Classify = %+v, want %+v", got, want)
	}

	assertNoDisclosure(t, err, &logs)
}

func TestNewPostgresPoolRetriesRetryableStartupError(t *testing.T) {
	server := newFakePostgres(t, startupFatal(sentinelFatal("57P03")))
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	pool, err := NewPostgresPool(ctx, sentinelConfig(server.port), startupLogger(&logs))
	elapsed := time.Since(start)

	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool")
	}
	// The unchanged backoff waits 0.5s + 1s + 2s + 4s between 5 attempts.
	if elapsed < 7*time.Second {
		t.Errorf("NewPostgresPool returned after %v, want the full retry policy (>= 7s)", elapsed)
	}
	if accepted, _, _ := server.state(); accepted != connectAttempts {
		t.Errorf("server accepted %d connections, want %d attempts", accepted, connectAttempts)
	}

	entries := retryLogs(t, &logs)
	if len(entries) != connectAttempts-1 {
		t.Fatalf("logged %d retries, want %d", len(entries), connectAttempts-1)
	}
	for i, entry := range entries {
		if entry["attempt"] != float64(i+1) || entry["max_attempts"] != float64(connectAttempts) ||
			entry["kind"] != "unavailable" || entry["sqlstate"] != "57P03" {
			t.Errorf("retry log %d = %v", i+1, entry)
		}
		types, _ := entry["error_type"].(string)
		if !strings.Contains(types, "*pgconn.ConnectError") || !strings.Contains(types, "*pgconn.PgError") {
			t.Errorf("retry log %d error_type = %q, want the ConnectError and PgError types", i+1, types)
		}
		if _, ok := entry["error"]; ok {
			t.Errorf("retry log %d has an error field: %v", i+1, entry)
		}
	}

	if want := "connect to postgres: after 5 attempts: unavailable (sqlstate 57P03)"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	assertNoDisclosure(t, err, &logs)
}

// A ping that times out after the connection is established is not
// retryable: the ping may have reached the server.
func TestNewPostgresPoolStopsOnPingTimeoutAfterConnect(t *testing.T) {
	server := newFakePostgres(t, hangAfterStartup)
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	pool, err := NewPostgresPool(ctx, sentinelConfig(server.port), startupLogger(&logs))
	elapsed := time.Since(start)

	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool")
	}
	if got := len(retryLogs(t, &logs)); got != 0 {
		t.Errorf("logged %d retries, want 0", got)
	}
	if elapsed < pingTimeout || elapsed > pingTimeout+2*time.Second {
		t.Errorf("NewPostgresPool took %v, want one %v ping", elapsed, pingTimeout)
	}
	if want := (Classification{Kind: KindTimeout}); Classify(err) != want {
		t.Errorf("Classify = %+v, want %+v", Classify(err), want)
	}
	if want := "connect to postgres: after 1 attempt: timeout"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	assertNoDisclosure(t, err, &logs)
}

// When the ping deadline expires while pgxpool is still establishing the
// connection, Ping returns a bare context error without a
// *pgconn.ConnectError. M6 classifies this timeout as non-retryable,
// so startup stops. This test pins that behavior.
func TestNewPostgresPoolStopsOnConnectPhaseTimeout(t *testing.T) {
	server := newFakePostgres(t, hangBeforeStartup)
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	pool, err := NewPostgresPool(ctx, sentinelConfig(server.port), startupLogger(&logs))
	elapsed := time.Since(start)

	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool")
	}
	if got := len(retryLogs(t, &logs)); got != 0 {
		t.Errorf("logged %d retries, want 0", got)
	}
	if elapsed < pingTimeout || elapsed > pingTimeout+2*time.Second {
		t.Errorf("NewPostgresPool took %v, want one %v ping", elapsed, pingTimeout)
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error chain = %s, want the bare deadline without a ConnectError", errorTypeNames(errors.Unwrap(err)))
	}
	if want := (Classification{Kind: KindTimeout}); Classify(err) != want {
		t.Errorf("Classify = %+v, want %+v", Classify(err), want)
	}
	if want := "connect to postgres: after 1 attempt: timeout"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	assertNoDisclosure(t, err, &logs)
}

func TestNewPostgresPoolDoesNotDiscloseHost(t *testing.T) {
	cfg := sentinelConfig(5432)
	// A reserved name (RFC 6761) that fails to resolve, so the driver's
	// error names the host.
	cfg.Host = "sentinel-host.invalid"

	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := NewPostgresPool(ctx, cfg, startupLogger(&logs))
	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool")
	}

	entries := retryLogs(t, &logs)
	if len(entries) == 0 {
		t.Fatalf("logged no retries for an unresolvable host (error %q)", err)
	}
	if types, _ := entries[0]["error_type"].(string); !strings.Contains(types, "*pgconn.ConnectError") {
		t.Errorf("error_type = %q, want the ConnectError type", types)
	}

	assertNoDisclosure(t, err, &logs)
}

func TestNewPostgresPoolSetupErrorsAreSafe(t *testing.T) {
	invalidMode := sentinelConfig(5432)
	invalidMode.SSLMode = "sentinel_mode"

	noConns := sentinelConfig(5432)
	noConns.MaxConns = 0

	tests := []struct {
		name string
		cfg  config.Database
		want string
	}{
		{"invalid config", invalidMode, "parse database config: unknown"},
		{"pool creation", noConns, "create database pool: unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			pool, err := NewPostgresPool(context.Background(), tt.cfg, startupLogger(&logs))
			if pool != nil {
				pool.Close()
				t.Fatal("NewPostgresPool returned a pool")
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if errors.Unwrap(err) == nil {
				t.Error("the driver error is not reachable through Unwrap")
			}

			assertNoDisclosure(t, err, &logs)
		})
	}

	var parseErr *pgconn.ParseConfigError
	if _, err := NewPostgresPool(context.Background(), invalidMode, slog.New(slog.DiscardHandler)); !errors.As(err, &parseErr) {
		t.Error("the ParseConfigError is not reachable through Unwrap")
	}
}

func TestStartupErrorText(t *testing.T) {
	inner := pgError("40001")
	inner.Message = "sentinel_message"

	tests := []struct {
		err  *startupError
		want string
	}{
		{newStartupError(opConnect, 1, inner), "connect to postgres: after 1 attempt: contention (sqlstate 40001)"},
		{newStartupError(opConnect, 3, inner), "connect to postgres: after 3 attempts: contention (sqlstate 40001)"},
		{newStartupError(opParseConfig, 0, errors.New("sentinel")), "parse database config: unknown"},
		{newStartupError(opConnect, 2, context.Canceled), "connect to postgres: after 2 attempts: canceled"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestStartupErrorPreservesChain(t *testing.T) {
	pgErr := pgError("57P03")
	inner := fmt.Errorf("ping: %w", errors.Join(&pgconn.ConnectError{}, pgErr))
	err := error(newStartupError(opConnect, 2, inner))

	var gotPg *pgconn.PgError
	if !errors.As(err, &gotPg) || gotPg != pgErr {
		t.Error("errors.As does not find the PgError")
	}
	var gotConnect *pgconn.ConnectError
	if !errors.As(err, &gotConnect) {
		t.Error("errors.As does not find the ConnectError")
	}
	if Classify(err) != Classify(inner) {
		t.Errorf("Classify = %+v, want %+v", Classify(err), Classify(inner))
	}
	if !errors.Is(newStartupError(opConnect, 1, context.Canceled), context.Canceled) {
		t.Error("errors.Is does not find context.Canceled")
	}
	if !errors.Is(newStartupError(opConnect, 1, context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Error("errors.Is does not find context.DeadlineExceeded")
	}
}

func TestErrorTypeNames(t *testing.T) {
	deep := error(pgError("40001"))
	for range 10 {
		deep = fmt.Errorf("wrap: %w", deep)
	}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"wrapped", fmt.Errorf("ping: %w", pgError("57P03")), "*fmt.wrapError,*pgconn.PgError"},
		{"joined", errors.Join(&pgconn.ConnectError{}, pgError("57P03")), "*errors.joinError,*pgconn.ConnectError,*pgconn.PgError"},
		{"capped", deep, strings.Repeat("*fmt.wrapError,", maxErrorTypeNames-1) + "*fmt.wrapError"},
		{"never formats", panickingError{pgError("57P03")}, "database.panickingError,*pgconn.PgError"},
		{"cycle", &selfError{}, ""},
		{"typed nil", errors.Join((*pgconn.ConnectError)(nil)), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorTypeNames(tt.err); got != tt.want {
				t.Errorf("errorTypeNames = %q, want %q", got, tt.want)
			}
		})
	}
}
