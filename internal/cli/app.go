package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
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

	// version is dev's own, stamped into every audit record.
	version string
	// auditLog is built on first use, like the store, so that `dev --help`
	// writes nothing. Tests leave it nil, which records nothing.
	auditLog *audit.Log
	auditOff bool
}

// audit returns the log every command records to, beside the database.
//
// A nil Log records nothing, so a state directory that cannot be located only
// costs the record, never the command — see internal/audit.
func (a *app) audit() *audit.Log {
	if a.auditLog == nil && !a.auditOff {
		path, err := store.DefaultPath()
		if err != nil {
			a.auditOff = true
			return nil
		}
		a.auditLog = audit.Open(filepath.Dir(path), a.version, func(err error) {
			warnf(a, "%v", err)
		})
	}
	return a.auditLog
}

// record appends one event to the audit log. Never fails the command.
func (a *app) record(event, workspace, container string, fields map[string]any) {
	a.audit().Record(event, workspace, container, fields)
}

// recordCreate appends the record for a container row just written. The guard
// columns join it as they land, so the log says what each container was
// allowed from the moment it existed.
func (a *app) recordCreate(workspace string, c model.Container) {
	a.record("create", workspace, c.Name, map[string]any{
		"source_kind":      string(c.SourceKind),
		"generated":        c.GeneratedConfig != "",
		"persist_state":    c.PersistState,
		"allow_privileged": c.AllowPrivileged,
	})
}

// recordRun appends one record for a command that ran in a container, once it
// has finished: start, end and how it ended in a single line. One record at the
// end rather than one at each side, so a session killed with the host leaves
// nothing — accepted, against doubling the file for the rare case.
func (a *app) recordRun(event string, t *target, started time.Time, fields map[string]any, err error) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["started"] = started.UTC().Format(time.RFC3339Nano)
	fields["ended"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["exit_code"] = exitCodeOfRun(err)
	a.record(event, t.workspace.Name, t.container.Name, fields)
}

// exitCodeOfRun is the exit status of whatever ran in the container: the
// command's own code when it exited non-zero, 1 for any other failure.
func exitCodeOfRun(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	return exitCodeOf(err)
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
