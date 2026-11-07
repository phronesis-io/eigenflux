// Package apperr holds the domain error type shared by every Hub layer.
// Business packages (session, invitation, followup) must not import each
// other just to reuse an error struct; the HTTP layer translates these into
// a stable {error:{code,message}} envelope.
package apperr

import (
	"errors"
	"fmt"
)

// Error is a domain failure with a stable wire code and HTTP status.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New builds an Error with a formatted message.
func New(status int, code, format string, args ...any) error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError extracts a domain *Error from err, if present.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
