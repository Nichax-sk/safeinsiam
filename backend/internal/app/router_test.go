package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

type fakePinger struct {
	err      error
	deadline time.Duration
}

func (p *fakePinger) Ping(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		p.deadline = time.Until(deadline)
	}
	return p.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func doRequest(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec
}

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "liveness",
			path:       "/health",
			wantStatus: http.StatusOK,
			wantBody:   `{"message":"SafeInSiam API is running","status":"ok"}`,
		},
		{
			name:       "database healthy",
			path:       "/health/db",
			wantStatus: http.StatusOK,
			wantBody:   `{"message":"Database connection is healthy","status":"ok"}`,
		},
		{
			name:       "database unavailable",
			path:       "/health/db",
			pingErr:    errors.New("connection refused"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"message":"Database connection unavailable","status":"error"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, err := newRouter(discardLogger(), &fakePinger{err: tt.pingErr})
			if err != nil {
				t.Fatalf("newRouter: %v", err)
			}

			rec := doRequest(t, router, tt.path)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
		})
	}
}

func TestHealthDBPingHasTimeout(t *testing.T) {
	db := &fakePinger{}
	router, err := newRouter(discardLogger(), db)
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}

	doRequest(t, router, "/health/db")

	if db.deadline <= 0 || db.deadline > dbPingTimeout {
		t.Errorf("ping deadline = %v, want within %v", db.deadline, dbPingTimeout)
	}
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

func TestRequestLoggerOmitsQueryString(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	router, err := newRouter(logger, &fakePinger{})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}

	doRequest(t, router, "/health?token=secret-value")

	out := buf.String()
	for _, want := range []string{
		`msg="http request"`, "method=GET", "route=/health", "status=200",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
	if strings.Contains(out, "secret-value") || strings.Contains(out, "token") {
		t.Errorf("log exposes the query string: %q", out)
	}
}

func TestRequestLoggerUnmatchedRoute(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	router, err := newRouter(logger, &fakePinger{})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}

	doRequest(t, router, "/missing?q=1")

	out := buf.String()
	if !strings.Contains(out, "route=/missing") ||
		!strings.Contains(out, "status=404") {
		t.Errorf("unexpected log for unmatched route: %q", out)
	}
}
