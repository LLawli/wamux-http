package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/grpc/codes"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/store"
)

// Error is a response other than success. Body is written as JSON verbatim:
// WAHA answers some errors with NestJS's {message, error, statusCode} and
// others with a bare object, and clients match on both.
type Error struct {
	Status int
	Body   any
	cause  error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%d: %v", e.Status, e.cause)
	}
	return fmt.Sprintf("%d: %v", e.Status, e.Body)
}

func (e *Error) Unwrap() error { return e.cause }

// nest is NestJS's body for an exception thrown with a string.
func nest(status int, message any) *Error {
	return &Error{Status: status, Body: map[string]any{
		"message": message, "error": http.StatusText(status), "statusCode": status,
	}}
}

// ErrBadRequest is a 400; message is a string or a list of strings.
func ErrBadRequest(message any) *Error { return nest(http.StatusBadRequest, message) }

// ErrNotFound is a 404.
func ErrNotFound(message string) *Error { return nest(http.StatusNotFound, message) }

// ErrUnprocessable is a 422 with NestJS's string body.
func ErrUnprocessable(message string) *Error { return nest(http.StatusUnprocessableEntity, message) }

// ErrUnauthorized is WAHA's bare 401.
func ErrUnauthorized() *Error {
	return &Error{Status: http.StatusUnauthorized, Body: map[string]any{"message": "Unauthorized", "statusCode": 401}}
}

// ErrForbidden is WAHA's bare 403.
func ErrForbidden() *Error {
	return &Error{Status: http.StatusForbidden, Body: map[string]any{"message": "Forbidden", "statusCode": 403}}
}

// ErrNotImplemented is WAHA's "not implemented by engine" 501. why, when set,
// names what wamux lacks, ahead of WAHA's sentence.
func ErrNotImplemented(why string) *Error {
	msg := fmt.Sprintf("The method is not implemented by '%s' engine. Check the docs and try another engine: https://waha.devlike.pro/", config.Engine)
	if why != "" {
		msg = why + " " + msg
	}
	return nest(http.StatusNotImplemented, msg)
}

// ErrSessionMissing is the 422 a working-session route gives for an unknown
// session.
func ErrSessionMissing(name string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Body: map[string]any{
		"error": fmt.Sprintf("Session %q does not exist", name), "session": name,
	}}
}

// ErrSessionStatus is the 422 for a session that is not in an expected status.
func ErrSessionStatus(name, status string, expected ...string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Body: map[string]any{
		"error":    "Session status is not as expected. Try again later or restart the session",
		"session":  name,
		"status":   status,
		"expected": expected,
	}}
}

// ErrSessionNotFound is the 404 of the session CRUD routes.
func ErrSessionNotFound() *Error { return ErrNotFound("Session not found") }

// ErrInternal is a 500 in WAHA's shape, minus the request body echo (which
// would hand base64 media and secrets back to any caller that triggers it).
func ErrInternal(err error, r *http.Request) *Error {
	return &Error{Status: http.StatusInternalServerError, cause: err, Body: map[string]any{
		"statusCode": 500,
		"timestamp":  nowISO(),
		"exception":  map[string]any{"message": err.Error(), "name": "Error"},
		"request":    map[string]any{"path": r.URL.Path, "method": r.Method, "query": r.URL.Query()},
		"version":    environment(),
	}}
}

// fromError turns any handler error into a response.
func fromError(err error, r *http.Request) *Error {
	var he *Error
	if errors.As(err, &he) {
		return he
	}
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound("Not Found")
	}
	if ce, ok := core.AsError(err); ok {
		return fromCore(ce, r)
	}
	return ErrInternal(err, r)
}

// fromCore maps a failed core call. The WhatsApp server's own code, when the
// core relayed one, is kept in the body as waCode.
func fromCore(ce *core.Error, r *http.Request) *Error {
	var e *Error
	switch ce.Code {
	case codes.InvalidArgument, codes.OutOfRange:
		e = ErrBadRequest(ce.Message)
	case codes.NotFound:
		e = ErrNotFound(ce.Message)
	case codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted:
		e = ErrUnprocessable(ce.Message)
	case codes.PermissionDenied:
		e = nest(http.StatusForbidden, ce.Message)
	case codes.ResourceExhausted:
		e = nest(http.StatusTooManyRequests, ce.Message)
	case codes.Unimplemented:
		e = ErrNotImplemented(ce.Message)
	case codes.Unavailable:
		e = nest(http.StatusServiceUnavailable, "wamux core unavailable: "+ce.Message)
	case codes.DeadlineExceeded:
		e = nest(http.StatusGatewayTimeout, ce.Message)
	default:
		return ErrInternal(ce, r)
	}
	if ce.WACode != "" {
		if m, ok := e.Body.(map[string]any); ok {
			m["waCode"] = ce.WACode
			if ce.WAText != "" {
				m["waText"] = ce.WAText
			}
		}
	}
	e.cause = ce
	return e
}
