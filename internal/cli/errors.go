package cli

import (
	"errors"
	"fmt"
)

// Exit codes. Anything a caller might reasonably branch on gets its own code;
// everything else is a plain 1.
const (
	exitOK       = 0
	exitError    = 1 // something went wrong, no finer meaning
	exitUsage    = 2 // the request itself was malformed or contradictory
	exitNotFound = 3 // a named provider, workspace or container does not exist
)

// codedError carries an exit code with the message. Commands return it rather
// than exiting, so deferred cleanup runs and the message prints once.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func usageError(err error) error {
	return &codedError{code: exitUsage, err: err}
}

func usageErrorf(format string, a ...any) error {
	return usageError(fmt.Errorf(format, a...))
}

func notFoundErrorf(format string, a ...any) error {
	return &codedError{code: exitNotFound, err: fmt.Errorf(format, a...)}
}

// exitCodeOf reports the code a finished command should exit with.
func exitCodeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return exitError
}

// overlayError is a failure to build the merged devcontainer.json. It is
// deferrable: stop, remove, logs and status find containers by label and never
// read the config, so failing them too would strand a container behind a syntax
// error.
type overlayError struct{ err error }

func (e *overlayError) Error() string { return e.err.Error() }
func (e *overlayError) Unwrap() error { return e.err }

// isOverlayError reports whether err is a deferrable overlay failure.
func isOverlayError(err error) bool {
	var oe *overlayError
	return errors.As(err, &oe)
}
