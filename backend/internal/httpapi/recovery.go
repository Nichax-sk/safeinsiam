package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// maxStackBytes caps the stack trace recorded for a recovered panic.
const maxStackBytes = 16 << 10

// panicError marks a recovered panic for WriteError. It carries no data from
// the panic value.
type panicError struct{}

func (panicError) Error() string { return "panic recovered" }

// recovery recovers panics from later handlers. It logs exactly one "panic
// recovered" entry and writes the standard 500 error envelope through
// WriteError, which also lets the request logger record the 500.
//
// It never calls Error, String, or any formatting method on the recovered
// value: application types can implement those (including runtime.Error)
// and may expose secrets or panic again. Only the value's type is logged.
//
// A panic that is or wraps http.ErrAbortHandler is re-panicked as the bare
// sentinel, because net/http aborts silently only for that exact value.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if isAbortHandler(rec) {
				panic(http.ErrAbortHandler)
			}

			logPanic(logger, c, rec)
			WriteError(c, panicError{})
		}()

		c.Next()
	}
}

// isAbortHandler reports whether rec is or wraps http.ErrAbortHandler.
// errors.Is calls Unwrap and Is methods on rec, which may be application
// code; a panic from them is contained and treated as "not an abort".
func isAbortHandler(rec any) (abort bool) {
	err, ok := rec.(error)
	if !ok || isNilError(err) {
		return false
	}

	defer func() {
		if recover() != nil {
			abort = false
		}
	}()

	return errors.Is(err, http.ErrAbortHandler)
}

// logPanic writes the single panic log entry. It records the panic value's
// type (%T reads the type only and calls no methods) and a capped stack
// trace, never the value's text, request headers, query string, or body.
func logPanic(logger *slog.Logger, c *gin.Context, rec any) {
	stack := debug.Stack()
	if len(stack) > maxStackBytes {
		stack = stack[:maxStackBytes]
	}

	logger.LogAttrs(c.Request.Context(), slog.LevelError, "panic recovered",
		slog.String("request_id", RequestIDFromContext(c.Request.Context())),
		slog.String("method", c.Request.Method),
		slog.String("route", routeOf(c)),
		slog.String("panic_type", fmt.Sprintf("%T", rec)),
		slog.String("stack", string(stack)),
	)
}
