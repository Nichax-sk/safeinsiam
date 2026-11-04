//go:build integration

package health_test

// Integration tests for the health endpoints with real pgxpool pools. The
// healthy pool uses the TEST_DB_* variables. Run with:
//
//	go test -tags=integration ./...

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nichax-sk/safeinsiam/internal/config"
	"github.com/Nichax-sk/safeinsiam/internal/database"
	"github.com/Nichax-sk/safeinsiam/internal/health"
	"github.com/Nichax-sk/safeinsiam/internal/httpapi"
)

// unavailablePassword is the dummy password of the pool that can't connect.
const unavailablePassword = "unavailable_password"

var requiredTestDBVars = []string{
	"TEST_DB_HOST", "TEST_DB_PORT", "TEST_DB_NAME", "TEST_DB_USER", "TEST_DB_PASSWORD",
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

var wantSecurityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"X-Frame-Options":         "DENY",
}

// integrationConfig returns the test database configuration from the
// TEST_DB_* variables. It fails the test, naming the variables but never
// their values, when one is missing or invalid.
func integrationConfig(t *testing.T) config.Database {
	t.Helper()

	var missing []string
	for _, name := range requiredTestDBVars {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("integration tests need %s", strings.Join(missing, ", "))
	}

	port, err := strconv.Atoi(os.Getenv("TEST_DB_PORT"))
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("TEST_DB_PORT must be a port number (1-65535)")
	}

	sslMode := os.Getenv("TEST_DB_SSLMODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	return config.Database{
		Host:     os.Getenv("TEST_DB_HOST"),
		Port:     port,
		Name:     os.Getenv("TEST_DB_NAME"),
		User:     os.Getenv("TEST_DB_USER"),
		Password: os.Getenv("TEST_DB_PASSWORD"),
		SSLMode:  sslMode,
		MaxConns: 2,
		MinConns: 2,
	}
}

// integrationRouter wires the health routes as app.newHandler does, with
// the access log captured as JSON at Debug level.
func integrationRouter(t *testing.T, db health.Pinger) (http.Handler, *bytes.Buffer) {
	t.Helper()

	var logs bytes.Buffer
	router, err := httpapi.NewRouter(httpapi.Options{
		Logger:      slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		DebugRoutes: health.Routes,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	health.Register(router.Engine, db)

	return router.Engine, &logs
}

// healthyPool returns a pool from database.NewPostgresPool on the test
// database.
func healthyPool(t *testing.T) (*pgxpool.Pool, config.Database) {
	t.Helper()

	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := database.NewPostgresPool(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewPostgresPool failed (%T)", err)
	}
	t.Cleanup(pool.Close)

	return pool, cfg
}

// unavailablePool returns a lazy pool for a refused local port. With
// MinConns 0 it never connects until pinged, and every ping fails.
func unavailablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	connURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("unavailable_user", unavailablePassword),
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     "/unavailable_db",
		RawQuery: url.Values{"sslmode": {"disable"}}.Encode(),
	}
	cfg, err := pgxpool.ParseConfig(connURL.String())
	if err != nil {
		t.Fatalf("ParseConfig failed (%T)", err)
	}
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig failed (%T)", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// assertResponse checks the status, exact body and content type, the request
// ID, the security headers, the single access log entry, and that secret
// appears in neither the body nor the log.
func assertResponse(t *testing.T, rec *httptest.ResponseRecorder, logs *bytes.Buffer,
	path string, wantStatus int, wantBody, wantLevel, secret string,
) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Errorf("status = %d, want %d", rec.Code, wantStatus)
	}
	if got := rec.Body.String(); got != wantBody {
		t.Errorf("body = %s, want %s", got, wantBody)
	}
	if got := rec.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}

	requestID := rec.Header().Values(httpapi.RequestIDHeader)
	if len(requestID) != 1 || !requestIDPattern.MatchString(requestID[0]) {
		t.Errorf("%s = %q, want one valid request ID", httpapi.RequestIDHeader, requestID)
	}

	for name, want := range wantSecurityHeaders {
		if got := rec.Header().Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want exactly [%q]", name, got, want)
		}
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log entries, want exactly 1", len(lines))
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal("access log entry is not JSON")
	}
	if len(requestID) == 1 && entry["request_id"] != requestID[0] {
		t.Errorf("log request_id = %v, want the response header", entry["request_id"])
	}
	if entry["route"] != path || entry["status"] != float64(wantStatus) || entry["level"] != wantLevel {
		t.Errorf("log route, status, level = %v, %v, %v; want %s, %d, %s",
			entry["route"], entry["status"], entry["level"], path, wantStatus, wantLevel)
	}
	if _, ok := entry["error_code"]; ok {
		t.Errorf("log has error_code %v; the health handler doesn't use WriteError", entry["error_code"])
	}

	if strings.Contains(rec.Body.String(), secret) || strings.Contains(logs.String(), secret) {
		t.Error("the response or the log contains the database password")
	}
}

func TestIntegrationHealthDBHealthy(t *testing.T) {
	pool, cfg := healthyPool(t)
	handler, logs := integrationRouter(t, pool)

	rec := get(t, handler, health.PathDB)

	assertResponse(t, rec, logs, health.PathDB, http.StatusOK,
		`{"message":"Database connection is healthy","status":"ok"}`, "DEBUG", cfg.Password)
}

func TestIntegrationHealthDBUnavailable(t *testing.T) {
	handler, logs := integrationRouter(t, unavailablePool(t))

	start := time.Now()
	rec := get(t, handler, health.PathDB)
	// The ping timeout is 3s; a refused connection fails well before it.
	if elapsed := time.Since(start); elapsed > 3*time.Second+500*time.Millisecond {
		t.Errorf("/health/db took %v, want at most the 3s ping timeout", elapsed)
	}

	assertResponse(t, rec, logs, health.PathDB, http.StatusServiceUnavailable,
		`{"message":"Database connection unavailable","status":"error"}`, "ERROR", unavailablePassword)
}

func TestIntegrationHealthLiveIndependentOfDB(t *testing.T) {
	handler, logs := integrationRouter(t, unavailablePool(t))

	rec := get(t, handler, health.PathLive)

	assertResponse(t, rec, logs, health.PathLive, http.StatusOK,
		`{"message":"SafeInSiam API is running","status":"ok"}`, "DEBUG", unavailablePassword)
}
