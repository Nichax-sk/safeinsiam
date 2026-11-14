package httpapi

import (
	"bytes"
	"cmp"
	"encoding"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// MaxJSONBodyBytes is the shared request body limit for DecodeJSON.
const MaxJSONBodyBytes = 1 << 20

// maxFieldPathBytes caps the field path reported for a wrong field type.
const maxFieldPathBytes = 256

// Detail codes for request body errors.
const (
	detailUnreadableBody = "unreadable_body"
	detailEmptyBody      = "empty_body"
	detailMalformedJSON  = "malformed_json"
	detailTrailingData   = "trailing_data"
	detailNullBody       = "null_body"
	detailUnknownField   = "unknown_field"
	detailInvalidType    = "invalid_type"
	detailInvalidBody    = "invalid_body"
)

var nullJSON = []byte("null")

// decodeOptions decode with encoding/json v1 semantics, but report typed
// v2 errors (so an unknown field is jsonv2.ErrUnknownName rather than an
// untyped v1 error) and reject unknown fields. With these options the
// decoder stops at the first error in document order.
//
// encoding/json/v2 builds only with the jsonv2 experiment, which is on by
// default in the Go version this module requires; with it disabled, this
// package fails to compile rather than falling back.
var decodeOptions = []jsonv2.Options{
	json.DefaultOptionsV1(),
	json.ReportErrorsWithLegacySemantics(false),
	jsonv2.RejectUnknownMembers(true),
}

// DecodeJSON reads the request body as exactly one JSON value into dst,
// which must be a non-nil pointer. It returns nil, or an error to pass to
// WriteError:
//
//   - 415 unsupported_media_type: the single Content-Type is not
//     application/json, or has a parameter other than charset=utf-8.
//   - 413 payload_too_large: the body exceeds MaxJSONBodyBytes.
//   - 400 bad_request: an unreadable, empty or malformed body, more than one
//     value or trailing data, a top-level null, a top-level value of the
//     wrong type, an unknown field, or any other decode error, such as a
//     custom UnmarshalJSON error.
//   - 422 validation_failed: a field of the wrong JSON type.
//   - 500 internal_error: dst is not a non-nil pointer, checked before the
//     body is read.
//
// When the body has several problems, the first in document order decides.
// Responses use the code's default message and a fixed detail code, never
// body content or decoder error text. A field path is reported only for a
// wrong field type, and only as names from dst's Go type, so client-chosen
// keys are never echoed. On error, dst may be partly written; don't use it.
func DecodeJSON(c *gin.Context, dst any) error {
	target := reflect.ValueOf(dst)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("httpapi: DecodeJSON needs a non-nil pointer, got %T", dst)
	}

	if !isJSONContentType(c.Request.Header.Values("Content-Type")) {
		return NewError(CodeUnsupportedMediaType, "")
	}

	if c.Request.ContentLength > MaxJSONBodyBytes {
		return NewError(CodePayloadTooLarge, "")
	}

	data, err := readBody(c)
	if err != nil {
		return err
	}

	raw, err := singleValue(data)
	if err != nil {
		return err
	}

	if err := jsonv2.Unmarshal(raw, dst, decodeOptions...); err != nil {
		return decodeError(target.Type().Elem(), err)
	}
	return nil
}

// isJSONContentType reports whether values is a single application/json
// media type whose only parameter, if any, is charset=utf-8.
func isJSONContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}

	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

// readBody reads at most MaxJSONBodyBytes of the request body.
func readBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxJSONBodyBytes)
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &Error{Code: CodePayloadTooLarge, Err: err}
		}
		return nil, bodyError(CodeBadRequest, detailUnreadableBody, err)
	}
	return data, nil
}

// singleValue returns the one JSON value in data, allowing surrounding
// whitespace. It rejects an empty body, malformed JSON, trailing data and a
// top-level null.
func singleValue(data []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))

	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, bodyError(CodeBadRequest, detailEmptyBody, err)
		}
		return nil, bodyError(CodeBadRequest, detailMalformedJSON, err)
	}

	var rest json.RawMessage
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		return nil, bodyError(CodeBadRequest, detailTrailingData, err)
	}

	// raw holds the value without surrounding whitespace.
	if bytes.Equal(raw, nullJSON) {
		return nil, bodyError(CodeBadRequest, detailNullBody, nil)
	}
	return raw, nil
}

// decodeError maps a decode error by its type, never its text: an unknown
// field, a wrong JSON type (422 for a field, 400 for the top-level value),
// or anything else, such as a custom UnmarshalJSON error, an ambiguous field
// name or an invalid struct definition, as an invalid body.
func decodeError(dstType reflect.Type, err error) error {
	var semantic *jsonv2.SemanticError
	switch {
	case errors.Is(err, jsonv2.ErrUnknownName):
		return bodyError(CodeBadRequest, detailUnknownField, err)
	case !errors.As(err, &semantic) || !isTypeMismatch(semantic):
		return bodyError(CodeBadRequest, detailInvalidBody, err)
	}

	tokens := slices.Collect(semantic.JSONPointer.Tokens())
	if len(tokens) == 0 {
		return bodyError(CodeBadRequest, detailInvalidType, err)
	}
	return &Error{
		Code:    CodeValidationFailed,
		Details: []Detail{{Field: safeFieldPath(dstType, tokens), Code: detailInvalidType}},
		Err:     err,
	}
}

// isTypeMismatch reports whether err is a JSON value of the wrong type for
// its Go type. These are exactly the errors encoding/json's v1 API reports
// as *json.UnmarshalTypeError (see transformUnmarshalError): no underlying
// error, or a number that doesn't parse into a numeric Go type.
func isTypeMismatch(err *jsonv2.SemanticError) bool {
	if err.Err == nil {
		return true
	}
	if err.Err != strconv.ErrRange && err.Err != strconv.ErrSyntax || err.GoType == nil {
		return false
	}
	switch err.GoType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func bodyError(code Code, detail string, err error) *Error {
	return &Error{Code: code, Details: []Detail{{Code: detail}}, Err: err}
}

// safeFieldPath rewrites the tokens of a decoder JSON Pointer using only
// dst's Go type. The tokens are the client's own keys, in the client's
// spelling, so each one is matched to a struct field the way the decoder
// selects it and replaced by that field's JSON name, or kept if it is a
// slice or array index. The path stops at a map, whose keys are client data,
// or at any token that doesn't select exactly one field. The result is
// capped at maxFieldPathBytes by dropping trailing segments.
func safeFieldPath(t reflect.Type, tokens []string) string {
	var kept []string
	size := 0

	for _, token := range tokens {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}

		var name string
		switch t.Kind() {
		case reflect.Struct:
			f, ok := cachedJSONFields(t).lookup(token)
			if !ok {
				return strings.Join(kept, ".")
			}
			name, t = f.name, f.typ
		case reflect.Slice, reflect.Array:
			if !isDigits(token) {
				return strings.Join(kept, ".")
			}
			name, t = token, t.Elem()
		default:
			return strings.Join(kept, ".")
		}

		if len(kept) > 0 {
			size++
		}
		if size+len(name) > maxFieldPathBytes {
			break
		}
		size += len(name)
		kept = append(kept, name)
	}

	return strings.Join(kept, ".")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// The rest of this file reproduces how encoding/json/v2 selects the struct
// field for an object member name (makeStructFields and the struct
// unmarshaler in encoding/json/v2), under the flags decodeOptions sets:
// MatchCaseInsensitiveNames and MatchCaseSensitiveDelimiter on, and
// ReportErrorsWithLegacySemantics off. The decoder's internal field list is
// not exported, so differential tests in decode_test.go check this copy
// against the real decoder.

const (
	caseIgnore = 1
	caseStrict = 2
)

var (
	jsontextValueType = reflect.TypeFor[jsontext.Value]()
	allMethodTypes    = []reflect.Type{
		reflect.TypeFor[jsonv2.MarshalerTo](), reflect.TypeFor[jsonv2.Marshaler](),
		reflect.TypeFor[encoding.TextAppender](), reflect.TypeFor[encoding.TextMarshaler](),
		reflect.TypeFor[jsonv2.UnmarshalerFrom](), reflect.TypeFor[jsonv2.Unmarshaler](),
		reflect.TypeFor[encoding.TextUnmarshaler](),
	}
)

// jsonField is a struct field the decoder can select by name.
type jsonField struct {
	id      int // breadth-first order
	name    string
	hasName bool // name came from a json tag
	casing  int  // 0, caseIgnore or caseStrict
	index   []int
	typ     reflect.Type
}

// jsonFields is a struct type's selectable fields.
type jsonFields struct {
	list     []jsonField // depth-first order
	byName   map[string]*jsonField
	byFolded map[string][]*jsonField // breadth-first order
}

// lookup selects the field for an object member name as the decoder does:
// an exact name, else the only field whose name matches case-insensitively
// (strings.EqualFold, unless the field has the `case:strict` option). Two
// or more such matches are ambiguous, which the decoder reports as an error.
func (fs jsonFields) lookup(name string) (jsonField, bool) {
	if f := fs.byName[name]; f != nil {
		return *f, true
	}

	var match *jsonField
	matches := 0
	for _, f := range fs.byFolded[foldName(name)] {
		if f.casing != caseStrict && strings.EqualFold(name, f.name) {
			if match == nil {
				match = f
			}
			matches++
		}
	}
	if matches != 1 {
		return jsonField{}, false
	}
	return *match, true
}

var jsonFieldsCache sync.Map // map[reflect.Type]jsonFields

// cachedJSONFields is typeJSONFields with a cache.
func cachedJSONFields(t reflect.Type) jsonFields {
	if fs, ok := jsonFieldsCache.Load(t); ok {
		return fs.(jsonFields)
	}
	fs, _ := jsonFieldsCache.LoadOrStore(t, typeJSONFields(t))
	return fs.(jsonFields)
}

// typeJSONFields reproduces encoding/json/v2's makeStructFields for field
// selection: a breadth-first walk of embedded structs, then for each name
// the field that alone sits at the shallowest depth or is uniquely tagged;
// names with no such field are dropped.
func typeJSONFields(root reflect.Type) jsonFields {
	type queueEntry struct {
		typ           reflect.Type
		index         []int
		visitChildren bool
	}
	queue := []queueEntry{{root, nil, true}}
	seen := map[reflect.Type]bool{root: true}
	var all []jsonField

	for qi := 0; qi < len(queue); qi++ {
		qe := queue[qi]
		t := qe.typ

		for i := range t.NumField() {
			sf := t.Field(i)
			opts, ignored := parseJSONFieldOptions(sf)
			if ignored {
				continue
			}
			f := jsonField{
				name:    opts.name,
				hasName: opts.hasName,
				casing:  opts.casing,
				index:   append(slices.Clone(qe.index), i),
				typ:     sf.Type,
			}

			embed := opts.embed
			if sf.Anonymous && !opts.hasName && indirectType(sf.Type).Kind() == reflect.Struct {
				embed = true
			}
			if embed && opts.hasName {
				// `json:"name,embed"` has options beyond embed, so the
				// decoder treats it as a regular field. Other options on an
				// unnamed embedded field are dropped.
				embed = false
			}

			if embed {
				tf := indirectType(sf.Type)
				if tf.Kind() == reflect.Struct {
					if qe.visitChildren {
						queue = append(queue, queueEntry{tf, f.index, !seen[tf]})
					}
					seen[tf] = true
					continue
				}
				if !sf.IsExported() {
					continue
				}
				if tf == jsontextValueType ||
					(tf.Kind() == reflect.Map && tf.Key().Kind() == reflect.String && !implementsAny(tf.Key())) {
					continue // collects unknown members; selects no named field
				}
				// Otherwise invalid as embedded: a regular field.
			}

			if !sf.IsExported() {
				tf := indirectType(sf.Type)
				if !sf.Anonymous || tf.Kind() != reflect.Struct || implementsAny(tf) {
					continue
				}
			}
			f.id = len(all)
			all = append(all, f)
		}
	}

	slices.SortStableFunc(all, func(x, y jsonField) int {
		return cmp.Or(
			strings.Compare(x.name, y.name),
			cmp.Compare(len(x.index), len(y.index)),
			boolsCompare(!x.hasName, !y.hasName))
	})
	var fields []jsonField
	for len(all) > 0 {
		n := 1
		for n < len(all) && all[n-1].name == all[n].name {
			n++
		}
		if n == 1 || len(all[0].index) != len(all[1].index) || all[0].hasName != all[1].hasName {
			fields = append(fields, all[0]) // only a dominant field is kept
		}
		all = all[n:]
	}

	slices.SortFunc(fields, func(x, y jsonField) int { return cmp.Compare(x.id, y.id) })
	for i := range fields {
		fields[i].id = i
	}
	slices.SortFunc(fields, func(x, y jsonField) int { return slices.Compare(x.index, y.index) })

	fs := jsonFields{
		list:     fields,
		byName:   make(map[string]*jsonField, len(fields)),
		byFolded: make(map[string][]*jsonField, len(fields)),
	}
	for i := range fs.list {
		f := &fs.list[i]
		folded := foldName(f.name)
		fs.byName[f.name] = f
		fs.byFolded[folded] = append(fs.byFolded[folded], f)
	}
	for _, folded := range fs.byFolded {
		slices.SortFunc(folded, func(x, y *jsonField) int { return cmp.Compare(x.id, y.id) })
	}
	return fs
}

type jsonFieldOptions struct {
	name    string
	hasName bool
	casing  int
	embed   bool
	other   bool // any option other than the name and embed
}

// parseJSONFieldOptions reproduces encoding/json/v2's parseFieldOptions for
// the parts that affect field selection.
func parseJSONFieldOptions(sf reflect.StructField) (out jsonFieldOptions, ignored bool) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return out, true
	}
	if !sf.IsExported() && !sf.Anonymous {
		return out, true
	}

	out.name = sf.Name
	if len(tag) > 0 && !strings.HasPrefix(tag, ",") {
		n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return !strings.ContainsRune(",\\'\"`", r)
		}))
		name := tag[:n]
		var err error
		if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
			name, n, err = consumeTagOption(tag, false)
		}
		if !utf8.ValidString(name) {
			name = string([]rune(name))
		}
		if err == nil {
			out.hasName = true
			out.name = name
		}
		tag = tag[n:]
	}

	for len(tag) > 0 {
		if tag[0] == ',' {
			tag = tag[len(","):]
			if len(tag) == 0 {
				break
			}
		}

		opt, n, _ := consumeTagOption(tag, false)
		tag = tag[n:]
		switch opt {
		case "case":
			out.other = true
			if !strings.HasPrefix(tag, ":") {
				break
			}
			tag = tag[len(":"):]
			value, n, err := consumeTagOption(tag, false)
			if err != nil {
				break
			}
			tag = tag[n:]
			switch value {
			case "ignore":
				out.casing |= caseIgnore
			case "strict":
				out.casing |= caseStrict
			}
		case "embed":
			out.embed = true
		case "omitzero", "omitempty", "string":
			out.other = true
		case "format":
			out.other = true
			if !strings.HasPrefix(tag, ":") {
				break
			}
			tag = tag[len(":"):]
			if _, n, err := consumeTagOption(tag, true); err == nil {
				tag = tag[n:]
			}
		}
	}
	return out, false
}

// consumeTagOption is encoding/json/v2's consumeTagOption: it consumes a Go
// identifier or a single-quoted string, or up to the next comma on error.
func consumeTagOption(in string, allowQuoted bool) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}

	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:n], n, nil
	case r == '\'':
		if !allowQuoted {
			return in[:i], i, errors.New("quoted option not allowed")
		}
		var inEscape bool
		b := []byte{'"'}
		n := len(`'`)
		for len(in) > n {
			r, rn := utf8.DecodeRuneInString(in[n:])
			switch {
			case inEscape:
				if r == '\'' {
					b = b[:len(b)-1]
				}
				inEscape = false
			case r == '\\':
				inEscape = true
			case r == '"':
				b = append(b, '\\')
			case r == '\'':
				b = append(b, '"')
				n += len(`'`)
				out, err := strconv.Unquote(string(b))
				if err != nil {
					return in[:i], i, err
				}
				return out, n, nil
			}
			b = append(b, in[n:][:rn]...)
			n += rn
		}
		return in[:i], i, errors.New("single-quoted string not terminated")
	case len(in) == 0:
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		return in[:i], i, errors.New("invalid option")
	}
}

func isLetterOrDigit(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

func boolsCompare(x, y bool) int {
	switch {
	case !x && y:
		return -1
	case x && !y:
		return +1
	default:
		return 0
	}
}

// indirectType unwraps one level of unnamed pointer, as Go embedding allows.
func indirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t
}

func implementsAny(t reflect.Type) bool {
	for _, iface := range allMethodTypes {
		if t.Implements(iface) || reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}
	return false
}

// foldName is encoding/json/v2's foldName: it drops ASCII dashes and
// underscores, upper-cases ASCII letters, and maps other runes to one rune
// per case-folding set.
func foldName(in string) string {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		if c := in[i]; c < utf8.RuneSelf {
			if c != '_' && c != '-' {
				if 'a' <= c && c <= 'z' {
					c -= 'a' - 'A'
				}
				out = append(out, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(in[i:])
		out = utf8.AppendRune(out, foldRune(r))
		i += n
	}
	return string(out)
}

// foldRune returns the same rune for all runes in a case-folding set.
func foldRune(r rune) rune {
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}
