package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// dbRouter returns a CORS-enabled router whose /db route writes
// DatabaseError(err), and its JSON log buffer.
func dbRouter(t *testing.T, err error) (*Router, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	router, routerErr := NewRouter(Options{
		Logger:             slog.New(slog.NewJSONHandler(&buf, nil)),
		CORSAllowedOrigins: []string{allowedOrigin},
	})
	if routerErr != nil {
		t.Fatalf("NewRouter: %v", routerErr)
	}
	router.Engine.GET("/db", func(c *gin.Context) {
		WriteError(c, DatabaseError(err))
	})

	return router, &buf
}

func TestDatabaseErrorResponses(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   Code
		wantMsg    string
		wantTypes  string // substring of error_type for 5xx
	}{
		{"not found", pgx.ErrNoRows, http.StatusNotFound, CodeNotFound, "Resource not found", ""},
		{"conflict", &pgconn.PgError{Code: "23505"}, http.StatusConflict, CodeConflict, "Resource conflict", ""},
		{"contention", &pgconn.PgError{Code: "40001"}, http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*pgconn.PgError"},
		{"unavailable", &pgconn.PgError{Code: "57P01"}, http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*pgconn.PgError"},
		{"connect", &pgconn.ConnectError{}, http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*pgconn.ConnectError"},
		{"timeout", fmt.Errorf("query: %w", context.DeadlineExceeded), http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*fmt.wrapError"},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*pgconn.PgError"},
		{"canceled", context.Canceled, http.StatusServiceUnavailable, CodeServiceUnavailable, "Service unavailable", "*errors.errorString"},
		{"unknown sqlstate", &pgconn.PgError{Code: "42P01"}, http.StatusInternalServerError, CodeInternal, "Internal server error", "*pgconn.PgError"},
		{"not null", &pgconn.PgError{Code: "23502"}, http.StatusInternalServerError, CodeInternal, "Internal server error", "*pgconn.PgError"},
		{"non-database", errors.New("boom"), http.StatusInternalServerError, CodeInternal, "Internal server error", "*errors.errorString"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, buf := dbRouter(t, tt.err)
			rec := serve(router, request(http.MethodGet, "/db", origin(allowedOrigin)))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not an envelope: %q", rec.Body.String())
			}
			want := errorEnvelope{Error: errorBody{
				Code:      tt.wantCode,
				Message:   tt.wantMsg,
				RequestID: rec.Header().Get(RequestIDHeader),
				Details:   []Detail{},
			}}
			if body.Error.RequestID == "" || body.Error.Code != want.Error.Code ||
				body.Error.Message != want.Error.Message ||
				body.Error.RequestID != want.Error.RequestID || len(body.Error.Details) != 0 {
				t.Errorf("body = %+v, want %+v", body, want)
			}
			if !strings.Contains(rec.Body.String(), `"details":[]`) {
				t.Errorf("body %q does not have empty details", rec.Body.String())
			}

			assertSecurityHeaders(t, rec.Header())
			assertCORSAllowed(t, rec.Header())

			entry := singleEntry(t, buf)
			if tt.wantStatus >= http.StatusInternalServerError {
				if entry["level"] != "ERROR" || entry["error_code"] != string(tt.wantCode) {
					t.Errorf("log entry = %v, want ERROR with error_code %s", entry, tt.wantCode)
				}
				if types, _ := entry["error_type"].(string); !strings.Contains(types, tt.wantTypes) ||
					!strings.HasPrefix(types, "*httpapi.Error") {
					t.Errorf("error_type = %q, want *httpapi.Error first and %s", types, tt.wantTypes)
				}
			} else {
				if entry["level"] != "WARN" {
					t.Errorf("level = %v, want WARN", entry["level"])
				}
				if _, ok := entry["error_code"]; ok {
					t.Errorf("4xx log entry has error_code: %v", entry)
				}
			}
		})
	}
}

func TestDatabaseErrorNil(t *testing.T) {
	if err := DatabaseError(nil); err != nil {
		t.Errorf("DatabaseError(nil) = %v, want nil", err)
	}
}

func TestDatabaseErrorKeepsAPIError(t *testing.T) {
	apiErr := NewError(CodeBadRequest, "Bad cursor")
	for _, err := range []error{
		apiErr,
		fmt.Errorf("list: %w", apiErr),
		errors.Join(&pgconn.PgError{Code: "40001"}, apiErr),
		&Error{Code: CodeBadRequest, Err: &pgconn.PgError{Code: "23505"}},
	} {
		if got := DatabaseError(err); got != err {
			t.Errorf("DatabaseError(%T) = %v, want the error unchanged", err, got)
		}
	}

	router, _ := dbRouter(t, fmt.Errorf("list: %w", apiErr))
	if rec := serve(router, request(http.MethodGet, "/db")); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDatabaseErrorTypedNilChain(t *testing.T) {
	for _, err := range []error{
		(*pgconn.ConnectError)(nil),
		errors.Join((*pgconn.ConnectError)(nil), (*Error)(nil)),
	} {
		got := DatabaseError(err)
		var apiErr *Error
		if !errors.As(got, &apiErr) || apiErr.Code != CodeInternal {
			t.Errorf("DatabaseError(%v) = %#v, want internal_error", err, got)
		}
	}
}

func TestDatabaseErrorKeepsChain(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505"}
	got := DatabaseError(fmt.Errorf("insert: %w", pgErr))

	var found *pgconn.PgError
	if !errors.As(got, &found) || found != pgErr {
		t.Error("errors.As does not find the original *pgconn.PgError")
	}
	if !errors.Is(DatabaseError(fmt.Errorf("query: %w", context.Canceled)), context.Canceled) {
		t.Error("errors.Is does not find context.Canceled")
	}
	if got.Error() != string(CodeConflict) {
		t.Errorf("Error() = %q, want only the code", got.Error())
	}
}

// sentinelConnectError returns a real *pgconn.ConnectError whose text holds
// the user, database and host of its config.
func sentinelConnectError(t *testing.T) error {
	t.Helper()

	// Resolve the sentinel host to a closed local port, so the dial fails
	// and the error text names both the host and the address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg, err := pgconn.ParseConfig(fmt.Sprintf(
		"postgres://sentinel_user:sentinel_password@sentinel-host.invalid:%d/sentinel_db?sslmode=disable", port))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = pgconn.ConnectConfig(ctx, cfg)

	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("ConnectConfig error %v is not a *pgconn.ConnectError", err)
	}
	// The test is only meaningful if the driver text holds the sentinels.
	for _, s := range []string{"sentinel_user", "sentinel_db", "sentinel-host", "127.0.0.1"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("ConnectError text %q lacks %q", err.Error(), s)
		}
	}
	return err
}

func sentinelPgError(code string) *pgconn.PgError {
	return &pgconn.PgError{
		Severity:         "ERROR",
		Code:             code,
		Message:          "sentinel_message",
		Detail:           "sentinel_detail Key (email)=(victim@example.com)",
		Hint:             "sentinel_hint",
		Where:            "sentinel_where",
		InternalQuery:    "SELECT sentinel_internal_query",
		SchemaName:       "sentinel_schema",
		TableName:        "sentinel_table",
		ColumnName:       "sentinel_column",
		DataTypeName:     "sentinel_type",
		ConstraintName:   "sentinel_constraint",
		File:             "sentinel_file.c",
		Routine:          "sentinel_routine",
		Position:         4242,
		InternalPosition: 4343,
	}
}

var disclosureSentinels = []string{
	"sentinel", "victim@example.com", "SELECT", "4242", "4343", "password",
}

func TestDatabaseErrorNeverDisclosesDriverDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"conflict", fmt.Errorf("insert: %w", sentinelPgError("23505")), http.StatusConflict},
		{"contention", sentinelPgError("40001"), http.StatusServiceUnavailable},
		{"unknown sqlstate", sentinelPgError("42P01"), http.StatusInternalServerError},
		{"connect error", sentinelConnectError(t), http.StatusServiceUnavailable},
		{"not found", fmt.Errorf("sentinel_wrap: %w", pgx.ErrNoRows), http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, buf := dbRouter(t, tt.err)
			rec := serve(router, request(http.MethodGet, "/db", origin(allowedOrigin)))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var headers strings.Builder
			for name, values := range rec.Header() {
				headers.WriteString(name + ": " + strings.Join(values, ",") + "\n")
			}
			for _, s := range disclosureSentinels {
				if strings.Contains(rec.Body.String(), s) {
					t.Errorf("response body %q contains %q", rec.Body.String(), s)
				}
				if strings.Contains(headers.String(), s) {
					t.Errorf("response headers contain %q", s)
				}
				if strings.Contains(buf.String(), s) {
					t.Errorf("log %q contains %q", buf.String(), s)
				}
			}

			entries := logEntries(t, buf)
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1", len(entries))
			}
			if tt.wantStatus >= http.StatusInternalServerError {
				types := strings.Split(entries[0]["error_type"].(string), ",")
				want := "*pgconn.PgError"
				if tt.name == "connect error" {
					want = "*pgconn.ConnectError"
				}
				if !slices.Contains(types, want) {
					t.Errorf("error_type = %v, want it to include %s", types, want)
				}
			}
		})
	}
}

// selfError unwraps to itself.
type selfError struct{}

func (e *selfError) Error() string { return "self" }
func (e *selfError) Unwrap() error { return e }

// linkError unwraps to next; tests point it back into the chain.
type linkError struct{ next error }

func (e *linkError) Error() string { return "link" }
func (e *linkError) Unwrap() error { return e.next }

// multiLinkError unwraps to errs; tests point them back into the chain.
type multiLinkError struct{ errs []error }

func (e *multiLinkError) Error() string   { return "multi" }
func (e *multiLinkError) Unwrap() []error { return e.errs }

// endlessValueError is a non-comparable value whose chain never ends.
type endlessValueError struct{ data []byte }

func (e endlessValueError) Error() string { return "endless" }
func (e endlessValueError) Unwrap() error { return e }

// within runs fn and fails if it doesn't return in time.
func within(t *testing.T, fn func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not return")
	}
}

func TestDatabaseErrorCyclicChainKeepsReachableAPIError(t *testing.T) {
	// The *Error is reached before the cycle repeats, as errors.As reaches it.
	link := &linkError{}
	apiErr := &Error{Code: CodeBadRequest, Err: link}
	link.next = apiErr

	multi := &multiLinkError{}
	multi.errs = []error{&pgconn.PgError{Code: "40001"}, NewError(CodeConflict, ""), multi}

	for name, tt := range map[string]struct {
		err        error
		wantStatus int
	}{
		"link":        {link, http.StatusBadRequest},
		"error first": {apiErr, http.StatusBadRequest},
		"multi":       {multi, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			var got error
			within(t, func() { got = DatabaseError(tt.err) })
			if got != tt.err {
				t.Fatalf("DatabaseError = %#v, want the error unchanged", got)
			}

			router, _ := dbRouter(t, tt.err)
			var rec *httptest.ResponseRecorder
			within(t, func() { rec = serve(router, request(http.MethodGet, "/db")) })
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestDatabaseErrorCyclicChainWithoutReachableAPIError(t *testing.T) {
	// The cycle comes first in errors.As order, so errors.As would never
	// reach the *Error behind it; the chain is wrapped as an internal error.
	behindCycle := &multiLinkError{errs: []error{&selfError{}, NewError(CodeBadRequest, "")}}

	twoLinks := func() error {
		a, b := &linkError{}, &linkError{}
		a.next, b.next = b, a
		return a
	}
	multiCycle := func() error {
		m := &multiLinkError{}
		m.errs = []error{&pgconn.PgError{Code: "40001"}, m}
		return m
	}
	withTypedNil := func() error {
		a := &linkError{}
		a.next = errors.Join((*pgconn.ConnectError)(nil), a)
		return a
	}

	for name, err := range map[string]error{
		"self":           &selfError{},
		"two links":      twoLinks(),
		"multi cycle":    multiCycle(),
		"with typed nil": withTypedNil(),
		"endless value":  endlessValueError{data: []byte("x")},
		"behind cycle":   behindCycle,
	} {
		t.Run(name, func(t *testing.T) {
			var got error
			within(t, func() { got = DatabaseError(err) })

			apiErr, ok := got.(*Error)
			// DeepEqual, because == panics on the non-comparable value.
			if !ok || apiErr.Code != CodeInternal || !reflect.DeepEqual(apiErr.Err, err) {
				t.Fatalf("DatabaseError = %T, want internal_error wrapping the chain", got)
			}

			router, buf := dbRouter(t, err)
			var rec *httptest.ResponseRecorder
			within(t, func() { rec = serve(router, request(http.MethodGet, "/db")) })
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if entry := singleEntry(t, buf); entry["error_code"] != string(CodeInternal) {
				t.Errorf("error_code = %v, want internal_error", entry["error_code"])
			}
		})
	}
}

func TestDatabaseErrorSharedReferencesBeforeAPIError(t *testing.T) {
	// 300 references to one shared chain come first in errors.As order;
	// the shared errors are searched once, so the *Error after them is
	// still found.
	shared := &linkError{next: &pgconn.PgError{Code: "40001"}}
	errs := make([]error, 0, 301)
	for range 300 {
		errs = append(errs, shared)
	}
	err := errors.Join(append(errs, NewError(CodeBadRequest, ""))...)

	var got error
	within(t, func() { got = DatabaseError(err) })
	if got != err {
		t.Fatalf("DatabaseError = %T, want the error unchanged", got)
	}

	router, _ := dbRouter(t, err)
	var rec *httptest.ResponseRecorder
	within(t, func() { rec = serve(router, request(http.MethodGet, "/db")) })
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDatabaseErrorDistinctErrorLimit(t *testing.T) {
	// chain puts n distinct errors before the *Error, each reached once.
	chain := func(n int) error {
		var err error = NewError(CodeBadRequest, "")
		for range n {
			err = &linkError{next: err}
		}
		return err
	}

	within(t, func() {
		if err := chain(maxAPIErrorSearch); DatabaseError(err) != err {
			t.Errorf("%d distinct errors: the *Error was not kept", maxAPIErrorSearch)
		}

		err := chain(maxAPIErrorSearch + 1)
		apiErr, ok := DatabaseError(err).(*Error)
		if !ok || apiErr.Code != CodeInternal || apiErr.Err != err {
			t.Errorf("%d distinct errors: DatabaseError = %#v, want internal_error wrapping the chain",
				maxAPIErrorSearch+1, apiErr)
		}
	})
}

// mapLinkError is a map-backed error: non-comparable, identified by its map
// pointer. It unwraps to the "next" entry.
type mapLinkError map[string]error

func (e mapLinkError) Error() string { return "map" }
func (e mapLinkError) Unwrap() error { return e["next"] }

func TestDatabaseErrorMapBackedErrors(t *testing.T) {
	keep := func(name string, err error, wantStatus int) {
		t.Helper()
		var got error
		within(t, func() { got = DatabaseError(err) })
		if !reflect.DeepEqual(got, err) {
			t.Errorf("%s: DatabaseError = %T, want the error unchanged", name, got)
		}
		router, _ := dbRouter(t, err)
		var rec *httptest.ResponseRecorder
		within(t, func() { rec = serve(router, request(http.MethodGet, "/db")) })
		if rec.Code != wantStatus {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, wantStatus)
		}
	}

	keep("traversal", mapLinkError{"next": mapLinkError{"next": NewError(CodeBadRequest, "")}}, http.StatusBadRequest)

	// 300 references to one map error are searched once.
	shared := mapLinkError{"next": &pgconn.PgError{Code: "40001"}}
	refs := make([]error, 0, 301)
	for range 300 {
		refs = append(refs, shared)
	}
	keep("shared", errors.Join(append(refs, NewError(CodeConflict, ""))...), http.StatusConflict)

	// Without an *Error, a map chain is still classified.
	router, _ := dbRouter(t, mapLinkError{"next": &pgconn.PgError{Code: "23505"}})
	if rec := serve(router, request(http.MethodGet, "/db")); rec.Code != http.StatusConflict {
		t.Errorf("map chain to 23505: status = %d, want 409", rec.Code)
	}

	self := mapLinkError{}
	self["next"] = self
	var got error
	within(t, func() { got = DatabaseError(self) })
	if apiErr, ok := got.(*Error); !ok || apiErr.Code != CodeInternal || !reflect.DeepEqual(apiErr.Err, self) {
		t.Errorf("self-referencing map error: DatabaseError = %T, want internal_error wrapping it", got)
	}
	router, _ = dbRouter(t, self)
	var rec *httptest.ResponseRecorder
	within(t, func() { rec = serve(router, request(http.MethodGet, "/db")) })
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("self-referencing map error: status = %d, want 500", rec.Code)
	}
}
