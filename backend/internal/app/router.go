package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

const dbPingTimeout = 3 * time.Second

type pinger interface {
	Ping(ctx context.Context) error
}

func ginMode(env config.Env) string {
	switch env {
	case config.EnvDevelopment:
		return gin.DebugMode
	case config.EnvTest:
		return gin.TestMode
	default:
		return gin.ReleaseMode
	}
}

func newRouter(logger *slog.Logger, db pinger) (*gin.Engine, error) {
	router := gin.New()
	router.Use(requestLogger(logger), gin.Recovery())

	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"message": "SafeInSiam API is running",
		})
	})

	router.GET("/health/db", func(c *gin.Context) {
		queryCtx, cancel := context.WithTimeout(
			c.Request.Context(),
			dbPingTimeout,
		)
		defer cancel()

		if err := db.Ping(queryCtx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":  "error",
				"message": "Database connection unavailable",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"message": "Database connection is healthy",
		})
	})

	return router, nil
}

// requestLogger logs one structured line per request. It logs the route
// template (or the path for unmatched routes) and never the query string.
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

		logger.LogAttrs(c.Request.Context(), level, "http request",
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", c.Writer.Size()),
		)
	}
}
