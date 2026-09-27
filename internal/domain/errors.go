package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors the service layer returns; transports map them to
// HTTP statuses and CLI exit codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
	ErrRateLimited  = errors.New("rate limited")
	// ErrMethodNotAllowed is returned when a monitor restricts ping methods.
	ErrMethodNotAllowed = errors.New("method not allowed")
)

// FieldError is one validation problem, tied to a field name the UI can
// render next to its input.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"msg"`
}

// ValidationError collects field errors. A nil or empty ValidationError
// means valid; use OrNil to return it.
type ValidationError struct {
	Errors []FieldError
}

// Add records a problem.
func (e *ValidationError) Add(field, msg string) {
	e.Errors = append(e.Errors, FieldError{Field: field, Msg: msg})
}

// Addf records a formatted problem.
func (e *ValidationError) Addf(field, format string, a ...any) {
	e.Add(field, fmt.Sprintf(format, a...))
}

// OrNil returns nil when no problem was recorded.
func (e *ValidationError) OrNil() error {
	if e == nil || len(e.Errors) == 0 {
		return nil
	}
	return e
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, fe := range e.Errors {
		parts = append(parts, fe.Field+": "+fe.Msg)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Conflict wraps ErrConflict with a message.
func Conflict(msg string) error { return fmt.Errorf("%w: %s", ErrConflict, msg) }

// NotFound wraps ErrNotFound with a message.
func NotFound(what string) error { return fmt.Errorf("%s %w", what, ErrNotFound) }

// AsValidation returns the ValidationError inside err, if any.
func AsValidation(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
