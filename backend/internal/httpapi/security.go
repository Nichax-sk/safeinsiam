package httpapi

import "github.com/gin-gonic/gin"

// The global security headers. securityHeaders is the only code that sets
// them: handlers and helpers must not set, change, or remove them. Changing
// the policy is a deliberate change to this file.
//
// The CSP and X-Frame-Options values are defense in depth for this backend's
// responses only. They do not secure the separate Nuxt frontend, whose
// security headers belong to the frontend and its hosting.
const (
	headerContentTypeOptions = "X-Content-Type-Options"
	headerCSP                = "Content-Security-Policy"
	headerFrameOptions       = "X-Frame-Options"

	valueContentTypeOptions = "nosniff"
	valueCSP                = "default-src 'none'; frame-ancestors 'none'"
	valueFrameOptions       = "DENY"
)

// securityHeaders sets the global security headers before any later handler
// can write, so they reach every response written through the chain,
// including errors, 404/405, and recovered panics.
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header(headerContentTypeOptions, valueContentTypeOptions)
		c.Header(headerCSP, valueCSP)
		c.Header(headerFrameOptions, valueFrameOptions)

		c.Next()
	}
}
