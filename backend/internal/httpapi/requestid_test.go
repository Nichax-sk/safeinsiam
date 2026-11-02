package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// echoRouter returns a router whose /echo route writes the request ID from
// the request context as its body, along with the captured access log.
func echoRouter(t *testing.T) (*Router, *strings.Builder) {
	t.Helper()

	var log strings.Builder
	router, err := NewRouter(Options{Logger: slog.New(slog.NewJSONHandler(&log, nil))})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.Engine.GET("/echo", func(c *gin.Context) {
		c.String(http.StatusOK, RequestIDFromContext(c.Request.Context()))
	})

	return router, &log
}

// echo sends a GET /echo with the given X-Request-ID values and returns the
// response header, the context ID seen by the handler, and the access log.
func echo(t *testing.T, inbound ...string) (header, context, log string) {
	t.Helper()

	router, logBuf := echoRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/echo", nil)
	for _, value := range inbound {
		req.Header.Add(RequestIDHeader, value)
	}

	rec := httptest.NewRecorder()
	router.Engine.ServeHTTP(rec, req)

	return rec.Header().Get(RequestIDHeader), rec.Body.String(), logBuf.String()
}

func TestRequestIDKeepsValidInbound(t *testing.T) {
	tests := map[string]string{
		"mixed characters": "abc-123_X.y",
		"64 characters":    strings.Repeat("a", 64),
		"single character": "a",
	}

	for name, inbound := range tests {
		t.Run(name, func(t *testing.T) {
			header, ctxID, log := echo(t, inbound)

			if header != inbound {
				t.Errorf("header = %q, want %q", header, inbound)
			}
			if ctxID != inbound {
				t.Errorf("context ID = %q, want %q", ctxID, inbound)
			}
			if !strings.Contains(log, `"request_id":"`+inbound+`"`) {
				t.Errorf("log does not carry request_id %q: %s", inbound, log)
			}
		})
	}
}

func TestRequestIDReplacesMissingOrInvalidInbound(t *testing.T) {
	tests := map[string][]string{
		"missing":             nil,
		"empty":               {""},
		"65 characters":       {strings.Repeat("a", 65)},
		"space":               {"abc def"},
		"leading space":       {" abc"},
		"trailing space":      {"abc "},
		"slash":               {"abc/def"},
		"plus":                {"abc+def"},
		"equals":              {"abc=def"},
		"non-ASCII":           {"abcé"},
		"CR LF":               {"abc\r\nX-Injected: 1"},
		"multiple headers":    {"first-id", "second-id"},
		"multiple duplicates": {"same-id", "same-id"},
	}

	for name, inbound := range tests {
		t.Run(name, func(t *testing.T) {
			header, ctxID, log := echo(t, inbound...)

			if !requestIDPattern.MatchString(header) {
				t.Fatalf("header = %q, want a valid generated ID", header)
			}
			if len(header) < 26 {
				t.Errorf("generated ID %q is shorter than crypto/rand.Text output", header)
			}
			for _, value := range inbound {
				if value != "" && header == value {
					t.Errorf("header kept the rejected inbound value %q", value)
				}
			}
			if ctxID != header {
				t.Errorf("context ID = %q, want header %q", ctxID, header)
			}
			if !strings.Contains(log, `"request_id":"`+header+`"`) {
				t.Errorf("log does not carry request_id %q: %s", header, log)
			}
			for _, value := range inbound {
				if value != "" && strings.Contains(log, value) {
					t.Errorf("log contains the rejected inbound value %q", value)
				}
			}
		})
	}
}

func TestRequestIDHeaderNameIsCaseInsensitive(t *testing.T) {
	router, _ := echoRouter(t)
	server := httptest.NewServer(router.Engine)
	defer server.Close()

	// Send a raw request so the lower-case header name reaches net/http's
	// parser as written on the wire.
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	raw := "GET /echo HTTP/1.1\r\nHost: test\r\n" +
		"x-request-id: lower-case-ok\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(RequestIDHeader); got != "lower-case-ok" {
		t.Errorf("header = %q, want lower-case-ok", got)
	}
}

func TestRequestIDGeneratesUniqueIDs(t *testing.T) {
	first, _, _ := echo(t)
	second, _, _ := echo(t)

	if first == second {
		t.Errorf("two requests got the same generated ID %q", first)
	}
}

func TestRequestIDFromContextWithoutMiddleware(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("RequestIDFromContext = %q, want empty", got)
	}
}

func TestRequestIDOnUnmatchedAndUnsupportedRoutes(t *testing.T) {
	router, err := NewRouter(Options{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.Engine.GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/missing", nil),
		httptest.NewRequest(http.MethodPost, "/thing", nil),
	} {
		rec := httptest.NewRecorder()
		router.Engine.ServeHTTP(rec, req)

		header := rec.Header().Get(RequestIDHeader)
		if !requestIDPattern.MatchString(header) {
			t.Errorf("%s %s: header = %q, want a valid request ID",
				req.Method, req.URL.Path, header)
		}
		if !strings.Contains(rec.Body.String(), `"request_id":"`+header+`"`) {
			t.Errorf("%s %s: body %s does not carry request_id %q",
				req.Method, req.URL.Path, rec.Body.String(), header)
		}
	}
}
