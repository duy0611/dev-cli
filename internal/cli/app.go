package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/store"
)

// app is what every command needs: the database and somewhere to print.
//
// The store is opened lazily, on the first command that wants it, so that
// `dev --help` and `dev --version` do not create a database as a side effect of
// asking a question.
type app struct {
	out io.Writer

	st       *store.Store
	openErr  error
	isOpened bool
}

func (a *app) store() (*store.Store, error) {
	if !a.isOpened {
		a.st, a.openErr = store.OpenDefault()
		a.isOpened = true
	}
	return a.st, a.openErr
}

func (a *app) close() {
	if a.st != nil {
		_ = a.st.Close()
	}
}

// workspaceName resolves which workspace a command acts on: the --workspace
// flag when given, otherwise the active one.
func (a *app) workspaceName(override string) (string, error) {
	st, err := a.store()
	if err != nil {
		return "", err
	}

	if override != "" {
		if _, err := st.GetWorkspace(override); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", notFoundErrorf("no such workspace: %s", override)
			}
			return "", err
		}
		return override, nil
	}

	active, err := st.ActiveWorkspace()
	if err != nil {
		return "", err
	}
	if active == "" {
		return "", usageErrorf("no active workspace; run: dev workspace use NAME")
	}
	return active, nil
}

// table writes aligned columns. Every list command prints through this so the
// output lines up whatever the widths are.
func (a *app) table(write func(w io.Writer)) error {
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	write(tw)
	return tw.Flush()
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}
