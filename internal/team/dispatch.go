package team

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

const (
	// DefaultWait bounds a lead's wait so it returns before the lead tool's own
	// shell timeout.
	DefaultWait = 60 * time.Second
	// MaxAttempts is the first attempt plus two retries.
	MaxAttempts = 3
)

const (
	taskLog    = "log.jsonl"
	taskResult = "result.md"
	waitPoll   = 100 * time.Millisecond
	tailCount  = 10
	// stderrTail is how much of a worker's stderr a failure answer keeps.
	stderrTail = 4 << 10

	supervisorDied = "supervisor died"
)

// terminateGrace is how long a stopped worker's process group has to honour
// SIGTERM before it is killed outright. A variable so tests need not wait five
// seconds for a stub that traps TERM.
var terminateGrace = 5 * time.Second

// exitDrain bounds how long a finished worker's stream is drained before the
// leftovers of its process group are killed. A variable so tests can shorten it.
var exitDrain = 250 * time.Millisecond

// taskCreatedHook is a test seam for the window between creating our record and
// re-checking the single-coder gate. Production leaves it doing nothing.
var taskCreatedHook = func() {}

// waitProbeHook is a test seam between Wait's stale running read and the
// dead-supervisor re-check. Production leaves it doing nothing.
var waitProbeHook = func() {}

// Start records a task and launches its supervisor detached, so the worker
// outlives a lead that dies. It returns as soon as the supervisor is running.
func (r Run) Start(role Role, text, retryOf string, self string) (Task, error) {
	cfg, err := r.Config()
	if err != nil {
		return Task{}, err
	}
	if !slices.Contains(cfg.Roles(), role) {
		return Task{}, fmt.Errorf("run has no %s worker: roles are %v", role, cfg.Roles())
	}

	var previous Task
	if retryOf != "" {
		if err := validTaskID(retryOf); err != nil {
			return Task{}, err
		}
		previous, err = r.Load(retryOf)
		if err != nil {
			return Task{}, err
		}
		if previous.Role != role {
			return Task{}, fmt.Errorf("cannot retry task %s as %s: it ran as %s, and a retry resumes that worker's session", previous.ID, role, previous.Role)
		}
		// Attempts is derived from the task named, so only the newest in a
		// chain may be retried or the cap could be walked around.
		tasks, err := r.List()
		if err != nil {
			return Task{}, err
		}
		for _, task := range tasks {
			if task.RetryOf == previous.ID {
				return Task{}, fmt.Errorf("%s was already retried as %s; retry %s", previous.ID, task.ID, task.ID)
			}
		}
		if previous.Status == StatusRunning {
			if supervisorAlive(previous.Pid) {
				return Task{}, fmt.Errorf("cannot retry task %s: it is still running", previous.ID)
			}
			if err := r.failIfRunning(previous.ID, supervisorDied); err != nil {
				return Task{}, err
			}
			if previous, err = r.Load(previous.ID); err != nil {
				return Task{}, err
			}
		}
		if previous.Attempts >= MaxAttempts {
			return Task{}, fmt.Errorf("cannot retry task %s: all %d attempts are used; use takeover instead", previous.ID, MaxAttempts)
		}
	}

	if role == RoleCoder {
		other, running, err := r.runningCoder("")
		if err != nil {
			return Task{}, err
		}
		if running {
			return Task{}, coderRunningError(other.ID)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Task{}, fmt.Errorf("read working directory: %w", err)
	}
	task, err := r.NewTask(role, text)
	if err != nil {
		return Task{}, err
	}
	task.Cwd = cwd
	if retryOf != "" {
		task.Attempts = previous.Attempts + 1
		task.RetryOf = previous.ID
		task.SessionID = previous.SessionID
	}
	if err := r.Save(task); err != nil {
		return Task{}, err
	}
	taskCreatedHook()
	if role == RoleCoder {
		// The pre-check and NewTask are not atomic: a concurrent start may have
		// created a lower id between them, so re-check now that ours is visible.
		other, running, err := r.runningCoder(task.ID)
		if err != nil {
			return Task{}, err
		}
		if running {
			if err := r.failTask(task.ID, "refused: coder "+other.ID+" is running"); err != nil {
				return Task{}, err
			}
			return Task{}, coderRunningError(other.ID)
		}
	}

	pid, err := spawn(self, task.ID, cwd)
	if err != nil {
		if failErr := r.failTask(task.ID, "could not start supervision: "+err.Error()); failErr != nil {
			return Task{}, failErr
		}
		return Task{}, fmt.Errorf("start supervisor for task %s: %w", task.ID, err)
	}
	return r.updateTask(task.ID, func(task *Task) { task.Pid = pid })
}

func spawn(self, id, cwd string) (int, error) {
	if self == "" {
		exe, err := os.Executable()
		if err != nil {
			return 0, fmt.Errorf("locate dispatch binary: %w", err)
		}
		self = exe
	}
	cmd := exec.Command(self, "supervise", id)
	cmd.Dir = cwd
	// The supervisor must survive the lead: its own session keeps it clear of
	// the lead's terminal and process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Nobody will wait for a process that is meant to outlive us.
	_ = cmd.Process.Release()
	return pid, nil
}

// validTaskID rejects a user-supplied id that could leave tasks/. NewTask hands
// out "t" plus a decimal without a leading zero, and only that shape is taken.
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

func coderRunningError(id string) error {
	return fmt.Errorf("a coder is already running as task %s; wait for it to finish", id)
}

// runningCoder returns a coder whose supervisor is alive. A running record
// whose supervisor is gone is marked failed instead of blocking the gate
// forever. With olderThan set only a lower id counts: of two concurrent starts,
// the higher id is the one that must yield.
func (r Run) runningCoder(olderThan string) (Task, bool, error) {
	tasks, err := r.List()
	if err != nil {
		return Task{}, false, err
	}
	limit := 0
	if olderThan != "" {
		limit, _ = taskNumber(olderThan)
	}
	for _, task := range tasks {
		if task.Role != RoleCoder || task.Status != StatusRunning {
			continue
		}
		if limit > 0 {
			n, ok := taskNumber(task.ID)
			if !ok || n >= limit {
				continue
			}
		}
		if !supervisorAlive(task.Pid) {
			if err := r.failIfRunning(task.ID, supervisorDied); err != nil {
				return Task{}, false, err
			}
			continue
		}
		return task, true, nil
	}
	return Task{}, false, nil
}

// supervisorAlive probes the supervisor Start recorded. A zero pid means Start
// has not recorded it yet, which is not death; an unreadable process is not ours
// to judge, so EPERM counts as alive.
func supervisorAlive(pid int) bool {
	if pid == 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Supervise runs one task's worker to its end and records what happened. The
// process that calls Start is gone by now; this is the detached half.
func (r Run) Supervise(ctx context.Context, id string) error {
	if err := validTaskID(id); err != nil {
		return err
	}
	task, err := r.Load(id)
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return r.failTask(id, err.Error())
	}
	if ctx == nil {
		ctx = context.Background()
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	model := task.Model
	if model == "" {
		model = cfg.Models[task.Role]
	}
	spec := WorkerSpec{Role: task.Role, Model: model, SessionID: task.SessionID, MaxTurns: maxTurns}
	wcmd, parser, err := Worker(cfg.Workers, spec)
	if err != nil {
		return r.failTask(id, err.Error())
	}
	if _, err := exec.LookPath(wcmd.Argv[0]); err != nil {
		return r.failTask(id, fmt.Sprintf("worker tool %q is not on PATH", wcmd.Argv[0]))
	}

	prompt, err := os.ReadFile(path.Join(r.TaskDir(id), taskPrompt))
	if err != nil {
		return r.failTask(id, fmt.Sprintf("read task %s prompt: %v", id, err))
	}
	var stdin []byte
	if wcmd.Stdin {
		stdin = prompt
	}
	before, gitBefore := gitSnapshot(task.Cwd)

	logPath := path.Join(r.TaskDir(id), taskLog)
	log, err := os.Create(logPath)
	if err != nil {
		return r.failTask(id, fmt.Sprintf("create task %s log: %v", id, err))
	}
	end, runErr := r.runWorker(ctx, workerRun{
		argv:     wcmd.Argv,
		env:      wcmd.Env,
		stdin:    stdin,
		dir:      task.Cwd,
		timeout:  timeout,
		maxTurns: maxTurns,
		parser:   parser,
		log:      log,
	})
	closeErr := log.Close()
	if runErr != nil {
		return r.failTask(id, runErr.Error())
	}
	if closeErr != nil {
		return r.failTask(id, fmt.Sprintf("close task %s log: %v", id, closeErr))
	}

	outcome := parser.Outcome()
	after, gitAfter := gitSnapshot(task.Cwd)
	edited := streamPaths(task.Cwd, outcome.Edited)
	var files []string
	if gitBefore && gitAfter {
		// A partial diff must not hide an edit the stream saw; the union keeps
		// git's paths first and appends only what git missed.
		files = unionPaths(changedPaths(before, after), edited)
	} else {
		// No usable git: the worker's own edit events are all the attribution.
		files = edited
	}
	tail := tailLines(logPath, workerParser(cfg.Workers, spec), tailCount)

	result := taskOutcome{
		turns:   end.turns,
		cost:    outcome.CostUSD,
		session: outcome.SessionID,
		files:   files,
	}
	switch {
	case end.cut != "":
		result.status = end.cut
		result.answer = failureAnswer(end.cutReason, tail, "")
	case outcome.Failed:
		result.status = StatusFailed
		reason := outcome.FailReason
		if reason == "" {
			reason = outcome.Final
		}
		if reason == "" {
			reason = "worker reported an error"
		}
		result.answer = failureAnswer(reason, tail, end.stderr)
	case end.waitErr != nil && !end.sawResult:
		// A non-zero exit with no result line has nothing else to explain it.
		result.status = StatusFailed
		result.answer = failureAnswer(exitReason(end.waitErr), tail, end.stderr)
	default:
		result.status = StatusDone
		result.answer = outcome.Final
	}
	if err := r.finish(id, result); err != nil {
		return r.failTask(id, "record task result: "+err.Error())
	}
	return nil
}

// tailBuffer keeps the last max bytes written to it. exec copies stderr from
// its own goroutine, so Write and Bytes are serialised.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	if over := len(b.buf) + len(p) - b.max; over > 0 {
		b.buf = b.buf[:copy(b.buf, b.buf[over:])]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.buf)
}

type workerRun struct {
	argv     []string
	env      []string
	stdin    []byte
	dir      string
	timeout  time.Duration
	maxTurns int
	parser   Parser
	log      io.Writer
}

type streamEnd struct {
	turns     int
	sawResult bool
	cut       Status // empty when the worker ended on its own
	cutReason string
	waitErr   error
	stderr    string
}

func (r Run) runWorker(ctx context.Context, w workerRun) (streamEnd, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return streamEnd{}, fmt.Errorf("create worker pipe: %w", err)
	}
	defer pr.Close()

	cmd := exec.Command(w.argv[0], w.argv[1:]...)
	cmd.Dir = w.dir
	// os/exec adds PWD=<Dir> only while Env is nil, so an explicit environment
	// must put the worker's directory back; the later entries win, letting the
	// worker's per-run config override an inherited one.
	if len(w.env) > 0 {
		cmd.Env = append(os.Environ(), "PWD="+w.dir)
		cmd.Env = append(cmd.Env, w.env...)
	}
	cmd.Stdin = bytes.NewReader(w.stdin)
	cmd.Stdout = pw
	stderr := newTailBuffer(stderrTail)
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A grandchild can inherit our stdin pipe and keep the copier blocked; the
	// delay makes Wait return once the worker itself is gone.
	cmd.WaitDelay = exitDrain
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return streamEnd{}, fmt.Errorf("start worker: %w", err)
	}
	_ = pw.Close()
	pid := cmd.Process.Pid

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	lines := make(chan []byte, 32)
	stop := make(chan struct{})
	readDone := make(chan struct{})
	var stopOnce sync.Once
	stopReader := func() {
		stopOnce.Do(func() {
			close(stop)
			_ = pr.Close()
		})
	}
	defer stopReader()
	go func() {
		defer close(readDone)
		defer close(lines)
		reader := bufio.NewReader(pr)
		for {
			// ReadBytes, not Scanner: a stream line over 64 KiB must reach the
			// log and the parser intact.
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var end streamEnd
	waited := false
	feed := func(line []byte) {
		_, _ = w.log.Write(line)
		for _, event := range w.parser.Feed(line) {
			if event.Kind == "result" {
				end.sawResult = true
			}
		}
		end.turns = w.parser.Outcome().Turns
	}
	drain := func() {
		deadline := time.After(exitDrain)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					return
				}
				feed(line)
			case <-deadline:
				// A child of the worker still holds the pipe. Stop the reader
				// and keep only what it already handed over.
				stopReader()
				for line := range lines {
					feed(line)
				}
				return
			}
		}
	}

	timer := time.NewTimer(w.timeout)
	defer timer.Stop()

loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			feed(line)
			// Strictly greater: a worker may complete exactly MaxTurns turns.
			if end.turns > w.maxTurns {
				end.cut = StatusTurnLimit
				end.cutReason = "hit the turn limit"
				break loop
			}
		case waitErr := <-waitCh:
			waited = true
			end.waitErr = waitErr
			// The worker exits before any child it left holding stdout. Drain
			// what it wrote, then end the leftovers so the stream can close.
			drain()
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			break loop
		case <-timer.C:
			// A worker that finished as the timer fired is done, not timed out.
			select {
			case waitErr := <-waitCh:
				waited = true
				end.waitErr = waitErr
				drain()
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				break loop
			default:
			}
			end.cut = StatusTimedOut
			end.cutReason = "timed out"
			break loop
		case <-ctx.Done():
			end.cut = StatusFailed
			end.cutReason = "supervision cancelled"
			break loop
		}
	}

	if end.cut == "" {
		// The worker is gone; whatever remains in its group is not the worker.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if end.cut != "" {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		grace := time.NewTimer(terminateGrace)
		stopping := true
		for stopping {
			select {
			case line, ok := <-lines:
				if !ok {
					stopping = false
					break
				}
				feed(line)
			case <-grace.C:
				// A worker that traps TERM now takes its whole group with it.
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				select {
				case <-readDone:
				case <-time.After(2 * time.Second):
				}
				stopReader()
				for line := range lines {
					feed(line)
				}
				stopping = false
			}
		}
		grace.Stop()
	}

	if !waited {
		end.waitErr = <-waitCh
	}
	end.stderr = printableTail(stderr.Bytes())
	return end, nil
}

type taskOutcome struct {
	status  Status
	answer  string
	turns   int
	cost    *float64
	session string
	files   []string
}

func (r Run) finish(id string, outcome taskOutcome) error {
	if err := writeAtomic(r.TaskDir(id), taskResult, []byte(outcome.answer)); err != nil {
		return fmt.Errorf("write task %s result: %w", id, err)
	}
	_, err := r.updateTask(id, func(task *Task) {
		task.Status = outcome.status
		task.Turns = outcome.turns
		if outcome.cost != nil {
			task.CostUSD = outcome.cost
		}
		if outcome.session != "" {
			task.SessionID = outcome.session
		}
		task.FilesChanged = outcome.files
		task.EndedAt = time.Now().UTC()
	})
	return err
}

func (r Run) failTask(id, reason string) error {
	writeErr := writeAtomic(r.TaskDir(id), taskResult, []byte(reason))
	_, saveErr := r.updateTask(id, func(task *Task) {
		task.Status = StatusFailed
		task.EndedAt = time.Now().UTC()
	})
	// The status is what unwedges the task; a bad result file must not leave it
	// running, so the record is saved regardless and the write error follows.
	if saveErr != nil {
		return saveErr
	}
	if writeErr != nil {
		return fmt.Errorf("write task %s result: %w", id, writeErr)
	}
	return nil
}

// failIfRunning marks a task failed only if its record is still running: the
// supervisor may have finished between the caller's load and this call, and a
// terminal status owns its result. The re-check must precede the result write,
// because a reader that sees a terminal status must find the result.
func (r Run) failIfRunning(id, reason string) error {
	task, err := r.Load(id)
	if err != nil {
		return err
	}
	if task.Status != StatusRunning {
		return nil
	}
	writeErr := writeAtomic(r.TaskDir(id), taskResult, []byte(reason))
	// The supervisor is dead, so no live writer can finish the task between the
	// re-check and this save; Wait's Collected and Takeover touch disjoint fields.
	_, saveErr := r.updateTask(id, func(task *Task) {
		task.Status = StatusFailed
		task.EndedAt = time.Now().UTC()
	})
	if saveErr != nil {
		return saveErr
	}
	if writeErr != nil {
		return fmt.Errorf("write task %s result: %w", id, writeErr)
	}
	return nil
}

// updateTask re-loads immediately before saving. Wait, Takeover and Supervise
// run in different processes and each changes only its own fields, so a save
// must start from the record as it is on disk, not from a stale copy. A writer
// can still land in the Load..Save instant and lose the other's change: the
// fields each process writes are disjoint, so that window is tolerable.
func (r Run) updateTask(id string, change func(*Task)) (Task, error) {
	task, err := r.Load(id)
	if err != nil {
		return Task{}, err
	}
	change(&task)
	if err := r.Save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// Wait blocks until the task is terminal or the bound elapses. A bounded wait
// that is still running prints the rendered log tail; a terminal one prints the
// status and the result anyone can now collect.
func (r Run) Wait(id string, bound time.Duration, w io.Writer) error {
	if err := validTaskID(id); err != nil {
		return err
	}
	deadline := time.Now().Add(bound)
	for {
		task, err := r.Load(id)
		if err != nil {
			return err
		}
		if task.Status == StatusRunning && !supervisorAlive(task.Pid) {
			// The timeout and turn cap died with the supervisor; adopt the
			// failure here so the task cannot stay running forever.
			waitProbeHook()
			if err := r.failIfRunning(id, supervisorDied); err != nil {
				return err
			}
			if task, err = r.Load(id); err != nil {
				return err
			}
		}
		if task.Status != StatusRunning {
			if err := r.printFinal(task, w); err != nil {
				return err
			}
			_, err := r.updateTask(id, func(task *Task) { task.Collected = true })
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return r.printRunning(task, w)
		}
		time.Sleep(min(waitPoll, remaining))
	}
}

func (r Run) printRunning(task Task, w io.Writer) error {
	if _, err := fmt.Fprintf(w, "STATUS: %s\n", StatusRunning); err != nil {
		return err
	}
	for _, line := range tailLines(path.Join(r.TaskDir(task.ID), taskLog), r.parserFor(task), tailCount) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func (r Run) printFinal(task Task, w io.Writer) error {
	if _, err := fmt.Fprintf(w, "STATUS: %s\n", task.Status); err != nil {
		return err
	}
	data, err := os.ReadFile(path.Join(r.TaskDir(task.ID), taskResult))
	if err != nil {
		// A terminal record whose result is gone can still say what is known.
		if line := failureLine(task.Status); line != "" {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		for _, line := range tailLines(path.Join(r.TaskDir(task.ID), taskLog), r.parserFor(task), tailCount) {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if data[len(data)-1] != '\n' {
		_, err = fmt.Fprintln(w)
	}
	return err
}

func failureLine(status Status) string {
	switch status {
	case StatusTimedOut:
		return "timed out"
	case StatusTurnLimit:
		return "hit the turn limit"
	case StatusFailed:
		return "task failed"
	}
	return ""
}

func failureAnswer(reason string, tail []string, stderr string) string {
	if len(tail) == 0 && stderr == "" {
		return reason
	}
	var b strings.Builder
	b.WriteString(reason)
	if len(tail) > 0 {
		b.WriteString("\n\nlog tail:\n")
		for _, line := range tail {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if stderr != "" {
		b.WriteString("\n\nstderr (last 4 KiB):\n")
		b.WriteString(stderr)
		b.WriteByte('\n')
	}
	return b.String()
}

// printableTail makes a byte-cut stderr tail safe to show: the cut can land
// inside a rune, and terminal control bytes would reach the lead's screen.
func printableTail(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
	return strings.TrimSpace(s)
}

func exitReason(err error) string {
	if err == nil {
		return "worker exited without a result"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return fmt.Sprintf("worker exited with code %d", code)
		}
	}
	return "worker failed: " + err.Error()
}

// Takeover records that the lead finished the task itself.
func (r Run) Takeover(id, reason string) error {
	if err := validTaskID(id); err != nil {
		return err
	}
	_, err := r.updateTask(id, func(task *Task) {
		task.TookOver = true
		task.TakeoverReason = reason
	})
	return err
}

func (r Run) parserFor(task Task) Parser {
	tool := task.Tool
	if cfg, err := r.Config(); err == nil && cfg.Workers != "" {
		tool = cfg.Workers
	}
	return workerParser(tool, WorkerSpec{Role: task.Role, Model: task.Model})
}

func workerParser(tool string, spec WorkerSpec) Parser {
	_, parser, err := Worker(tool, spec)
	if err != nil {
		return nil
	}
	return parser
}

// tailLines renders the last n readable lines of a worker log. It reads with a
// Reader so a log line over 64 KiB is still parsed.
func tailLines(file string, parser Parser, n int) []string {
	if parser == nil {
		return nil
	}
	log, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer log.Close()

	var tail []string
	reader := bufio.NewReader(log)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			for _, event := range parser.Feed(line) {
				if rendered := Render(event); rendered != "" {
					tail = append(tail, rendered)
					if len(tail) > n {
						tail = tail[1:]
					}
				}
			}
		}
		if err != nil {
			return tail
		}
	}
}
