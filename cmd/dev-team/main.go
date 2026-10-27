// Command dev-team runs an agent team's workers inside a container. dev embeds
// a build of it per architecture and the lead calls it as a shell command; see
// internal/teambin. It may import internal/team and the standard library only,
// because it is cross-built for the container rather than the host.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/duy0611/dev-cli/internal/team"
)

const (
	exitFailure = 1
	exitUsage   = 2

	resultFile = "result.md"
	runsDir    = "runs"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	if len(args) == 0 {
		return fail(stderr, exitUsage, "no command: want start, wait, result, takeover, list or supervise")
	}
	switch args[0] {
	case "start":
		return runStart(args[1:], stdin, stdout, stderr, env)
	case "wait":
		return runWait(args[1:], stdout, stderr, env)
	case "result":
		return runResult(args[1:], stdout, stderr, env)
	case "takeover":
		return runTakeover(args[1:], stderr, env)
	case "list":
		return runList(args[1:], stdout, stderr, env)
	case "supervise":
		return runSupervise(args[1:], stderr, env)
	}
	return fail(stderr, exitUsage, "unknown command %q: want start, wait, result, takeover, list or supervise", args[0])
}

func fail(stderr io.Writer, code int, format string, args ...any) int {
	fmt.Fprintf(stderr, "dev-team: "+format+"\n", args...)
	return code
}

// parseFlags accepts the positional either before or after its flags, the
// order the lead instruction documents. Go's flag package stops at the first
// positional, so each pass consumes one positional and parses what follows
// again; a dash-leading value still belongs to its flag, as flag.Parse takes
// the token after a value-taking flag whatever it looks like.
//
// Output stays off stderr: every error that leaves this binary is one line
// carrying the dev-team prefix.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// currentRun reads DEV_TEAM_RUN. Start's detached supervisor inherits it, which
// is how that child finds the run: its argv names only the task id.
func currentRun(env []string) (team.Run, error) {
	dir := envValue(env, "DEV_TEAM_RUN")
	if dir == "" {
		return team.Run{}, fmt.Errorf("DEV_TEAM_RUN is not set: run inside a team container")
	}
	if !path.IsAbs(dir) {
		// The supervisor runs in the task's working directory, so a relative
		// run dir would name a different folder for it than for the lead.
		return team.Run{}, fmt.Errorf("DEV_TEAM_RUN must be an absolute path, got %q", dir)
	}
	return team.Run{Dir: dir}, nil
}

func envValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// validTaskID mirrors internal/team's unexported check. Store lookups build a
// path from the id, so a CLI-supplied ".." must not reach them.
func validTaskID(id string) error {
	ok := len(id) > 1 && id[0] == 't' && id[1] != '0'
	for i := 1; ok && i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("invalid task id %q: want t followed by digits", id)
	}
	return nil
}

func runStart(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	retry := fs.String("retry", "", "task id to send the work back to")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	switch len(positional) {
	case 0:
		return fail(stderr, exitUsage, "start needs a role: start ROLE [--retry ID]")
	case 1:
	default:
		return fail(stderr, exitUsage, "start takes one role, got %d arguments", len(positional))
	}
	role := team.Role(positional[0])
	switch role {
	case team.RoleCoder, team.RoleReviewer, team.RoleResearcher:
	default:
		return fail(stderr, exitUsage, "unknown role %q: want coder, reviewer or researcher", role)
	}
	if *retry != "" {
		if err := validTaskID(*retry); err != nil {
			return fail(stderr, exitUsage, "%v", err)
		}
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	text, err := io.ReadAll(stdin)
	if err != nil {
		return fail(stderr, exitFailure, "read task text: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail(stderr, exitFailure, "locate dev-team binary: %v", err)
	}
	task, err := run.Start(role, string(text), *retry, self)
	if err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	if _, err := fmt.Fprintln(stdout, task.ID); err != nil {
		return fail(stderr, exitFailure, "print task id: %v", err)
	}
	return 0
}

// durationValue is a flag.Value because flag's own time.Duration parser
// reports a bare "parse error", which does not say what the value should be.
type durationValue time.Duration

func (d *durationValue) String() string { return time.Duration(*d).String() }

func (d *durationValue) Set(value string) error {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = durationValue(parsed)
	return nil
}

func runWait(args []string, stdout, stderr io.Writer, env []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	bound := durationValue(team.DefaultWait)
	fs.Var(&bound, "for", "how long to wait for the task")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	switch len(positional) {
	case 0:
		return fail(stderr, exitUsage, "wait needs a task id: wait ID [--for DURATION]")
	case 1:
	default:
		return fail(stderr, exitUsage, "wait takes one task id, got %d arguments", len(positional))
	}
	id := positional[0]
	if err := validTaskID(id); err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	if err := run.Wait(id, time.Duration(bound), stdout); err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	return 0
}

func runResult(args []string, stdout, stderr io.Writer, env []string) int {
	if len(args) != 1 {
		return fail(stderr, exitUsage, "result needs a task id: result ID")
	}
	id := args[0]
	if err := validTaskID(id); err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	task, err := run.Load(id)
	if err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	if task.Status == team.StatusRunning {
		return fail(stderr, exitFailure, "task %s is still running: use `dev-team wait %s`", id, id)
	}
	data, err := os.ReadFile(path.Join(run.TaskDir(id), resultFile))
	if err != nil {
		return fail(stderr, exitFailure, "read task %s result: %v", id, err)
	}
	if _, err := stdout.Write(data); err != nil {
		return fail(stderr, exitFailure, "print task %s result: %v", id, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := fmt.Fprintln(stdout); err != nil {
			return fail(stderr, exitFailure, "print task %s result: %v", id, err)
		}
	}
	return 0
}

func runTakeover(args []string, stderr io.Writer, env []string) int {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	reason := fs.String("reason", "", "why the lead took the task over")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	switch len(positional) {
	case 0:
		return fail(stderr, exitUsage, "takeover needs a task id: takeover ID --reason TEXT")
	case 1:
	default:
		return fail(stderr, exitUsage, "takeover takes one task id, got %d arguments", len(positional))
	}
	if *reason == "" {
		return fail(stderr, exitUsage, "takeover needs --reason TEXT")
	}
	id := positional[0]
	if err := validTaskID(id); err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	if err := run.Takeover(id, *reason); err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	return 0
}

func runList(args []string, stdout, stderr io.Writer, env []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	allRuns := fs.Bool("all-runs", false, "list every run under --base or DEV_TEAM_BASE")
	base := fs.String("base", "", "directory holding runs/ (with --all-runs)")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	if len(positional) != 0 {
		return fail(stderr, exitUsage, "list takes no arguments, got %d", len(positional))
	}
	if *allRuns {
		dir := *base
		if dir == "" {
			dir = envValue(env, "DEV_TEAM_BASE")
		}
		if dir == "" {
			return fail(stderr, exitUsage, "list --all-runs needs --base DIR or DEV_TEAM_BASE")
		}
		runs, err := listAllRuns(dir)
		if err != nil {
			return fail(stderr, exitFailure, "%v", err)
		}
		if *asJSON {
			return writeJSON(stdout, stderr, runs)
		}
		if err := printRuns(stdout, runs); err != nil {
			return fail(stderr, exitFailure, "print runs: %v", err)
		}
		return 0
	}
	if *base != "" {
		return fail(stderr, exitUsage, "--base only applies to list --all-runs")
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	tasks, err := run.List()
	if err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	if *asJSON {
		return writeJSON(stdout, stderr, tasks)
	}
	if err := printTasks(stdout, tasks); err != nil {
		return fail(stderr, exitFailure, "print tasks: %v", err)
	}
	return 0
}

func listAllRuns(base string) ([]team.RunTasks, error) {
	ids, err := team.ListRuns(base)
	if err != nil {
		return nil, err
	}
	runs := make([]team.RunTasks, 0, len(ids))
	for _, id := range ids {
		tasks, err := (team.Run{Dir: path.Join(base, runsDir, id)}).List()
		if err != nil {
			return nil, err
		}
		runs = append(runs, team.RunTasks{Run: id, Tasks: tasks})
	}
	return runs, nil
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return fail(stderr, exitFailure, "print json: %v", err)
	}
	return 0
}

func printTasks(w io.Writer, tasks []team.Task) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tROLE\tSTATUS\tATTEMPTS\tUNCOLLECTED"); err != nil {
		return err
	}
	for _, task := range tasks {
		// A running task has nothing to collect yet; the flag marks a finished
		// one no wait has reported.
		uncollected := ""
		if !task.Collected && task.Status != team.StatusRunning {
			uncollected = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", task.ID, task.Role, task.Status, task.Attempts, uncollected); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func printRuns(w io.Writer, runs []team.RunTasks) error {
	for i, run := range runs {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, run.Run); err != nil {
			return err
		}
		if err := printTasks(w, run.Tasks); err != nil {
			return err
		}
	}
	return nil
}

func runSupervise(args []string, stderr io.Writer, env []string) int {
	if len(args) != 1 {
		return fail(stderr, exitUsage, "supervise needs a task id")
	}
	id := args[0]
	if err := validTaskID(id); err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	run, err := currentRun(env)
	if err != nil {
		return fail(stderr, exitUsage, "%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run.Supervise(ctx, id); err != nil {
		return fail(stderr, exitFailure, "%v", err)
	}
	return 0
}
