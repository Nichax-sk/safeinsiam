package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// APIV1Prefix is the path prefix for version 1 of the API.
const APIV1Prefix = "/api/v1"

type Options struct {
	// Logger receives one access log entry per request. Required.
	Logger *slog.Logger
	// DebugRoutes are route templates, such as health checks, whose
	// successful requests (status < 400) are logged at Debug instead of Info.
	DebugRoutes []string
}

// Router is the HTTP router. Unversioned operational routes, such as health
// checks, register on Engine; versioned API routes register on V1.
type Router struct {
	Engine *gin.Engine
	V1     *gin.RouterGroup
}

// NewRouter builds the Gin engine with the shared middleware and JSON error
// responses for unknown routes (404) and unsupported methods (405). Set the
// Gin mode before calling it.
func NewRouter(opts Options) (*Router, error) {
	if opts.Logger == nil {
		return nil, errors.New("httpapi: logger is required")
	}

	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	// requestID runs first so the ID and its response header exist before
	// anything else. securityHeaders then sets the global security headers
	// before any handler can write. requestLogger wraps recovery so a
	// recovered panic is still logged exactly once.
	engine.Use(
		requestID(),
		securityHeaders(),
		requestLogger(opts.Logger, opts.DebugRoutes),
		recovery(opts.Logger),
	)

	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, err
	}

	engine.NoRoute(func(c *gin.Context) {
		WriteError(c, NewError(CodeNotFound, ""))
	})
	// Gin sets the Allow header before running NoMethod handlers.
	engine.NoMethod(func(c *gin.Context) {
		WriteError(c, NewError(CodeMethodNotAllowed, ""))
	})

	return &Router{
		Engine: engine,
		V1:     engine.Group(APIV1Prefix),
	}, nil
}

// requestLogger logs exactly one structured entry per request: Error for
// 5xx, Warn for 4xx, Debug for successful requests to debugRoutes, and Info
// otherwise. It logs the route template (or the path for unmatched routes)
// and never the query string, headers, or body. For 5xx responses written by
// WriteError it adds error_code and error_type, never the error text.
func requestLogger(logger *slog.Logger, debugRoutes []string) gin.HandlerFunc {
	quiet := make(map[string]bool, len(debugRoutes))
	for _, route := range debugRoutes {
		quiet[route] = true
	}

	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		status := c.Writer.Status()

		var level slog.Level
		switch {
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case status >= http.StatusBadRequest:
			level = slog.LevelWarn
		case quiet[c.FullPath()]: // matched route templates only
			level = slog.LevelDebug
		default:
			level = slog.LevelInfo
		}

		attrs := []slog.Attr{
			slog.String("method", c.Request.Method),
			slog.String("route", routeOf(c)),
			slog.Int("status", status),
			slog.Float64("latency_ms", float64(time.Since(start).Microseconds())/1000),
			slog.String("client_ip", c.ClientIP()),
			// Size is -1 when no body was written.
			slog.Int("bytes", max(c.Writer.Size(), 0)),
			slog.String("request_id", RequestIDFromContext(c.Request.Context())),
		}

		if status >= http.StatusInternalServerError {
			if value, ok := c.Get(internalErrorKey); ok {
				if internal, ok := value.(*internalError); ok {
					attrs = append(attrs, slog.String("error_code", string(internal.code)))
					if len(internal.types) > 0 {
						attrs = append(attrs,
							slog.String("error_type", strings.Join(internal.types, ",")),
						)
					}
				}
			}
		}

		logger.LogAttrs(c.Request.Context(), level, "http request", attrs...)
	}
}

// routeOf returns the matched route template, or the path for unmatched
// routes. It never includes the query string.
func routeOf(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return c.Request.URL.Path
}
