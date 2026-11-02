package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNewErrorStatusForEveryCode(t *testing.T) {
	tests := map[Code]int{
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

	if len(codeStatus) != len(tests) {
		t.Errorf("codeStatus has %d codes, test covers %d", len(codeStatus), len(tests))
	}

	for code, want := range tests {
		t.Run(string(code), func(t *testing.T) {
			if got := NewError(code, "").Status(); got != want {
				t.Errorf("Status() = %d, want %d", got, want)
			}
			if defaultMessages[code] == "" {
				t.Errorf("no default message for %s", code)
			}
		})
	}
}

func TestUnknownCodeStatusIs500(t *testing.T) {
	if got := NewError("weird", "").Status(); got != http.StatusInternalServerError {
		t.Errorf("Status() = %d, want 500", got)
	}
}

func TestErrorStringNeverIncludesCause(t *testing.T) {
	cause := errors.New("password=hunter2")
	err := &Error{Code: CodeInternal, Message: "detail", Err: cause}

	if got := err.Error(); got != "internal_error" {
		t.Errorf("Error() = %q, want internal_error", got)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Error() exposes the cause: %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not find the cause through Unwrap")
	}
}
