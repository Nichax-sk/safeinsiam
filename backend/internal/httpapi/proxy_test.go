package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// clientIP serves one GET request from remoteAddr with the given header
// lines through a router trusting proxies, and returns the logged client_ip.
func clientIP(t *testing.T, proxies []string, remoteAddr string, header ...[2]string) string {
	t.Helper()

	var buf bytes.Buffer
	router, err := NewRouter(Options{
		Logger:         slog.New(slog.NewJSONHandler(&buf, nil)),
		TrustedProxies: proxies,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.Engine.GET("/ip", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = remoteAddr
	for _, h := range header {
		req.Header.Add(h[0], h[1])
	}
	router.Engine.ServeHTTP(httptest.NewRecorder(), req)

	ip, _ := singleEntry(t, &buf)["client_ip"].(string)
	return ip
}

func forwardedFor(value string) [2]string { return [2]string{"X-Forwarded-For", value} }

var spoofedHeaders = [][2]string{
	forwardedFor("6.6.6.6"),
	{"X-Real-IP", "7.7.7.7"},
	{"X-Appengine-Remote-Addr", "8.8.8.8"},
}

func TestClientIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	if got := clientIP(t, nil, "198.51.100.1:1234", spoofedHeaders...); got != "198.51.100.1" {
		t.Errorf("client_ip = %q, want the remote address 198.51.100.1", got)
	}
}

func TestClientIPFromTrustedProxy(t *testing.T) {
	proxies := []string{"10.0.0.0/8"}
	const proxy = "10.1.2.3:4567"

	tests := []struct {
		name   string
		header [][2]string
		want   string
	}{
		{"forwarded client", [][2]string{forwardedFor("203.0.113.7")}, "203.0.113.7"},
		{"spoofed left entry ignored", [][2]string{forwardedFor("6.6.6.6, 203.0.113.7")}, "203.0.113.7"},
		{"trusted hop skipped", [][2]string{forwardedFor("203.0.113.7, 10.9.9.9")}, "203.0.113.7"},
		{"X-Real-IP ignored", [][2]string{{"X-Real-IP", "7.7.7.7"}}, "10.1.2.3"},
		{"no forwarded header", nil, "10.1.2.3"},
		// Malformed X-Forwarded-For (Gin's validateHeader walks from the right
		// and stops at the first unparsable entry).
		{"garbage", [][2]string{forwardedFor("garbage")}, "10.1.2.3"},
		{"garbage rightmost", [][2]string{forwardedFor("203.0.113.7, garbage")}, "10.1.2.3"},
		{"garbage leftmost", [][2]string{forwardedFor("garbage, 203.0.113.7")}, "203.0.113.7"},
		{"IP with port", [][2]string{forwardedFor("203.0.113.7:1234")}, "10.1.2.3"},
		{"empty value", [][2]string{forwardedFor("")}, "10.1.2.3"},
		{"surrounding spaces", [][2]string{forwardedFor(" 203.0.113.7 ")}, "203.0.113.7"},
		{"two header lines", [][2]string{forwardedFor("6.6.6.6"), forwardedFor("203.0.113.7")}, "203.0.113.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clientIP(t, proxies, proxy, tt.header...); got != tt.want {
				t.Errorf("client_ip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientIPUntrustedRemoteIgnoresForwardedHeaders(t *testing.T) {
	proxies := []string{"10.0.0.0/8"}

	for _, header := range [][2]string{
		forwardedFor("10.1.2.3"),
		forwardedFor("garbage"),
		forwardedFor("203.0.113.7, garbage"),
		{"X-Real-IP", "10.1.2.3"},
	} {
		if got := clientIP(t, proxies, "198.51.100.1:1234", header); got != "198.51.100.1" {
			t.Errorf("%v: client_ip = %q, want the remote address 198.51.100.1", header, got)
		}
	}
}

func TestNewRouterRejectsInvalidTrustedProxy(t *testing.T) {
	_, err := NewRouter(Options{
		Logger:         slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
		TrustedProxies: []string{"nope"},
	})
	if err == nil {
		t.Fatal("NewRouter accepted an invalid trusted proxy")
	}
}
