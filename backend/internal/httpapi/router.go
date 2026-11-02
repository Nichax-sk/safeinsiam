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
	engine.Use(requestLogger(opts.Logger), gin.Recovery())

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

// requestLogger logs exactly one structured entry per request: Info below
// 500 and Error from 500. It logs the route template (or the path for
// unmatched routes) and never the query string, headers, or body. For 5xx
// responses written by WriteError it adds error_code and error_type, never
// the error text.
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}

		status := c.Writer.Status()
		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}

		attrs := []slog.Attr{
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", c.Writer.Size()),
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
