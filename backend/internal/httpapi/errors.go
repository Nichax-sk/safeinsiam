package httpapi

import "net/http"

// Code is a stable, machine-readable API error code.
type Code string

const (
	CodeBadRequest           Code = "bad_request"
	CodeUnauthorized         Code = "unauthorized"
	CodeForbidden            Code = "forbidden"
	CodeNotFound             Code = "not_found"
	CodeMethodNotAllowed     Code = "method_not_allowed"
	CodeConflict             Code = "conflict"
	CodePayloadTooLarge      Code = "payload_too_large"
	CodeUnsupportedMediaType Code = "unsupported_media_type"
	CodeValidationFailed     Code = "validation_failed"
	CodeTooManyRequests      Code = "too_many_requests"
	CodeInternal             Code = "internal_error"
	CodeServiceUnavailable   Code = "service_unavailable"
)

// codeStatus is the single source of truth for the HTTP status of each code.
var codeStatus = map[Code]int{
	CodeBadRequest:           http.StatusBadRequest,
	CodeUnauthorized:         http.StatusUnauthorized,
	CodeForbidden:            http.StatusForbidden,
	CodeNotFound:             http.StatusNotFound,
	CodeMethodNotAllowed:     http.StatusMethodNotAllowed,
	CodeConflict:             http.StatusConflict,
	CodePayloadTooLarge:      http.StatusRequestEntityTooLarge,
	CodeUnsupportedMediaType: http.StatusUnsupportedMediaType,
	CodeValidationFailed:     http.StatusUnprocessableEntity,
	CodeTooManyRequests:      http.StatusTooManyRequests,
	CodeInternal:             http.StatusInternalServerError,
	CodeServiceUnavailable:   http.StatusServiceUnavailable,
}

// defaultMessages are safe, fixed client-facing messages for each code.
var defaultMessages = map[Code]string{
	CodeBadRequest:           "Bad request",
	CodeUnauthorized:         "Authentication required",
	CodeForbidden:            "Access denied",
	CodeNotFound:             "Resource not found",
	CodeMethodNotAllowed:     "Method not allowed",
	CodeConflict:             "Resource conflict",
	CodePayloadTooLarge:      "Request body too large",
	CodeUnsupportedMediaType: "Unsupported media type",
	CodeValidationFailed:     "Request validation failed",
	CodeTooManyRequests:      "Too many requests",
	CodeInternal:             "Internal server error",
	CodeServiceUnavailable:   "Service unavailable",
}

// Detail describes one problem with a request, such as an invalid field.
type Detail struct {
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error is an API error. Its HTTP status is derived from Code, so a status
// that disagrees with the code cannot be expressed.
type Error struct {
	Code Code
	// Message is shown to clients; "" means the code's default message.
	// It is ignored for internal errors, which always use a fixed message.
	Message string
	Details []Detail
	// Err is the internal cause. It is never serialized or logged as text.
	Err error
}

// NewError returns an Error with the given code. An empty message selects
// the code's default message.
func NewError(code Code, message string, details ...Detail) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

// Status returns the HTTP status for the error's code, or 500 when the code
// is unknown.
func (e *Error) Status() int {
	if status, ok := codeStatus[e.Code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// Error returns only the code, so formatting an Error can never expose the
// internal cause.
func (e *Error) Error() string {
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	return e.Err
}
