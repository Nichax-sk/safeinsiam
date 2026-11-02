package httpapi

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

var wantSecurityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"X-Frame-Options":         "DENY",
}

// assertSecurityHeaders checks each global security header appears exactly
// once with its approved value, and that unapproved headers are absent.
func assertSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()

	for name, want := range wantSecurityHeaders {
		values := header.Values(name)
		if len(values) != 1 || values[0] != want {
			t.Errorf("%s = %q, want exactly [%q]", name, values, want)
		}
	}
	for _, name := range []string{"Strict-Transport-Security", "Referrer-Policy", "Permissions-Policy"} {
		if values := header.Values(name); len(values) != 0 {
			t.Errorf("%s = %q, want absent", name, values)
		}
	}
}

func TestSecurityHeadersOnEveryResponsePath(t *testing.T) {
	router, _ := testRouter(t)
	router.V1.GET("/thing", func(c *gin.Context) {
		WriteJSON(c, http.StatusOK, gin.H{"ok": true})
	})
	router.Engine.GET("/conflict", func(c *gin.Context) {
		WriteError(c, NewError(CodeConflict, ""))
	})
	router.Engine.GET("/empty", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.Engine.GET("/panic", func(c *gin.Context) { panic("boom") })
	router.Engine.GET("/started", func(c *gin.Context) {
		c.String(http.StatusOK, "partial")
		panic("late")
	})

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"normal API response", http.MethodGet, "/api/v1/thing", http.StatusOK},
		{"client error", http.MethodGet, "/conflict", http.StatusConflict},
		{"empty body", http.MethodGet, "/empty", http.StatusNoContent},
		{"unmatched route", http.MethodGet, "/missing", http.StatusNotFound},
		{"unmatched API route", http.MethodGet, "/api/v1/missing", http.StatusNotFound},
		{"unsupported method", http.MethodPost, "/conflict", http.StatusMethodNotAllowed},
		{"HEAD on GET-only route", http.MethodHead, "/conflict", http.StatusMethodNotAllowed},
		{"recovered panic", http.MethodGet, "/panic", http.StatusInternalServerError},
		{"panic after response started", http.MethodGet, "/started", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(router, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			assertSecurityHeaders(t, rec.Header())
		})
	}
}

func TestSecurityHeadersKeepExistingResponses(t *testing.T) {
	router, _ := testRouter(t)
	router.Engine.GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.Engine.GET("/panic", func(c *gin.Context) { panic("boom") })

	notFound := serve(router, requestWithID(http.MethodGet, "/missing"))
	want := `{"error":{"code":"not_found","message":"Resource not found",` +
		`"request_id":"test-req-1","details":[]}}`
	if got := notFound.Body.String(); got != want {
		t.Errorf("404 body = %s\nwant       %s", got, want)
	}

	notAllowed := serve(router, httptest.NewRequest(http.MethodPost, "/thing", nil))
	if got := notAllowed.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("405 Allow = %q, want GET", got)
	}

	panicked := serve(router, httptest.NewRequest(http.MethodGet, "/panic", nil))
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(panicked.Body.Bytes(), &body); err != nil {
		t.Fatalf("panic body is not JSON: %v", err)
	}
	if body.Error.Code != "internal_error" ||
		body.Error.RequestID != panicked.Header().Get(RequestIDHeader) {
		t.Errorf("panic body = %s, want internal_error with the header's request ID",
			panicked.Body.String())
	}
}

// securityPolicyFile is the only non-test file allowed to name the global
// security headers.
const securityPolicyFile = "internal/httpapi/security.go"

// TestSecurityHeadersOwnedBySecurityMiddleware fails if any non-test Go file
// other than security.go names a global security header, so a handler or
// helper cannot accidentally weaken the approved policy.
func TestSecurityHeadersOwnedBySecurityMiddleware(t *testing.T) {
	root := filepath.Join("..", "..") // backend/, from internal/httpapi
	names := make([]string, 0, len(wantSecurityHeaders))
	for name := range wantSecurityHeaders {
		names = append(names, strings.ToLower(name))
	}

	foundPolicy := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(content))

		if filepath.ToSlash(rel) == securityPolicyFile {
			foundPolicy = true
			for _, name := range names {
				if !strings.Contains(lower, name) {
					t.Errorf("%s does not define %s", rel, name)
				}
			}
			return nil
		}

		for _, name := range names {
			if strings.Contains(lower, name) {
				t.Errorf("%s names %s; only %s may set the security headers",
					rel, name, securityPolicyFile)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if !foundPolicy {
		t.Fatalf("%s not found; the ownership scan would be vacuous", securityPolicyFile)
	}
}
