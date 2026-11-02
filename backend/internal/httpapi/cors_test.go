package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const allowedOrigin = "http://localhost:3000"

// corsRouter returns a router with CORS enabled for allowedOrigin, test
// routes, and a log buffer.
func corsRouter(t *testing.T) (*Router, *bytes.Buffer, *bool) {
	t.Helper()

	var buf bytes.Buffer
	router, err := NewRouter(Options{
		Logger:             slog.New(slog.NewJSONHandler(&buf, nil)),
		CORSAllowedOrigins: []string{allowedOrigin},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	handlerRan := false
	router.Engine.GET("/thing", func(c *gin.Context) {
		handlerRan = true
		c.Status(http.StatusOK)
	})
	router.Engine.POST("/thing", func(c *gin.Context) {
		handlerRan = true
		c.Status(http.StatusOK)
	})
	router.Engine.GET("/conflict", func(c *gin.Context) {
		WriteError(c, NewError(CodeConflict, ""))
	})
	router.Engine.GET("/panic", func(c *gin.Context) { panic("boom") })
	router.Engine.GET("/vary", func(c *gin.Context) {
		c.Writer.Header().Add(headerVary, "Accept-Encoding")
		c.Status(http.StatusOK)
	})

	return router, &buf, &handlerRan
}

// request builds a request with header lines added in order.
func request(method, path string, header ...[2]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	for _, h := range header {
		req.Header.Add(h[0], h[1])
	}
	return req
}

func origin(value string) [2]string { return [2]string{"Origin", value} }
func requestMethod(value string) [2]string {
	return [2]string{headerAccessControlRequestMethod, value}
}
func requestHeaders(value string) [2]string {
	return [2]string{headerAccessControlRequestHeaders, value}
}

// varyTokens returns every Vary token, trimmed.
func varyTokens(h http.Header) []string {
	var tokens []string
	for _, line := range h.Values(headerVary) {
		for _, token := range strings.Split(line, ",") {
			tokens = append(tokens, strings.TrimSpace(token))
		}
	}
	return tokens
}

func assertNoCredentials(t *testing.T, h http.Header) {
	t.Helper()
	if values := h.Values("Access-Control-Allow-Credentials"); len(values) != 0 {
		t.Errorf("Access-Control-Allow-Credentials = %q, want absent", values)
	}
}

func assertCORSAllowed(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Values(headerAccessControlAllowOrigin); !slices.Equal(got, []string{allowedOrigin}) {
		t.Errorf("Access-Control-Allow-Origin = %q, want [%q]", got, allowedOrigin)
	}
	if !slices.Contains(varyTokens(h), "Origin") {
		t.Errorf("Vary = %q, want Origin", h.Values(headerVary))
	}
	assertNoCredentials(t, h)
}

func assertCORSDenied(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Values(headerAccessControlAllowOrigin); len(got) != 0 {
		t.Errorf("Access-Control-Allow-Origin = %q, want absent", got)
	}
	if !slices.Contains(varyTokens(h), "Origin") {
		t.Errorf("Vary = %q, want Origin", h.Values(headerVary))
	}
	assertNoCredentials(t, h)
}

func TestCORSAllowedOrigin(t *testing.T) {
	router, _, _ := corsRouter(t)

	rec := serve(router, request(http.MethodGet, "/thing", origin(allowedOrigin)))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	assertCORSAllowed(t, rec.Header())
	if got := rec.Header().Get(headerAccessControlExposeHeaders); got != RequestIDHeader {
		t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, RequestIDHeader)
	}
}

func TestCORSRejectedOrigins(t *testing.T) {
	tests := map[string][][2]string{
		"other site":        {origin("http://evil.test")},
		"other port":        {origin("http://localhost:3001")},
		"other scheme":      {origin("https://localhost:3000")},
		"suffix attack":     {origin("http://localhost:3000.evil.test")},
		"default port form": {origin("http://localhost:3000:80")},
		"null":              {origin("null")},
		"two Origin lines":  {origin(allowedOrigin), origin(allowedOrigin)},
		"comma-joined":      {origin(allowedOrigin + ", " + allowedOrigin)},
		"uppercase":         {origin("HTTP://LOCALHOST:3000")},
	}

	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			router, _, _ := corsRouter(t)

			rec := serve(router, request(http.MethodGet, "/thing", header...))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (served normally)", rec.Code)
			}
			assertCORSDenied(t, rec.Header())
		})
	}
}

func TestCORSNoOriginHeader(t *testing.T) {
	router, _, _ := corsRouter(t)

	rec := serve(router, request(http.MethodGet, "/thing"))

	for name := range rec.Header() {
		if strings.HasPrefix(name, "Access-Control-") {
			t.Errorf("unexpected %s on a request without Origin", name)
		}
	}
	if !slices.Contains(varyTokens(rec.Header()), "Origin") {
		t.Errorf("Vary = %q, want Origin", rec.Header().Values(headerVary))
	}
}

func TestCORSPreflightAllowed(t *testing.T) {
	tests := map[string][][2]string{
		"POST with headers": {
			origin(allowedOrigin), requestMethod("POST"),
			requestHeaders("content-type, X-Request-ID"),
		},
		"whitespace":       {origin(allowedOrigin), requestMethod("GET"), requestHeaders(" content-type ,  x-request-id ")},
		"duplicates":       {origin(allowedOrigin), requestMethod("GET"), requestHeaders("content-type, Content-Type, CONTENT-TYPE")},
		"mixed case":       {origin(allowedOrigin), requestMethod("GET"), requestHeaders("X-Request-Id, Content-TYPE")},
		"two header lines": {origin(allowedOrigin), requestMethod("GET"), requestHeaders("Content-Type"), requestHeaders("X-Request-ID")},
		"empty items":      {origin(allowedOrigin), requestMethod("GET"), requestHeaders("content-type,,")},
		"no headers":       {origin(allowedOrigin), requestMethod("GET")},
	}
	for _, method := range corsAllowedMethods {
		tests["method "+method] = [][2]string{origin(allowedOrigin), requestMethod(method)}
	}

	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			router, buf, handlerRan := corsRouter(t)

			rec := serve(router, request(http.MethodOptions, "/thing", header...))

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status = %d body = %q, want 204 with no body", rec.Code, rec.Body.String())
			}
			assertCORSAllowed(t, rec.Header())
			h := rec.Header()
			if got := h.Get(headerAccessControlAllowMethods); got != "GET, POST, PUT, PATCH, DELETE" {
				t.Errorf("Access-Control-Allow-Methods = %q", got)
			}
			if got := h.Get(headerAccessControlAllowHeaders); got != "Content-Type, X-Request-ID" {
				t.Errorf("Access-Control-Allow-Headers = %q", got)
			}
			if got := h.Get(headerAccessControlMaxAge); got != "600" {
				t.Errorf("Access-Control-Max-Age = %q, want 600", got)
			}
			for _, token := range []string{"Origin", headerAccessControlRequestMethod, headerAccessControlRequestHeaders} {
				if !slices.Contains(varyTokens(h), token) {
					t.Errorf("Vary = %q, want %s", h.Values(headerVary), token)
				}
			}
			if *handlerRan {
				t.Error("route handler ran for a preflight")
			}
			if entry := singleEntry(t, buf); entry["level"] != "INFO" || entry["status"] != float64(204) {
				t.Errorf("access entry = %v, want INFO 204", entry)
			}
			assertSecurityHeaders(t, h)
			if !requestIDPattern.MatchString(h.Get(RequestIDHeader)) {
				t.Errorf("%s missing on preflight", RequestIDHeader)
			}
		})
	}
}

func TestCORSPreflightRejected(t *testing.T) {
	tests := map[string][][2]string{
		"origin not allowed":      {origin("http://evil.test"), requestMethod("GET")},
		"origin null":             {origin("null"), requestMethod("GET")},
		"two Origin lines":        {origin(allowedOrigin), origin(allowedOrigin), requestMethod("GET")},
		"comma-joined Origin":     {origin(allowedOrigin + "," + allowedOrigin), requestMethod("GET")},
		"header not allowed":      {origin(allowedOrigin), requestMethod("GET"), requestHeaders("x-evil")},
		"one header not allowed":  {origin(allowedOrigin), requestMethod("GET"), requestHeaders("content-type, x-evil")},
		"empty method":            {origin(allowedOrigin), requestMethod("")},
		"unsupported TRACE":       {origin(allowedOrigin), requestMethod("TRACE")},
		"unsupported CONNECT":     {origin(allowedOrigin), requestMethod("CONNECT")},
		"unsupported OPTIONS":     {origin(allowedOrigin), requestMethod("OPTIONS")},
		"unsupported HEAD":        {origin(allowedOrigin), requestMethod("HEAD")},
		"unsupported PROPFIND":    {origin(allowedOrigin), requestMethod("PROPFIND")},
		"lowercase get":           {origin(allowedOrigin), requestMethod("get")},
		"mixed case Patch":        {origin(allowedOrigin), requestMethod("Patch")},
		"lowercase delete":        {origin(allowedOrigin), requestMethod("delete")},
		"space separated":         {origin(allowedOrigin), requestMethod("GET POST")},
		"tab inside":              {origin(allowedOrigin), requestMethod("GE\tT")},
		"separator":               {origin(allowedOrigin), requestMethod("G(ET")},
		"trailing semicolon":      {origin(allowedOrigin), requestMethod("GET;")},
		"comma-joined methods":    {origin(allowedOrigin), requestMethod("GET, POST")},
		"comma-joined no space":   {origin(allowedOrigin), requestMethod("GET,POST")},
		"two method lines":        {origin(allowedOrigin), requestMethod("GET"), requestMethod("GET")},
		"two different methods":   {origin(allowedOrigin), requestMethod("GET"), requestMethod("POST")},
		"rejected method, origin": {origin("http://evil.test"), requestMethod("TRACE"), requestHeaders("x-evil")},
	}

	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			router, buf, handlerRan := corsRouter(t)

			rec := serve(router, request(http.MethodOptions, "/thing", header...))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			id := rec.Header().Get(RequestIDHeader)
			want := `{"error":{"code":"forbidden","message":"CORS request not allowed",` +
				`"request_id":"` + id + `","details":[]}}`
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %s\nwant   %s", got, want)
			}
			assertCORSDenied(t, rec.Header())
			if *handlerRan {
				t.Error("route handler ran for a rejected preflight")
			}
			if entry := singleEntry(t, buf); entry["level"] != "WARN" || entry["status"] != float64(403) {
				t.Errorf("access entry = %v, want WARN 403", entry)
			}
			assertSecurityHeaders(t, rec.Header())
		})
	}
}

func TestCORSOptionsWithoutRequestMethodIsNotPreflight(t *testing.T) {
	router, _, _ := corsRouter(t)

	known := serve(router, request(http.MethodOptions, "/thing", origin(allowedOrigin)))
	if known.Code != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS on a GET route: status = %d, want 405", known.Code)
	}
	unknown := serve(router, request(http.MethodOptions, "/missing", origin(allowedOrigin)))
	if unknown.Code != http.StatusNotFound {
		t.Errorf("OPTIONS on an unknown path: status = %d, want 404", unknown.Code)
	}
}

func TestCORSPreflightToUnknownPath(t *testing.T) {
	router, _, _ := corsRouter(t)

	rec := serve(router, request(http.MethodOptions, "/missing", origin(allowedOrigin), requestMethod("GET")))

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	assertCORSAllowed(t, rec.Header())
}

func TestCORSHeadersOnErrorResponses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
	}{
		{"404", http.MethodGet, "/missing", http.StatusNotFound, "not_found"},
		{"405", http.MethodDelete, "/thing", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"409", http.MethodGet, "/conflict", http.StatusConflict, "conflict"},
		{"recovered panic", http.MethodGet, "/panic", http.StatusInternalServerError, "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, _, _ := corsRouter(t)

			rec := serve(router, request(tt.method, tt.path, origin(allowedOrigin)))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			assertCORSAllowed(t, rec.Header())
			id := rec.Header().Get(RequestIDHeader)
			if !strings.Contains(rec.Body.String(), `"code":"`+tt.code+`"`) ||
				!strings.Contains(rec.Body.String(), `"request_id":"`+id+`"`) {
				t.Errorf("body = %s, want %s envelope with request ID", rec.Body.String(), tt.code)
			}
		})
	}
}

func TestCORSKeepsExistingVaryValues(t *testing.T) {
	router, _, _ := corsRouter(t)

	rec := serve(router, request(http.MethodGet, "/vary", origin(allowedOrigin)))

	tokens := varyTokens(rec.Header())
	for _, want := range []string{"Origin", "Accept-Encoding"} {
		if !slices.Contains(tokens, want) {
			t.Errorf("Vary = %q, want %s", tokens, want)
		}
	}
}

func TestCORSDisabledByDefault(t *testing.T) {
	router, _ := testRouter(t)
	router.Engine.GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	preflight := serve(router, request(http.MethodOptions, "/thing", origin(allowedOrigin), requestMethod("GET")))
	if preflight.Code != http.StatusMethodNotAllowed {
		t.Errorf("preflight status = %d, want 405 (no CORS)", preflight.Code)
	}
	unknown := serve(router, request(http.MethodOptions, "/missing", origin(allowedOrigin), requestMethod("GET")))
	if unknown.Code != http.StatusNotFound {
		t.Errorf("preflight to unknown path = %d, want 404 (no CORS)", unknown.Code)
	}
	get := serve(router, request(http.MethodGet, "/thing", origin(allowedOrigin)))

	for _, rec := range []*httptest.ResponseRecorder{preflight, unknown, get} {
		for name := range rec.Header() {
			if strings.HasPrefix(name, "Access-Control-") {
				t.Errorf("unexpected %s with CORS disabled", name)
			}
		}
		if values := rec.Header().Values(headerVary); len(values) != 0 {
			t.Errorf("Vary = %q with CORS disabled, want absent", values)
		}
	}
}

func TestAddVary(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		add      []string
		want     []string
	}{
		{"empty", nil, []string{"Origin"}, []string{"Origin"}},
		{"keeps existing", []string{"Accept-Encoding"}, []string{"Origin"}, []string{"Accept-Encoding", "Origin"}},
		{"case-insensitive duplicate", []string{"origin"}, []string{"Origin"}, []string{"origin"}},
		{"preflight tokens added once", []string{"Accept-Encoding, Origin"},
			[]string{"Origin", headerAccessControlRequestMethod, headerAccessControlRequestHeaders},
			[]string{"Accept-Encoding, Origin", headerAccessControlRequestMethod, headerAccessControlRequestHeaders}},
		{"star unchanged", []string{"*"}, []string{"Origin"}, []string{"*"}},
		{"multi-line kept", []string{"Accept-Encoding", "Accept-Language"}, []string{"Origin"},
			[]string{"Accept-Encoding", "Accept-Language", "Origin"}},
		{"duplicate tokens in one call", nil, []string{"Origin", "origin"}, []string{"Origin"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, line := range tt.existing {
				h.Add(headerVary, line)
			}

			addVary(h, tt.add...)

			if got := h.Values(headerVary); !slices.Equal(got, tt.want) {
				t.Errorf("Vary = %q, want %q", got, tt.want)
			}
		})
	}

	h := http.Header{}
	addVary(h, "Origin")
	addVary(h, "Origin")
	if got := h.Values(headerVary); !slices.Equal(got, []string{"Origin"}) {
		t.Errorf("Vary after two calls = %q, want [Origin]", got)
	}
}

func TestIsToken(t *testing.T) {
	for _, valid := range []string{"GET", "M-SEARCH", "x!#$%&'*+-.^_`|~9"} {
		if !isToken(valid) {
			t.Errorf("isToken(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "G T", "GET,", "G\x7fT", "GÉT", "G(ET", "GET;", "GE\tT"} {
		if isToken(invalid) {
			t.Errorf("isToken(%q) = true, want false", invalid)
		}
	}
}
