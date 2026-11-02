package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
)

// internalErrorKey stores the *internalError recorded by WriteError for the
// request logger. It is unexported so only this package can read or set it.
const internalErrorKey = "httpapi.internal_error"

// maxErrorTypes caps how many error types are recorded per request.
const maxErrorTypes = 8

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      Code     `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id"`
	Details   []Detail `json:"details"`
}

// internalError is what the request logger may record about a 5xx response:
// the response code and the Go type names of the cause, never its text.
type internalError struct {
	code  Code
	types []string
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// WriteError writes err as a JSON error envelope and aborts the request.
//
// An *Error anywhere in err's chain determines the response. Any other error
// becomes a 500 internal_error with a fixed message, so internal error text
// never reaches the client.
//
// WriteError never logs. For 5xx responses it records the response code and
// the cause's Go type names on the request, and the request logger includes
// them in its single log entry for the request. Error text is never logged.
//
// If the response has already started, WriteError only aborts the request,
// leaving the status and body that were already written.
func WriteError(c *gin.Context, err error) {
	// Writing now would corrupt the response that has already started.
	if c.Writer.Written() {
		c.Abort()
		return
	}

	var apiErr *Error
	// apiErr is nil when err holds a typed-nil *Error.
	if !errors.As(err, &apiErr) || apiErr == nil {
		apiErr = &Error{Code: CodeInternal, Err: err}
	}

	status, body := normalize(apiErr)
	if c.Request != nil {
		body.RequestID = RequestIDFromContext(c.Request.Context())
	}

	if status >= http.StatusInternalServerError {
		c.Set(internalErrorKey, &internalError{
			code:  body.Code,
			types: errorTypes(err),
		})
	}

	c.AbortWithStatusJSON(status, errorEnvelope{Error: body})
}

// normalize derives the response for e. Unknown codes and internal errors
// become a 500 internal_error with the fixed message and no details.
func normalize(e *Error) (int, errorBody) {
	status, known := codeStatus[e.Code]
	if !known || e.Code == CodeInternal {
		return http.StatusInternalServerError, errorBody{
			Code:    CodeInternal,
			Message: defaultMessages[CodeInternal],
			Details: []Detail{},
		}
	}

	message := e.Message
	if message == "" {
		message = defaultMessages[e.Code]
	}

	details := e.Details
	if details == nil {
		details = []Detail{}
	}

	return status, errorBody{Code: e.Code, Message: message, Details: details}
}

// errorTypes returns the Go type names in err's chain, depth first, capped
// at maxErrorTypes. Type names come from code, not data, so they are safe to
// log.
func errorTypes(err error) []string {
	var types []string

	var walk func(error)
	walk = func(err error) {
		if err == nil || len(types) >= maxErrorTypes {
			return
		}
		types = append(types, fmt.Sprintf("%T", err))

		// Methods on a typed-nil receiver may panic, so don't unwrap it.
		if isNilError(err) {
			return
		}

		switch unwrapper := err.(type) {
		case interface{ Unwrap() error }:
			walk(unwrapper.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range unwrapper.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)

	return types
}

// isNilError reports whether err is nil or holds a nil value of a nil-able
// kind, whose methods may panic on the nil receiver.
func isNilError(err error) bool {
	if err == nil {
		return true
	}

	v := reflect.ValueOf(err)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
