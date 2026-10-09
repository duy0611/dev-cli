package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/spf13/cobra"
)

func newAuditCmd(a *app) *cobra.Command {
	var (
		f      auditFlags
		events []string
	)

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show what dev has done to containers",
		Long: "Show the audit log: every container dev created, rebuilt, started,\n" +
			"stopped or removed, every command it ran in one, every kube token it\n" +
			"minted and every ssh agent relay it opened — oldest first.\n\n" +
			"The log is on the host, beside the database, where no container can\n" +
			"reach it. It records names, never values: no setting, environment\n" +
			"value or token is ever written to it.",
		Args: noArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			f.events = events
			return runAudit(a, f)
		},
	}
	cmd.Flags().StringVar(&f.workspace, "workspace", "", "only records for this workspace")
	cmd.Flags().StringVar(&f.container, "container", "", "only records for this container")
	cmd.Flags().StringSliceVar(&events, "event", nil, "only these events (repeatable, or comma-separated)")
	cmd.Flags().StringVar(&f.since, "since", "", "only records since a duration ago (24h) or an RFC 3339 time")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the matching records as raw JSON lines")
	return cmd
}

type auditFlags struct {
	workspace, container, since string
	events                      []string
	json                        bool
}

func runAudit(a *app, f auditFlags) error {
	filter := audit.Filter{Workspace: f.workspace, Container: f.container, Events: f.events}
	if f.since != "" {
		t, err := parseSince(f.since, time.Now())
		if err != nil {
			return usageError(err)
		}
		filter.Since = t
	}

	path, err := store.DefaultPath()
	if err != nil {
		return err
	}
	// Names are not resolved against the database. A container that has been
	// removed still has history, and the log is the one place it can be read —
	// so an unknown name matches nothing rather than exiting 3. Nor does
	// --workspace default to the active workspace: this is the view across
	// everything.
	entries, err := audit.Read(filepath.Join(filepath.Dir(path), audit.FileName), filter,
		func(line int, err error) { warnf(a, "audit log line %d skipped: %v", line, err) })
	if err != nil {
		return err
	}

	if f.json {
		for _, e := range entries {
			if _, err := a.out.Write(append(e.Raw, '\n')); err != nil {
				return err
			}
		}
		return nil
	}
	return a.table(func(w io.Writer) {
		for _, e := range entries {
			row(w, e.Time.Local().Format("2006-01-02 15:04:05"), where(e), e.Event, summarise(e))
		}
	})
}

// parseSince reads --since as a duration back from now, or as a time.
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("--since %s is in the future; give a positive duration", s)
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q is neither a duration (24h) nor an RFC 3339 time", s)
}

func where(e audit.Entry) string {
	switch ws, c := e.Workspace(), e.Container(); {
	case ws != "" && c != "":
		return ws + "/" + c
	case ws != "":
		return ws
	default:
		return c
	}
}

// summarise is the one human line for a record. Per event, because what is
// worth reading differs: an exit code for a command, an identity for a token.
func summarise(e audit.Entry) string {
	str := func(k string) string { s, _ := e.Fields[k].(string); return s }
	num := func(k string) (int, bool) { n, ok := e.Fields[k].(float64); return int(n), ok }

	switch e.Event {
	case "exec", "shell", "start-agent":
		var parts []string
		if argv := joinArgv(e.Fields["argv"]); argv != "" {
			parts = append(parts, argv)
		}
		if code, ok := num("exit_code"); ok {
			parts = append(parts, fmt.Sprintf("exit %d", code))
		}
		if d := elapsed(str("started"), str("ended")); d != "" {
			parts = append(parts, "after "+d)
		}
		return strings.Join(parts, ", ")
	case "kube-token":
		s := "sa " + str("service_account") + " in " + str("namespace") + " (" + str("context") + ")"
		if exp, err := time.Parse(time.RFC3339, str("expires")); err == nil {
			s += ", expires " + exp.Local().Format("15:04")
		}
		return s
	case "relay":
		s := str("end")
		if d := elapsed(str("started"), str("ended")); d != "" {
			s += " after " + d
		}
		return s
	case "sync":
		return joinArgv(e.Fields["settings"])
	case "refused":
		return str("reason")
	case "create":
		return str("source_kind")
	}
	return ""
}

func joinArgv(v any) string {
	items, _ := v.([]any)
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func elapsed(start, end string) string {
	s, err1 := time.Parse(time.RFC3339Nano, start)
	e, err2 := time.Parse(time.RFC3339Nano, end)
	if err1 != nil || err2 != nil {
		return ""
	}
	return e.Sub(s).Round(time.Second).String()
}
