package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	headerOrigin                      = "Origin"
	headerVary                        = "Vary"
	headerAccessControlRequestMethod  = "Access-Control-Request-Method"
	headerAccessControlRequestHeaders = "Access-Control-Request-Headers"
	headerAccessControlAllowOrigin    = "Access-Control-Allow-Origin"
	headerAccessControlAllowMethods   = "Access-Control-Allow-Methods"
	headerAccessControlAllowHeaders   = "Access-Control-Allow-Headers"
	headerAccessControlExposeHeaders  = "Access-Control-Expose-Headers"
	headerAccessControlMaxAge         = "Access-Control-Max-Age"

	// corsMaxAge is how long, in seconds, browsers may cache a preflight.
	corsMaxAge = "600"
)

// The fixed CORS policy. Only the allowed origins are configurable.
// Credentials are never allowed.
var (
	corsAllowedMethods = []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	}
	corsAllowedHeaders = []string{"Content-Type", RequestIDHeader}
	corsExposedHeaders = []string{RequestIDHeader}
)

// cors applies the CORS policy for allowedOrigins, which must be canonical
// origins (see config). A request matches only if it has exactly one Origin
// header equal to an allowed origin; the response then names that origin.
// Disallowed origins get no CORS headers and are otherwise served normally,
// so browsers cannot read the response.
//
// A preflight is an OPTIONS request with an Origin header and an
// Access-Control-Request-Method header (even an empty one). A valid
// preflight gets 204; any invalid one gets the standard 403 envelope.
//
// Headers are set before later handlers run, so error, 404/405, and
// recovered panic responses carry them too.
func cors(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	allowMethods := strings.Join(corsAllowedMethods, ", ")
	allowHeaders := strings.Join(corsAllowedHeaders, ", ")
	exposeHeaders := strings.Join(corsExposedHeaders, ", ")

	return func(c *gin.Context) {
		h := c.Writer.Header()
		addVary(h, headerOrigin)

		origins := c.Request.Header.Values(headerOrigin)
		originAllowed := len(origins) == 1 && allowed[origins[0]]

		// Presence, not value: an empty Access-Control-Request-Method still
		// makes this a preflight, which is then rejected.
		_, requestsMethod := c.Request.Header[headerAccessControlRequestMethod]
		if c.Request.Method == http.MethodOptions && len(origins) > 0 && requestsMethod {
			addVary(h, headerAccessControlRequestMethod, headerAccessControlRequestHeaders)

			if !originAllowed ||
				!requestedMethodAllowed(c.Request.Header.Values(headerAccessControlRequestMethod)) ||
				!requestedHeadersAllowed(c.Request.Header.Values(headerAccessControlRequestHeaders)) {
				WriteError(c, NewError(CodeForbidden, "CORS request not allowed"))
				return
			}

			h.Set(headerAccessControlAllowOrigin, origins[0])
			h.Set(headerAccessControlAllowMethods, allowMethods)
			h.Set(headerAccessControlAllowHeaders, allowHeaders)
			h.Set(headerAccessControlMaxAge, corsMaxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		if originAllowed {
			h.Set(headerAccessControlAllowOrigin, origins[0])
			h.Set(headerAccessControlExposeHeaders, exposeHeaders)
		}

		c.Next()
	}
}

// requestedMethodAllowed reports whether a preflight's
// Access-Control-Request-Method values are exactly one valid method token
// that exactly (case-sensitively) matches an allowed method.
func requestedMethodAllowed(values []string) bool {
	return len(values) == 1 && isToken(values[0]) &&
		slices.Contains(corsAllowedMethods, values[0])
}

// requestedHeadersAllowed reports whether every header named in a
// preflight's Access-Control-Request-Headers values is allowed. Names are
// compared case-insensitively; whitespace, duplicates, empty items, and
// multiple header lines are tolerated.
func requestedHeadersAllowed(values []string) bool {
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !slices.ContainsFunc(corsAllowedHeaders, func(allowed string) bool {
				return strings.EqualFold(allowed, name)
			}) {
				return false
			}
		}
	}
	return true
}

// isToken reports whether s is a non-empty HTTP token (RFC 9110 tchar).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// addVary adds tokens to the Vary header without removing or rewriting
// existing values. Tokens already present (compared case-insensitively) are
// skipped, and nothing is added when Vary is already "*".
func addVary(h http.Header, tokens ...string) {
	var existing []string
	for _, line := range h.Values(headerVary) {
		for _, token := range strings.Split(line, ",") {
			token = strings.TrimSpace(token)
			if token == "*" {
				return
			}
			if token != "" {
				existing = append(existing, token)
			}
		}
	}

	for _, token := range tokens {
		if !slices.ContainsFunc(existing, func(e string) bool {
			return strings.EqualFold(e, token)
		}) {
			h.Add(headerVary, token)
			existing = append(existing, token)
		}
	}
}
