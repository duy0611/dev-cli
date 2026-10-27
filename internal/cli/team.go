package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/team"
	"github.com/duy0611/dev-cli/internal/teambin"
	"github.com/spf13/cobra"
)

// teamRunEnv is the variable the dispatch binary reads to find its run folder.
const teamRunEnv = "DEV_TEAM_RUN"

// now stamps a new run's id; a variable so tests can keep two launches apart.
var now = time.Now

// teamOpts holds the flags of `container start-agent-team` beyond the name and
// folder.
type teamOpts struct {
	lead        string
	leadModel   string
	workers     string
	workerSpecs []string
	maxTurns    int
	timeout     time.Duration
	agentName   string
}

func newContainerStartAgentTeamCmd(a *app) *cobra.Command {
	var (
		workspace string
		o         teamOpts
		kubeToken bool
		sshAgent  sshAgentFlags
	)

	cmd := &cobra.Command{
		Use:   "start-agent-team NAME [-- LEAD-ARGS...]",
		Short: "Run a lead agent with a team of workers (experimental)",
		Long: "Run a lead coding agent in a container with a team of workers it hands\n" +
			"tasks to.\n\n" +
			"Experimental: the command, its flags and the team folder may change\n" +
			"without a migration.\n\n" +
			"The lead and the workers have to be installed in the image already. This\n" +
			"tool does not edit a project's devcontainer.json, so installing one\n" +
			"means adding it there and rebuilding.\n\n" +
			"Arguments after -- go to the lead, as for start-agent.",
		Args:                  minArgs(1),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var extra []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				extra = args[dash:]
			}
			sshOverride, err := sshAgent.override()
			if err != nil {
				return err
			}
			return runContainerStartAgentTeam(cmd.Context(), a, workspace, args[0], o, extra,
				kubeToken, sshOverride)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	addKubeTokenFlag(cmd, &kubeToken)
	addSSHAgentFlags(cmd, &sshAgent)
	cmd.Flags().StringVar(&o.lead, "lead", "",
		"which tool leads the team (claude, opencode)")
	cmd.Flags().StringVar(&o.leadModel, "lead-model", "",
		"the lead's model (default: the tool's own)")
	cmd.Flags().StringVar(&o.workers, "workers", "",
		"which tool runs the workers (claude, opencode)")
	cmd.Flags().StringArrayVar(&o.workerSpecs, "worker", nil,
		"one worker as ROLE=MODEL, where ROLE is coder, reviewer or researcher; repeat for up to three")
	cmd.Flags().IntVar(&o.maxTurns, "worker-max-turns", team.DefaultMaxTurns,
		"stop a worker after this many turns")
	cmd.Flags().DurationVar(&o.timeout, "worker-timeout", team.DefaultTimeout,
		"stop a worker after this long")
	cmd.Flags().StringVar(&o.agentName, "agent-name", "",
		"the agent's name in Herdr (default: the container name)")
	return cmd
}

func runContainerStartAgentTeam(ctx context.Context, a *app, workspace, name string, o teamOpts,
	extra []string, kubeToken bool, sshOverride *bool) error {
	cfg, err := teamConfig(o)
	if err != nil {
		return err
	}
	herdrName, err := herdrAgentName(name, o.agentName)
	if err != nil {
		return err
	}
	lead, err := agent.Lookup(cfg.Lead.ID)
	if err != nil {
		return usageError(err)
	}
	worker, err := agent.Lookup(cfg.Workers)
	if err != nil {
		return usageError(err)
	}

	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.requireOverride(a); err != nil {
		return err
	}

	base := team.Base(t.container.PersistState)
	run, dispatch := teamPaths(t.container, now())
	// A lead asked to continue reads the newest run, so its next turn lands in
	// the folder the team already wrote. The lookup needs a running container;
	// a stopped one is started by launchAgent below, so a new run is the only
	// fallback there.
	resumed := false
	if resumeFlag(extra) {
		if id, ok := newestRun(ctx, t, base); ok {
			run = team.Run{Dir: path.Join(base, "runs", id)}
			resumed = true
		}
	}
	instruction := team.Instruction(cfg, dispatch)
	instructionFile := path.Join(run.Dir, "lead-instruction.md")
	argv, leadEnv, err := lead.LeadCommand(agent.LeadInput{
		Instruction:     instruction,
		InstructionFile: instructionFile,
		Model:           cfg.Lead.Model,
	}, extra)
	if err != nil {
		return usageError(err)
	}

	config, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the team config: %w", err)
	}
	config = append(config, '\n')

	onStart := func() error {
		if !agentInstalled(ctx, t, worker) {
			return fmt.Errorf("%s is not installed in %s's image; add it to %s and rebuild",
				worker.Binary, t.container.Name, t.container.ConfigPath)
		}

		var arch bytes.Buffer
		if err := t.provider.Exec(ctx, t.container, []string{"uname", "-m"},
			provider.ExecOpts{Stdout: &arch, Stderr: io.Discard}); err != nil {
			return fmt.Errorf("reading the container's architecture: %w", err)
		}
		bin, err := teambin.Binary(arch.String())
		if err != nil {
			return err
		}
		exec := func(ctx context.Context, argv []string, stdin io.Reader) error {
			return t.provider.Exec(ctx, t.container, argv,
				provider.ExecOpts{Stdin: stdin, Stdout: io.Discard, Stderr: io.Discard})
		}
		if err := team.Install(ctx, exec, dispatch, bin); err != nil {
			return err
		}
		// The opencode lead reads its instruction from a file, so a resumed run
		// rewrites it to follow this launch's flags. The path is an argument,
		// never interpolated into the script.
		if lead.ID == "opencode" {
			write := []string{"sh", "-c", `mkdir -p "$1" && cat > "$2"`, "sh", run.Dir, instructionFile}
			if err := t.provider.Exec(ctx, t.container, write,
				provider.ExecOpts{Stdin: strings.NewReader(instruction), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
				return fmt.Errorf("writing the lead instruction: %w", err)
			}
		}
		// A resumed run keeps the config.json it started with: dispatch reads
		// the workers from it, and replacing it would leave the tasks already
		// in the folder describing a different team.
		if resumed {
			return nil
		}

		write := []string{"sh", "-c", `mkdir -p "$1" && cat > "$1/config.json"`, "sh", run.Dir}
		if err := t.provider.Exec(ctx, t.container, write,
			provider.ExecOpts{Stdin: bytes.NewReader(config), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
			return fmt.Errorf("writing the team run folder: %w", err)
		}
		return nil
	}

	// The instruction is several KB and identical every launch; the log keeps
	// the lead's own flags and drops it.
	auditArgv := slices.Clone(argv)
	for i, arg := range auditArgv {
		if arg == instruction {
			auditArgv[i] = "<team instruction>"
		}
	}

	env := append([]provider.EnvVar{}, leadEnv...)
	env = append(env, provider.EnvVar{Key: teamRunEnv, Value: run.Dir})
	return a.launchAgent(ctx, t, lead, argv, launchOpts{
		herdrName:   herdrName,
		kubeToken:   kubeToken,
		sshOverride: sshOverride,
		env:         env,
		event:       "start-agent-team",
		auditArgv:   auditArgv,
		onStart:     onStart,
	})
}

// teamConfig checks the flags before anything is resolved or started, and
// returns the config every later step reads.
func teamConfig(o teamOpts) (team.Config, error) {
	switch {
	case o.lead == "":
		return team.Config{}, usageErrorf("--lead is required (claude or opencode)")
	case o.workers == "":
		return team.Config{}, usageErrorf("--workers is required (claude or opencode)")
	case len(o.workerSpecs) == 0:
		return team.Config{}, usageErrorf("--worker is required, as ROLE=MODEL; give one to three")
	}

	models, err := team.ParseWorkers(o.workerSpecs)
	if err != nil {
		return team.Config{}, usageError(err)
	}
	if o.maxTurns <= 0 {
		return team.Config{}, usageErrorf("--worker-max-turns must be positive, got %d", o.maxTurns)
	}
	if o.timeout <= 0 {
		return team.Config{}, usageErrorf("--worker-timeout must be positive, got %s", o.timeout)
	}
	// Named here rather than by Validate, which reports the field: the operator
	// needs the flag to correct.
	if o.lead != "claude" && o.lead != "opencode" {
		return team.Config{}, usageErrorf("--lead %q cannot join a team: only claude and opencode run unattended workers", o.lead)
	}
	if o.workers != "claude" && o.workers != "opencode" {
		return team.Config{}, usageErrorf("--workers %q cannot join a team: only claude and opencode run unattended workers", o.workers)
	}

	cfg := team.Config{
		Lead:     team.Tool{ID: o.lead, Model: o.leadModel},
		Workers:  o.workers,
		Models:   models,
		MaxTurns: o.maxTurns,
		Timeout:  o.timeout,
	}
	if err := cfg.Validate(); err != nil {
		return team.Config{}, usageError(err)
	}
	// Every role is built once here, so a tool the command accepts but dispatch
	// cannot run is refused before the container is brought up.
	for _, role := range cfg.Roles() {
		if _, _, err := team.Worker(cfg.Workers, team.WorkerSpec{
			Role: role, Model: cfg.Models[role], MaxTurns: cfg.MaxTurns,
		}); err != nil {
			return team.Config{}, usageError(err)
		}
	}
	return cfg, nil
}

// teamPaths is where a launch's dispatch binary and run folder live inside the
// container.
func teamPaths(c model.Container, at time.Time) (team.Run, string) {
	base := team.Base(c.PersistState)
	return team.Run{Dir: path.Join(base, "runs", team.RunID(c.Name, at))}, team.BinPath(base)
}

// resumeFlag reports whether the lead's own arguments contain a flag that
// continues an existing session. The lead's argv is otherwise left alone.
func resumeFlag(extra []string) bool {
	for _, arg := range extra {
		switch arg {
		case "--continue", "-c", "--resume", "-r":
			return true
		}
	}
	return false
}

// --- team list ------------------------------------------------------------------

func newContainerTeamCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Watch an agent team's progress (experimental)",
		Args:  noArgs(),
		RunE:  groupRunE,
	}

	var (
		workspace string
		runID     string
	)
	list := &cobra.Command{
		Use:   "list NAME [--run ID]",
		Short: "List a container's team runs and tasks (experimental)",
		Long: "List a container's team runs and their tasks.\n\n" +
			"Every run is listed newest first, followed by the newest run's tasks;\n" +
			"--run shows one run's tasks alone. A cost of - means the worker never\n" +
			"reported one.\n\n" +
			"Experimental: the command, its flags and the output may change without\n" +
			"a migration.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerTeamList(cmd.Context(), a, workspace, args[0], runID)
		},
	}
	addWorkspaceFlag(list, &workspace)
	list.Flags().StringVar(&runID, "run", "",
		"show only this run's tasks (default: every run and the newest run's tasks)")

	var (
		logWorkspace string
		logRun       string
		logTask      string
		follow       bool
	)
	logs := &cobra.Command{
		Use:   "logs NAME [--run ID] [--task ID] [--follow]",
		Short: "Show a team task's worker log (experimental)",
		Long: "Show a team task's log as readable lines: messages, tool calls and edits.\n\n" +
			"Without --task it shows the run's running task, or its most recent one;\n" +
			"without --run it reads the newest run. --follow keeps printing new lines\n" +
			"until the task finishes, and stops on interrupt.\n\n" +
			"Experimental: the command, its flags and the output may change without\n" +
			"a migration.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerTeamLogs(cmd.Context(), a, logWorkspace, args[0], logRun, logTask, follow)
		},
	}
	addWorkspaceFlag(logs, &logWorkspace)
	logs.Flags().StringVar(&logRun, "run", "",
		"read this run (default: the newest run)")
	logs.Flags().StringVar(&logTask, "task", "",
		"read this task (default: the run's running task, or its most recent one)")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output until the task finishes")

	cmd.AddCommand(list, logs)
	return cmd
}

func runContainerTeamList(ctx context.Context, a *app, workspace, name, runID string) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if err := teamRunning(ctx, t, name); err != nil {
		return err
	}

	runs, err := teamRuns(ctx, t, team.Base(t.container.PersistState))
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		a.printf("no team runs in %s; start one with: dev container start-agent-team %s\n", name, name)
		return nil
	}

	if runID != "" {
		at := slices.IndexFunc(runs, func(r team.RunTasks) bool { return r.Run == runID })
		if at < 0 {
			ids := make([]string, len(runs))
			for i, r := range runs {
				ids[i] = r.Run
			}
			return notFoundErrorf("no run %s in %s; runs: %s", runID, name, strings.Join(ids, ", "))
		}
		a.printf("%s", team.FormatTasks(runs[at].Tasks))
		return nil
	}

	a.printf("RUNS\n")
	for _, run := range runs {
		a.printf("%s\n", run.Run)
	}
	a.printf("\n%s", team.FormatTasks(runs[0].Tasks))
	return nil
}

// teamRunning is the pre-flight both watch commands share: their reads go
// through the container's dispatch binary and paths, so a stopped one has
// nothing to answer.
func teamRunning(ctx context.Context, t *target, name string) error {
	status, err := t.provider.Status(ctx, t.container)
	if err != nil {
		return fmt.Errorf("cannot read container %s's status: %v; start its team with: dev container start-agent-team %s",
			name, err, name)
	}
	if status != model.StatusRunning {
		return fmt.Errorf("container %s is not running; start its team with: dev container start-agent-team %s",
			name, name)
	}
	return nil
}

// teamRuns reads every run through the dispatch binary: the run folders exist
// only inside the container. The base travels as --base rather than through
// DEV_TEAM_BASE, which would also reach the tasks' own children.
func teamRuns(ctx context.Context, t *target, base string) ([]team.RunTasks, error) {
	var out bytes.Buffer
	argv := []string{team.BinPath(base), "list", "--json", "--all-runs", "--base", base}
	err := t.provider.Exec(ctx, t.container, argv,
		provider.ExecOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil {
		// A failed listing is not proof no team was started: a broken run
		// folder or a busy engine lands here too, so the cause stays quoted and
		// the hint is conditional.
		return nil, fmt.Errorf("cannot list team runs in %s (%v); if no team was started there, run: dev container start-agent-team %s",
			t.container.Name, err, t.container.Name)
	}

	var runs []team.RunTasks
	if err := json.Unmarshal(out.Bytes(), &runs); err != nil {
		return nil, fmt.Errorf("parsing %s's team runs: %w; received: %s",
			t.container.Name, err, firstBytes(out.String(), 200))
	}
	return runs, nil
}

// firstBytes bounds what an error quotes of a reply that did not parse.
func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// newestRun returns the newest run id under base, when the container is
// already running. A stopped container, a failed listing and an empty one all
// mean no run to resume: the launch then starts a new one rather than fail.
func newestRun(ctx context.Context, t *target, base string) (string, bool) {
	status, err := t.provider.Status(ctx, t.container)
	if err != nil || status != model.StatusRunning {
		return "", false
	}

	var out bytes.Buffer
	argv := []string{"ls", "-1t", path.Join(base, "runs")}
	if err := t.provider.Exec(ctx, t.container, argv,
		provider.ExecOpts{Stdout: &out, Stderr: io.Discard}); err != nil {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	name = strings.TrimSpace(name)
	// The listing is container-controlled, so only a plain single path element
	// may become DEV_TEAM_RUN; anything else could point outside base.
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}
