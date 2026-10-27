package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/team"
)

// TestMain lets the test binary stand in for the dev-team executable: Start
// spawns `self supervise ID`, and self is os.Executable(), which under go test
// is this binary. The environment reaches that child by inheritance, because
// the test sets DEV_TEAM_RUN and PATH with t.Setenv.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "supervise" {
		os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
	}
	os.Exit(m.Run())
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, env []string, stdin string, args ...string) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr, env)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func newRunDir(t *testing.T, models map[team.Role]string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := team.Config{
		Lead:     team.Tool{ID: "claude"},
		Workers:  "claude",
		Models:   models,
		MaxTurns: team.DefaultMaxTurns,
		Timeout:  team.DefaultTimeout,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	return dir
}

// stubClaude puts a claude stub on a fresh PATH and returns that PATH. The
// sleep keeps the worker alive past Start's pid save, whose Load..Save window
// the engine documents as tolerable rather than impossible.
func stubClaude(t *testing.T) string {
	t.Helper()
	return stubClaudeSleeping(t, "0.5")
}

func stubClaudeSleeping(t *testing.T, sleep string) string {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "team", "testdata", "claude-stream.jsonl"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	dir := t.TempDir()
	body := "#!/bin/sh\ncat > /dev/null\nsleep " + sleep + "\ncat " + fixture + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	return dir + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
}

func TestStartThenWaitThenResult(t *testing.T) {
	dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
	t.Setenv("DEV_TEAM_RUN", dir)
	t.Setenv("PATH", stubClaude(t))
	env := os.Environ()

	start := runCLI(t, env, "write the note\n", "start", "coder")
	if start.code != 0 {
		t.Fatalf("start exit = %d, stderr = %q", start.code, start.stderr)
	}
	id := strings.TrimSuffix(start.stdout, "\n")
	if !strings.HasPrefix(id, "t") {
		t.Fatalf("start printed %q, want a task id", start.stdout)
	}
	if strings.Count(start.stdout, "\n") != 1 {
		t.Errorf("start printed %q, want only the task id and a newline", start.stdout)
	}

	wait := runCLI(t, env, "", "wait", id)
	if wait.code != 0 {
		t.Fatalf("wait exit = %d, stderr = %q", wait.code, wait.stderr)
	}
	if !strings.HasPrefix(wait.stdout, "STATUS: done\n") {
		t.Errorf("wait output = %q, want a STATUS: done block", wait.stdout)
	}
	if !strings.Contains(wait.stdout, "DONE") {
		t.Errorf("wait output = %q, want the worker's answer", wait.stdout)
	}

	result := runCLI(t, env, "", "result", id)
	if result.code != 0 {
		t.Fatalf("result exit = %d, stderr = %q", result.code, result.stderr)
	}
	if strings.TrimSpace(result.stdout) != "DONE" {
		t.Errorf("result output = %q, want DONE", result.stdout)
	}
}

func TestRunDirComesFromEnv(t *testing.T) {
	// The process environment has a run dir; the injected one does not. A
	// lookup through os.Getenv would pass here, so the env slice must win.
	t.Setenv("DEV_TEAM_RUN", t.TempDir())
	res := runCLI(t, nil, "", "list")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "DEV_TEAM_RUN") {
		t.Errorf("stderr = %q, want it to name DEV_TEAM_RUN", res.stderr)
	}
}

func TestUnknownRole(t *testing.T) {
	env := []string{"DEV_TEAM_RUN=" + newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})}
	res := runCLI(t, env, "task", "start", "reviewerr")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "reviewerr") {
		t.Errorf("stderr = %q, want it to name the role", res.stderr)
	}
}

func TestResultWhileRunning(t *testing.T) {
	dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
	task, err := team.Run{Dir: dir}.NewTask(team.RoleCoder, "a task")
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	res := runCLI(t, []string{"DEV_TEAM_RUN=" + dir}, "", "result", task.ID)
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "still running") || !strings.Contains(res.stderr, "wait") {
		t.Errorf("stderr = %q, want it to say the task is still running and to wait", res.stderr)
	}
}

func TestTakeoverNeedsReason(t *testing.T) {
	env := []string{"DEV_TEAM_RUN=" + t.TempDir()}
	res := runCLI(t, env, "", "takeover", "t1")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "--reason") {
		t.Errorf("stderr = %q, want it to name --reason", res.stderr)
	}
}

func TestInvalidTaskID(t *testing.T) {
	env := []string{"DEV_TEAM_RUN=" + t.TempDir()}
	res := runCLI(t, env, "", "wait", "../escape")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "invalid task id") {
		t.Errorf("stderr = %q, want it to say the id is invalid", res.stderr)
	}
}

func TestListJSON(t *testing.T) {
	dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
	run := team.Run{Dir: dir}
	for _, text := range []string{"one", "two"} {
		if _, err := run.NewTask(team.RoleCoder, text); err != nil {
			t.Fatalf("NewTask: %v", err)
		}
	}

	res := runCLI(t, []string{"DEV_TEAM_RUN=" + dir}, "", "list", "--json")
	if res.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", res.code, res.stderr)
	}
	var tasks []team.Task
	if err := json.Unmarshal([]byte(res.stdout), &tasks); err != nil {
		t.Fatalf("parse %q: %v", res.stdout, err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].ID != "t1" || tasks[1].ID != "t2" {
		t.Errorf("ids = %s, %s, want t1, t2", tasks[0].ID, tasks[1].ID)
	}

	table := runCLI(t, []string{"DEV_TEAM_RUN=" + dir}, "", "list")
	if table.code != 0 {
		t.Fatalf("table exit = %d, stderr = %q", table.code, table.stderr)
	}
	for _, want := range []string{"t1", "coder", "running"} {
		if !strings.Contains(table.stdout, want) {
			t.Errorf("table = %q, want it to contain %q", table.stdout, want)
		}
	}
}

func TestListAllRunsJSON(t *testing.T) {
	base := t.TempDir()
	// Run ids end in a UTC stamp, which ListRuns orders newest first.
	for _, spec := range []struct {
		id    string
		tasks int
	}{
		{"c-20260101T000000Z", 1},
		{"c-20260202T000000Z", 2},
	} {
		dir := filepath.Join(base, "runs", spec.id)
		run := team.Run{Dir: dir}
		for i := 0; i < spec.tasks; i++ {
			if _, err := run.NewTask(team.RoleCoder, "task"); err != nil {
				t.Fatalf("NewTask: %v", err)
			}
		}
	}

	// Decoded through the type cmd/dev-team encodes with, so a renamed JSON
	// field fails here rather than silently emptying the host's run list.
	parse := func(res cliResult) []team.RunTasks {
		t.Helper()
		if res.code != 0 {
			t.Fatalf("exit = %d, stderr = %q", res.code, res.stderr)
		}
		var runs []team.RunTasks
		if err := json.Unmarshal([]byte(res.stdout), &runs); err != nil {
			t.Fatalf("parse %q: %v", res.stdout, err)
		}
		return runs
	}

	// No DEV_TEAM_RUN: --all-runs locates runs under --base alone.
	runs := parse(runCLI(t, nil, "", "list", "--json", "--all-runs", "--base", base))
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Run != "c-20260202T000000Z" {
		t.Errorf("first run = %q, want the newest first", runs[0].Run)
	}
	if len(runs[0].Tasks) != 2 || len(runs[1].Tasks) != 1 {
		t.Errorf("task counts = %d, %d, want 2, 1", len(runs[0].Tasks), len(runs[1].Tasks))
	}

	// DEV_TEAM_BASE supplies the base when the flag is absent.
	runs = parse(runCLI(t, []string{"DEV_TEAM_BASE=" + base}, "", "list", "--json", "--all-runs"))
	if len(runs) != 2 {
		t.Fatalf("DEV_TEAM_BASE: got %d runs, want 2", len(runs))
	}

	// The flag wins over the environment.
	empty := t.TempDir()
	runs = parse(runCLI(t, []string{"DEV_TEAM_BASE=" + base}, "", "list", "--json", "--all-runs", "--base", empty))
	if len(runs) != 0 {
		t.Errorf("flag precedence: got %d runs, want 0", len(runs))
	}
}

func TestUnknownCommand(t *testing.T) {
	res := runCLI(t, nil, "", "bogus")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "bogus") {
		t.Errorf("stderr = %q, want it to name the command", res.stderr)
	}
}

func TestStartRetryFlagEitherOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(id string) []string
	}{
		{"flag after role", func(id string) []string { return []string{"start", "coder", "--retry", id} }},
		{"flag before role", func(id string) []string { return []string{"start", "--retry", id, "coder"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
			t.Setenv("DEV_TEAM_RUN", dir)
			t.Setenv("PATH", stubClaude(t))
			env := os.Environ()
			run := team.Run{Dir: dir}

			first, err := run.NewTask(team.RoleCoder, "first")
			if err != nil {
				t.Fatalf("NewTask: %v", err)
			}
			first.Status = team.StatusDone
			if err := run.Save(first); err != nil {
				t.Fatalf("Save: %v", err)
			}

			start := runCLI(t, env, "retry it\n", tc.args(first.ID)...)
			if start.code != 0 {
				t.Fatalf("start exit = %d, stderr = %q", start.code, start.stderr)
			}
			id := strings.TrimSpace(start.stdout)
			if wait := runCLI(t, env, "", "wait", id); wait.code != 0 {
				t.Fatalf("wait exit = %d, stderr = %q", wait.code, wait.stderr)
			}

			task, err := run.Load(id)
			if err != nil {
				t.Fatalf("Load(%s): %v", id, err)
			}
			if task.RetryOf != first.ID {
				t.Errorf("retry_of = %q, want %q", task.RetryOf, first.ID)
			}
			if task.Attempts != 2 {
				t.Errorf("attempts = %d, want 2", task.Attempts)
			}
		})
	}
}

func TestWaitForFlagEitherOrder(t *testing.T) {
	dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
	t.Setenv("DEV_TEAM_RUN", dir)
	t.Setenv("PATH", stubClaudeSleeping(t, "1.5"))
	env := os.Environ()

	start := runCLI(t, env, "slow task\n", "start", "coder")
	if start.code != 0 {
		t.Fatalf("start exit = %d, stderr = %q", start.code, start.stderr)
	}
	id := strings.TrimSpace(start.stdout)

	// Flags first, then after the positional: both are documented forms.
	running := runCLI(t, env, "", "wait", "--for", "50ms", id)
	if running.code != 0 {
		t.Fatalf("wait exit = %d, stderr = %q", running.code, running.stderr)
	}
	if !strings.HasPrefix(running.stdout, "STATUS: running") {
		t.Errorf("wait output = %q, want STATUS: running inside the bound", running.stdout)
	}

	done := runCLI(t, env, "", "wait", id, "--for", "10s")
	if done.code != 0 {
		t.Fatalf("wait exit = %d, stderr = %q", done.code, done.stderr)
	}
	if !strings.HasPrefix(done.stdout, "STATUS: done\n") {
		t.Errorf("wait output = %q, want STATUS: done", done.stdout)
	}
}

func TestWaitBadDurationAfterTaskID(t *testing.T) {
	env := []string{"DEV_TEAM_RUN=" + t.TempDir()}
	res := runCLI(t, env, "", "wait", "t1", "--for", "abc")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "duration") {
		t.Errorf("stderr = %q, want the bad duration reported", res.stderr)
	}
	if strings.Contains(res.stderr, "needs a task id") {
		t.Errorf("stderr = %q, want the duration error, not a missing positional", res.stderr)
	}
}

func TestTakeoverReasonEitherOrder(t *testing.T) {
	dir := newRunDir(t, map[team.Role]string{team.RoleCoder: "haiku"})
	run := team.Run{Dir: dir}
	for i := 0; i < 3; i++ {
		if _, err := run.NewTask(team.RoleCoder, "task"); err != nil {
			t.Fatalf("NewTask: %v", err)
		}
	}
	env := []string{"DEV_TEAM_RUN=" + dir}

	for _, tc := range []struct {
		name   string
		id     string
		args   []string
		reason string
	}{
		{"flag after id", "t1", []string{"takeover", "t1", "--reason", "because"}, "because"},
		{"flag before id", "t2", []string{"takeover", "--reason", "because too", "t2"}, "because too"},
		{"dash-leading value", "t3", []string{"takeover", "t3", "--reason", "-x"}, "-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, env, "", tc.args...)
			if res.code != 0 {
				t.Fatalf("exit = %d, stderr = %q", res.code, res.stderr)
			}
			task, err := run.Load(tc.id)
			if err != nil {
				t.Fatalf("Load(%s): %v", tc.id, err)
			}
			if !task.TookOver {
				t.Errorf("took_over = false, want true")
			}
			if task.TakeoverReason != tc.reason {
				t.Errorf("takeover_reason = %q, want %q", task.TakeoverReason, tc.reason)
			}
		})
	}
}

func TestPositionalCounts(t *testing.T) {
	env := []string{"DEV_TEAM_RUN=" + t.TempDir()}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"wait extra", []string{"wait", "t1", "t2"}, "one task id"},
		{"wait missing", []string{"wait", "--for", "1s"}, "needs a task id"},
		{"start extra", []string{"start", "coder", "reviewer"}, "one role"},
		{"takeover extra", []string{"takeover", "t1", "t2", "--reason", "x"}, "one task id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, env, "", tc.args...)
			if res.code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", res.code, res.stderr)
			}
			if !strings.Contains(res.stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", res.stderr, tc.want)
			}
		})
	}
}
