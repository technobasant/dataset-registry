// Package problem carries errors that know their HTTP status, rendered as
// RFC 9457 problem details.
package problem

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is an error a client can act on.
type Error struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	// Extra carries structured context, e.g. a compatibility report or schema
	// validation errors.
	Extra any `json:"extra,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Detail) }

func newf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Detail: fmt.Sprintf(format, a...)}
}

// BadRequest is a 400: the request is malformed.
func BadRequest(format string, a ...any) *Error {
	return newf(http.StatusBadRequest, "bad_request", format, a...)
}

// Unauthorized is a 401: no valid token.
func Unauthorized(format string, a ...any) *Error {
	return newf(http.StatusUnauthorized, "unauthorized", format, a...)
}

// Forbidden is a 403: valid token, wrong role or team.
func Forbidden(format string, a ...any) *Error {
	return newf(http.StatusForbidden, "forbidden", format, a...)
}

// NotFound is a 404.
func NotFound(format string, a ...any) *Error {
	return newf(http.StatusNotFound, "not_found", format, a...)
}

// Conflict is a 409: the request clashes with current state.
func Conflict(format string, a ...any) *Error {
	return newf(http.StatusConflict, "conflict", format, a...)
}

// Unprocessable is a 422: well-formed, but not acceptable (e.g. an invalid schema).
func Unprocessable(format string, a ...any) *Error {
	return newf(http.StatusUnprocessableEntity, "unprocessable", format, a...)
}

// With attaches structured context.
func (e *Error) With(extra any) *Error { e.Extra = extra; return e }

// As extracts a *Error, or reports false for anything else (a 500).
func As(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
