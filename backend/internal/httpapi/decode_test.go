package httpapi

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type decodeInner struct {
	N int `json:"n"`
}

type decodeBase struct {
	ID int `json:"id"`
}

// failingField's UnmarshalJSON always fails, with text that must never reach
// a response or log.
type failingField struct{}

func (*failingField) UnmarshalJSON([]byte) error {
	return errors.New("sentinel_unmarshal_failure")
}

type decodeTarget struct {
	decodeBase
	Name  string         `json:"name"`
	Age   int            `json:"age"`
	Inner decodeInner    `json:"inner"`
	List  []decodeInner  `json:"list"`
	Tags  map[string]int `json:"tags"`
	Opt   *int           `json:"opt"`
	Skip  string         `json:"-"`
	Fail  *failingField  `json:"fail"`
}

// readTracker records whether the body was read.
type readTracker struct {
	r    io.Reader
	read bool
}

func (rt *readTracker) Read(p []byte) (int, error) {
	rt.read = true
	return rt.r.Read(p)
}

// brokenReader fails like a connection reset mid-body.
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("sentinel_read_failure") }

// decodeRouter returns a CORS-enabled router with routes that decode into a
// decodeTarget, a non-pointer and a nil pointer, and its JSON log buffer.
func decodeRouter(t *testing.T) (*Router, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	router, err := NewRouter(Options{
		Logger:             slog.New(slog.NewJSONHandler(&buf, nil)),
		CORSAllowedOrigins: []string{allowedOrigin},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	router.Engine.POST("/decode", func(c *gin.Context) {
		var dst decodeTarget
		if err := DecodeJSON(c, &dst); err != nil {
			WriteError(c, err)
			return
		}
		WriteJSON(c, http.StatusOK, dst)
	})
	router.Engine.POST("/non-pointer", func(c *gin.Context) {
		WriteError(c, DecodeJSON(c, decodeTarget{}))
	})
	router.Engine.POST("/nil-pointer", func(c *gin.Context) {
		var dst *decodeTarget
		WriteError(c, DecodeJSON(c, dst))
	})

	return router, &buf
}

// postJSON builds a POST with body, an allowed Origin, and the given
// Content-Type header lines (application/json when none are given).
func postJSON(path string, body io.Reader, contentType ...string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Origin", allowedOrigin)
	if len(contentType) == 0 {
		contentType = []string{"application/json"}
	}
	for _, ct := range contentType {
		if ct != "" {
			req.Header.Add("Content-Type", ct)
		}
	}
	return req
}

// assertDecodeError checks the envelope, the code's default message, the
// exact details, the request ID, the security and CORS headers, and the
// single access log entry.
func assertDecodeError(t *testing.T, rec *httptest.ResponseRecorder, logs *bytes.Buffer,
	wantStatus int, wantCode Code, wantDetails []Detail,
) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, wantStatus, rec.Body.String())
	}

	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not an envelope: %s", rec.Body.String())
	}
	if wantDetails == nil {
		wantDetails = []Detail{}
	}
	want := errorBody{
		Code:      wantCode,
		Message:   defaultMessages[wantCode],
		RequestID: rec.Header().Get(RequestIDHeader),
		Details:   wantDetails,
	}
	if body.Error.RequestID == "" || !reflect.DeepEqual(body.Error, want) {
		t.Errorf("error = %+v, want %+v", body.Error, want)
	}

	assertSecurityHeaders(t, rec.Header())
	assertCORSAllowed(t, rec.Header())

	entry := singleEntry(t, logs)
	wantLevel := "WARN"
	if wantStatus >= http.StatusInternalServerError {
		wantLevel = "ERROR"
	}
	if entry["level"] != wantLevel || entry["status"] != float64(wantStatus) {
		t.Errorf("log level, status = %v, %v; want %s, %d", entry["level"], entry["status"], wantLevel, wantStatus)
	}
	if _, ok := entry["error_code"]; ok && wantStatus < http.StatusInternalServerError {
		t.Errorf("4xx log entry has error_code: %v", entry)
	}
}

func detail(code string) []Detail { return []Detail{{Code: code}} }

func TestDecodeJSONAccepts(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType []string
		want        decodeTarget
	}{
		{"object", `{"name":"a","age":3,"inner":{"n":4}}`, nil,
			decodeTarget{Name: "a", Age: 3, Inner: decodeInner{N: 4}}},
		{"embedded field", `{"id":7}`, nil, decodeTarget{decodeBase: decodeBase{ID: 7}}},
		{"charset", `{"name":"a"}`, []string{"application/json; charset=utf-8"}, decodeTarget{Name: "a"}},
		{"charset uppercase", `{"name":"a"}`, []string{"application/json; charset=UTF-8"}, decodeTarget{Name: "a"}},
		{"charset quoted", `{"name":"a"}`, []string{`application/json;charset="utf-8"`}, decodeTarget{Name: "a"}},
		{"media type uppercase", `{"name":"a"}`, []string{"APPLICATION/JSON"}, decodeTarget{Name: "a"}},
		{"surrounding whitespace", " \n\t{\"name\":\"a\"}\r\n ", nil, decodeTarget{Name: "a"}},
		// encoding/json matches names case-insensitively.
		{"case-insensitive name", `{"NAME":"a"}`, nil, decodeTarget{Name: "a"}},
		{"empty object", `{}`, nil, decodeTarget{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, logs := decodeRouter(t)
			rec := serve(router, postJSON("/decode", strings.NewReader(tt.body), tt.contentType...))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
			var got decodeTarget
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decoded = %+v, want %+v", got, tt.want)
			}
			assertSecurityHeaders(t, rec.Header())
			assertCORSAllowed(t, rec.Header())
			if entry := singleEntry(t, logs); entry["level"] != "INFO" {
				t.Errorf("log level = %v, want INFO", entry["level"])
			}
		})
	}
}

func TestDecodeJSONRejectsContentType(t *testing.T) {
	for name, contentType := range map[string][]string{
		"missing":             {""},
		"text/plain":          {"text/plain"},
		"latin1 charset":      {"application/json; charset=latin1"},
		"other parameter":     {"application/json; foo=bar"},
		"charset and another": {"application/json; charset=utf-8; foo=bar"},
		"two headers":         {"application/json", "application/json"},
		"json suffix":         {"application/merge-patch+json"},
		"malformed":           {"application/"},
		"form":                {"application/x-www-form-urlencoded"},
	} {
		t.Run(name, func(t *testing.T) {
			router, logs := decodeRouter(t)
			body := &readTracker{r: strings.NewReader(`{"name":"a"}`)}
			rec := serve(router, postJSON("/decode", body, contentType...))

			assertDecodeError(t, rec, logs, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, nil)
			if body.read {
				t.Error("the body was read despite an unsupported Content-Type")
			}
		})
	}
}

func TestDecodeJSONBodyLimit(t *testing.T) {
	// padded returns a valid JSON object padded with spaces to n bytes.
	padded := func(n int) string { return "{}" + strings.Repeat(" ", n-2) }

	t.Run("exactly the limit", func(t *testing.T) {
		router, _ := decodeRouter(t)
		rec := serve(router, postJSON("/decode", strings.NewReader(padded(MaxJSONBodyBytes))))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("one byte over", func(t *testing.T) {
		router, logs := decodeRouter(t)
		rec := serve(router, postJSON("/decode", strings.NewReader(padded(MaxJSONBodyBytes+1))))
		assertDecodeError(t, rec, logs, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, nil)
	})

	t.Run("declared length over, not read", func(t *testing.T) {
		router, logs := decodeRouter(t)
		body := &readTracker{r: strings.NewReader(`{}`)}
		req := postJSON("/decode", body)
		req.ContentLength = MaxJSONBodyBytes + 1
		rec := serve(router, req)

		assertDecodeError(t, rec, logs, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, nil)
		if body.read {
			t.Error("the body was read despite a declared oversized length")
		}
	})

	for _, tt := range []struct {
		name       string
		size       int
		wantStatus int
	}{
		{"chunked at the limit", MaxJSONBodyBytes, http.StatusOK},
		{"chunked one byte over", MaxJSONBodyBytes + 1, http.StatusRequestEntityTooLarge},
		{"chunked far over", 4 * MaxJSONBodyBytes, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router, logs := decodeRouter(t)
			// Hide the length, as a chunked request does.
			req := postJSON("/decode", io.MultiReader(strings.NewReader(padded(tt.size))))
			req.ContentLength = -1
			rec := serve(router, req)

			if tt.wantStatus == http.StatusOK {
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}
				return
			}
			assertDecodeError(t, rec, logs, tt.wantStatus, CodePayloadTooLarge, nil)
		})
	}
}

func TestDecodeJSONRejectsBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantCode    Code
		wantDetails []Detail
	}{
		{"empty", ``, 400, CodeBadRequest, detail(detailEmptyBody)},
		{"whitespace only", " \n\t ", 400, CodeBadRequest, detail(detailEmptyBody)},
		{"truncated", `{"name":`, 400, CodeBadRequest, detail(detailMalformedJSON)},
		{"missing colon", `{"name" 1}`, 400, CodeBadRequest, detail(detailMalformedJSON)},
		{"bad literal", `nul`, 400, CodeBadRequest, detail(detailMalformedJSON)},
		{"two values", `{} {}`, 400, CodeBadRequest, detail(detailTrailingData)},
		{"trailing garbage", `{} x`, 400, CodeBadRequest, detail(detailTrailingData)},
		{"trailing bracket", `{}]`, 400, CodeBadRequest, detail(detailTrailingData)},
		{"null", `null`, 400, CodeBadRequest, detail(detailNullBody)},
		{"null with whitespace", "  null \n", 400, CodeBadRequest, detail(detailNullBody)},

		{"top-level array", `[]`, 400, CodeBadRequest, detail(detailInvalidType)},
		{"top-level string", `"s"`, 400, CodeBadRequest, detail(detailInvalidType)},
		{"top-level number", `1`, 400, CodeBadRequest, detail(detailInvalidType)},

		{"unknown field", `{"zz":1}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"nested unknown field", `{"inner":{"zz":1}}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"unknown field in a list", `{"list":[{"zz":1}]}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"ignored field", `{"skip":"x"}`, 400, CodeBadRequest, detail(detailUnknownField)},
		// encoding/json reports the first error in document order.
		{"unknown field then wrong type", `{"zz":1,"age":"x"}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"wrong type then unknown field", `{"age":"x","zz":1}`, 422, CodeValidationFailed,
			[]Detail{{Field: "age", Code: detailInvalidType}}},

		{"string for number", `{"name":1}`, 422, CodeValidationFailed, []Detail{{Field: "name", Code: detailInvalidType}}},
		{"fraction for int", `{"age":1.5}`, 422, CodeValidationFailed, []Detail{{Field: "age", Code: detailInvalidType}}},
		{"overflow", `{"age":1e100}`, 422, CodeValidationFailed, []Detail{{Field: "age", Code: detailInvalidType}}},
		{"pointer field", `{"opt":"x"}`, 422, CodeValidationFailed, []Detail{{Field: "opt", Code: detailInvalidType}}},
		{"embedded field", `{"id":"x"}`, 422, CodeValidationFailed, []Detail{{Field: "id", Code: detailInvalidType}}},
		{"nested", `{"inner":{"n":"x"}}`, 422, CodeValidationFailed, []Detail{{Field: "inner.n", Code: detailInvalidType}}},
		{"list element", `{"list":[{"n":1},{"n":"x"}]}`, 422, CodeValidationFailed,
			[]Detail{{Field: "list.1.n", Code: detailInvalidType}}},
		// The client's spelling is replaced by the Go type's names.
		{"client spelling", `{"INNER":{"N":"x"}}`, 422, CodeValidationFailed,
			[]Detail{{Field: "inner.n", Code: detailInvalidType}}},
		// A map key is client data, so the path stops at the map.
		{"map value", `{"tags":{"some_key":"x"}}`, 422, CodeValidationFailed,
			[]Detail{{Field: "tags", Code: detailInvalidType}}},

		{"custom UnmarshalJSON error", `{"fail":{}}`, 400, CodeBadRequest, detail(detailInvalidBody)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, logs := decodeRouter(t)
			rec := serve(router, postJSON("/decode", strings.NewReader(tt.body)))
			assertDecodeError(t, rec, logs, tt.wantStatus, tt.wantCode, tt.wantDetails)
		})
	}
}

func TestDecodeJSONUnreadableBody(t *testing.T) {
	router, logs := decodeRouter(t)
	req := postJSON("/decode", brokenReader{})
	req.ContentLength = -1
	rec := serve(router, req)

	assertDecodeError(t, rec, logs, http.StatusBadRequest, CodeBadRequest, detail(detailUnreadableBody))
}

func TestDecodeJSONInvalidDestination(t *testing.T) {
	for _, path := range []string{"/non-pointer", "/nil-pointer"} {
		t.Run(path, func(t *testing.T) {
			router, logs := decodeRouter(t)
			body := &readTracker{r: strings.NewReader(`{"name":"a"}`)}
			rec := serve(router, postJSON(path, body))

			assertDecodeError(t, rec, logs, http.StatusInternalServerError, CodeInternal, nil)
			if body.read {
				t.Error("the body was read before the destination was validated")
			}
			if entry := singleEntry(t, logs); entry["error_code"] != string(CodeInternal) {
				t.Errorf("error_code = %v, want internal_error", entry["error_code"])
			}
		})
	}
}

func TestDecodeJSONNeverDisclosesBody(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		size int // when set, a chunked body of this many bytes
	}{
		{"unknown field", "/decode", `{"name":"sentinel_value","sentinel_unknown":1}`, 0},
		{"map key", "/decode", `{"tags":{"sentinel_key":"sentinel_value"}}`, 0},
		{"client spelling", "/decode", `{"INNER":{"N":"sentinel_value"}}`, 0},
		{"custom UnmarshalJSON", "/decode", `{"fail":{"sentinel_member":1}}`, 0},
		{"malformed", "/decode", `{"sentinel_malformed`, 0},
		{"trailing", "/decode", `{} "sentinel_trailing"`, 0},
		{"top-level type", "/decode", `"sentinel_string"`, 0},
		{"oversized", "/decode", "", MaxJSONBodyBytes + 1},
		{"invalid destination", "/non-pointer", `{"name":"sentinel_value"}`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, logs := decodeRouter(t)

			var body io.Reader = strings.NewReader(tt.body)
			if tt.size > 0 {
				body = io.MultiReader(strings.NewReader(`{"name":"sentinel_value"`), strings.NewReader(strings.Repeat(" ", tt.size)))
			}
			req := postJSON(tt.path, body)
			if tt.size > 0 {
				req.ContentLength = -1
			}
			rec := serve(router, req)

			if rec.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, want an error", rec.Code)
			}
			var headers strings.Builder
			for name, values := range rec.Header() {
				headers.WriteString(name + ": " + strings.Join(values, ",") + "\n")
			}
			for where, text := range map[string]string{
				"body": rec.Body.String(), "headers": headers.String(), "log": logs.String(),
			} {
				if strings.Contains(text, "sentinel") {
					t.Errorf("response %s contains request body content: %s", where, text)
				}
			}
		})
	}
}

func TestDecodeJSONWithoutOrigin(t *testing.T) {
	// The error path still carries the request ID and security headers when
	// CORS doesn't apply.
	router, _ := decodeRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader(`{"zz":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(router, req)

	if rec.Code != http.StatusBadRequest || rec.Header().Get(RequestIDHeader) == "" {
		t.Errorf("status = %d, request ID = %q", rec.Code, rec.Header().Get(RequestIDHeader))
	}
	assertSecurityHeaders(t, rec.Header())
	if got := rec.Header().Get(headerAccessControlAllowOrigin); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q without an Origin", got)
	}
}

type pathNamed struct {
	Code int
}

type pathTarget struct {
	decodeBase
	*pathNamed
	Inner  decodeInner              `json:"inner"`
	Plain  int                      `json:",omitempty"`
	Arr    [2]decodeInner           `json:"arr"`
	PP     **decodeInner            `json:"pp"`
	Nested map[string][]decodeInner `json:"nested"`
	Skip   int                      `json:"-"`
	hidden int                      // unexported: not a JSON field
	Deep   []map[string]decodeInner `json:"deep"`
	Self   *pathTarget              `json:"self"`
	Matrix [][]decodeInner          `json:"matrix"`
	Iface  any                      `json:"iface"`
	Fn     func()                   `json:"-"`
	Ptrs   []*decodeInner           `json:"ptrs"`
	Shadow struct {
		decodeBase
		ID string `json:"id"`
	} `json:"shadow"`
}

// longName is a 120-byte JSON name for the path cap test.
var longName = strings.Repeat("a", 120)

type longPath struct {
	Next []longPath `json:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`
}

// pathTokens splits a dotted path into JSON Pointer tokens.
func pathTokens(path string) []string {
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

func TestSafeFieldPath(t *testing.T) {
	target := reflect.TypeFor[pathTarget]()

	tests := []struct {
		path string
		want string
	}{
		{"inner.n", "inner.n"},
		{"INNER.N", "inner.n"},
		{"Inner.n", "inner.n"},
		{"id", "id"},
		{"ID", "id"},
		{"Code", "Code"},
		{"code", "Code"},
		{"Plain", "Plain"},
		{"plain", "Plain"},
		{"arr.1.n", "arr.1.n"},
		{"arr.x", "arr"},
		{"pp.n", "pp.n"},
		{"nested.secret_key.0.n", "nested"},
		{"deep.0.secret_key.n", "deep.0"},
		{"self.self.inner.N", "self.self.inner.n"},
		{"matrix.1.2.n", "matrix.1.2.n"},
		{"iface.x", "iface"},
		{"ptrs.4.n", "ptrs.4.n"},
		{"shadow.id", "shadow.id"},
		// The decoder folds case but, under v1 options, not dashes or
		// underscores.
		{"in_ner.n", ""},
		{"in-ner.n", ""},
		{"skip", ""},
		{"Skip", ""},
		{"hidden", ""},
		{"unknown", ""},
		{"inner.unknown", "inner"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := safeFieldPath(target, pathTokens(tt.path)); got != tt.want {
			t.Errorf("safeFieldPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSafeFieldPathCap(t *testing.T) {
	long := reflect.TypeFor[longPath]()
	tokens := []string{longName, "0", longName, "0", longName}

	// The third name would exceed maxFieldPathBytes, so it is dropped.
	want := strings.Join([]string{longName, "0", longName, "0"}, ".")
	if got := safeFieldPath(long, tokens); got != want {
		t.Errorf("safeFieldPath = %d bytes, want %d", len(got), len(want))
	}
	if got := safeFieldPath(long, tokens); len(got) > maxFieldPathBytes {
		t.Errorf("safeFieldPath = %d bytes, over %d", len(got), maxFieldPathBytes)
	}
}

func TestDecodeJSONFirstErrorInDocumentOrder(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantCode    Code
		wantDetails []Detail
	}{
		{"custom error then wrong type", `{"fail":{},"age":"x"}`, 400, CodeBadRequest, detail(detailInvalidBody)},
		{"wrong type then custom error", `{"age":"x","fail":{}}`, 422, CodeValidationFailed,
			[]Detail{{Field: "age", Code: detailInvalidType}}},
		{"custom error then unknown field", `{"fail":{},"zz":1}`, 400, CodeBadRequest, detail(detailInvalidBody)},
		{"unknown field then custom error", `{"zz":1,"fail":{}}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"unknown field then wrong type", `{"zz":1,"age":"x"}`, 400, CodeBadRequest, detail(detailUnknownField)},
		{"wrong type then unknown field", `{"age":"x","zz":1}`, 422, CodeValidationFailed,
			[]Detail{{Field: "age", Code: detailInvalidType}}},
		{"nested unknown field then custom error", `{"inner":{"zz":1},"fail":{}}`, 400, CodeBadRequest,
			detail(detailUnknownField)},
		{"unknown field in a list then custom error", `{"list":[{"n":1},{"zz":1}],"fail":{}}`, 400, CodeBadRequest,
			detail(detailUnknownField)},
		{"custom error then nested unknown field", `{"fail":{},"inner":{"zz":1}}`, 400, CodeBadRequest,
			detail(detailInvalidBody)},
		{"wrong type in a list then unknown field", `{"list":[{"n":"x"}],"zz":1}`, 422, CodeValidationFailed,
			[]Detail{{Field: "list.0.n", Code: detailInvalidType}}},
		{"wrong map value then unknown field", `{"tags":{"k":{}},"zz":1}`, 422, CodeValidationFailed,
			[]Detail{{Field: "tags", Code: detailInvalidType}}},
		// A custom UnmarshalJSON receives its value whole; its keys are not
		// fields of dst.
		{"unknown key inside a custom value", `{"fail":{"zz":1}}`, 400, CodeBadRequest, detail(detailInvalidBody)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, logs := decodeRouter(t)
			rec := serve(router, postJSON("/decode", strings.NewReader(tt.body)))

			assertDecodeError(t, rec, logs, tt.wantStatus, tt.wantCode, tt.wantDetails)
			for where, text := range map[string]string{"body": rec.Body.String(), "log": logs.String()} {
				if strings.Contains(text, "sentinel") || strings.Contains(text, "zz") {
					t.Errorf("response %s discloses the custom error or a key: %s", where, text)
				}
			}
		})
	}
}

// Field-selection test types. Each int field is a distinct leaf, so the
// field the decoder selects can be found from the decoded value.
type (
	selA       struct{ X int }
	selB       struct{ X int }
	selTaggedA struct {
		X int `json:"X"`
	}
	selDeep   struct{ selA }
	selHidden struct{ H int }
	SelPtr    struct{ P int }

	// Two untagged X at the same depth: ambiguous, dropped.
	selSameDepth struct {
		selA
		selB
	}
	// A tagged X beats an untagged X at the same depth.
	selTaggedWins struct {
		selTaggedA
		selB
	}
	// A shallower X shadows a deeper one.
	selShallow struct {
		X int
		selDeep
	}
	// An exact name wins; a name that folds to both fields is ambiguous.
	selCase struct {
		Lower int `json:"name"`
		Upper int `json:"NAME"`
	}
	selMisc struct {
		selHidden
		Skip int `json:"-"`
		Dash int `json:"-,omitempty"`
		Omit int `json:",omitempty"`
		*SelPtr
	}
	// Folding keeps dashes and underscores; case:strict disables folding.
	selDelimiter struct {
		FooBar int
		Strict int `json:"strict,case:strict"`
	}
	selNested struct {
		Inner selA            `json:"inner"`
		Ptr   *selA           `json:"ptr"`
		List  []selA          `json:"list"`
		Arr   [2]selA         `json:"arr"`
		Map   map[string]selA `json:"map"`
	}
)

// SelTaggedOne and SelTaggedTwo both tag X as "X".
type (
	SelTaggedOne struct {
		X int `json:"X"`
	}
	SelTaggedTwo struct {
		X int `json:"X"`
	}
)

// selBothTagged embeds SelTaggedOne and SelTaggedTwo: two tagged X at the
// same depth, which the decoder treats as ambiguous and drops. It is built at
// run time because go vet rejects the duplicate tag in a literal type, which
// is exactly the conflict under test.
var selBothTagged = reflect.StructOf([]reflect.StructField{
	{Name: "SelTaggedOne", Type: reflect.TypeFor[SelTaggedOne](), Anonymous: true},
	{Name: "SelTaggedTwo", Type: reflect.TypeFor[SelTaggedTwo](), Anonymous: true},
})

// decodedIndex decodes {key: 7} into a new value of typ with DecodeJSON's
// decoder and options, and returns the index path of the int field that
// received it.
func decodedIndex(t *testing.T, typ reflect.Type, key string) ([]int, error) {
	t.Helper()

	body, err := json.Marshal(map[string]int{key: 7})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	v := reflect.New(typ)
	if err := jsonv2.Unmarshal(body, v.Interface(), decodeOptions...); err != nil {
		return nil, err
	}

	var found []int
	var walk func(v reflect.Value, index []int)
	walk = func(v reflect.Value, index []int) {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return
		}
		for i := range v.NumField() {
			fv, idx := v.Field(i), append(slices.Clone(index), i)
			if fv.Kind() == reflect.Int && fv.Int() == 7 {
				found = idx
			}
			walk(fv, idx)
		}
	}
	walk(v.Elem(), nil)
	return found, nil
}

func TestJSONFieldSelectionMatchesDecoder(t *testing.T) {
	tests := []struct {
		typ      reflect.Type
		key      string
		wantName string // "" means the decoder selects no field
	}{
		{reflect.TypeFor[selSameDepth](), "X", ""},
		{reflect.TypeFor[selSameDepth](), "x", ""},
		{reflect.TypeFor[selTaggedWins](), "X", "X"},
		{reflect.TypeFor[selTaggedWins](), "x", "X"},
		{reflect.TypeFor[selShallow](), "X", "X"},
		{reflect.TypeFor[selShallow](), "x", "X"},
		{selBothTagged, "X", ""},
		{selBothTagged, "x", ""},
		{reflect.TypeFor[selCase](), "name", "name"},
		{reflect.TypeFor[selCase](), "NAME", "NAME"},
		{reflect.TypeFor[selCase](), "Name", ""},
		{reflect.TypeFor[selCase](), "nAmE", ""},
		{reflect.TypeFor[selMisc](), "H", "H"},
		{reflect.TypeFor[selMisc](), "h", "H"},
		{reflect.TypeFor[selMisc](), "Skip", ""},
		{reflect.TypeFor[selMisc](), "skip", ""},
		{reflect.TypeFor[selMisc](), "-", "-"},
		{reflect.TypeFor[selMisc](), "Omit", "Omit"},
		{reflect.TypeFor[selMisc](), "omit", "Omit"},
		{reflect.TypeFor[selMisc](), "P", "P"},
		{reflect.TypeFor[selMisc](), "p", "P"},
		{reflect.TypeFor[selMisc](), "selHidden", ""},
		{reflect.TypeFor[selMisc](), "SelPtr", ""},
		{reflect.TypeFor[selDelimiter](), "FooBar", "FooBar"},
		{reflect.TypeFor[selDelimiter](), "foobar", "FooBar"},
		{reflect.TypeFor[selDelimiter](), "foo_bar", ""},
		{reflect.TypeFor[selDelimiter](), "FOO-BAR", ""},
		{reflect.TypeFor[selDelimiter](), "strict", "strict"},
		{reflect.TypeFor[selDelimiter](), "STRICT", ""},
		{reflect.TypeFor[selDelimiter](), "Strict", ""},
	}

	for _, tt := range tests {
		t.Run(tt.typ.String()+"/"+tt.key, func(t *testing.T) {
			// The field the decoder fills is the one lookup selects; when
			// the decoder rejects the name, lookup selects nothing.
			index, decodeErr := decodedIndex(t, tt.typ, tt.key)
			f, ok := cachedJSONFields(tt.typ).lookup(tt.key)
			switch {
			case decodeErr != nil && ok:
				t.Fatalf("lookup selected %s %v; the decoder rejects the name: %v", f.name, f.index, decodeErr)
			case decodeErr == nil && (index == nil || !ok || !slices.Equal(f.index, index)):
				t.Fatalf("lookup selected %v (%v); the decoder fills %v", f.index, ok, index)
			}
			if got := f.name; got != tt.wantName {
				t.Errorf("selected name = %q, want %q", got, tt.wantName)
			}

			// A real type error's path sanitizes to the selected field's
			// name; an unselected name is never reported.
			body := []byte(`{"` + tt.key + `":"wrong"}`)
			err := jsonv2.Unmarshal(body, reflect.New(tt.typ).Interface(), decodeOptions...)
			var semantic *jsonv2.SemanticError
			if errors.As(err, &semantic) && isTypeMismatch(semantic) {
				got := safeFieldPath(tt.typ, slices.Collect(semantic.JSONPointer.Tokens()))
				if got != tt.wantName {
					t.Errorf("safeFieldPath(%q) = %q, want %q", semantic.JSONPointer, got, tt.wantName)
				}
			} else if tt.wantName != "" {
				t.Errorf("no type error for a selected field: %v", err)
			}
			if tt.wantName == "" {
				if got := safeFieldPath(tt.typ, []string{tt.key}); got != "" {
					t.Errorf("safeFieldPath(%q) = %q for an unselected name, want empty", tt.key, got)
				}
			}
		})
	}
}

// TestSafeFieldPathFromDecoderErrors sanitizes the JSON Pointer of real
// decoder type errors, through pointers, slices, arrays and maps.
func TestSafeFieldPathFromDecoderErrors(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		body string
		want string
	}{
		{reflect.TypeFor[selNested](), `{"inner":{"X":"w"}}`, "inner.X"},
		{reflect.TypeFor[selNested](), `{"INNER":{"x":"w"}}`, "inner.X"},
		{reflect.TypeFor[selNested](), `{"ptr":{"x":"w"}}`, "ptr.X"},
		{reflect.TypeFor[selNested](), `{"list":[{"X":1},{"x":"w"}]}`, "list.1.X"},
		{reflect.TypeFor[selNested](), `{"arr":[{},{"X":"w"}]}`, "arr.1.X"},
		{reflect.TypeFor[selNested](), `{"map":{"secret_key":{"X":"w"}}}`, "map"},
		{reflect.TypeFor[selNested](), `{"map":{"a/b~c":{"X":"w"}}}`, "map"},
		{reflect.TypeFor[selTaggedWins](), `{"x":"w"}`, "X"},
		{reflect.TypeFor[selShallow](), `{"x":"w"}`, "X"},
		{reflect.TypeFor[pathTarget](), `{"pp":{"N":"w"}}`, "pp.n"},
		{reflect.TypeFor[pathTarget](), `{"ptrs":[null,{"n":"w"}]}`, "ptrs.1.n"},
		{reflect.TypeFor[pathTarget](), `{"matrix":[[{"n":"w"}]]}`, "matrix.0.0.n"},
		{reflect.TypeFor[pathTarget](), `{"self":{"Self":{"INNER":{"n":"w"}}}}`, "self.self.inner.n"},
		{reflect.TypeFor[pathTarget](), `{"deep":[{"secret_key":{"n":"w"}}]}`, "deep.0"},
		{reflect.TypeFor[pathTarget](), `{"ID":"w"}`, "id"},
		{reflect.TypeFor[pathTarget](), `{"shadow":{"id":1}}`, "shadow.id"},
	}

	for _, tt := range tests {
		t.Run(tt.typ.String()+tt.body, func(t *testing.T) {
			err := jsonv2.Unmarshal([]byte(tt.body), reflect.New(tt.typ).Interface(), decodeOptions...)
			var semantic *jsonv2.SemanticError
			if !errors.As(err, &semantic) || !isTypeMismatch(semantic) {
				t.Fatalf("decode error is not a type mismatch: %v", err)
			}
			if got := safeFieldPath(tt.typ, slices.Collect(semantic.JSONPointer.Tokens())); got != tt.want {
				t.Errorf("safeFieldPath(%q) = %q, want %q", semantic.JSONPointer, got, tt.want)
			}
		})
	}
}

// selBadTag's malformed tag makes the whole struct invalid for the decoder.
type selBadTag struct {
	Name int `json:"name,"`
}

// TestDecodeErrorStructDefinition: the decoder rejects every object for a
// struct with an invalid definition; that is an invalid body, never a field
// type error, so no field path from the struct is reported.
func TestDecodeErrorStructDefinition(t *testing.T) {
	err := jsonv2.Unmarshal([]byte(`{"name":"wrong"}`), new(selBadTag), decodeOptions...)
	if err == nil {
		t.Fatal("the decoder accepted a struct with a malformed json tag")
	}

	var apiErr *Error
	if !errors.As(decodeError(reflect.TypeFor[selBadTag](), err), &apiErr) ||
		apiErr.Code != CodeBadRequest || !reflect.DeepEqual(apiErr.Details, detail(detailInvalidBody)) {
		t.Errorf("decodeError = %+v, want 400 invalid_body", apiErr)
	}
}

func TestJSONFieldsShadowing(t *testing.T) {
	type shadowed struct {
		decodeBase
		ID string `json:"id"`
	}

	var ids []jsonField
	for _, f := range cachedJSONFields(reflect.TypeFor[shadowed]()).list {
		if f.name == "id" {
			ids = append(ids, f)
		}
	}
	if len(ids) != 1 || ids[0].typ != reflect.TypeFor[string]() {
		t.Errorf("id fields = %v, want only the outer string field", ids)
	}
}
