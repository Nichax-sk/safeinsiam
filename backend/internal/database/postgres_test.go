package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestRetrySucceedsAfterFailures(t *testing.T) {
	calls, retries := 0, 0

	err := retry(context.Background(), 5, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not ready")
			}
			return nil
		},
		func(int, time.Duration, error) { retries++ },
	)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if calls != 3 || retries != 2 {
		t.Errorf("calls = %d, retries = %d, want 3 and 2", calls, retries)
	}
}

func TestRetryReturnsLastErrorAfterMaxAttempts(t *testing.T) {
	lastErr := errors.New("still down")
	calls := 0

	err := retry(context.Background(), 4, time.Microsecond, time.Millisecond,
		func(context.Context) error {
			calls++
			return lastErr
		},
		func(int, time.Duration, error) {},
	)
	if !errors.Is(err, lastErr) {
		t.Fatalf("retry error = %v, want %v", err, lastErr)
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
	if !strings.Contains(err.Error(), "after 4 attempts") {
		t.Errorf("error %q does not report the attempt count", err)
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	err := retry(ctx, 5, time.Hour, time.Hour,
		func(context.Context) error { return errors.New("not ready") },
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

	_ = retry(context.Background(), 6, time.Microsecond, 4*time.Microsecond,
		func(context.Context) error { return errors.New("not ready") },
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
