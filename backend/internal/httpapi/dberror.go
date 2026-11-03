package httpapi

import (
	"reflect"

	"github.com/Nichax-sk/safeinsiam/internal/database"
)

// DatabaseError maps a database error to an API error for WriteError, using
// database.Classify. The response uses the code's default message only, so
// driver text never reaches the client; err is kept as the cause.
//
// It returns nil for nil, and err unchanged when err already holds an
// *Error, so a handler's own decision wins.
func DatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if holdsAPIError(err) {
		return err
	}

	return &Error{Code: databaseCode(database.Classify(err).Kind), Err: err}
}

func databaseCode(kind database.Kind) Code {
	switch kind {
	case database.KindNotFound:
		return CodeNotFound
	case database.KindConflict:
		return CodeConflict
	case database.KindContention, database.KindUnavailable,
		database.KindTimeout, database.KindCanceled:
		return CodeServiceUnavailable
	default:
		return CodeInternal
	}
}

// maxAPIErrorSearch caps how many errors holdsAPIError visits: each
// distinct identified error once, and each error without an identity
// (non-pointer values) every time it is reached, since a repeat can't be
// recognized.
const maxAPIErrorSearch = 256

// errorID identifies an error by its dynamic type and address. It never
// compares the error itself, whose dynamic value may not be comparable.
type errorID struct {
	typ reflect.Type
	ptr uintptr
}

// identity returns v's errorID when its kind has a stable address. Funcs
// and slices are excluded: distinct closures can share a code pointer, and
// distinct slices a backing array.
func identity(v reflect.Value) (errorID, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		return errorID{typ: v.Type(), ptr: v.Pointer()}, true
	default:
		return errorID{}, false
	}
}

// holdsAPIError reports whether err's chain holds a non-nil *Error. Unlike
// errors.As, it never unwraps a nil receiver, whose methods may panic.
//
// It visits errors in errors.As order, depth first. An identified error
// already searched by another path (as errors.Join can share one) is
// skipped, since it holds no *Error. The search gives up with false at a
// cycle or after maxAPIErrorSearch errors: errors.As would never return on
// a cycle reached before an *Error, so the error is then wrapped and
// WriteError finds the wrapper first. It never compares errors, whose
// values may not be comparable.
func holdsAPIError(err error) bool {
	onPath := make(map[errorID]bool)
	done := make(map[errorID]bool)
	visited := 0

	// walk reports whether it found an *Error and whether to stop.
	var walk func(error) (found, stop bool)
	walk = func(err error) (bool, bool) {
		if isNilError(err) {
			return false, false
		}
		if _, ok := err.(*Error); ok {
			return true, true
		}

		v := reflect.ValueOf(err)
		id, identified := identity(v)
		if identified {
			if onPath[id] {
				return false, true
			}
			if done[id] {
				return false, false
			}
		}
		if visited++; visited > maxAPIErrorSearch {
			return false, true
		}
		if identified {
			onPath[id] = true
			defer func() {
				delete(onPath, id)
				done[id] = true
			}()
		}

		switch unwrapper := err.(type) {
		case interface{ Unwrap() error }:
			return walk(unwrapper.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range unwrapper.Unwrap() {
				if found, stop := walk(inner); found || stop {
					return found, stop
				}
			}
		}
		return false, false
	}

	found, _ := walk(err)
	return found
}
