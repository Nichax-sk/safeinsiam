package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// testRouter returns a router whose access log is captured as JSON lines.
func testRouter(t *testing.T) (*Router, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	router, err := NewRouter(Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	return router, &buf
}

// logEntries parses every captured log line.
func logEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
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

// singleEntry asserts exactly one log entry was written and returns it.
func singleEntry(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	entries := logEntries(t, buf)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want exactly 1: %s", len(entries), buf.String())
	}

	return entries[0]
}

func serve(router *Router, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.Engine.ServeHTTP(rec, req)
	return rec
}

func TestNewRouterRequiresLogger(t *testing.T) {
	if _, err := NewRouter(Options{}); err == nil {
		t.Fatal("NewRouter succeeded without a logger")
	}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	want := `{"error":{"code":"not_found","message":"Resource not found",` +
		`"request_id":"","details":[]}}`

	for _, path := range []string{"/nope", "/api/v1/anything"} {
		t.Run(path, func(t *testing.T) {
			router, buf := testRouter(t)
			rec := serve(router, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != jsonContentType {
				t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
			}
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %s\nwant   %s", got, want)
			}

			entry := singleEntry(t, buf)
			if entry["level"] != "INFO" || entry["status"] != float64(404) {
				t.Errorf("404 log entry = %v, want INFO with status 404", entry)
			}
			if _, ok := entry["error_code"]; ok {
				t.Errorf("404 logged as an internal error: %v", entry)
			}
		})
	}
}

func TestUnsupportedMethodReturnsJSON405(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := serve(router, httptest.NewRequest(http.MethodPost, "/thing", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want GET", got)
	}
	want := `{"error":{"code":"method_not_allowed","message":"Method not allowed",` +
		`"request_id":"","details":[]}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
	if entry := singleEntry(t, buf); entry["level"] != "INFO" {
		t.Errorf("405 log level = %v, want INFO", entry["level"])
	}
}

func TestInternalErrorLoggedOnceWithoutSensitiveData(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.POST("/fail", func(c *gin.Context) {
		WriteError(c, errors.New("connect postgres://app:hunter2@db/x password=hunter2"))
	})

	req := httptest.NewRequest(http.MethodPost, "/fail?token=qsecret",
		strings.NewReader("bodysecret"))
	req.Header.Set("Authorization", "Bearer hdrsecret")
	req.Header.Set("Cookie", "session=cookiesecret")

	rec := serve(router, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	entry := singleEntry(t, buf)
	if entry["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", entry["level"])
	}
	if entry["error_code"] != "internal_error" {
		t.Errorf("error_code = %v, want internal_error", entry["error_code"])
	}
	if entry["error_type"] != "*errors.errorString" {
		t.Errorf("error_type = %v, want *errors.errorString", entry["error_type"])
	}
	if entry["route"] != "/fail" {
		t.Errorf("route = %v, want /fail", entry["route"])
	}

	secrets := []string{
		"hunter2", "postgres://", "qsecret", "token",
		"hdrsecret", "Bearer", "cookiesecret", "bodysecret",
	}
	for _, secret := range secrets {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log exposes %q: %s", secret, buf.String())
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response exposes %q: %s", secret, rec.Body.String())
		}
	}
}

func TestClientErrorIsNotLoggedAsInternal(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.GET("/conflict", func(c *gin.Context) {
		WriteError(c, NewError(CodeConflict, ""))
	})

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/conflict", nil))

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	entry := singleEntry(t, buf)
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	for _, key := range []string{"error_code", "error_type"} {
		if _, ok := entry[key]; ok {
			t.Errorf("4xx entry has %s: %v", key, entry)
		}
	}
}

func TestDirect5xxLoggedAtErrorWithoutErrorFields(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.GET("/down", func(c *gin.Context) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error"})
	})

	serve(router, httptest.NewRequest(http.MethodGet, "/down", nil))

	entry := singleEntry(t, buf)
	if entry["level"] != "ERROR" || entry["status"] != float64(503) {
		t.Errorf("entry = %v, want ERROR with status 503", entry)
	}
	for _, key := range []string{"error_code", "error_type"} {
		if _, ok := entry[key]; ok {
			t.Errorf("direct 503 entry has %s: %v", key, entry)
		}
	}
}

func TestUnknownCodeLoggedAsInternalError(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.GET("/weird", func(c *gin.Context) {
		WriteError(c, &Error{Code: "weird", Message: "leak?"})
	})

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/weird", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	entry := singleEntry(t, buf)
	if entry["level"] != "ERROR" || entry["error_code"] != "internal_error" {
		t.Errorf("entry = %v, want ERROR with error_code internal_error", entry)
	}
	if strings.Contains(rec.Body.String(), "leak") {
		t.Errorf("response exposes the custom message: %s", rec.Body.String())
	}
}

func TestInternalErrorTypeField(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType any // nil: the error_type key must be absent
	}{
		{"nil error", nil, nil},
		{"plain error", errors.New("x"), "*errors.errorString"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, buf := testRouter(t)
			router.Engine.GET("/fail", func(c *gin.Context) { WriteError(c, tt.err) })

			rec := serve(router, httptest.NewRequest(http.MethodGet, "/fail", nil))

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			entry := singleEntry(t, buf)
			if entry["level"] != "ERROR" || entry["error_code"] != "internal_error" {
				t.Errorf("entry = %v, want ERROR with error_code internal_error", entry)
			}

			gotType, present := entry["error_type"]
			if tt.wantType == nil && present {
				t.Errorf("error_type = %v, want the key omitted", gotType)
			}
			if tt.wantType != nil && gotType != tt.wantType {
				t.Errorf("error_type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestWriteErrorAfterResponseStarted(t *testing.T) {
	router, buf := testRouter(t)
	ran := false
	router.Engine.GET("/started",
		func(c *gin.Context) {
			c.String(http.StatusOK, "partial")
			WriteError(c, errors.New("late"))
		},
		func(c *gin.Context) { ran = true },
	)

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/started", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "partial" {
		t.Errorf("body = %q, want %q", got, "partial")
	}
	if ran {
		t.Error("handler after WriteError ran")
	}
	entry := singleEntry(t, buf)
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if _, ok := entry["error_code"]; ok {
		t.Errorf("entry has error_code: %v", entry)
	}
}

func TestPanicLoggedOnceAtError(t *testing.T) {
	// gin.Recovery captures DefaultErrorWriter when the router is built;
	// discard its output, which this test does not inspect.
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = io.Discard
	t.Cleanup(func() { gin.DefaultErrorWriter = previous })

	router, buf := testRouter(t)
	router.Engine.GET("/panic", func(c *gin.Context) { panic("boom") })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	entry := singleEntry(t, buf)
	if entry["level"] != "ERROR" || entry["status"] != float64(500) {
		t.Errorf("entry = %v, want ERROR with status 500", entry)
	}
}

func TestRequestLoggerOmitsQueryString(t *testing.T) {
	router, buf := testRouter(t)
	router.Engine.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	serve(router, httptest.NewRequest(http.MethodGet, "/health?token=secret-value", nil))

	entry := singleEntry(t, buf)
	if entry["msg"] != "http request" || entry["method"] != "GET" ||
		entry["route"] != "/health" || entry["status"] != float64(200) {
		t.Errorf("unexpected entry: %v", entry)
	}
	if strings.Contains(buf.String(), "secret-value") || strings.Contains(buf.String(), "token") {
		t.Errorf("log exposes the query string: %s", buf.String())
	}
}

func TestRequestLoggerUnmatchedRoute(t *testing.T) {
	router, buf := testRouter(t)

	serve(router, httptest.NewRequest(http.MethodGet, "/missing?q=1", nil))

	entry := singleEntry(t, buf)
	if entry["route"] != "/missing" || entry["status"] != float64(404) {
		t.Errorf("unexpected entry for unmatched route: %v", entry)
	}
}
