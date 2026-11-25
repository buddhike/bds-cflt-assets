// Package apperr holds the failures the API turns into HTTP responses.
package apperr

import "errors"

// Error is a failure the caller should see. Code is stable, Message is for people.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// New returns an Error with an explicit status.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func BadRequest(code, message string) *Error { return New(400, code, message) }

// Unauthorized is a 401. With no message it asks the person to sign in.
func Unauthorized(message ...string) *Error {
	return New(401, "unauthorized", first(message, "Sign in to continue"))
}

// Forbidden is a 403. With no message it uses a generic one.
func Forbidden(message ...string) *Error {
	return New(403, "forbidden", first(message, "You do not have access to this"))
}

// NotFound is a 404. With no message it says "Not found".
func NotFound(message ...string) *Error {
	return New(404, "not_found", first(message, "Not found"))
}

func Conflict(code, message string) *Error { return New(409, code, message) }

// As reports whether err is, or wraps, an *Error.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

func first(values []string, fallback string) string {
	if len(values) > 0 {
		return values[0]
	}
	return fallback
}
