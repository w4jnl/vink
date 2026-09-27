// Package cli is the REST client side of the binary: contexts in
// ~/.config/vink/config.toml, one HTTP client, and the table and JSON
// printers every read command shares.
package cli

import "fmt"

// Exit codes: 0 ok, 1 user error, 2 server or network error, 3 for
// `vink status` when something is down.
const (
	ExitOK     = 0
	ExitUser   = 1
	ExitServer = 2
	ExitDown   = 3
)

// ExitError carries a process exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

// ExitCode implements the interface main looks for.
func (e *ExitError) ExitCode() int { return e.Code }

// Unwrap exposes the cause.
func (e *ExitError) Unwrap() error { return e.Err }

// UserError is a mistake on the caller's side.
func UserError(format string, a ...any) error {
	return &ExitError{Code: ExitUser, Err: fmt.Errorf(format, a...)}
}

// ServerError is a server or network failure.
func ServerError(err error) error {
	return &ExitError{Code: ExitServer, Err: err}
}
