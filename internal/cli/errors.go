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

// codedError carries an exit code alongside the message. Commands return these
// instead of exiting, so that cobra unwinds normally and the message is printed
// in exactly one place.
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

// overlayError is a failure to build the merged devcontainer.json.
//
// Distinguishable from any other failure because it is deferrable: materialise
// runs for every container command, but stop, remove, logs and status find
// their container through docker label filters and never read a config at all.
// Failing those too would leave a syntax error in a project's file holding a
// container hostage in the engine, with dev refusing to clean up after itself.
//
// Exit 1, like any other runtime failure: the request was well-formed and the
// container exists.
type overlayError struct{ err error }

func (e *overlayError) Error() string { return e.err.Error() }
func (e *overlayError) Unwrap() error { return e.err }

// isOverlayError reports whether err is a deferrable overlay failure.
func isOverlayError(err error) bool {
	var oe *overlayError
	return errors.As(err, &oe)
}
