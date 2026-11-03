package database

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kind is the category of a database error.
type Kind uint8

const (
	// KindUnknown is any error that is not recognized. It is not marked
	// retryable.
	KindUnknown Kind = iota
	// KindNotFound is pgx.ErrNoRows.
	KindNotFound
	// KindConflict is an integrity conflict the client can resolve.
	KindConflict
	// KindContention is a serialization failure, deadlock or unavailable lock.
	KindContention
	// KindUnavailable is a connection, startup, shutdown, resource or
	// authentication failure.
	KindUnavailable
	// KindTimeout is a deadline or statement_timeout.
	KindTimeout
	// KindCanceled is a cancellation by the caller.
	KindCanceled
)

var kindNames = [...]string{
	KindUnknown:     "unknown",
	KindNotFound:    "not_found",
	KindConflict:    "conflict",
	KindContention:  "contention",
	KindUnavailable: "unavailable",
	KindTimeout:     "timeout",
	KindCanceled:    "canceled",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Classification describes a database error.
type Classification struct {
	Kind Kind
	// Retryable reports that retrying the whole operation unchanged may
	// succeed and is known not to duplicate effects. Nothing retries
	// automatically.
	Retryable bool
	// SQLState is the server's SQLSTATE, or "" when the error holds no
	// *pgconn.PgError with a well-formed code.
	SQLState string
}

// SQLSTATE codes, from PostgreSQL 17 documentation, Appendix A
// ("PostgreSQL Error Codes"). pgx ships no constants for them.
const (
	sqlstateConnectionException                        = "08000"
	sqlstateSQLClientUnableToEstablishSQLConnection    = "08001"
	sqlstateConnectionDoesNotExist                     = "08003"
	sqlstateSQLServerRejectedEstablishmentOfConnection = "08004"
	sqlstateConnectionFailure                          = "08006"
	sqlstateTransactionResolutionUnknown               = "08007"
	sqlstateRestrictViolation                          = "23001"
	sqlstateForeignKeyViolation                        = "23503"
	sqlstateUniqueViolation                            = "23505"
	sqlstateExclusionViolation                         = "23P01"
	sqlstateInvalidAuthorizationSpecification          = "28000"
	sqlstateInvalidPassword                            = "28P01"
	sqlstateInvalidCatalogName                         = "3D000"
	sqlstateSerializationFailure                       = "40001"
	sqlstateStatementCompletionUnknown                 = "40003"
	sqlstateDeadlockDetected                           = "40P01"
	sqlstateInsufficientResources                      = "53000"
	sqlstateDiskFull                                   = "53100"
	sqlstateOutOfMemory                                = "53200"
	sqlstateTooManyConnections                         = "53300"
	sqlstateConfigurationLimitExceeded                 = "53400"
	sqlstateLockNotAvailable                           = "55P03"
	sqlstateQueryCanceled                              = "57014"
	sqlstateAdminShutdown                              = "57P01"
	sqlstateCrashShutdown                              = "57P02"
	sqlstateCannotConnectNow                           = "57P03"
	sqlstateDatabaseDropped                            = "57P04"
	sqlstateIdleSessionTimeout                         = "57P05"
)

// retryRule says when a SQLSTATE is retryable.
type retryRule uint8

const (
	retryNever retryRule = iota
	// retryAlways: PostgreSQL aborted the transaction (ERROR level, raised
	// before commit), or no client statement was executing.
	retryAlways
	// retryDuringConnect: retryable only when a *pgconn.ConnectError in the
	// chain proves the error came from session startup, before any query.
	retryDuringConnect
)

type sqlstateRule struct {
	kind  Kind
	retry retryRule
}

// sqlstateRules lists every recognized SQLSTATE. Codes are matched exactly;
// unlisted codes, including other codes of a listed class, are KindUnknown.
var sqlstateRules = map[string]sqlstateRule{
	sqlstateSerializationFailure: {KindContention, retryAlways},
	sqlstateDeadlockDetected:     {KindContention, retryAlways},
	sqlstateLockNotAvailable:     {KindContention, retryAlways},

	// Raised only while the session is idle outside a transaction.
	sqlstateIdleSessionTimeout: {KindUnavailable, retryAlways},

	sqlstateCannotConnectNow:   {KindUnavailable, retryDuringConnect},
	sqlstateTooManyConnections: {KindUnavailable, retryDuringConnect},

	// These can be raised after a commit became durable, or their outcome
	// is unknown, so retrying could duplicate effects.
	sqlstateAdminShutdown:                              {KindUnavailable, retryNever},
	sqlstateCrashShutdown:                              {KindUnavailable, retryNever},
	sqlstateInsufficientResources:                      {KindUnavailable, retryNever},
	sqlstateOutOfMemory:                                {KindUnavailable, retryNever},
	sqlstateConnectionException:                        {KindUnavailable, retryNever},
	sqlstateSQLClientUnableToEstablishSQLConnection:    {KindUnavailable, retryNever},
	sqlstateConnectionDoesNotExist:                     {KindUnavailable, retryNever},
	sqlstateSQLServerRejectedEstablishmentOfConnection: {KindUnavailable, retryNever},
	sqlstateConnectionFailure:                          {KindUnavailable, retryNever},
	sqlstateTransactionResolutionUnknown:               {KindUnavailable, retryNever},
	sqlstateStatementCompletionUnknown:                 {KindUnavailable, retryNever},

	// Retrying won't help without operator action.
	sqlstateDiskFull:                          {KindUnavailable, retryNever},
	sqlstateConfigurationLimitExceeded:        {KindUnavailable, retryNever},
	sqlstateInvalidAuthorizationSpecification: {KindUnavailable, retryNever},
	sqlstateInvalidPassword:                   {KindUnavailable, retryNever},
	sqlstateInvalidCatalogName:                {KindUnavailable, retryNever},
	sqlstateDatabaseDropped:                   {KindUnavailable, retryNever},

	// statement_timeout or an administrator's cancel. A cancellation by the
	// caller is classified by its context error first.
	sqlstateQueryCanceled: {KindTimeout, retryNever},

	sqlstateUniqueViolation:     {KindConflict, retryNever},
	sqlstateForeignKeyViolation: {KindConflict, retryNever},
	sqlstateExclusionViolation:  {KindConflict, retryNever},
	sqlstateRestrictViolation:   {KindConflict, retryNever},
}

// Classify reports the kind of err and whether it is safe to retry. It
// inspects err's chain, never its text, and never wraps or changes it. The
// first matching rule wins:
//
//  0. A chain holding a typed-nil error or a cycle, or more than
//     maxChainErrors errors: unknown.
//  1. context.Canceled: canceled.
//  2. A deadline or network timeout: timeout.
//  3. A *pgconn.PgError: its SQLSTATE's rule.
//  4. pgx.ErrNoRows: not found.
//  5. A *pgconn.ConnectError, pgconn.ErrConnClosed or an error pgx reports
//     as safe to retry: unavailable.
//  6. Anything else: unknown.
//
// Without a SQLSTATE rule, an error is retryable only when it provably
// happened before the operation reached the server: a *pgconn.ConnectError
// with no *pgconn.PgError in the chain, or pgconn.SafeToRetry. In chains
// joined with errors.Join, each check takes the first match, depth first.
func Classify(err error) Classification {
	if err == nil {
		return Classification{}
	}

	// errors.Is and errors.As may panic on a nil receiver and never return
	// on a cycle, so they only run on chains walkChain accepts.
	if !walkChain(err) {
		return Classification{Kind: KindUnknown}
	}

	if errors.Is(err, context.Canceled) {
		return Classification{Kind: KindCanceled}
	}

	if isTimeout(err) {
		return Classification{Kind: KindTimeout, Retryable: sentNothing(err)}
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if !validSQLState(pgErr.Code) {
			return Classification{Kind: KindUnknown}
		}

		c := Classification{Kind: KindUnknown, SQLState: pgErr.Code}
		if rule, ok := sqlstateRules[pgErr.Code]; ok {
			c.Kind = rule.kind
			switch rule.retry {
			case retryAlways:
				c.Retryable = true
			case retryDuringConnect:
				c.Retryable = isConnectError(err)
			}
		}
		return c
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return Classification{Kind: KindNotFound}
	}

	// No PgError is in the chain past this point.
	if isConnectError(err) {
		return Classification{Kind: KindUnavailable, Retryable: true}
	}

	if errors.Is(err, pgconn.ErrConnClosed) || pgconn.SafeToRetry(err) {
		return Classification{Kind: KindUnavailable, Retryable: pgconn.SafeToRetry(err)}
	}

	return Classification{Kind: KindUnknown}
}

// IsRetryable reports Classify(err).Retryable.
func IsRetryable(err error) bool {
	return Classify(err).Retryable
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isConnectError(err error) bool {
	var connectErr *pgconn.ConnectError
	return errors.As(err, &connectErr)
}

// sentNothing reports whether err provably happened before the operation
// reached the server.
func sentNothing(err error) bool {
	var pgErr *pgconn.PgError
	if isConnectError(err) && !errors.As(err, &pgErr) {
		return true
	}
	return pgconn.SafeToRetry(err)
}

// validSQLState reports whether code has the SQLSTATE form: five digits or
// uppercase ASCII letters.
func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for i := range len(code) {
		if c := code[i]; (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// maxChainErrors caps how many errors walkChain visits: each distinct
// identified error once, and each error without an identity (non-pointer
// values) every time it is reached, since a repeat can't be recognized.
// This bound is what ends an endless chain of the latter.
const maxChainErrors = 256

// errorID identifies an error by its dynamic type and address. It never
// compares the error itself, whose dynamic value may not be comparable.
type errorID struct {
	typ reflect.Type
	ptr uintptr
}

// identity returns err's errorID when its kind has a stable address.
// Funcs and slices are excluded: distinct closures can share a code
// pointer, and distinct slices a backing array.
func identity(v reflect.Value) (errorID, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		return errorID{typ: v.Type(), ptr: v.Pointer()}, true
	default:
		return errorID{}, false
	}
}

// walkChain reports whether errors.Is and errors.As can safely walk err's
// chain through Unwrap() error and Unwrap() []error: it holds no nil value
// of a nil-able kind, no cycle, and at most maxChainErrors errors as counted
// there. An error
// reached twice by different paths (as errors.Join can share one) is not a
// cycle; only one reached again from itself is. It calls no method other
// than Unwrap.
func walkChain(err error) bool {
	onPath := make(map[errorID]bool)
	done := make(map[errorID]bool)
	visited := 0

	var walk func(error) bool
	walk = func(err error) bool {
		if err == nil {
			return true
		}

		v := reflect.ValueOf(err)
		switch v.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
			reflect.Pointer, reflect.Slice:
			if v.IsNil() {
				return false
			}
		}

		// An identified error is counted once, the first time it is
		// reached; one without identity is counted every time.
		id, identified := identity(v)
		if identified {
			if onPath[id] {
				return false
			}
			if done[id] {
				return true
			}
		}
		if visited++; visited > maxChainErrors {
			return false
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
				if !walk(inner) {
					return false
				}
			}
		}
		return true
	}

	return walk(err)
}
