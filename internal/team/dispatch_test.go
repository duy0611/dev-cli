package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	fixtureSession = "f7771d53-d8d6-4c81-969a-39ae4219bbb9"
	fixtureCost    = 0.006312285
	fixtureEdit    = "/tmp/claudefix/note.txt"
)

type harness struct {
	t   *testing.T
	run Run
	bin string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, run: Run{Dir: t.TempDir()}, bin: t.TempDir()}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.run.Dir, configFile), data, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	h.setPath(h.bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin")
	return h
}

func testConfig(models map[Role]string) Config {
	return Config{
		Lead:     Tool{ID: "claude"},
		Workers:  "claude",
		Models:   models,
		MaxTurns: DefaultMaxTurns,
		Timeout:  DefaultTimeout,
	}
}

func (h *harness) setPath(p string) {
	h.t.Helper()
	h.t.Setenv("PATH", p)
}

func (h *harness) stub(body string) {
	h.t.Helper()
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(filepath.Join(h.bin, "claude"), []byte(script), 0o755); err != nil {
		h.t.Fatalf("write claude stub: %v", err)
	}
}

func (h *harness) task(role Role, text, cwd string) Task {
	h.t.Helper()
	task := mustNewTask(h.t, h.run, role, text)
	task.Cwd = cwd
	if err := h.run.Save(task); err != nil {
		h.t.Fatalf("Save: %v", err)
	}
	return task
}

// finished saves a terminal task, as if its supervisor had run.
func (h *harness) finished(role Role, text, session string) Task {
	h.t.Helper()
	task := h.task(role, text, h.t.TempDir())
	task.SessionID = session
	h.save(task)
	h.markDone(task.ID)
	return h.load(task.ID)
}

func (h *harness) save(task Task) {
	h.t.Helper()
	if err := h.run.Save(task); err != nil {
		h.t.Fatalf("Save: %v", err)
	}
}

func (h *harness) markDone(id string) {
	h.t.Helper()
	task := h.load(id)
	task.Status = StatusDone
	h.save(task)
}

func (h *harness) load(id string) Task {
	h.t.Helper()
	task, err := h.run.Load(id)
	if err != nil {
		h.t.Fatalf("Load(%s): %v", id, err)
	}
	return task
}

func (h *harness) result(id string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.run.TaskDir(id), taskResult))
	if err != nil {
		h.t.Fatalf("read result.md: %v", err)
	}
	return string(data)
}

func (h *harness) log(id string) []byte {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.run.TaskDir(id), taskLog))
	if err != nil {
		h.t.Fatalf("read log.jsonl: %v", err)
	}
	return data
}

func (h *harness) supervise(id string) error {
	h.t.Helper()
	return h.run.Supervise(context.Background(), id)
}

func fixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "claude-stream.jsonl"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	return p
}

func writeSelf(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "self")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write self script: %v", err)
	}
	return p
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return p
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileHasBytes(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid file %q: %v", data, err)
	}
	return pid
}

func assertGone(t *testing.T, pid int, what string) {
	t.Helper()
	waitFor(t, 3*time.Second, what+" to exit", func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
}

func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait true: %v", err)
	}
	return pid
}

const happyStub = `cat > "$TEAM_TEST_STDIN"
printf '%s\n' "$@" > "$TEAM_TEST_ARGV"
cat "$TEAM_TEST_FIXTURE"
`

func TestSuperviseHappyPath(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	argvFile := filepath.Join(t.TempDir(), "argv")
	t.Setenv("TEAM_TEST_STDIN", stdinFile)
	t.Setenv("TEAM_TEST_ARGV", argvFile)
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(happyStub)

	task := h.task(RoleCoder, "write the note", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want %q", got.Status, StatusDone)
	}
	if got.SessionID != fixtureSession {
		t.Errorf("session id = %q, want %q", got.SessionID, fixtureSession)
	}
	if got.CostUSD == nil {
		t.Fatalf("cost = nil, want %v", fixtureCost)
	}
	if diff := math.Abs(*got.CostUSD - fixtureCost); diff > 1e-9 {
		t.Errorf("cost = %v, want %v", *got.CostUSD, fixtureCost)
	}
	if got.Turns != 3 {
		t.Errorf("turns = %d, want 3", got.Turns)
	}
	if got.EndedAt.IsZero() || got.EndedAt.Location() != time.UTC {
		t.Errorf("ended at = %v, want a UTC timestamp", got.EndedAt)
	}
	if got.Collected {
		t.Error("collected = true, want false before anyone waits")
	}
	if answer := h.result(task.ID); answer != "DONE" {
		t.Errorf("result.md = %q, want DONE", answer)
	}
	fixture, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if log := h.log(task.ID); !bytes.Equal(log, fixture) {
		t.Errorf("log.jsonl is %d bytes, want the %d byte fixture verbatim", len(log), len(fixture))
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if string(stdin) != "write the note" {
		t.Errorf("stdin = %q, want the task text", stdin)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	if !strings.Contains(string(argv), "--model\nhaiku\n") {
		t.Errorf("argv = %q, want the worker model", argv)
	}
	if strings.Contains(string(argv), "write the note") {
		t.Errorf("argv = %q, want no task text in argv", argv)
	}
}

func TestRunWorkerEnvironment(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		env  []string
	}{
		{name: "no worker env"},
		{name: "worker env", env: []string{"TEAM_TEST_EXTRA=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			if _, err := (Run{}).runWorker(context.Background(), workerRun{
				argv:     []string{"env"},
				env:      tt.env,
				dir:      dir,
				timeout:  10 * time.Second,
				maxTurns: 10,
				parser:   newClaudeParser(),
				log:      &log,
			}); err != nil {
				t.Fatalf("runWorker: %v", err)
			}

			env := make(map[string]string)
			for _, line := range strings.Split(log.String(), "\n") {
				if key, value, ok := strings.Cut(line, "="); ok {
					env[key] = value
				}
			}
			if env["PWD"] != dir {
				t.Errorf("PWD = %q, want the worker directory %q", env["PWD"], dir)
			}
			if tt.env == nil {
				if _, ok := env["TEAM_TEST_EXTRA"]; ok {
					t.Error("TEAM_TEST_EXTRA is set without the worker env")
				}
				return
			}
			if env["TEAM_TEST_EXTRA"] != "1" {
				t.Errorf("TEAM_TEST_EXTRA = %q, want the worker env applied", env["TEAM_TEST_EXTRA"])
			}
		})
	}
}

func TestSuperviseWorkerPWD(t *testing.T) {
	t.Run("claude without env", func(t *testing.T) {
		h := newHarness(t, testConfig(map[Role]string{RoleCoder: "m"}))
		dir := t.TempDir()
		pwdFile := filepath.Join(t.TempDir(), "pwd")
		t.Setenv("TEAM_TEST_PWD", pwdFile)
		t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
		h.stub(`cat > /dev/null
printf '%s\n' "$PWD" > "$TEAM_TEST_PWD"
cat "$TEAM_TEST_FIXTURE"
`)

		task := h.task(RoleCoder, "job", dir)
		if err := h.supervise(task.ID); err != nil {
			t.Fatalf("Supervise: %v", err)
		}
		assertPWD(t, pwdFile, dir)
	})

	t.Run("opencode with env", func(t *testing.T) {
		cfg := testConfig(map[Role]string{RoleCoder: "m"})
		cfg.Workers = "opencode"
		h := newHarness(t, cfg)
		dir := t.TempDir()
		pwdFile := filepath.Join(t.TempDir(), "pwd")
		envFile := filepath.Join(t.TempDir(), "env")
		t.Setenv("TEAM_TEST_PWD", pwdFile)
		t.Setenv("TEAM_TEST_ENV", envFile)
		t.Setenv("TEAM_TEST_FIXTURE", opencodeFixturePath(t))
		writeOpencodeStub(t, h.bin, `cat > /dev/null
printf '%s\n' "$PWD" > "$TEAM_TEST_PWD"
env > "$TEAM_TEST_ENV"
cat "$TEAM_TEST_FIXTURE"
`)

		task := h.task(RoleCoder, "job", dir)
		if err := h.supervise(task.ID); err != nil {
			t.Fatalf("Supervise: %v", err)
		}
		assertPWD(t, pwdFile, dir)
		env, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatalf("read recorded env: %v", err)
		}
		if !strings.Contains(string(env), opencodeConfigEnv+"=") {
			t.Errorf("worker env = %q, want the per-run config applied", env)
		}
	})
}

func assertPWD(t *testing.T, file, dir string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read recorded PWD: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != dir {
		t.Errorf("$PWD = %q, want the task directory %q", got, dir)
	}
}

func TestTaskTextReachesStdinVerbatim(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("TEAM_TEST_STDIN", stdinFile)
	t.Setenv("TEAM_TEST_ARGV", filepath.Join(t.TempDir(), "argv"))
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(happyStub)

	text := "- leading dash\n-- second dash\n\nquotes: \"double\" and 'single'\n" +
		"shell: $(touch /tmp/dev-team-should-not-exist) `id` $HOME\n" +
		"backslash \\ and a tab:\tend"
	task := h.task(RoleCoder, text, t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if string(got) != text {
		t.Errorf("stdin = %q, want %q byte for byte", got, text)
	}
}

func TestTaskTextHundredsOfKBReachesStdinVerbatim(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("TEAM_TEST_STDIN", stdinFile)
	t.Setenv("TEAM_TEST_ARGV", filepath.Join(t.TempDir(), "argv"))
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(happyStub)

	line := "-- line with \"quotes\", 'single', $(touch nope), `id`, $HOME, \\ and\ttab\n"
	text := "- leading dash\n" + strings.Repeat(line, 6000) + "end without newline"
	if len(text) < 300*1024 {
		t.Fatalf("test text is %d bytes, want several hundred KB", len(text))
	}
	task := h.task(RoleCoder, text, t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if !bytes.Equal(got, []byte(text)) {
		t.Errorf("stub received %d bytes, want the %d byte task text byte for byte", len(got), len(text))
	}
}

func TestSuperviseTimeoutKillsProcessGroup(t *testing.T) {
	oldGrace := terminateGrace
	terminateGrace = 100 * time.Millisecond
	t.Cleanup(func() { terminateGrace = oldGrace })

	cfg := testConfig(map[Role]string{RoleCoder: "haiku"})
	cfg.Timeout = time.Second
	h := newHarness(t, cfg)
	pidFile := filepath.Join(t.TempDir(), "pid")
	childFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("TEAM_TEST_PID", pidFile)
	t.Setenv("TEAM_TEST_CHILD", childFile)
	h.stub(`cat > /dev/null
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"working"}]},"session_id":"s1"}'
trap '' TERM
sleep 300 &
echo $! > "$TEAM_TEST_CHILD"
echo $$ > "$TEAM_TEST_PID"
while :; do sleep 1; done
`)

	task := h.task(RoleCoder, "slow job", t.TempDir())
	started := time.Now()
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Errorf("Supervise took %s, want the timeout plus a short grace period", elapsed)
	}

	got := h.load(task.ID)
	if got.Status != StatusTimedOut {
		t.Errorf("status = %q, want %q", got.Status, StatusTimedOut)
	}
	answer := h.result(task.ID)
	if !strings.Contains(answer, "timed out") {
		t.Errorf("result.md = %q, want it to say timed out", answer)
	}
	if !strings.Contains(answer, "text: working") {
		t.Errorf("result.md = %q, want the log tail", answer)
	}
	if got.Turns != 1 {
		t.Errorf("turns = %d, want 1", got.Turns)
	}
	assertGone(t, readPid(t, pidFile), "worker shell")
	assertGone(t, readPid(t, childFile), "worker child")
}

func TestSuperviseExitsWithChildHoldingStdout(t *testing.T) {
	oldDrain := exitDrain
	exitDrain = 50 * time.Millisecond
	t.Cleanup(func() { exitDrain = oldDrain })

	cfg := testConfig(map[Role]string{RoleCoder: "haiku"})
	cfg.Timeout = 10 * time.Second
	h := newHarness(t, cfg)
	childFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("TEAM_TEST_CHILD", childFile)
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
cat "$TEAM_TEST_FIXTURE"
sleep 300 &
echo $! > "$TEAM_TEST_CHILD"
exit 0
`)

	task := h.task(RoleCoder, "job", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run.Supervise(ctx, task.ID) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	waitFor(t, 2*time.Second, "the worker to finish", func() bool {
		task, err := h.run.Load(task.ID)
		return err == nil && task.Status != StatusRunning
	})
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done: the worker exited even though its child held stdout", got.Status)
	}
	if answer := h.result(task.ID); answer != "DONE" {
		t.Errorf("result.md = %q, want DONE", answer)
	}
	assertGone(t, readPid(t, childFile), "worker's background child")
}

func TestSuperviseTurnLimit(t *testing.T) {
	cfg := testConfig(map[Role]string{RoleCoder: "haiku"})
	cfg.MaxTurns = 2
	h := newHarness(t, cfg)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("TEAM_TEST_PID", pidFile)
	// The first two lines share a message id: a line-counting cap would kill
	// one turn too early and record the wrong count.
	h.stub(`cat > /dev/null
echo $$ > "$TEAM_TEST_PID"
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":""}]}}'
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"one"}]}}'
printf '%s\n' '{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"two"}]}}'
printf '%s\n' '{"type":"assistant","message":{"id":"m3","content":[{"type":"text","text":"three"}]}}'
sleep 300
`)

	task := h.task(RoleCoder, "loop", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusTurnLimit {
		t.Errorf("status = %q, want %q", got.Status, StatusTurnLimit)
	}
	if got.Turns != 3 {
		t.Errorf("turns = %d, want the 3 distinct assistant messages", got.Turns)
	}
	if answer := h.result(task.ID); !strings.Contains(answer, "hit the turn limit") {
		t.Errorf("result.md = %q, want it to say hit the turn limit", answer)
	}
	assertGone(t, readPid(t, pidFile), "worker")
}

func TestSuperviseFailureAnswerCarriesStderrTail(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	h.stub("cat > /dev/null\necho 'error: unknown option --bogus' >&2\nexit 1\n")
	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	if got := h.load(task.ID); got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	answer := h.result(task.ID)
	for _, want := range []string{"worker exited with code 1", "stderr (last 4 KiB):", "error: unknown option --bogus"} {
		if !strings.Contains(answer, want) {
			t.Errorf("result.md = %q, want it to say %q", answer, want)
		}
	}

	var out bytes.Buffer
	if err := h.run.Wait(task.ID, time.Second, &out); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(out.String(), "error: unknown option --bogus") {
		t.Errorf("Wait printed %q, want the stderr tail", out.String())
	}
	if strings.Contains(string(h.log(task.ID)), "unknown option") {
		t.Error("stderr reached log.jsonl, which the stream parser reads")
	}
}

func TestSuperviseStderrTailIsBounded(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	h.stub(`cat > /dev/null
i=0
while [ $i -lt 2000 ]; do
	echo "noise line $i ............................................................" >&2
	i=$((i+1))
done
echo 'the-final-reason' >&2
exit 1
`)
	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	answer := h.result(task.ID)
	if !strings.Contains(answer, "the-final-reason") {
		t.Errorf("result.md lacks the last stderr line:\n%s", answer)
	}
	if strings.Contains(answer, "noise line 0 ") {
		t.Errorf("result.md keeps the start of >100 KiB of stderr")
	}
	if len(answer) > stderrTail+1024 {
		t.Errorf("result.md is %d bytes, want the stderr tail kept to %d", len(answer), stderrTail)
	}
}

func TestSuperviseSuccessIgnoresStderrNoise(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub("cat > /dev/null\necho 'deprecation warning' >&2\ncat \"$TEAM_TEST_FIXTURE\"\n")
	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	if got := h.load(task.ID); got.Status != StatusDone {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if answer := h.result(task.ID); answer != "DONE" {
		t.Errorf("result.md = %q, want the bare answer", answer)
	}
}

func TestTailBufferKeepsOnlyTheEnd(t *testing.T) {
	b := newTailBuffer(8)
	for _, chunk := range []string{"abc", "defgh", "ij", "klmnopqrstu"} {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := string(b.Bytes()); got != "nopqrstu" {
		t.Errorf("tail = %q, want nopqrstu", got)
	}
}

func TestSuperviseWorkerFailsToStart(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	h.setPath(t.TempDir())

	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want %q", got.Status, StatusFailed)
	}
	if got.EndedAt.IsZero() {
		t.Error("ended at is zero, want a timestamp")
	}
	if answer := h.result(task.ID); !strings.Contains(answer, "claude") {
		t.Errorf("result.md = %q, want it to name claude", answer)
	}
}

func TestSuperviseErrorReasonFromStream(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		want    string
		notWant string
	}{
		{
			name:    "tool message",
			stream:  `{"type":"error","timestamp":1782345600000,"error":{"type":"unknown","message":"Invalid model reference: deepseek-v4.1-flash"}}`,
			want:    "Invalid model reference: deepseek-v4.1-flash",
			notWant: "worker reported an error",
		},
		{
			name:   "no message and no final text",
			stream: `{"type":"error"}`,
			want:   "worker reported an error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(map[Role]string{RoleCoder: "m"})
			cfg.Workers = "opencode"
			h := newHarness(t, cfg)
			writeOpencodeStub(t, h.bin, `cat > /dev/null
printf '%s\n' '`+tt.stream+`'
exit 0
`)

			task := h.task(RoleCoder, "job", t.TempDir())
			if err := h.supervise(task.ID); err != nil {
				t.Fatalf("Supervise: %v", err)
			}
			if got := h.load(task.ID); got.Status != StatusFailed {
				t.Errorf("status = %q, want %q", got.Status, StatusFailed)
			}
			answer := h.result(task.ID)
			if !strings.Contains(answer, tt.want) {
				t.Errorf("result.md = %q, want it to contain %q", answer, tt.want)
			}
			if tt.notWant != "" && strings.Contains(answer, tt.notWant) {
				t.Errorf("result.md = %q, want it not to contain %q", answer, tt.notWant)
			}
		})
	}
}

func TestSuperviseErrorMarksFailed(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	h.stub("cat > /dev/null\n")
	task := h.task(RoleCoder, "job", t.TempDir())
	if err := os.Remove(filepath.Join(h.run.TaskDir(task.ID), taskPrompt)); err != nil {
		t.Fatalf("remove task.md: %v", err)
	}
	_ = h.supervise(task.ID)

	got := h.load(task.ID)
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed: every Supervise error must record a failure", got.Status)
	}
	if answer := h.result(task.ID); !strings.Contains(answer, "prompt") {
		t.Errorf("result.md = %q, want it to name the unreadable prompt", answer)
	}
}

func TestSecondCoderRefused(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku", RoleResearcher: "haiku"}))
	spawnLog := filepath.Join(t.TempDir(), "spawn.log")
	t.Setenv("TEAM_TEST_SPAWN", spawnLog)
	self := writeScript(t, `printf '%s\n' "$@" >> "$TEAM_TEST_SPAWN"
sleep 300
`)
	var pids []int
	t.Cleanup(func() {
		for _, pid := range pids {
			if pid > 0 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})

	first, err := h.run.Start(RoleCoder, "one", "", self)
	if err != nil {
		t.Fatalf("Start coder: %v", err)
	}
	pids = append(pids, first.Pid)
	if first.Pid == 0 {
		t.Error("pid = 0, want the supervisor's pid")
	}
	_, err = h.run.Start(RoleCoder, "two", "", self)
	if err == nil {
		t.Fatal("second coder start = nil error")
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("error = %q, want it to name the running task %s", err, first.ID)
	}

	second, err := h.run.Start(RoleResearcher, "look", "", self)
	if err != nil {
		t.Fatalf("Start researcher alongside a coder: %v", err)
	}
	pids = append(pids, second.Pid)
	tasks, err := h.run.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 || tasks[1].ID != second.ID {
		t.Errorf("tasks = %v, want the coder and the researcher only", tasks)
	}

	waitFor(t, 3*time.Second, "the supervisor to be spawned", func() bool {
		data, err := os.ReadFile(spawnLog)
		return err == nil && strings.Contains(string(data), "supervise\nt1\n")
	})
}

func TestResearcherAlongsideCoderAllowed(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{
		RoleCoder:      "haiku",
		RoleResearcher: "haiku",
	}))
	self := writeSelf(t)

	coder, err := h.run.Start(RoleCoder, "one", "", self)
	if err != nil {
		t.Fatalf("Start coder: %v", err)
	}
	researcher, err := h.run.Start(RoleResearcher, "look", "", self)
	if err != nil {
		t.Fatalf("Start researcher: %v", err)
	}
	if coder.Status != StatusRunning || researcher.Status != StatusRunning {
		t.Errorf("statuses = %q, %q, want both running", coder.Status, researcher.Status)
	}
}

func TestRetryBoundedAtThreeAttempts(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	argvFile := filepath.Join(t.TempDir(), "argv")
	t.Setenv("TEAM_TEST_ARGV", argvFile)
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
printf '%s\n' "$@" >> "$TEAM_TEST_ARGV"
printf '%s\n' '---' >> "$TEAM_TEST_ARGV"
cat "$TEAM_TEST_FIXTURE"
`)
	self := writeSelf(t)

	first, err := h.run.Start(RoleCoder, "first", "", self)
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	if err := h.supervise(first.ID); err != nil {
		t.Fatalf("Supervise first: %v", err)
	}
	first = h.load(first.ID)
	if first.Status != StatusDone || first.SessionID != fixtureSession {
		t.Fatalf("first = %+v, want done with session %s", first, fixtureSession)
	}

	second, err := h.run.Start(RoleCoder, "second", first.ID, self)
	if err != nil {
		t.Fatalf("Start retry: %v", err)
	}
	if second.Attempts != 2 || second.RetryOf != first.ID || second.SessionID != fixtureSession {
		t.Errorf("retry = %+v, want attempt 2 of %s resuming %s", second, first.ID, fixtureSession)
	}
	if err := h.supervise(second.ID); err != nil {
		t.Fatalf("Supervise retry: %v", err)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if !strings.Contains(string(argv), "--resume\n"+fixtureSession+"\n") {
		t.Errorf("worker argv = %q, want --resume %s", argv, fixtureSession)
	}

	third, err := h.run.Start(RoleCoder, "third", second.ID, self)
	if err != nil {
		t.Fatalf("Start third attempt: %v", err)
	}
	if third.Attempts != MaxAttempts || third.RetryOf != second.ID {
		t.Errorf("third = %+v, want attempt %d of %s", third, MaxAttempts, second.ID)
	}
	if err := h.supervise(third.ID); err != nil {
		t.Fatalf("Supervise third attempt: %v", err)
	}

	if _, err := h.run.Start(RoleCoder, "fourth", third.ID, self); err == nil {
		t.Fatal("fourth attempt = nil error")
	} else if !strings.Contains(err.Error(), "takeover") {
		t.Errorf("error = %q, want it to point at takeover", err)
	}
}

func TestRetryOfAnAlreadyRetriedTaskRefused(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	self := writeSelf(t)

	first := h.finished(RoleCoder, "first", "")
	second, err := h.run.Start(RoleCoder, "second", first.ID, self)
	if err != nil {
		t.Fatalf("Start retry: %v", err)
	}
	h.markDone(second.ID)

	_, err = h.run.Start(RoleCoder, "again", first.ID, self)
	if err == nil {
		t.Fatal("retrying the same task twice = nil error")
	}
	for _, want := range []string{first.ID, second.ID, "already retried"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
	tasks, err := h.run.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("got %d tasks, want the refused retry to create none", len(tasks))
	}
}

func TestRetryChainCountsAttemptsThenRefuses(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	self := writeSelf(t)

	current := h.finished(RoleCoder, "first", "")
	for attempt := 2; attempt <= MaxAttempts; attempt++ {
		next, err := h.run.Start(RoleCoder, "again", current.ID, self)
		if err != nil {
			t.Fatalf("retry %d: %v", attempt, err)
		}
		if next.Attempts != attempt || next.RetryOf != current.ID {
			t.Fatalf("retry = %+v, want attempt %d of %s", next, attempt, current.ID)
		}
		h.markDone(next.ID)
		current = h.load(next.ID)
	}
	_, err := h.run.Start(RoleCoder, "once more", current.ID, self)
	if err == nil || !strings.Contains(err.Error(), "takeover") {
		t.Fatalf("retry past MaxAttempts error = %v, want one pointing at takeover", err)
	}
}

func TestRetryWithAnotherRoleRefused(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku", RoleReviewer: "haiku"}))

	coder := h.finished(RoleCoder, "build", "session-c")
	_, err := h.run.Start(RoleReviewer, "review", coder.ID, writeSelf(t))
	if err == nil {
		t.Fatal("a reviewer retrying a coder task = nil error")
	}
	for _, want := range []string{string(RoleCoder), string(RoleReviewer)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestWaitDetectsDeadSupervisor(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	task := h.task(RoleCoder, "job", t.TempDir())
	task.Pid = deadPid(t)
	if err := h.run.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var out bytes.Buffer
	started := time.Now()
	if err := h.run.Wait(task.ID, time.Second, &out); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("Wait took %s, want an immediate failure once the supervisor is dead", elapsed)
	}
	if !strings.HasPrefix(out.String(), "STATUS: failed\n") {
		t.Errorf("Wait output = %q, want STATUS: failed", out.String())
	}
	if !strings.Contains(out.String(), "supervisor") {
		t.Errorf("Wait output = %q, want it to mention the dead supervisor", out.String())
	}
	got := h.load(task.ID)
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if !got.Collected {
		t.Error("collected = false, want true after Wait returned the failure")
	}
}

func TestFailIfRunningLeavesFinishedTask(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	task := h.task(RoleCoder, "job", t.TempDir())
	task.Pid = deadPid(t)
	if err := h.run.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The supervisor finished after the caller's stale running read and before
	// the failure path got to re-check.
	finished := h.load(task.ID)
	finished.Status = StatusDone
	finished.EndedAt = time.Now().UTC()
	if err := h.run.Save(finished); err != nil {
		t.Fatalf("Save finished: %v", err)
	}
	answer := "the real answer"
	resultPath := filepath.Join(h.run.TaskDir(task.ID), taskResult)
	if err := os.WriteFile(resultPath, []byte(answer), 0o644); err != nil {
		t.Fatalf("write result.md: %v", err)
	}
	recordPath := filepath.Join(h.run.TaskDir(task.ID), taskRecord)
	wantRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read task.json: %v", err)
	}

	if err := h.run.failIfRunning(task.ID, supervisorDied); err != nil {
		t.Fatalf("failIfRunning: %v", err)
	}

	gotRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read task.json after: %v", err)
	}
	if !bytes.Equal(gotRecord, wantRecord) {
		t.Errorf("task.json changed:\n%s\nwant:\n%s", gotRecord, wantRecord)
	}
	gotResult, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result.md after: %v", err)
	}
	if string(gotResult) != answer {
		t.Errorf("result.md = %q, want the finished answer %q", gotResult, answer)
	}
	if got := h.load(task.ID); got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
}

func TestWaitDoesNotOverwriteTaskFinishedMeanwhile(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	task := h.task(RoleCoder, "job", t.TempDir())
	task.Pid = deadPid(t)
	if err := h.run.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}
	answer := "the real answer"

	oldHook := waitProbeHook
	waitProbeHook = func() {
		finished, err := h.run.Load(task.ID)
		if err != nil {
			t.Errorf("hook load: %v", err)
			return
		}
		finished.Status = StatusDone
		finished.EndedAt = time.Now().UTC()
		if err := h.run.Save(finished); err != nil {
			t.Errorf("hook save: %v", err)
		}
		if err := os.WriteFile(filepath.Join(h.run.TaskDir(task.ID), taskResult), []byte(answer), 0o644); err != nil {
			t.Errorf("hook write result.md: %v", err)
		}
	}
	t.Cleanup(func() { waitProbeHook = oldHook })

	var out bytes.Buffer
	if err := h.run.Wait(task.ID, time.Second, &out); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.HasPrefix(out.String(), "STATUS: done\n") {
		t.Errorf("Wait output = %q, want STATUS: done", out.String())
	}
	if !strings.Contains(out.String(), answer) {
		t.Errorf("Wait output = %q, want the finished answer", out.String())
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if !got.Collected {
		t.Error("collected = false, want Wait to collect the finished task")
	}
	if result := h.result(task.ID); result != answer {
		t.Errorf("result.md = %q, want %q", result, answer)
	}
}

func TestStartAllowsCoderAfterDeadSupervisor(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	dead := h.task(RoleCoder, "dead", t.TempDir())
	dead.Pid = deadPid(t)
	if err := h.run.Save(dead); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := h.run.Start(RoleCoder, "next", "", writeSelf(t))
	if err != nil {
		t.Fatalf("Start after a dead supervisor: %v", err)
	}
	if next.Status != StatusRunning {
		t.Errorf("new coder status = %q, want running", next.Status)
	}
	if got := h.load(dead.ID); got.Status != StatusFailed {
		t.Errorf("dead coder status = %q, want failed", got.Status)
	}
}

func TestRetryAfterDeadSupervisor(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	dead := h.task(RoleCoder, "dead", t.TempDir())
	dead.Pid = deadPid(t)
	dead.SessionID = "session-x"
	if err := h.run.Save(dead); err != nil {
		t.Fatalf("Save: %v", err)
	}

	retry, err := h.run.Start(RoleCoder, "retry", dead.ID, writeSelf(t))
	if err != nil {
		t.Fatalf("retry after a dead supervisor: %v", err)
	}
	if retry.Attempts != 2 || retry.RetryOf != dead.ID || retry.SessionID != "session-x" {
		t.Errorf("retry = %+v, want attempt 2 of %s resuming session-x", retry, dead.ID)
	}
	if got := h.load(dead.ID); got.Status != StatusFailed {
		t.Errorf("dead coder status = %q, want failed", got.Status)
	}
}

func TestStartRechecksCoderAfterNewTask(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	existing := h.task(RoleResearcher, "first", t.TempDir())

	oldHook := taskCreatedHook
	taskCreatedHook = func() {
		task, err := h.run.Load(existing.ID)
		if err != nil {
			t.Errorf("hook load: %v", err)
			return
		}
		task.Role = RoleCoder
		task.Status = StatusRunning
		task.Pid = os.Getpid()
		if err := h.run.Save(task); err != nil {
			t.Errorf("hook save: %v", err)
		}
	}
	t.Cleanup(func() { taskCreatedHook = oldHook })

	_, err := h.run.Start(RoleCoder, "second", "", writeSelf(t))
	if err == nil {
		t.Fatal("Start = nil error, want the older coder refused")
	}
	if !strings.Contains(err.Error(), existing.ID) {
		t.Errorf("error = %q, want it to name the older coder %s", err, existing.ID)
	}
	tasks, err := h.run.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %v, want the older coder and the refused task", tasks)
	}
	refused := tasks[1]
	if refused.Status != StatusFailed {
		t.Errorf("refused task status = %q, want failed", refused.Status)
	}
	if refused.Pid != 0 {
		t.Errorf("refused task pid = %d, want no spawned supervisor", refused.Pid)
	}
	if answer := h.result(refused.ID); !strings.Contains(answer, "refused: coder "+existing.ID+" is running") {
		t.Errorf("refused result.md = %q, want the refusal reason", answer)
	}
}

func TestFilesChangedFromGit(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "seed.txt"), "seed\n")
	gitCmd(t, repo, "add", "seed.txt")
	gitCmd(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-qm", "init")

	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
printf 'changed\n' > a.txt
printf 'new\n' > b.txt
cat "$TEAM_TEST_FIXTURE"
`)

	task := h.task(RoleCoder, "edit", repo)
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if want := []string{"a.txt", "b.txt", fixtureEdit}; !slices.Equal(got.FilesChanged, want) {
		t.Errorf("files changed = %v, want %v", got.FilesChanged, want)
	}
}

func TestFilesChangedIncludesAlreadyDirtyFile(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "clean\n")
	gitCmd(t, repo, "add", "dirty.txt")
	gitCmd(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-qm", "init")
	// The lead's uncommitted edit makes the path dirty before the worker runs.
	writeFile(t, filepath.Join(repo, "dirty.txt"), "lead edit\n")

	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
printf 'worker edit\n' >> dirty.txt
cat "$TEAM_TEST_FIXTURE"
`)

	task := h.task(RoleCoder, "edit", repo)
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if want := []string{"dirty.txt", fixtureEdit}; !slices.Equal(got.FilesChanged, want) {
		t.Errorf("files changed = %v, want the re-edited dirty file and the stream-only edit %v", got.FilesChanged, want)
	}
}

func TestFilesChangedFallsBackToStream(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	h.setPath(h.bin) // no git, no cat: the stub uses shell builtins only
	h.stub(`printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/team-test/note.txt"}}]},"session_id":"s1"}'
printf '%s\n' '{"type":"result","result":"DONE","session_id":"s1","num_turns":1,"is_error":false}'
`)

	if changed, err := gitChanged(t.TempDir()); err != nil || len(changed) != 0 {
		t.Errorf("gitChanged without git = %v, %v, want an empty map and no error", changed, err)
	}

	task := h.task(RoleCoder, "edit", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if want := []string{"/tmp/team-test/note.txt"}; !slices.Equal(got.FilesChanged, want) {
		t.Errorf("files changed = %v, want the stream's edits %v", got.FilesChanged, want)
	}
}

func TestWaitReturnsRunningAtBound(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("TEAM_TEST_RELEASE", release)
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"thinking about it"}]},"session_id":"s1"}'
while [ ! -f "$TEAM_TEST_RELEASE" ]; do sleep 0.05; done
cat "$TEAM_TEST_FIXTURE"
`)

	task := h.task(RoleCoder, "slow", t.TempDir())
	var once sync.Once
	var supErr error
	done := make(chan error, 1)
	go func() { done <- h.run.Supervise(context.Background(), task.ID) }()
	finish := func() {
		once.Do(func() {
			_ = os.WriteFile(release, nil, 0o644)
			select {
			case supErr = <-done:
			case <-time.After(10 * time.Second):
				supErr = errors.New("Supervise did not return")
			}
		})
	}
	t.Cleanup(finish)

	waitFor(t, 5*time.Second, "the worker to start", func() bool {
		return fileHasBytes(filepath.Join(h.run.TaskDir(task.ID), taskLog))
	})

	var out bytes.Buffer
	started := time.Now()
	if err := h.run.Wait(task.ID, 200*time.Millisecond, &out); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	elapsed := time.Since(started)
	if elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("Wait took %s, want about the 200ms bound", elapsed)
	}
	if lines := strings.Split(out.String(), "\n"); len(lines) < 2 || lines[0] != "STATUS: running" {
		t.Errorf("Wait output = %q, want STATUS: running then the log tail", out.String())
	}
	if !strings.Contains(out.String(), "thinking about it") {
		t.Errorf("Wait output = %q, want the rendered log tail", out.String())
	}
	if h.load(task.ID).Collected {
		t.Error("collected = true, want false after a wait that only saw running")
	}

	finish()
	if supErr != nil {
		t.Fatalf("Supervise: %v", supErr)
	}
	if got := h.load(task.ID); got.Status != StatusDone {
		t.Errorf("status after release = %q, want done", got.Status)
	}
}

func TestWaitMarksCollected(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	t.Setenv("TEAM_TEST_STDIN", filepath.Join(t.TempDir(), "stdin"))
	t.Setenv("TEAM_TEST_ARGV", filepath.Join(t.TempDir(), "argv"))
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(happyStub)

	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	var out bytes.Buffer
	if err := h.run.Wait(task.ID, time.Second, &out); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.HasPrefix(out.String(), "STATUS: done\n") {
		t.Errorf("Wait output = %q, want STATUS: done first", out.String())
	}
	if !strings.Contains(out.String(), "DONE") {
		t.Errorf("Wait output = %q, want the final answer", out.String())
	}
	if got := h.load(task.ID); !got.Collected {
		t.Error("collected = false, want true after Wait returned the answer")
	}

	// Waiting on an already-terminal task returns at once, not at the bound.
	var again bytes.Buffer
	started := time.Now()
	if err := h.run.Wait(task.ID, 10*time.Second, &again); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("second Wait took %s, want an immediate return", elapsed)
	}
}

func TestUncollectedAfterLeadGone(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	t.Setenv("TEAM_TEST_STDIN", filepath.Join(t.TempDir(), "stdin"))
	t.Setenv("TEAM_TEST_ARGV", filepath.Join(t.TempDir(), "argv"))
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(happyStub)

	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if got.Collected {
		t.Error("collected = true, want false when nobody waited")
	}
}

func TestTakeoverRecorded(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	task := h.task(RoleCoder, "job", t.TempDir())
	if err := h.run.Takeover(task.ID, "implemented it myself"); err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	got := h.load(task.ID)
	if !got.TookOver || got.TakeoverReason != "implemented it myself" {
		t.Errorf("takeover = %v %q, want true and the reason", got.TookOver, got.TakeoverReason)
	}
}

func TestCollectedSurvivesSuperviseSave(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("TEAM_TEST_READY", ready)
	t.Setenv("TEAM_TEST_RELEASE", release)
	t.Setenv("TEAM_TEST_FIXTURE", fixturePath(t))
	h.stub(`cat > /dev/null
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"working"}]},"session_id":"s1"}'
touch "$TEAM_TEST_READY"
while [ ! -f "$TEAM_TEST_RELEASE" ]; do sleep 0.05; done
cat "$TEAM_TEST_FIXTURE"
`)

	task := h.task(RoleCoder, "job", t.TempDir())
	var once sync.Once
	var supErr error
	done := make(chan error, 1)
	go func() { done <- h.run.Supervise(context.Background(), task.ID) }()
	finish := func() {
		once.Do(func() {
			_ = os.WriteFile(release, nil, 0o644)
			select {
			case supErr = <-done:
			case <-time.After(10 * time.Second):
				supErr = errors.New("Supervise did not return")
			}
		})
	}
	t.Cleanup(finish)

	waitFor(t, 5*time.Second, "the worker to block", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	// What Wait does in another process while the worker is still running.
	if _, err := h.run.updateTask(task.ID, func(t *Task) { t.Collected = true }); err != nil {
		t.Fatalf("set collected: %v", err)
	}

	finish()
	if supErr != nil {
		t.Fatalf("Supervise: %v", supErr)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if !got.Collected {
		t.Error("collected = false, want the flag set while the worker ran to survive the final save")
	}
}

func TestRejectsBadTaskIDs(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	self := writeSelf(t)
	bad := []string{"", "t", "t0", "t01", "t1x", "../t1", "t1/../t2", "tasks/t1", "T1", "t-1", "t 1"}

	for _, id := range bad {
		t.Run("id="+strconv.Quote(id), func(t *testing.T) {
			if id != "" {
				if _, err := h.run.Start(RoleCoder, "x", id, self); err == nil {
					t.Error("Start with a bad retry id = nil error")
				} else if !strings.Contains(err.Error(), "task id") {
					t.Errorf("Start error = %q, want it to name the invalid task id", err)
				}
			}
			if err := h.run.Wait(id, 0, io.Discard); err == nil {
				t.Error("Wait with a bad id = nil error")
			} else if !strings.Contains(err.Error(), "task id") {
				t.Errorf("Wait error = %q, want it to name the invalid task id", err)
			}
			if err := h.run.Takeover(id, "reason"); err == nil {
				t.Error("Takeover with a bad id = nil error")
			} else if !strings.Contains(err.Error(), "task id") {
				t.Errorf("Takeover error = %q, want it to name the invalid task id", err)
			}
			if err := h.run.Supervise(context.Background(), id); err == nil {
				t.Error("Supervise with a bad id = nil error")
			} else if !strings.Contains(err.Error(), "task id") {
				t.Errorf("Supervise error = %q, want it to name the invalid task id", err)
			}
		})
	}
}

func TestSuperviseHandlesHugeLine(t *testing.T) {
	h := newHarness(t, testConfig(map[Role]string{RoleCoder: "haiku"}))
	huge := strings.Repeat("x", 200*1024)
	stream := `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"` + huge + `"}]},"session_id":"s1"}` + "\n" +
		`{"type":"result","result":"DONE","session_id":"s1","num_turns":1,"is_error":false}` // no trailing newline on the last line
	fixture := filepath.Join(t.TempDir(), "huge.jsonl")
	writeFile(t, fixture, stream)
	t.Setenv("TEAM_TEST_FIXTURE", fixture)
	h.stub(`cat > /dev/null
cat "$TEAM_TEST_FIXTURE"
`)

	task := h.task(RoleCoder, "huge", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if got.Turns != 1 {
		t.Errorf("turns = %d, want 1", got.Turns)
	}
	if log := h.log(task.ID); string(log) != stream {
		t.Errorf("log.jsonl is %d bytes, want the %d byte stream intact", len(log), len(stream))
	}
	if answer := h.result(task.ID); answer != "DONE" {
		t.Errorf("result.md = %q, want DONE", answer)
	}
}

func TestGitStatusParsing(t *testing.T) {
	out := " M plain.txt\n" +
		"?? \"with space.txt\"\n" +
		"R  old.txt -> new.txt\n" +
		"R  \"old name\" -> \"new name\"\n" +
		" D gone.txt"
	want := map[string]string{
		"plain.txt":      " M",
		"with space.txt": "??",
		"old.txt":        "R ",
		"new.txt":        "R ",
		"old name":       "R ",
		"new name":       "R ",
		"gone.txt":       " D",
	}
	if got := parseStatus([]byte(out)); !maps.Equal(got, want) {
		t.Errorf("parseStatus = %v, want %v", got, want)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
