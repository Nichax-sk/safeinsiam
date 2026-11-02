package health_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nichax-sk/safeinsiam/internal/health"
	"github.com/Nichax-sk/safeinsiam/internal/httpapi"
)

const jsonContentType = "application/json; charset=utf-8"

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

type fakePinger struct {
	err         error
	deadline    time.Time
	hasDeadline bool
}

func (p *fakePinger) Ping(ctx context.Context) error {
	p.deadline, p.hasDeadline = ctx.Deadline()
	return p.err
}

// newRouter wires health routes exactly as app.Run does, so the shared
// middleware is part of every compatibility check.
func newRouter(t *testing.T, db health.Pinger) http.Handler {
	t.Helper()

	router, err := httpapi.NewRouter(httpapi.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	health.Register(router.Engine, db)

	return router.Engine
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec
}

// The expected bodies are the exact responses of the original handlers at
// commit 8464a46; these endpoints must not change.
func TestHealthEndpointsCompatibility(t *testing.T) {
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
			rec := get(t, newRouter(t, &fakePinger{err: tt.pingErr}), tt.path)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != jsonContentType {
				t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
		})
	}
}

func TestHealthDBPingTimeoutIsThreeSeconds(t *testing.T) {
	const timeout = 3 * time.Second
	// Tolerance for clock granularity only; the bounds are otherwise exact.
	const tolerance = 50 * time.Millisecond

	db := &fakePinger{}
	handler := newRouter(t, db)

	before := time.Now()
	get(t, handler, "/health/db")
	after := time.Now()

	if !db.hasDeadline {
		t.Fatal("ping context has no deadline")
	}

	earliest := before.Add(timeout - tolerance)
	latest := after.Add(timeout + tolerance)
	if db.deadline.Before(earliest) || db.deadline.After(latest) {
		t.Errorf("ping deadline is %v after the request started, want %v",
			db.deadline.Sub(before), timeout)
	}
}

func TestHealthTrailingSlashRedirects(t *testing.T) {
	rec := get(t, newRouter(t, &fakePinger{}), "/health/")

	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMovedPermanently)
	}
	if got := rec.Header().Get("Location"); got != "/health" {
		t.Errorf("Location = %q, want /health", got)
	}
}

func TestHealthUnsupportedMethodReturnsJSON405(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newRouter(t, &fakePinger{}).ServeHTTP(
				rec, httptest.NewRequest(method, "/health/db", nil),
			)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != http.MethodGet {
				t.Errorf("Allow = %q, want GET", got)
			}
			if got := rec.Header().Get("Content-Type"); got != jsonContentType {
				t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
			}
		})
	}
}
