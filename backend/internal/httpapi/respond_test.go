package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const jsonContentType = "application/json; charset=utf-8"

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// writeError runs WriteError for err in a test context and returns the
// response and the context.
func writeError(t *testing.T, err error) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	WriteError(c, err)

	return rec, c
}

func TestWriteErrorEnvelope(t *testing.T) {
	rec, c := writeError(t, NewError(CodeNotFound, ""))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}
	want := `{"error":{"code":"not_found","message":"Resource not found","request_id":"","details":[]}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
	if !c.IsAborted() {
		t.Error("WriteError did not abort the request")
	}
}

func TestWriteErrorCustomMessageAndDetails(t *testing.T) {
	rec, _ := writeError(t, NewError(CodeValidationFailed, "Check the form",
		Detail{Field: "email", Code: "required"},
	))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	want := `{"error":{"code":"validation_failed","message":"Check the form",` +
		`"request_id":"","details":[{"field":"email","code":"required"}]}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

func TestWriteErrorNormalizesToSafeInternalError(t *testing.T) {
	want := `{"error":{"code":"internal_error","message":"Internal server error",` +
		`"request_id":"","details":[]}}`

	tests := map[string]error{
		"plain error":   errors.New("connect postgres://app:hunter2@db/x"),
		"unknown code":  &Error{Code: "weird", Message: "leak hunter2?"},
		"internal code": NewError(CodeInternal, "db password is hunter2", Detail{Code: "x"}),
		"wrapped plain": fmt.Errorf("query: %w", errors.New("hunter2")),
	}

	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			rec, _ := writeError(t, err)

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %s\nwant   %s", got, want)
			}
		})
	}
}

func TestWriteErrorFindsWrappedAPIError(t *testing.T) {
	rec, _ := writeError(t, fmt.Errorf("load item: %w", NewError(CodeConflict, "")))

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"conflict"`) {
		t.Errorf("body = %s, want conflict code", rec.Body.String())
	}
}

func TestWriteErrorRecordsInternalErrorOnlyFor5xx(t *testing.T) {
	_, c := writeError(t, fmt.Errorf("query: %w", errors.New("hunter2")))

	value, ok := c.Get(internalErrorKey)
	if !ok {
		t.Fatal("no internal error recorded for a 500")
	}
	internal := value.(*internalError)
	if internal.code != CodeInternal {
		t.Errorf("recorded code = %q, want internal_error", internal.code)
	}
	wantTypes := []string{"*fmt.wrapError", "*errors.errorString"}
	if !slices.Equal(internal.types, wantTypes) {
		t.Errorf("recorded types = %v, want %v", internal.types, wantTypes)
	}

	_, c = writeError(t, NewError(CodeConflict, ""))
	if _, ok := c.Get(internalErrorKey); ok {
		t.Error("internal error recorded for a 409")
	}
}

func TestErrorTypesFollowsJoinedErrorsAndCaps(t *testing.T) {
	joined := errors.Join(errors.New("a"), fmt.Errorf("b: %w", errors.New("c")))
	want := []string{"*errors.joinError", "*errors.errorString",
		"*fmt.wrapError", "*errors.errorString"}
	if got := errorTypes(joined); !slices.Equal(got, want) {
		t.Errorf("errorTypes = %v, want %v", got, want)
	}

	var deep error = errors.New("root")
	for range 20 {
		deep = fmt.Errorf("wrap: %w", deep)
	}
	if got := len(errorTypes(deep)); got != maxErrorTypes {
		t.Errorf("len(errorTypes) = %d, want %d", got, maxErrorTypes)
	}
}

func TestWriteErrorTypedNilAPIError(t *testing.T) {
	var apiErr *Error
	var err error = apiErr

	rec, c := writeError(t, err)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}
	want := `{"error":{"code":"internal_error","message":"Internal server error",` +
		`"request_id":"","details":[]}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}

	value, ok := c.Get(internalErrorKey)
	if !ok {
		t.Fatal("no internal error recorded")
	}
	if got := value.(*internalError).types; !slices.Equal(got, []string{"*httpapi.Error"}) {
		t.Errorf("recorded types = %v, want [*httpapi.Error]", got)
	}
}

func TestErrorTypesTypedNilDoesNotUnwrap(t *testing.T) {
	var typedNil *Error

	got := errorTypes(fmt.Errorf("x: %w", typedNil))

	if want := []string{"*fmt.wrapError", "*httpapi.Error"}; !slices.Equal(got, want) {
		t.Errorf("errorTypes = %v, want %v", got, want)
	}
}

// funcError's methods call the func, so they panic on a nil receiver.
type funcError func() error

func (f funcError) Error() string { return f().Error() }
func (f funcError) Unwrap() error { return f() }

func TestErrorTypesNilFuncErrorDoesNotUnwrap(t *testing.T) {
	got := errorTypes(fmt.Errorf("x: %w", funcError(nil)))

	if want := []string{"*fmt.wrapError", "httpapi.funcError"}; !slices.Equal(got, want) {
		t.Errorf("errorTypes = %v, want %v", got, want)
	}
}

type (
	mapError    map[string]string
	sliceError  []string
	chanError   chan struct{}
	stringError string
)

func (mapError) Error() string    { return "map" }
func (sliceError) Error() string  { return "slice" }
func (chanError) Error() string   { return "chan" }
func (stringError) Error() string { return "string" }

func TestIsNilError(t *testing.T) {
	var (
		nilPointer *Error
		nilFunc    funcError
		nilMap     mapError
		nilSlice   sliceError
		nilChan    chanError
	)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"untyped nil", nil, true},
		{"typed-nil pointer", nilPointer, true},
		{"typed-nil func", nilFunc, true},
		{"typed-nil map", nilMap, true},
		{"typed-nil slice", nilSlice, true},
		{"typed-nil chan", nilChan, true},
		{"non-nil pointer", NewError(CodeConflict, ""), false},
		{"non-nil map", mapError{}, false},
		{"non-nil-able kind", stringError("x"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNilError(tt.err); got != tt.want {
				t.Errorf("isNilError = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	WriteJSON(c, http.StatusCreated, map[string]string{"id": "1"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != jsonContentType {
		t.Errorf("Content-Type = %q, want %q", got, jsonContentType)
	}
	if got := rec.Body.String(); got != `{"id":"1"}` {
		t.Errorf("body = %s", got)
	}
}
