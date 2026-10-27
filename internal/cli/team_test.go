package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/team"
	"github.com/duy0611/dev-cli/internal/teambin"
)

// validTeamOpts is a team every refusal case starts from; each test changes one
// field.
func validTeamOpts() teamOpts {
	return teamOpts{
		lead:        "claude",
		workers:     "claude",
		workerSpecs: []string{"coder=sonnet"},
		maxTurns:    team.DefaultMaxTurns,
		timeout:     team.DefaultTimeout,
	}
}

func requireTeamBin(t *testing.T) {
	t.Helper()
	if _, err := teambin.Binary(teambin.HostArch()); err != nil {
		t.Skip("dev-team is not built; run make team")
	}
}

// seedFakeTeam is seedFake with the container answering `uname -m`, as the
// launcher reads it before installing dispatch.
func seedFakeTeam(t *testing.T) *app {
	t.Helper()
	requireTeamBin(t)
	a := seedFake(t, model.StatusStopped)
	fake.outputs = map[string]string{"uname -m": teambin.HostArch()}
	return a
}

func TestStartAgentTeamRefusesBeforeStarting(t *testing.T) {
	tests := []struct {
		name string
		edit func(*teamOpts)
		want string
	}{
		{
			name: "missing lead",
			edit: func(o *teamOpts) { o.lead = "" },
			want: "--lead",
		},
		{
			name: "missing workers",
			edit: func(o *teamOpts) { o.workers = "" },
			want: "--workers",
		},
		{
			name: "no worker",
			edit: func(o *teamOpts) { o.workerSpecs = nil },
			want: "--worker",
		},
		{
			name: "lead hermes",
			edit: func(o *teamOpts) { o.lead = "hermes" },
			want: "hermes",
		},
		{
			name: "workers codex",
			edit: func(o *teamOpts) { o.workers = "codex" },
			want: "codex",
		},
		{
			name: "repeated role",
			edit: func(o *teamOpts) { o.workerSpecs = []string{"coder=a", "coder=b"} },
			want: "coder",
		},
		{
			name: "unknown role",
			edit: func(o *teamOpts) { o.workerSpecs = []string{"qa=a"} },
			want: "qa",
		},
		{
			name: "bad agent name",
			edit: func(o *teamOpts) { o.agentName = "Not.Valid" },
			want: "--agent-name",
		},
		{
			name: "zero max turns",
			edit: func(o *teamOpts) { o.maxTurns = 0 },
			want: "--worker-max-turns",
		},
		{
			name: "negative max turns",
			edit: func(o *teamOpts) { o.maxTurns = -1 },
			want: "--worker-max-turns",
		},
		{
			name: "zero timeout",
			edit: func(o *teamOpts) { o.timeout = 0 },
			want: "--worker-timeout",
		},
		{
			name: "negative timeout",
			edit: func(o *teamOpts) { o.timeout = -time.Minute },
			want: "--worker-timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := seedFake(t, model.StatusStopped)
			o := validTeamOpts()
			tt.edit(&o)

			err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, nil, false, nil)
			if err == nil || exitCodeOf(err) != exitUsage {
				t.Fatalf("err = %v (exit %d), want a usage error", err, exitCodeOf(err))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to name %q", err, tt.want)
			}
			if slices.Contains(fake.log, "up") {
				t.Errorf("started the container before refusing: %v", fake.log)
			}
		})
	}
}

func TestTeamConfigAcceptsOpencodeWorkers(t *testing.T) {
	o := validTeamOpts()
	o.workers = "opencode"

	cfg, err := teamConfig(o)
	if err != nil {
		t.Fatalf("teamConfig refused opencode workers: %v", err)
	}
	if cfg.Workers != "opencode" {
		t.Errorf("workers = %q, want opencode", cfg.Workers)
	}
}

func TestStartAgentTeamOpencodeLeadWritesInstructionOutsideTree(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.lead = "opencode"
	o.leadModel = "litellm/deepseek-v4.1-flash"

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o,
		[]string{"--continue"}, false, nil); err != nil {
		t.Fatal(err)
	}

	lead := fake.execs[len(fake.execs)-1]
	wantArgv := []string{"opencode", "--standalone", "--continue"}
	if !slices.Equal(lead.cmd, wantArgv) {
		t.Errorf("lead argv = %q, want %q", lead.cmd, wantArgv)
	}
	runDir := teamRunDir(t, lead)

	var leadConfig string
	for _, e := range lead.env {
		if e.Key == "OPENCODE_CONFIG_CONTENT" {
			leadConfig = e.Value
		}
	}
	if leadConfig == "" {
		t.Fatalf("the lead exec has no OPENCODE_CONFIG_CONTENT: %v", lead.env)
	}
	var cfg struct {
		Instructions []string `json:"instructions"`
		Model        string   `json:"model"`
	}
	if err := json.Unmarshal([]byte(leadConfig), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v: %s", err, leadConfig)
	}
	wantFile := runDir + "/lead-instruction.md"
	if !slices.Equal(cfg.Instructions, []string{wantFile}) {
		t.Errorf("instructions = %q, want only %q", cfg.Instructions, wantFile)
	}
	if cfg.Model != o.leadModel {
		t.Errorf("model = %q, want %q", cfg.Model, o.leadModel)
	}

	idx := instructionFileWritten()
	if idx < 0 {
		t.Fatalf("the instruction file was never written: %v", fake.log)
	}
	write := fake.execs[idx]
	if got := write.cmd[len(write.cmd)-1]; got != wantFile {
		t.Errorf("wrote %q, want %q", got, wantFile)
	}
	if got := write.cmd[len(write.cmd)-2]; got != runDir {
		t.Errorf("mkdir target = %q, want the run folder %q", got, runDir)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range write.cmd {
		if c.Source != "" && strings.Contains(arg, c.Source) {
			t.Errorf("the instruction write reaches the container's source %q: %q", c.Source, arg)
		}
	}

	teamCfg, err := teamConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	instruction := team.Instruction(teamCfg, team.BinPath(team.Base(false)))
	if write.stdin != instruction {
		t.Errorf("instruction on stdin =\n%s\nwant\n%s", write.stdin, instruction)
	}
	for _, arg := range lead.cmd {
		if strings.Contains(arg, instruction) {
			t.Errorf("instruction leaked into the lead's argv: %q", arg)
		}
	}

	if install := installed(); install < 0 || install > idx {
		t.Errorf("install at %d, instruction write at %d: the write must follow install", install, idx)
	}
	if idx >= len(fake.execs)-1 {
		t.Errorf("instruction write at %d, lead at %d: the write must precede the lead", idx, len(fake.execs)-1)
	}
	if config := configWritten(); config < 0 || config >= len(fake.execs)-1 {
		t.Errorf("config write at %d, lead at %d: the run folder must precede the lead", config, len(fake.execs)-1)
	}
}

func TestStartAgentTeamAuditsPlaceholderNotInstruction(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.leadModel = "opus"

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, []string{"--resume", "abc"}, false, nil); err != nil {
		t.Fatal(err)
	}

	teamCfg, err := teamConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	instruction := team.Instruction(teamCfg, team.BinPath(team.Base(false)))
	lead := fake.execs[len(fake.execs)-1]
	if !slices.Contains(lead.cmd, instruction) {
		t.Errorf("the lead's real argv lost the instruction: %q", lead.cmd)
	}

	entries := auditEntries(t, audit.Filter{Events: []string{"start-agent-team"}})
	if len(entries) != 1 {
		t.Fatalf("got %d start-agent-team records, want 1", len(entries))
	}
	recorded := joinArgv(entries[0].Fields["argv"])
	if strings.Contains(recorded, instruction) || strings.Contains(auditBytes(t), instruction) {
		t.Errorf("the audit log holds the instruction text: %s", recorded)
	}
	want := "claude --model opus --append-system-prompt <team instruction> --resume abc"
	if recorded != want {
		t.Errorf("recorded argv = %q, want %q", recorded, want)
	}
	if got := summarise(entries[0]); !strings.Contains(got, "<team instruction>") || !strings.Contains(got, "exit 0") {
		t.Errorf("summary = %q, want the recorded argv and exit code", got)
	}
}

func TestStartAgentTeamOpencodeLeadAuditsItsArgv(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.lead = "opencode"

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, nil, false, nil); err != nil {
		t.Fatal(err)
	}

	entries := auditEntries(t, audit.Filter{Events: []string{"start-agent-team"}})
	if len(entries) != 1 {
		t.Fatalf("got %d start-agent-team records, want 1", len(entries))
	}
	if got := joinArgv(entries[0].Fields["argv"]); got != "opencode --standalone" {
		t.Errorf("recorded argv = %q, want the lead's argv untouched", got)
	}
}

func TestStartAgentTeamInstructionWriteFailureStopsBeforeLead(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.lead = "opencode"
	fake.failCmd = func(cmd []string) error {
		if len(cmd) > 0 && strings.HasSuffix(cmd[len(cmd)-1], "/lead-instruction.md") {
			return errors.New("read-only file system")
		}
		return nil
	}

	err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, nil, false, nil)
	if err == nil {
		t.Fatal("launched without writing the lead instruction")
	}
	for _, want := range []string{"writing the lead instruction", "read-only file system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
	if opencodeRan() {
		t.Error("ran the lead after the instruction write failed")
	}
}

func TestStartAgentTeamLaunchesLead(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.leadModel = "opus"
	o.workerSpecs = []string{"coder=sonnet", "reviewer=haiku"}
	extra := []string{"--resume", "abc"}

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, extra, false, nil); err != nil {
		t.Fatal(err)
	}

	if len(fake.execs) == 0 {
		t.Fatal("nothing ran in the container")
	}
	lead := fake.execs[len(fake.execs)-1]

	cfg := team.Config{
		Lead:     team.Tool{ID: "claude", Model: "opus"},
		Workers:  "claude",
		Models:   map[team.Role]string{team.RoleCoder: "sonnet", team.RoleReviewer: "haiku"},
		MaxTurns: team.DefaultMaxTurns,
		Timeout:  team.DefaultTimeout,
	}
	dispatch := team.BinPath(team.Base(false))
	instruction := team.Instruction(cfg, dispatch)
	want := []string{"claude", "--model", "opus", "--append-system-prompt", instruction, "--resume", "abc"}
	if !slices.Equal(lead.cmd, want) {
		t.Errorf("lead argv =\n%q\nwant\n%q", lead.cmd, want)
	}
	if !strings.Contains(instruction, dispatch) {
		t.Errorf("instruction does not name the dispatch path %q: %s", dispatch, instruction)
	}

	install := slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return strings.Contains(strings.Join(e.cmd, " "), "chmod 700")
	})
	runFolder := slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return strings.Contains(strings.Join(e.cmd, " "), "config.json")
	})
	if install < 0 {
		t.Fatalf("the dispatch binary was never installed: %v", fake.log)
	}
	if runFolder < 0 {
		t.Fatalf("the run folder was never created: %v", fake.log)
	}
	if install >= len(fake.execs)-1 || runFolder >= len(fake.execs)-1 {
		t.Errorf("install at %d, run folder at %d, lead at %d: both must precede the lead",
			install, runFolder, len(fake.execs)-1)
	}
	if !strings.Contains(strings.Join(fake.execs[install].cmd, " "), dispatch) {
		t.Errorf("installed somewhere other than %s: %v", dispatch, fake.execs[install].cmd)
	}
	if idx := instructionFileWritten(); idx >= 0 {
		t.Errorf("wrote an instruction file for claude: %v", fake.execs[idx].cmd)
	}

	var runDir string
	for _, e := range lead.env {
		if e.Key == "DEV_TEAM_RUN" {
			runDir = e.Value
		}
	}
	if runDir == "" {
		t.Fatalf("the lead exec has no DEV_TEAM_RUN: %v", lead.env)
	}
	runArgs := fake.execs[runFolder].cmd
	if got := runArgs[len(runArgs)-1]; got != runDir {
		t.Errorf("DEV_TEAM_RUN = %q, want the folder written at %q", runDir, got)
	}
}

func TestStartAgentTeamWritesConfig(t *testing.T) {
	a := seedFakeTeam(t)
	o := validTeamOpts()
	o.leadModel = "opus"
	o.workerSpecs = []string{"coder=sonnet", "reviewer=haiku"}
	o.maxTurns = 7
	o.timeout = 3 * time.Minute

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o, nil, false, nil); err != nil {
		t.Fatal(err)
	}

	idx := slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return strings.Contains(strings.Join(e.cmd, " "), "config.json")
	})
	if idx < 0 {
		t.Fatalf("config.json was never written: %v", fake.log)
	}

	var got team.Config
	if err := json.Unmarshal([]byte(fake.execs[idx].stdin), &got); err != nil {
		t.Fatalf("config.json on stdin: %v", err)
	}
	want := team.Config{
		Lead:     team.Tool{ID: "claude", Model: "opus"},
		Workers:  "claude",
		Models:   map[team.Role]string{team.RoleCoder: "sonnet", team.RoleReviewer: "haiku"},
		MaxTurns: 7,
		Timeout:  3 * time.Minute,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config.json = %+v, want %+v", got, want)
	}
}

func TestStartAgentTeamMissingWorkerStopsBeforeLead(t *testing.T) {
	a, name := seedFakeTeamWithConfig(t)
	checks := 0
	fake.failCmd = func(cmd []string) error {
		if strings.Join(cmd, " ") == "sh -c command -v claude" {
			checks++
			// The lead's presence check is the first; the worker's is the
			// second. Only the worker is missing.
			if checks == 2 {
				return errors.New("exit status 1")
			}
		}
		return nil
	}

	err := runContainerStartAgentTeam(t.Context(), a, "", name, validTeamOpts(), nil, false, nil)
	if err == nil {
		t.Fatal("launched with the worker binary missing")
	}
	for _, want := range []string{"claude", "/repo/.devcontainer/devcontainer.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if checks != 2 {
		t.Errorf("worker presence check ran %d times, want the second check", checks)
	}
	if leadRan() {
		t.Error("ran the lead after the worker check failed")
	}
}

func TestStartAgentTeamUnameFailureStopsBeforeLead(t *testing.T) {
	a := seedFakeTeam(t)
	fake.failCmd = func(cmd []string) error {
		if strings.Join(cmd, " ") == "uname -m" {
			return errors.New("exec failed")
		}
		return nil
	}

	err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(), nil, false, nil)
	if err == nil {
		t.Fatal("launched without reading the container's architecture")
	}
	for _, want := range []string{"reading the container's architecture", "exec failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
	if leadRan() {
		t.Error("ran the lead after uname failed")
	}
}

func TestStartAgentTeamUnsupportedArchStopsBeforeLead(t *testing.T) {
	a := seedFakeTeam(t)
	fake.outputs["uname -m"] = "riscv64"

	err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(), nil, false, nil)
	if err == nil {
		t.Fatal("launched with no dispatch binary for the architecture")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error = %q, want it to name riscv64", err)
	}
	if leadRan() {
		t.Error("ran the lead after finding no binary for the architecture")
	}
}

func TestStartAgentTeamInstallFailureStopsBeforeLead(t *testing.T) {
	a := seedFakeTeam(t)
	fake.failCmd = func(cmd []string) error {
		if strings.Contains(strings.Join(cmd, " "), "$1.part") {
			return errors.New("chmod failed")
		}
		return nil
	}

	err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(), nil, false, nil)
	if err == nil {
		t.Fatal("launched after installing the dispatch binary failed")
	}
	for _, want := range []string{"installing the team dispatch binary", "chmod failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
	if leadRan() {
		t.Error("ran the lead after the install failed")
	}
}

func TestStartAgentTeamRunFolderFailureStopsBeforeLead(t *testing.T) {
	a := seedFakeTeam(t)
	fake.failCmd = func(cmd []string) error {
		if strings.Contains(strings.Join(cmd, " "), "config.json") {
			return errors.New("read-only file system")
		}
		return nil
	}

	err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(), nil, false, nil)
	if err == nil {
		t.Fatal("launched without writing the run folder")
	}
	for _, want := range []string{"writing the team run folder", "read-only file system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
	if leadRan() {
		t.Error("ran the lead after the run folder write failed")
	}
}

// fixedNow pins the run clock so a new run's id is predictable.
func fixedNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// steppingNow advances the run clock a second per read, so two launches in one
// test get distinct run ids.
func steppingNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	calls := 0
	now = func() time.Time {
		calls++
		return at.Add(time.Duration(calls) * time.Second)
	}
	t.Cleanup(func() { now = old })
}

// seedFakeTeamRunning is seedFakeTeam with a container already up, as a resume
// finds it.
func seedFakeTeamRunning(t *testing.T) *app {
	t.Helper()
	a := seedFakeTeam(t)
	fake.status = model.StatusRunning
	return a
}

// teamRunDir is the DEV_TEAM_RUN value of an exec.
func teamRunDir(t *testing.T, e recordedExec) string {
	t.Helper()
	for _, v := range e.env {
		if v.Key == teamRunEnv {
			return v.Value
		}
	}
	t.Fatalf("exec %v has no %s", e.cmd, teamRunEnv)
	return ""
}

// configWritten reports the index of the exec that writes a run's config.json.
func configWritten() int {
	return slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return strings.Contains(strings.Join(e.cmd, " "), "config.json")
	})
}

// installed reports the index of the exec that installs the dispatch binary.
func installed() int {
	return slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return strings.Contains(strings.Join(e.cmd, " "), "chmod 700")
	})
}

// instructionFileWritten reports the index of the exec that writes a lead's
// instruction file. The path is the final argument, not part of the script.
func instructionFileWritten() int {
	return slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return len(e.cmd) > 0 && strings.HasSuffix(e.cmd[len(e.cmd)-1], "/lead-instruction.md")
	})
}

// opencodeRan reports whether opencode itself was executed, as opposed to the
// setup commands run before it.
func opencodeRan() bool {
	return slices.ContainsFunc(fake.execs, func(e recordedExec) bool {
		return len(e.cmd) > 0 && e.cmd[0] == "opencode"
	})
}

// lsRan reports whether the run listing was attempted; fake.log records an
// exec before failCmd can fail it.
func lsRan() bool {
	return slices.ContainsFunc(fake.log, func(line string) bool {
		return strings.HasPrefix(line, "exec ls ")
	})
}

func TestContinueReusesNewestRun(t *testing.T) {
	const existing = "api-20261026T101500Z"
	for _, flag := range []string{"--continue", "-c", "--resume", "-r"} {
		t.Run(flag, func(t *testing.T) {
			a := seedFakeTeamRunning(t)
			fixedNow(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
			fake.outputs["ls -1t "+team.Base(false)+"/runs"] = existing + "\n"

			if err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(),
				[]string{flag}, false, nil); err != nil {
				t.Fatal(err)
			}

			want := team.Base(false) + "/runs/" + existing
			if got := teamRunDir(t, fake.execs[len(fake.execs)-1]); got != want {
				t.Errorf("%s: DEV_TEAM_RUN = %q, want %q", flag, got, want)
			}
			if idx := configWritten(); idx >= 0 {
				t.Errorf("%s: rewrote the run folder on resume: %v", flag, fake.execs[idx].cmd)
			}
			if installed() < 0 {
				t.Errorf("%s: did not install the dispatch binary: %v", flag, fake.log)
			}
		})
	}
}

func TestContinueRewritesOpencodeInstruction(t *testing.T) {
	const existing = "api-20261026T101500Z"
	a := seedFakeTeamRunning(t)
	fixedNow(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	fake.outputs["ls -1t "+team.Base(false)+"/runs"] = existing + "\n"
	o := validTeamOpts()
	o.lead = "opencode"
	o.workerSpecs = []string{"coder=sonnet", "reviewer=haiku"}

	if err := runContainerStartAgentTeam(t.Context(), a, "", "api", o,
		[]string{"--continue"}, false, nil); err != nil {
		t.Fatal(err)
	}

	wantDir := team.Base(false) + "/runs/" + existing
	wantFile := wantDir + "/lead-instruction.md"
	if got := teamRunDir(t, fake.execs[len(fake.execs)-1]); got != wantDir {
		t.Errorf("DEV_TEAM_RUN = %q, want the resumed run %q", got, wantDir)
	}
	idx := instructionFileWritten()
	if idx < 0 {
		t.Fatalf("a resumed opencode lead has no instruction file: %v", fake.log)
	}
	if got := fake.execs[idx].cmd[len(fake.execs[idx].cmd)-1]; got != wantFile {
		t.Errorf("wrote %q, want the resumed run's %q", got, wantFile)
	}
	cfg, err := teamConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	instruction := team.Instruction(cfg, team.BinPath(team.Base(false)))
	if fake.execs[idx].stdin != instruction {
		t.Errorf("the resumed instruction does not follow this launch's flags")
	}
	if idx >= len(fake.execs)-1 {
		t.Errorf("instruction write at %d, lead at %d: the write must precede the lead", idx, len(fake.execs)-1)
	}
	if configWritten() >= 0 {
		t.Errorf("rewrote config.json on resume: %v", fake.execs[configWritten()].cmd)
	}
}

func TestContinueWithNoRunStartsOne(t *testing.T) {
	const existing = "api-20261026T101500Z"
	tests := []struct {
		name       string
		status     model.Status
		failLS     bool
		failStatus bool
		seedRun    bool
		wantLS     bool
	}{
		{name: "listing prints nothing", status: model.StatusRunning, wantLS: true},
		{name: "listing fails", status: model.StatusRunning, failLS: true, wantLS: true},
		{name: "container stopped", status: model.StatusStopped, seedRun: true},
		{name: "status fails", status: model.StatusRunning, failStatus: true, seedRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := seedFakeTeam(t)
			fake.status = tt.status
			// A run the lookup must not read: only a container that answers
			// Status with "running" may list its runs.
			if tt.seedRun {
				fake.outputs["ls -1t "+team.Base(false)+"/runs"] = existing + "\n"
			}
			if tt.failLS {
				fake.failCmd = func(cmd []string) error {
					if len(cmd) > 0 && cmd[0] == "ls" {
						return errors.New("exit status 1")
					}
					return nil
				}
			}
			if tt.failStatus {
				calls := 0
				fake.failStatus = func() error {
					calls++
					if calls == 1 {
						return errors.New("status failed")
					}
					return nil
				}
			}
			fixedNow(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))

			if err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(),
				[]string{"--continue"}, false, nil); err != nil {
				t.Fatal(err)
			}

			want := team.Base(false) + "/runs/api-20261026T120000Z"
			if got := teamRunDir(t, fake.execs[len(fake.execs)-1]); got != want {
				t.Errorf("DEV_TEAM_RUN = %q, want a new run at %q", got, want)
			}
			if lsRan() != tt.wantLS {
				t.Errorf("ls ran = %v, want %v: %v", !tt.wantLS, tt.wantLS, fake.log)
			}
			idx := configWritten()
			if idx < 0 {
				t.Fatalf("no run folder was created: %v", fake.log)
			}
			write := fake.execs[idx].cmd
			if !strings.Contains(strings.Join(write, " "), "mkdir -p") || write[len(write)-1] != want {
				t.Errorf("run folder write = %q, want mkdir and the new run at %q", write, want)
			}
		})
	}
}

func TestNoContinueMakesNewRun(t *testing.T) {
	a := seedFakeTeam(t)
	steppingNow(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))

	var dirs []string
	for i := 0; i < 2; i++ {
		if err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(),
			nil, false, nil); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, teamRunDir(t, fake.execs[len(fake.execs)-1]))
	}
	if dirs[0] == dirs[1] {
		t.Errorf("two launches shared the run folder %q", dirs[0])
	}
	if lsRan() {
		t.Errorf("listed runs without a continue flag: %v", fake.log)
	}
}

func TestResumeFlagMatchesOnlyExactFlags(t *testing.T) {
	tests := []struct {
		extra []string
		want  bool
	}{
		{[]string{"--continue"}, true},
		{[]string{"-c"}, true},
		{[]string{"--resume"}, true},
		{[]string{"-r"}, true},
		{[]string{"--model", "opus", "--continue"}, true},
		{[]string{"--continued"}, false},
		{[]string{"-cx"}, false},
		{[]string{"--resume=abc"}, false},
		{[]string{"--continue=true"}, false},
		{[]string{"x-c"}, false},
		{[]string{""}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := resumeFlag(tt.extra); got != tt.want {
			t.Errorf("resumeFlag(%q) = %v, want %v", tt.extra, got, tt.want)
		}
	}
}

func TestContinueRejectsHostileRunName(t *testing.T) {
	tests := []struct{ name, output string }{
		{"parent", "../etc\n"},
		{"nested", "a/b\n"},
		{"dotdot", "..\n"},
		{"dot", ".\n"},
		{"blank", "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := seedFakeTeamRunning(t)
			fixedNow(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
			fake.outputs["ls -1t "+team.Base(false)+"/runs"] = tt.output

			if err := runContainerStartAgentTeam(t.Context(), a, "", "api", validTeamOpts(),
				[]string{"--continue"}, false, nil); err != nil {
				t.Fatal(err)
			}

			want := team.Base(false) + "/runs/api-20261026T120000Z"
			if got := teamRunDir(t, fake.execs[len(fake.execs)-1]); got != want {
				t.Errorf("DEV_TEAM_RUN = %q, want a new run at %q", got, want)
			}
			if configWritten() < 0 {
				t.Errorf("no run folder was created: %v", fake.log)
			}
		})
	}
}

// leadRan reports whether the agent itself was executed, as opposed to the
// setup commands run before it.
func leadRan() bool {
	return slices.ContainsFunc(fake.execs, func(e recordedExec) bool {
		return len(e.cmd) > 0 && e.cmd[0] == "claude"
	})
}

// seedFakeTeamWithConfig is seedFakeTeam plus a container whose ConfigPath is
// real, so a missing-binary message can be checked for it.
func seedFakeTeamWithConfig(t *testing.T) (*app, string) {
	t.Helper()
	a := seedFakeTeam(t)
	const (
		name       = "full"
		configPath = "/repo/.devcontainer/devcontainer.json"
	)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(model.Container{
		Name:          name,
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        t.TempDir(),
		ConfigPath:    configPath,
	}); err != nil {
		t.Fatal(err)
	}
	return a, name
}

// teamRunsJSON is the shape cmd/dev-team's `list --json --all-runs` prints:
// newest run first, each with its tasks.
const teamRunsJSON = `[
  {"run":"api-20261026T120000Z","tasks":[
    {"id":"t1","role":"coder","status":"done","attempts":1,"cost_usd":0.0123,"collected":true,"started_at":"2026-10-26T12:00:00Z"},
    {"id":"t2","role":"reviewer","status":"running","attempts":1,"started_at":"2026-10-26T12:01:00Z"}
  ]},
  {"run":"api-20261026T101500Z","tasks":[
    {"id":"t1","role":"coder","status":"failed","attempts":2,"took_over":true,"takeover_reason":"the lead finished it","started_at":"2026-10-26T10:15:00Z"}
  ]}
]`

// seedFakeTeamList is seedFake with the container answering the host's run
// listing, which is how `team list` reads it.
func seedFakeTeamList(t *testing.T) (*app, []string) {
	t.Helper()
	a := seedFake(t, model.StatusRunning)
	argv := []string{team.BinPath(team.Base(false)), "list", "--json", "--all-runs", "--base", team.Base(false)}
	fake.outputs = map[string]string{strings.Join(argv, " "): teamRunsJSON}
	return a, argv
}

func teamListOutput(t *testing.T, a *app) string {
	t.Helper()
	buf, ok := a.out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("out is %T, want *bytes.Buffer", a.out)
	}
	return buf.String()
}

func TestTeamListReadsThroughExec(t *testing.T) {
	t.Run("every run, then the newest run's tasks", func(t *testing.T) {
		a, argv := seedFakeTeamList(t)

		if err := runContainerTeamList(t.Context(), a, "", "api", ""); err != nil {
			t.Fatal(err)
		}

		if len(fake.execs) != 1 {
			t.Fatalf("ran %d commands, want only the listing: %v", len(fake.execs), fake.log)
		}
		list := fake.execs[0]
		if !slices.Equal(list.cmd, argv) {
			t.Errorf("argv = %q, want %q", list.cmd, argv)
		}
		if envHas(list.env, teamRunEnv) {
			t.Errorf("the listing carries %s; it names its base with --base", teamRunEnv)
		}

		out := teamListOutput(t, a)
		wantRuns := "RUNS\napi-20261026T120000Z\napi-20261026T101500Z\n\n"
		if !strings.Contains(out, wantRuns) {
			t.Errorf("runs are not listed newest first with a blank line after:\n%s", out)
		}
		for _, want := range []string{"ID", "ROLE", "STATUS", "ATTEMPTS", "COST", "TAKEOVER", "UNCOLLECTED"} {
			if !strings.Contains(out, want) {
				t.Errorf("task table lacks the %s column:\n%s", want, out)
			}
		}
		if !strings.Contains(out, "$0.0123") {
			t.Errorf("the newest run's cost is not shown:\n%s", out)
		}
		if !strings.Contains(out, "running") {
			t.Errorf("the newest run's running task is not shown:\n%s", out)
		}
		// Only the newest run's tasks: the older run's failed coder would
		// otherwise appear as a second t1 row.
		if strings.Contains(out, "failed") {
			t.Errorf("tasks of past runs are shown without --run:\n%s", out)
		}
	})

	t.Run("--run selects one run", func(t *testing.T) {
		a, _ := seedFakeTeamList(t)

		if err := runContainerTeamList(t.Context(), a, "", "api", "api-20261026T101500Z"); err != nil {
			t.Fatal(err)
		}

		out := teamListOutput(t, a)
		if strings.Contains(out, "RUNS") {
			t.Errorf("--run printed the runs list too:\n%s", out)
		}
		for _, want := range []string{"t1", "failed", "yes"} {
			if !strings.Contains(out, want) {
				t.Errorf("--run's table lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "running") {
			t.Errorf("--run printed another run's tasks:\n%s", out)
		}
	})
}

func TestTeamListNoRuns(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	argv := []string{team.BinPath(team.Base(false)), "list", "--json", "--all-runs", "--base", team.Base(false)}
	fake.outputs = map[string]string{strings.Join(argv, " "): "[]\n"}

	if err := runContainerTeamList(t.Context(), a, "", "api", ""); err != nil {
		t.Fatalf("err = %v, want no error for a team with no runs", err)
	}
	out := teamListOutput(t, a)
	for _, want := range []string{"no team runs in api", "dev container start-agent-team api"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
}

func TestTeamListNeedsRunningContainer(t *testing.T) {
	tests := []struct {
		name       string
		status     model.Status
		failStatus bool
	}{
		{name: "stopped", status: model.StatusStopped},
		{name: "status fails", status: model.StatusRunning, failStatus: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := seedFake(t, tt.status)
			if tt.failStatus {
				fake.failStatus = func() error { return errors.New("engine unreachable") }
			}

			err := runContainerTeamList(t.Context(), a, "", "api", "")
			if err == nil {
				t.Fatal("listed a team in a container that is not running")
			}
			if got := exitCodeOf(err); got != exitError {
				t.Errorf("exit code = %d, want %d", got, exitError)
			}
			for _, want := range []string{"dev container start-agent-team", "api"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
			if len(fake.execs) != 0 {
				t.Errorf("ran %d commands in a container that is not running: %v", len(fake.execs), fake.log)
			}
		})
	}
}

func TestTeamListWithoutADispatchBinary(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.failCmd = func(cmd []string) error {
		if len(cmd) > 0 && cmd[0] == team.BinPath(team.Base(false)) {
			return errors.New("exec: no such file or directory")
		}
		return nil
	}

	err := runContainerTeamList(t.Context(), a, "", "api", "")
	if err == nil {
		t.Fatal("listed a team where no team was ever started")
	}
	if got := exitCodeOf(err); got != exitError {
		t.Errorf("exit code = %d, want %d", got, exitError)
	}
	// A failed listing is not proof no team was started: the container's own
	// reason stays in the message, and the hint is conditional.
	for _, want := range []string{
		"cannot list team runs in api",
		"exec: no such file or directory",
		"if no team was started there",
		"dev container start-agent-team api",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
}

func TestTeamListUnknownRun(t *testing.T) {
	a, _ := seedFakeTeamList(t)

	err := runContainerTeamList(t.Context(), a, "", "api", "api-20261026T090000Z")
	if err == nil {
		t.Fatal("accepted a run id that does not exist")
	}
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d", got, exitNotFound)
	}
	for _, want := range []string{"api-20261026T120000Z", "api-20261026T101500Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name the available run %q", err, want)
		}
	}
}

func TestTeamListMalformedJSON(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	argv := []string{team.BinPath(team.Base(false)), "list", "--json", "--all-runs", "--base", team.Base(false)}
	fake.outputs = map[string]string{strings.Join(argv, " "): `{"run": "api`}

	err := runContainerTeamList(t.Context(), a, "", "api", "")
	if err == nil {
		t.Fatal("accepted output that is not a run list")
	}
	for _, want := range []string{"parsing", `{"run": "api`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
}

// --- team logs ------------------------------------------------------------------

const teamLogsRunsJSON = `[
  {"run":"api-20261026T120000Z","tasks":[
    {"id":"t1","role":"coder","tool":"claude","status":"done","attempts":1,"started_at":"2026-10-26T12:00:00Z"},
    {"id":"t2","role":"reviewer","tool":"claude","status":"running","attempts":1,"started_at":"2026-10-26T12:01:00Z"},
    {"id":"t3","role":"researcher","tool":"claude","status":"done","attempts":1,"started_at":"2026-10-26T12:02:00Z"}
  ]},
  {"run":"api-20261026T101500Z","tasks":[
    {"id":"t1","role":"coder","tool":"claude","status":"failed","attempts":2,"started_at":"2026-10-26T10:15:00Z"}
  ]}
]`

const teamLogsAllDoneJSON = `[
  {"run":"api-20261026T120000Z","tasks":[
    {"id":"t1","role":"coder","tool":"claude","status":"done","attempts":1,"started_at":"2026-10-26T12:00:00Z"},
    {"id":"t2","role":"reviewer","tool":"claude","status":"done","attempts":1,"started_at":"2026-10-26T12:01:00Z"},
    {"id":"t3","role":"researcher","tool":"claude","status":"done","attempts":1,"started_at":"2026-10-26T12:02:00Z"}
  ]}
]`

const teamLogsStream = `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"working"}]}}
{"type":"result","result":"DONE"}
`

// teamLogsListArgv is how the host reads runs, the same command team list uses.
func teamLogsListArgv() []string {
	return []string{team.BinPath(team.Base(false)), "list", "--json", "--all-runs", "--base", team.Base(false)}
}

func teamTaskLogPath(runID, taskID string) string {
	return path.Join(team.Base(false), "runs", runID, "tasks", taskID, "log.jsonl")
}

func seedFakeTeamLogs(t *testing.T, runsJSON, logRun, logTask string) *app {
	t.Helper()
	a := seedFake(t, model.StatusRunning)
	fake.outputs = map[string]string{
		strings.Join(teamLogsListArgv(), " "): runsJSON,
	}
	if logRun != "" {
		fake.outputs[strings.Join([]string{"cat", teamTaskLogPath(logRun, logTask)}, " ")] = teamLogsStream
	}
	return a
}

func recordedCat(t *testing.T) recordedExec {
	t.Helper()
	at := slices.IndexFunc(fake.execs, func(e recordedExec) bool {
		return len(e.cmd) == 2 && e.cmd[0] == "cat"
	})
	if at < 0 {
		t.Fatalf("no cat exec; ran: %v", fake.log)
	}
	return fake.execs[at]
}

func TestTeamLogsPicksRunningTask(t *testing.T) {
	t.Run("the running task", func(t *testing.T) {
		a := seedFakeTeamLogs(t, teamLogsRunsJSON, "api-20261026T120000Z", "t2")

		if err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false); err != nil {
			t.Fatal(err)
		}

		cat := recordedCat(t)
		if want := teamTaskLogPath("api-20261026T120000Z", "t2"); cat.cmd[1] != want {
			t.Errorf("read %q, want the running task's log %q", cat.cmd[1], want)
		}
		if got, want := teamListOutput(t, a), "text: working\nresult: DONE\n"; !strings.HasSuffix(got, want) {
			t.Errorf("output = %q, want it to end with %q", got, want)
		}
	})

	t.Run("the most recent when none is running", func(t *testing.T) {
		a := seedFakeTeamLogs(t, teamLogsAllDoneJSON, "api-20261026T120000Z", "t3")

		if err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false); err != nil {
			t.Fatal(err)
		}

		cat := recordedCat(t)
		if want := teamTaskLogPath("api-20261026T120000Z", "t3"); cat.cmd[1] != want {
			t.Errorf("read %q, want the most recent task's log %q", cat.cmd[1], want)
		}
	})

	t.Run("an explicit task", func(t *testing.T) {
		a := seedFakeTeamLogs(t, teamLogsRunsJSON, "api-20261026T101500Z", "t1")

		if err := runContainerTeamLogs(t.Context(), a, "", "api", "api-20261026T101500Z", "t1", false); err != nil {
			t.Fatal(err)
		}

		cat := recordedCat(t)
		if want := teamTaskLogPath("api-20261026T101500Z", "t1"); cat.cmd[1] != want {
			t.Errorf("read %q, want --task's log %q", cat.cmd[1], want)
		}
	})
}

func TestTeamLogsReadsToolFromConfig(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	const runID = "api-20261026T120000Z"
	fake.outputs = map[string]string{
		strings.Join(teamLogsListArgv(), " "): `[{"run":"` + runID + `","tasks":[
			{"id":"t1","role":"coder","status":"running","attempts":1,"started_at":"2026-10-26T12:00:00Z"}
		]}]`,
		strings.Join([]string{"cat", path.Join(team.Base(false), "runs", runID, "config.json")}, " "): `{"workers":"opencode"}`,
		strings.Join([]string{"cat", teamTaskLogPath(runID, "t1")}, " "):                              `{"type":"text","part":{"type":"text","text":"from config"}}` + "\n",
	}

	if err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false); err != nil {
		t.Fatal(err)
	}
	if got := teamListOutput(t, a); !strings.HasSuffix(got, "text: from config\n") {
		t.Errorf("output = %q, want the task rendered with the run's configured worker", got)
	}
	if len(fake.execs) != 3 {
		t.Fatalf("ran %d commands, want listing, config read, log read: %v", len(fake.execs), fake.log)
	}
	if want := path.Join(team.Base(false), "runs", runID, "config.json"); fake.execs[1].cmd[1] != want {
		t.Errorf("read %q, want the run config %q", fake.execs[1].cmd[1], want)
	}
}

const (
	teamLogsFollowA = `{"type":"assistant","message":{"id":"msg_a","content":[{"type":"text","text":"tail A"}]}}` + "\n"
	teamLogsFollowB = `{"type":"assistant","message":{"id":"msg_b","content":[{"type":"text","text":"tail B"}]}}` + "\n"

	teamLogsSplitLine = `{"type":"assistant","message":{"id":"msg_split","content":[{"type":"text","text":"split event"}]}}` + "\n"
)

// shortTeamLogTiming keeps follow tests from waiting on production timings.
func shortTeamLogTiming(t *testing.T) {
	t.Helper()
	oldPoll, oldStop := teamLogPoll, teamLogStopWait
	teamLogPoll = 5 * time.Millisecond
	teamLogStopWait = 20 * time.Millisecond
	t.Cleanup(func() {
		teamLogPoll = oldPoll
		teamLogStopWait = oldStop
	})
}

func isTeamLogsList(cmd []string) bool {
	return len(cmd) > 1 && cmd[0] == team.BinPath(team.Base(false)) && cmd[1] == "list"
}

// teamLogsExecFn answers the follow reads: the status flips to all-done on the
// terminalAt'th listing; tail and cat are the test's.
func teamLogsExecFn(lists *atomic.Int32, terminalAt int32,
	tail, cat func(context.Context, provider.ExecOpts) error) func(context.Context, []string, provider.ExecOpts) (bool, error) {
	return func(ctx context.Context, cmd []string, opts provider.ExecOpts) (bool, error) {
		switch {
		case isTeamLogsList(cmd):
			body := teamLogsRunsJSON
			// The first read selects the task; the next one is the first poll.
			if lists.Add(1) >= terminalAt {
				body = teamLogsAllDoneJSON
			}
			_, _ = io.WriteString(opts.Stdout, body)
			return true, nil
		case len(cmd) > 0 && cmd[0] == "tail" && tail != nil:
			return true, tail(ctx, opts)
		case len(cmd) > 0 && cmd[0] == "cat" && cat != nil:
			return true, cat(ctx, opts)
		}
		return false, nil
	}
}

// runTeamLogsBounded runs the command in the background so a broken follow
// fails this test instead of hanging the package.
func runTeamLogsBounded(t *testing.T, ctx context.Context, a *app, follow bool) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runContainerTeamLogs(ctx, a, "", "api", "", "", follow) }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("team logs did not return within 2s")
		return nil
	}
}

func assertTeamLogsOnce(t *testing.T, out, want string) {
	t.Helper()
	if got := strings.Count(out, want); got != 1 {
		t.Errorf("%q appears %d times, want once:\n%s", want, got, out)
	}
}

func TestTeamLogsFollowCatchesInFlightTail(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	shortTeamLogTiming(t)

	var lists atomic.Int32
	fake.execFn = teamLogsExecFn(&lists, 3,
		func(ctx context.Context, opts provider.ExecOpts) error {
			// A reaches the host; B was never delivered by the tail.
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA)
			<-ctx.Done()
			return ctx.Err()
		},
		func(_ context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA+teamLogsFollowB)
			return nil
		})

	if err := runTeamLogsBounded(t, t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	out := teamListOutput(t, a)
	assertTeamLogsOnce(t, out, "text: tail A")
	assertTeamLogsOnce(t, out, "text: tail B")
	if !slices.ContainsFunc(fake.execs, func(e recordedExec) bool { return len(e.cmd) > 0 && e.cmd[0] == "cat" }) {
		t.Error("follow did not read the log's tail after the task ended")
	}
	if got := lists.Load(); got < 3 {
		t.Errorf("listed runs %d times, want a selection read and at least two polls", got)
	}
}

func TestTeamLogsFollowDoesNotDuplicateDeliveredBytes(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	shortTeamLogTiming(t)

	fake.execFn = teamLogsExecFn(new(atomic.Int32), 3,
		func(ctx context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA+teamLogsFollowB)
			<-ctx.Done()
			return ctx.Err()
		},
		func(_ context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA+teamLogsFollowB)
			return nil
		})

	if err := runTeamLogsBounded(t, t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	out := teamListOutput(t, a)
	assertTeamLogsOnce(t, out, "text: tail A")
	assertTeamLogsOnce(t, out, "text: tail B")
}

func TestTeamLogsFollowResumesPartialLine(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	shortTeamLogTiming(t)

	half := len(teamLogsSplitLine) / 2
	fake.execFn = teamLogsExecFn(new(atomic.Int32), 3,
		func(ctx context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsSplitLine[:half])
			<-ctx.Done()
			return ctx.Err()
		},
		func(_ context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsSplitLine)
			return nil
		})

	if err := runTeamLogsBounded(t, t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	assertTeamLogsOnce(t, teamListOutput(t, a), "text: split event")
}

func TestTeamLogsFollowOnTerminalTaskSkipsTail(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	shortTeamLogTiming(t)

	var tailRan atomic.Bool
	fake.outputs = map[string]string{
		strings.Join(teamLogsListArgv(), " "): teamLogsAllDoneJSON,
	}
	fake.execFn = func(_ context.Context, cmd []string, opts provider.ExecOpts) (bool, error) {
		switch {
		case len(cmd) > 0 && cmd[0] == "tail":
			tailRan.Store(true)
			return true, nil
		case len(cmd) > 0 && cmd[0] == "cat":
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA)
			return true, nil
		}
		return false, nil
	}

	if err := runTeamLogsBounded(t, t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if tailRan.Load() {
		t.Error("follow tailed a task that was already terminal")
	}
	assertTeamLogsOnce(t, teamListOutput(t, a), "text: tail A")
}

func TestTeamLogsFollowDoesNotHangOnStuckTail(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	shortTeamLogTiming(t)

	release := make(chan struct{})
	tailReturned := make(chan struct{})
	fake.execFn = teamLogsExecFn(new(atomic.Int32), 3,
		func(_ context.Context, opts provider.ExecOpts) error {
			// Deliberately ignores cancellation, as a host-side kill that does
			// not reach the in-container tail would.
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA)
			<-release
			close(tailReturned)
			return nil
		},
		func(_ context.Context, opts provider.ExecOpts) error {
			_, _ = io.WriteString(opts.Stdout, teamLogsFollowA+teamLogsFollowB)
			return nil
		})

	start := time.Now()
	if err := runTeamLogsBounded(t, t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("follow took %s with a stuck tail; the stop wait should bound it", elapsed)
	}
	close(release)
	select {
	case <-tailReturned:
	case <-time.After(time.Second):
		t.Fatal("stuck tail never returned after release")
	}

	out := teamListOutput(t, a)
	assertTeamLogsOnce(t, out, "text: tail A")
	assertTeamLogsOnce(t, out, "text: tail B")
}

func TestTeamLogsUnknownTask(t *testing.T) {
	a := seedFakeTeamLogs(t, teamLogsRunsJSON, "", "")

	err := runContainerTeamLogs(t.Context(), a, "", "api", "", "t9", false)
	if err == nil {
		t.Fatal("accepted a task id that does not exist")
	}
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d (not found)", got, exitNotFound)
	}
	for _, want := range []string{"no task t9", "t1", "t2", "t3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if slices.ContainsFunc(fake.execs, func(e recordedExec) bool { return len(e.cmd) > 0 && e.cmd[0] == "cat" }) {
		t.Error("read a log for a task that does not exist")
	}
}

func TestTeamLogsUnknownRun(t *testing.T) {
	a := seedFakeTeamLogs(t, teamLogsRunsJSON, "", "")

	err := runContainerTeamLogs(t.Context(), a, "", "api", "api-20261026T090000Z", "", false)
	if err == nil {
		t.Fatal("accepted a run id that does not exist")
	}
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d (not found)", got, exitNotFound)
	}
	for _, want := range []string{"api-20261026T120000Z", "api-20261026T101500Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name the available run %q", err, want)
		}
	}
}

func TestTeamLogsNoRuns(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.outputs = map[string]string{strings.Join(teamLogsListArgv(), " "): "[]\n"}

	if err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false); err != nil {
		t.Fatalf("err = %v, want no error for a team with no runs", err)
	}
	out := teamListOutput(t, a)
	for _, want := range []string{"no team runs in api", "dev container start-agent-team api"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
}

func TestTeamLogsNoTasks(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.outputs = map[string]string{
		strings.Join(teamLogsListArgv(), " "): `[{"run":"api-20261026T120000Z","tasks":[]}]`,
	}

	if err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false); err != nil {
		t.Fatalf("err = %v, want no error for a run with no tasks", err)
	}
	out := teamListOutput(t, a)
	if !strings.Contains(out, "no tasks in run api-20261026T120000Z") {
		t.Errorf("output does not say the run has no tasks:\n%s", out)
	}
}

func TestTeamLogsNeedsRunningContainer(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     model.Status
		failStatus bool
	}{
		{name: "stopped", status: model.StatusStopped},
		{name: "status fails", status: model.StatusRunning, failStatus: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := seedFake(t, tt.status)
			if tt.failStatus {
				fake.failStatus = func() error { return errors.New("engine unreachable") }
			}

			err := runContainerTeamLogs(t.Context(), a, "", "api", "", "", false)
			if err == nil {
				t.Fatal("read a log in a container that is not running")
			}
			if got := exitCodeOf(err); got != exitError {
				t.Errorf("exit code = %d, want %d", got, exitError)
			}
			for _, want := range []string{"dev container start-agent-team", "api"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
			if len(fake.execs) != 0 {
				t.Errorf("ran %d commands in a container that is not running: %v", len(fake.execs), fake.log)
			}
		})
	}
}

func TestTeamBaseFollowsPersistState(t *testing.T) {
	at := time.Date(2026, 10, 26, 10, 15, 0, 0, time.UTC)
	for _, tt := range []struct {
		persist bool
		base    string
	}{
		{false, "/tmp/dev-team"},
		{true, "/var/dev-state/dev-team"},
	} {
		run, dispatch := teamPaths(model.Container{Name: "api", PersistState: tt.persist}, at)
		if want := tt.base + "/runs/api-20261026T101500Z"; run.Dir != want {
			t.Errorf("persist %v: run dir = %q, want %q", tt.persist, run.Dir, want)
		}
		if want := tt.base + "/bin/dev-team"; dispatch != want {
			t.Errorf("persist %v: dispatch = %q, want %q", tt.persist, dispatch, want)
		}
	}
}
