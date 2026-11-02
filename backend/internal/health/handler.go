package health

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const dbPingTimeout = 3 * time.Second

type Pinger interface {
	Ping(ctx context.Context) error
}

// Register adds GET /health and GET /health/db. Their response bodies predate
// the API error envelope and are kept unchanged for compatibility.
func Register(r gin.IRouter, db Pinger) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"message": "SafeInSiam API is running",
		})
	})

	r.GET("/health/db", func(c *gin.Context) {
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
}
