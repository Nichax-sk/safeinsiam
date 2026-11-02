package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

const internalErrorEnvelope = `{"error":{"code":"internal_error",` +
	`"message":"Internal server error","request_id":"%s","details":[]}}`

// panicRouter returns a router whose GET /panic route runs handler, plus the
// captured JSON log.
func panicRouter(t *testing.T, handler gin.HandlerFunc) (*Router, *bytes.Buffer) {
	t.Helper()

	router, buf := testRouter(t)
	router.Engine.GET("/panic", handler)

	return router, buf
}

// panicLogs splits captured entries into panic and access entries, failing
// unless there is exactly one of each.
func panicLogs(t *testing.T, buf *bytes.Buffer) (panicEntry, accessEntry map[string]any) {
	t.Helper()

	entries := logEntries(t, buf)
	if len(entries) != 2 {
		t.Fatalf("got %d log entries, want 1 panic + 1 access: %s", len(entries), buf.String())
	}
	for _, entry := range entries {
		switch entry["msg"] {
		case "panic recovered":
			panicEntry = entry
		case "http request":
			accessEntry = entry
		}
	}
	if panicEntry == nil || accessEntry == nil {
		t.Fatalf("want one panic and one access entry, got: %s", buf.String())
	}

	return panicEntry, accessEntry
}

// assertSafe500 checks the response is the standard internal error envelope
// and that both log entries carry the response's request ID.
func assertSafe500(t *testing.T, rec *httptest.ResponseRecorder, buf *bytes.Buffer) (panicEntry map[string]any) {
	t.Helper()

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}

	id := rec.Header().Get(RequestIDHeader)
	if !requestIDPattern.MatchString(id) {
		t.Errorf("%s = %q, want a valid request ID", RequestIDHeader, id)
	}
	if want := fmt.Sprintf(internalErrorEnvelope, id); rec.Body.String() != want {
		t.Errorf("body = %s\nwant   %s", rec.Body.String(), want)
	}

	panicEntry, accessEntry := panicLogs(t, buf)
	if panicEntry["level"] != "ERROR" || panicEntry["request_id"] != id {
		t.Errorf("panic entry level=%v request_id=%v, want ERROR with %q",
			panicEntry["level"], panicEntry["request_id"], id)
	}
	if accessEntry["level"] != "ERROR" || accessEntry["status"] != float64(500) ||
		accessEntry["request_id"] != id || accessEntry["error_code"] != "internal_error" ||
		accessEntry["error_type"] != "httpapi.panicError" {
		t.Errorf("access entry = %v, want ERROR 500 internal_error httpapi.panicError %q",
			accessEntry, id)
	}

	return panicEntry
}

// assertNoLeak fails if any secret appears in the log or the response.
func assertNoLeak(t *testing.T, rec *httptest.ResponseRecorder, buf *bytes.Buffer, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log exposes %q", secret)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response exposes %q", secret)
		}
	}
}

func TestRecoveryCorrelatesPanicAndAccessLogs(t *testing.T) {
	router, buf := panicRouter(t, func(c *gin.Context) { panic("boom") })

	rec := serve(router, requestWithID(http.MethodGet, "/panic"))

	if got := rec.Header().Get(RequestIDHeader); got != testRequestID {
		t.Errorf("%s = %q, want %q", RequestIDHeader, got, testRequestID)
	}
	panicEntry := assertSafe500(t, rec, buf)

	want := map[string]any{
		"request_id": testRequestID,
		"method":     http.MethodGet,
		"route":      "/panic",
		"panic_type": "string",
	}
	for key, value := range want {
		if panicEntry[key] != value {
			t.Errorf("panic entry %s = %v, want %v", key, panicEntry[key], value)
		}
	}
	if stack, _ := panicEntry["stack"].(string); stack == "" {
		t.Error("panic entry has no stack")
	}
	if _, ok := panicEntry["panic_message"]; ok {
		t.Errorf("panic entry has panic_message: %v", panicEntry)
	}
	assertNoLeak(t, rec, buf, "boom")
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Error("response exposes a stack trace")
	}
}

func TestRecoveryGeneratedRequestID(t *testing.T) {
	router, buf := panicRouter(t, func(c *gin.Context) { panic("boom") })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assertSafe500(t, rec, buf) // checks header == body == both log entries
}

// secretRuntimeError implements runtime.Error; recovery must never call Error.
type secretRuntimeError struct{ called *atomic.Bool }

func (secretRuntimeError) RuntimeError() {}
func (e secretRuntimeError) Error() string {
	e.called.Store(true)
	return "password=hunter2-rt"
}

func TestRecoveryNeverCallsErrorOnCustomRuntimeError(t *testing.T) {
	called := new(atomic.Bool)
	router, buf := panicRouter(t, func(c *gin.Context) {
		panic(secretRuntimeError{called: called})
	})

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	panicEntry := assertSafe500(t, rec, buf)
	if panicEntry["panic_type"] != "httpapi.secretRuntimeError" {
		t.Errorf("panic_type = %v, want httpapi.secretRuntimeError", panicEntry["panic_type"])
	}
	if called.Load() {
		t.Error("Error() was called on the recovered value")
	}
	assertNoLeak(t, rec, buf, "hunter2-rt", "password=")
}

// secretStringer records calls to String; recovery must never call it.
type secretStringer struct{ called *atomic.Bool }

func (s secretStringer) String() string {
	s.called.Store(true)
	return "stringer-secret"
}

func TestRecoveryHidesSecretPanicValuesAndRequestData(t *testing.T) {
	stringerCalled := new(atomic.Bool)

	tests := map[string]any{
		"string":        "token=s3cr3t",
		"error":         errors.New("password=hunter2"),
		"wrapped error": fmt.Errorf("dsn postgres://u:hunter2@h: %w", errors.New("inner")),
		"fmt.Stringer":  secretStringer{called: stringerCalled},
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			router, buf := panicRouter(t, func(c *gin.Context) { panic(value) })

			req := httptest.NewRequest(http.MethodGet, "/panic?q=qsecret",
				strings.NewReader("bodysecret"))
			req.Header.Set("Authorization", "Bearer hdrsecret")
			req.Header.Set("Cookie", "s=cookiesecret")

			rec := serve(router, req)

			assertSafe500(t, rec, buf)
			assertNoLeak(t, rec, buf,
				"s3cr3t", "hunter2", "postgres://", "stringer-secret",
				"qsecret", "hdrsecret", "Bearer", "cookiesecret", "bodysecret")
			if strings.Contains(rec.Body.String(), "goroutine") {
				t.Error("response exposes a stack trace")
			}
		})
	}

	if stringerCalled.Load() {
		t.Error("String() was called on the recovered value")
	}
}

func TestRecoveryRuntimeErrorsLogTypeOnly(t *testing.T) {
	tests := []struct {
		name     string
		trigger  func()
		wantType string
	}{
		{"index out of range", func() {
			var values []int
			index := len(values) + 1
			_ = values[index]
		}, "runtime.boundsError"},
		{"nil map write", func() {
			var values map[string]int
			values["key"] = 1
		}, "runtime.plainError"},
		{"panic(nil)", func() { panic(nil) }, "*runtime.PanicNilError"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, buf := panicRouter(t, func(c *gin.Context) { tt.trigger() })

			rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

			panicEntry := assertSafe500(t, rec, buf)
			if panicEntry["panic_type"] != tt.wantType {
				t.Errorf("panic_type = %v, want %s", panicEntry["panic_type"], tt.wantType)
			}
			if _, ok := panicEntry["panic_message"]; ok {
				t.Errorf("panic entry has panic_message: %v", panicEntry)
			}
			assertNoLeak(t, rec, buf, "runtime error", "index out of range",
				"assignment to entry in nil map", "panic called with nil")
		})
	}
}

// panickingUnwrapError panics when unwrapped.
type panickingUnwrapError struct{}

func (panickingUnwrapError) Error() string { return "unwrap-secret" }
func (panickingUnwrapError) Unwrap() error { panic("Unwrap() exploded: unwrap-secret") }

func TestRecoveryContainsPanickingUnwrap(t *testing.T) {
	if isAbortHandler(panickingUnwrapError{}) {
		t.Error("isAbortHandler = true for a panicking Unwrap")
	}

	router, buf := panicRouter(t, func(c *gin.Context) { panic(panickingUnwrapError{}) })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	panicEntry := assertSafe500(t, rec, buf)
	if panicEntry["panic_type"] != "httpapi.panickingUnwrapError" {
		t.Errorf("panic_type = %v, want httpapi.panickingUnwrapError", panicEntry["panic_type"])
	}
	assertNoLeak(t, rec, buf, "unwrap-secret", "Unwrap() exploded")
}

// panickingIsError panics in Is and records calls to Error.
type panickingIsError struct{ errorCalled *atomic.Bool }

func (e panickingIsError) Error() string {
	e.errorCalled.Store(true)
	return "secret=is-hunter2"
}
func (panickingIsError) Is(error) bool { panic("Is() exploded: is-hunter2") }

func TestRecoveryContainsPanickingIs(t *testing.T) {
	errorCalled := new(atomic.Bool)
	value := panickingIsError{errorCalled: errorCalled}

	if isAbortHandler(value) {
		t.Error("isAbortHandler = true for a panicking Is")
	}

	router, buf := panicRouter(t, func(c *gin.Context) { panic(value) })

	// A panic escaping recovery would crash the test here.
	rec := serve(router, requestWithID(http.MethodGet, "/panic"))

	panicEntry := assertSafe500(t, rec, buf)
	if panicEntry["panic_type"] != "httpapi.panickingIsError" {
		t.Errorf("panic_type = %v, want httpapi.panickingIsError", panicEntry["panic_type"])
	}
	if panicEntry["request_id"] != testRequestID {
		t.Errorf("panic entry request_id = %v, want %q", panicEntry["request_id"], testRequestID)
	}
	if errorCalled.Load() {
		t.Error("Error() was called on the recovered value")
	}
	assertNoLeak(t, rec, buf, "is-hunter2", "secret=", "Is() exploded")
}

// customErr dereferences its receiver, so its methods panic on a typed nil.
type customErr struct{ msg string }

func (e *customErr) Error() string { return e.msg }
func (e *customErr) Unwrap() error { return errors.New(e.msg) }

func TestRecoveryTypedNilError(t *testing.T) {
	var typedNil *customErr
	if isAbortHandler(typedNil) {
		t.Error("isAbortHandler = true for a typed-nil error")
	}

	router, buf := panicRouter(t, func(c *gin.Context) { panic(typedNil) })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	panicEntry := assertSafe500(t, rec, buf)
	if panicEntry["panic_type"] != "*httpapi.customErr" {
		t.Errorf("panic_type = %v, want *httpapi.customErr", panicEntry["panic_type"])
	}
}

func TestRecoveryAfterResponseStarted(t *testing.T) {
	router, buf := panicRouter(t, func(c *gin.Context) {
		c.String(http.StatusOK, "partial")
		panic("late")
	})

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "partial" {
		t.Errorf("body = %q, want %q", got, "partial")
	}

	panicEntry, accessEntry := panicLogs(t, buf)
	if panicEntry["level"] != "ERROR" {
		t.Errorf("panic entry level = %v, want ERROR", panicEntry["level"])
	}
	if accessEntry["level"] != "INFO" || accessEntry["status"] != float64(200) {
		t.Errorf("access entry = %v, want INFO with status 200", accessEntry)
	}
	if _, ok := accessEntry["error_code"]; ok {
		t.Errorf("access entry has error_code: %v", accessEntry)
	}
}

var abortHandlerValues = map[string]error{
	"bare":    http.ErrAbortHandler,
	"wrapped": fmt.Errorf("x: %w", http.ErrAbortHandler),
}

func TestRecoveryRepanicsBareAbortHandler(t *testing.T) {
	for name, value := range abortHandlerValues {
		t.Run(name, func(t *testing.T) {
			router, buf := panicRouter(t, func(c *gin.Context) { panic(value) })

			var repanicked any
			func() {
				defer func() { repanicked = recover() }()
				serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))
			}()

			if repanicked != http.ErrAbortHandler {
				t.Errorf("re-panicked %T, want exactly http.ErrAbortHandler", repanicked)
			}
			if buf.Len() != 0 {
				t.Errorf("got log output, want none: %s", buf.String())
			}
		})
	}
}

func TestRecoveryAbortHandlerThroughServer(t *testing.T) {
	for name, value := range abortHandlerValues {
		t.Run(name, func(t *testing.T) {
			router, buf := panicRouter(t, func(c *gin.Context) { panic(value) })

			var serverLog bytes.Buffer
			server := httptest.NewUnstartedServer(router.Engine)
			server.Config.ErrorLog = log.New(&serverLog, "", 0)
			server.Start()

			resp, err := http.Get(server.URL + "/panic")
			if err == nil {
				resp.Body.Close()
				t.Errorf("got status %d, want the connection aborted", resp.StatusCode)
			}
			server.Close()

			if serverLog.Len() != 0 {
				t.Errorf("net/http ErrorLog = %q, want empty", serverLog.String())
			}
			if buf.Len() != 0 {
				t.Errorf("got log output, want none: %s", buf.String())
			}
		})
	}
}

func TestRecoveryDoesNotUseGinErrorWriter(t *testing.T) {
	var ginOut bytes.Buffer
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &ginOut
	t.Cleanup(func() { gin.DefaultErrorWriter = previous })

	router, buf := panicRouter(t, func(c *gin.Context) { panic("boom") })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assertSafe500(t, rec, buf)
	if ginOut.Len() != 0 {
		t.Errorf("gin.DefaultErrorWriter received %q, want nothing", ginOut.String())
	}
}

func TestRecoveryCapsStack(t *testing.T) {
	var recurse func(int)
	recurse = func(depth int) {
		if depth == 0 {
			panic("deep")
		}
		recurse(depth - 1)
	}
	router, buf := panicRouter(t, func(c *gin.Context) { recurse(2000) })

	rec := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))

	panicEntry := assertSafe500(t, rec, buf)
	stack, _ := panicEntry["stack"].(string)
	if stack == "" || len(stack) > maxStackBytes {
		t.Errorf("len(stack) = %d, want 1..%d", len(stack), maxStackBytes)
	}
}
