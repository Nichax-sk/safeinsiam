package httpapi

import (
	"context"
	"crypto/rand"
	"regexp"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader carries the request ID on requests and responses.
const RequestIDHeader = "X-Request-ID"

// requestIDPattern is what an inbound request ID must match to be kept. It
// keeps echoed IDs safe in headers, JSON, and logs.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

// RequestIDFromContext returns the request ID stored by the request ID
// middleware, or "" when there is none.
//
// Request IDs are for correlation only. Clients may choose their own, so
// never use one for authorization, deduplication, or idempotency.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID keeps a single valid inbound X-Request-ID or generates a new one,
// stores it in the request context, and sets it on the response before any
// later handler can write.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		var id string
		if values := c.Request.Header.Values(RequestIDHeader); len(values) == 1 &&
			requestIDPattern.MatchString(values[0]) {
			id = values[0]
		} else {
			id = rand.Text()
		}

		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), requestIDKey{}, id),
		)
		c.Header(RequestIDHeader, id)

		c.Next()
	}
}
