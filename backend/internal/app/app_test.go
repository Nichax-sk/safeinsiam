package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nichax-sk/safeinsiam/internal/config"
	"github.com/Nichax-sk/safeinsiam/internal/health"
	"github.com/Nichax-sk/safeinsiam/internal/httpapi"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGinMode(t *testing.T) {
	tests := map[config.Env]string{
		config.EnvDevelopment: gin.DebugMode,
		config.EnvTest:        gin.TestMode,
		config.EnvStaging:     gin.ReleaseMode,
		config.EnvProduction:  gin.ReleaseMode,
	}

	for env, want := range tests {
		if got := ginMode(env); got != want {
			t.Errorf("ginMode(%s) = %s, want %s", env, got, want)
		}
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	return listener
}

// startServe runs serve in the background and returns its result channel.
func startServe(
	ctx context.Context,
	handler http.Handler,
	listener net.Listener,
	shutdownTimeout time.Duration,
) <-chan error {
	done := make(chan error, 1)
	server := &http.Server{Handler: handler}

	go func() {
		done <- serve(ctx, server, listener, shutdownTimeout, discardLogger())
	}()

	return done
}

func waitResult(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
		return nil
	}
}

func TestServeStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listener := listenLocal(t)
	done := startServe(ctx, http.NotFoundHandler(), listener, time.Second)

	resp, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("request before shutdown: %v", err)
	}
	resp.Body.Close()

	cancel()

	if err := waitResult(t, done); err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

func TestServeDrainsInFlightRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	listener := listenLocal(t)
	done := startServe(ctx, handler, listener, 5*time.Second)

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	<-started
	cancel()

	select {
	case resp := <-respCh:
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("in-flight status = %d, want 200", resp.StatusCode)
		}
	case err := <-errCh:
		t.Fatalf("in-flight request failed: %v", err)
	}

	if err := waitResult(t, done); err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

func TestServeShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})

	listener := listenLocal(t)
	done := startServe(ctx, handler, listener, 50*time.Millisecond)

	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	cancel()

	err := waitResult(t, done)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("serve returned %v, want context.DeadlineExceeded", err)
	}
}

func TestRunReturnsErrorWhenContextCancelledBeforeDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &config.Config{
		Env: config.EnvTest,
		HTTP: config.HTTP{
			Host:            "127.0.0.1",
			Port:            1,
			ShutdownTimeout: time.Second,
		},
		Database: config.Database{
			Host:     "127.0.0.1",
			Port:     1,
			Name:     "app_db",
			User:     "app_user",
			Password: "unused",
			SSLMode:  "disable",
			MaxConns: 1,
		},
	}

	err := Run(ctx, cfg, discardLogger())
	if err == nil {
		t.Fatal("Run succeeded with a cancelled context")
	}
}

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

// handlerWithLog builds the real application handler with a Debug-level JSON
// access log captured in the returned buffer.
func handlerWithLog(t *testing.T, db health.Pinger) (http.Handler, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler, err := newHandler(logger, config.HTTP{}, db)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	return handler, &buf
}

// logLines parses the captured JSON log lines.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
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
		entries = append(entries, entry)
	}

	return entries
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func TestHandlerRequestIDAndLogLevels(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		inboundID  string
		pingErr    error
		wantStatus int
		wantLevel  string
	}{
		{"health generated ID", health.PathLive, "", nil, http.StatusOK, "DEBUG"},
		{"health inbound ID", health.PathLive, "health-check-1", nil, http.StatusOK, "DEBUG"},
		{"db healthy", health.PathDB, "", nil, http.StatusOK, "DEBUG"},
		{"db unavailable", health.PathDB, "", errors.New("down"), http.StatusServiceUnavailable, "ERROR"},
		{"unknown API route", "/api/v1/x", "", nil, http.StatusNotFound, "WARN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, buf := handlerWithLog(t, fakePinger{err: tt.pingErr})
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.inboundID != "" {
				req.Header.Set(httpapi.RequestIDHeader, tt.inboundID)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			header := rec.Header().Get(httpapi.RequestIDHeader)
			if !validRequestID.MatchString(header) {
				t.Fatalf("%s = %q, want a valid request ID", httpapi.RequestIDHeader, header)
			}
			if tt.inboundID != "" && header != tt.inboundID {
				t.Errorf("%s = %q, want inbound %q", httpapi.RequestIDHeader, header, tt.inboundID)
			}

			entries := logLines(t, buf)
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1: %s", len(entries), buf.String())
			}
			entry := entries[0]
			if entry["level"] != tt.wantLevel || entry["status"] != float64(tt.wantStatus) {
				t.Errorf("entry = %v, want level %s with status %d",
					entry, tt.wantLevel, tt.wantStatus)
			}
			if entry["request_id"] != header {
				t.Errorf("log request_id = %v, want header %q", entry["request_id"], header)
			}
			if tt.wantStatus == http.StatusNotFound &&
				!strings.Contains(rec.Body.String(), `"request_id":"`+header+`"`) {
				t.Errorf("body %s does not carry request_id %q", rec.Body.String(), header)
			}
		})
	}
}

// Trailing-slash redirects are written by Gin before any middleware runs, so
// they carry no request ID and produce no access log entry (unchanged in M2).
func TestHandlerTrailingSlashRedirectBypassesMiddleware(t *testing.T) {
	handler, buf := handlerWithLog(t, fakePinger{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, health.PathLive+"/", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != health.PathLive {
		t.Errorf("Location = %q, want %q", got, health.PathLive)
	}
	if got := rec.Header().Get(httpapi.RequestIDHeader); got != "" {
		t.Errorf("%s = %q, want none", httpapi.RequestIDHeader, got)
	}
	if entries := logLines(t, buf); len(entries) != 0 {
		t.Errorf("got %d log entries, want 0", len(entries))
	}
}

func TestNewHandlerPropagatesRouterError(t *testing.T) {
	_, routerErr := httpapi.NewRouter(httpapi.Options{})
	if routerErr == nil {
		t.Fatal("NewRouter accepted a nil logger")
	}

	handler, err := newHandler(nil, config.HTTP{}, fakePinger{})
	if handler != nil {
		t.Error("newHandler returned a handler alongside an error")
	}
	if err == nil || err.Error() != routerErr.Error() {
		t.Errorf("newHandler error = %v, want NewRouter's %v", err, routerErr)
	}
}

var wantSecurityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"X-Frame-Options":         "DENY",
}

func TestHandlerHealthSecurityHeaders(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		pingErr    error
		wantStatus int
	}{
		{"health", health.PathLive, nil, http.StatusOK},
		{"db unavailable", health.PathDB, errors.New("down"), http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := handlerWithLog(t, fakePinger{err: tt.pingErr})

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			for name, want := range wantSecurityHeaders {
				if values := rec.Header().Values(name); len(values) != 1 || values[0] != want {
					t.Errorf("%s = %q, want exactly [%q]", name, values, want)
				}
			}
		})
	}
}

// Trailing-slash redirects bypass the middleware chain, so they carry no
// security headers (documented gap).
func TestHandlerTrailingSlashRedirectHasNoSecurityHeaders(t *testing.T) {
	handler, _ := handlerWithLog(t, fakePinger{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, health.PathLive+"/", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", rec.Code)
	}
	for name := range wantSecurityHeaders {
		if values := rec.Header().Values(name); len(values) != 0 {
			t.Errorf("%s = %q, want absent on the redirect", name, values)
		}
	}
}

func TestHandlerAppliesCORSConfig(t *testing.T) {
	const origin = "http://localhost:3000"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	handler, err := newHandler(logger, config.HTTP{CORSAllowedOrigins: []string{origin}}, fakePinger{})
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, health.PathLive, nil)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, origin)
	}
}

func TestHandlerAppliesTrustedProxies(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler, err := newHandler(logger, config.HTTP{TrustedProxies: []string{"10.0.0.0/8"}}, fakePinger{})
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, health.PathLive, nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	entries := logLines(t, &buf)
	if len(entries) != 1 || entries[0]["client_ip"] != "203.0.113.7" {
		t.Errorf("log entries = %v, want one with client_ip 203.0.113.7", entries)
	}
}
