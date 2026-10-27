package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/team"
)

// teamLogPoll is how often --follow re-reads the task's status.
var teamLogPoll = time.Second

// teamLogStopWait bounds how long --follow waits for the tail to stop before
// reading the log's remainder directly.
var teamLogStopWait = 5 * time.Second

// --- team logs ------------------------------------------------------------------

func runContainerTeamLogs(ctx context.Context, a *app, workspace, name, runID, taskID string, follow bool) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if err := teamRunning(ctx, t, name); err != nil {
		return err
	}

	base := team.Base(t.container.PersistState)
	runs, err := teamRuns(ctx, t, base)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		a.printf("no team runs in %s; start one with: dev container start-agent-team %s\n", name, name)
		return nil
	}

	selected, err := pickTeamRun(runs, runID, name)
	if err != nil {
		return err
	}
	task, err := pickTeamTask(selected, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		a.printf("no tasks in run %s; wait for the lead to start one\n", selected.Run)
		return nil
	}

	run, err := team.LookupRun(base, selected.Run)
	if err != nil {
		return err
	}
	logPath, err := run.LogPath(task.ID)
	if err != nil {
		return err
	}
	tool, err := taskTool(ctx, t, run, *task)
	if err != nil {
		return err
	}
	if _, err := team.NewParser(tool); err != nil {
		return err
	}

	// Following a task that already ended is just a read: tailing it would
	// wait a poll and can cut a large log when the follow context stops.
	if !follow || task.Status != team.StatusRunning {
		return renderTeamLog(ctx, t, name, task.ID, logPath, tool, a.out)
	}
	return followTeamLog(ctx, t, base, selected.Run, task.ID, tool, logPath, a.out)
}

func pickTeamRun(runs []team.RunTasks, runID, name string) (team.RunTasks, error) {
	if runID == "" {
		return runs[0], nil
	}
	for _, run := range runs {
		if run.Run == runID {
			return run, nil
		}
	}
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.Run
	}
	return team.RunTasks{}, notFoundErrorf("no run %s in %s; available runs: %s",
		runID, name, strings.Join(ids, ", "))
}

// pickTeamTask keeps the run's order: the store lists tasks by numeric id, so
// the last one is the most recently started. A running task wins, the newest
// one when several are.
func pickTeamTask(run team.RunTasks, taskID string) (*team.Task, error) {
	if taskID != "" {
		for i := range run.Tasks {
			if run.Tasks[i].ID == taskID {
				return &run.Tasks[i], nil
			}
		}
		ids := make([]string, len(run.Tasks))
		for i, task := range run.Tasks {
			ids[i] = task.ID
		}
		available := strings.Join(ids, ", ")
		if available == "" {
			available = "none"
		}
		return nil, notFoundErrorf("no task %s in run %s; available tasks: %s",
			taskID, run.Run, available)
	}
	if len(run.Tasks) == 0 {
		return nil, nil
	}
	for i := len(run.Tasks) - 1; i >= 0; i-- {
		if run.Tasks[i].Status == team.StatusRunning {
			return &run.Tasks[i], nil
		}
	}
	return &run.Tasks[len(run.Tasks)-1], nil
}

// taskTool prefers the tool the task recorded; the run's config is the
// fallback for a record written before that field existed.
func taskTool(ctx context.Context, t *target, run team.Run, task team.Task) (string, error) {
	if task.Tool != "" {
		return task.Tool, nil
	}

	var out bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, []string{"cat", run.ConfigPath()},
		provider.ExecOpts{Stdout: &out, Stderr: io.Discard}); err != nil {
		return "", fmt.Errorf("cannot read run %s's config in %s: %w", path.Base(run.Dir), t.container.Name, err)
	}
	var cfg team.Config
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		return "", fmt.Errorf("parsing run %s's config in %s: %w", path.Base(run.Dir), t.container.Name, err)
	}
	if cfg.Workers == "" {
		return "", fmt.Errorf("run %s's config does not name a worker tool", path.Base(run.Dir))
	}
	return cfg.Workers, nil
}

// renderTeamLog reads a task's whole log and renders it.
func renderTeamLog(ctx context.Context, t *target, name, taskID, logPath, tool string, out io.Writer) error {
	data, err := readTeamLog(ctx, t, name, taskID, logPath)
	if err != nil {
		return err
	}
	return team.RenderLog(tool, bytes.NewReader(data), out)
}

func readTeamLog(ctx context.Context, t *target, name, taskID, logPath string) ([]byte, error) {
	var data bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, []string{"cat", logPath},
		provider.ExecOpts{Stdout: &data, Stderr: io.Discard}); err != nil {
		return nil, fmt.Errorf("cannot read task %s's log in %s: %w", taskID, name, err)
	}
	return data.Bytes(), nil
}

// tailStream feeds the live tail into the renderer and records how far the
// stream got. Resuming the direct read at that offset is what makes the
// handover lossless without repeating a line.
type tailStream struct {
	mu     sync.Mutex
	out    *io.PipeWriter
	offset int64
	sealed bool
}

var errTailSealed = errors.New("tail stream sealed")

func (s *tailStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return 0, errTailSealed
	}
	n, err := s.out.Write(p)
	s.offset += int64(n)
	return n, err
}

// seal stops accepting tail bytes and returns the offset the renderer reached.
func (s *tailStream) seal() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
	return s.offset
}

func (s *tailStream) append(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.out.Write(p)
	return err
}

func (s *tailStream) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
	return s.out.Close()
}

// waitTailStopped bounds the wait for a tail whose host-side exec was cancelled:
// a client that does not stop the in-container process must not hang follow.
func waitTailStopped(done <-chan error) {
	select {
	case <-done:
	case <-time.After(teamLogStopWait):
	}
}

// followTeamLog tails the log while polling its task's record. At the terminal
// status it cancels the tail, then reads the log again and renders only the
// bytes the tail never delivered, reusing the same pipe so a line split at the
// handover is parsed once.
func followTeamLog(ctx context.Context, t *target, base, runID, taskID, tool, logPath string, out io.Writer) error {
	followCtx, stop := context.WithCancel(ctx)
	defer stop()

	reader, writer := io.Pipe()
	stream := &tailStream{out: writer}
	tailDone := make(chan error, 1)
	go func() {
		err := t.provider.Exec(followCtx, t.container,
			[]string{"tail", "-n", "+1", "-F", logPath},
			provider.ExecOpts{Stdout: stream, Stderr: io.Discard})
		tailDone <- err
	}()

	renderDone := make(chan error, 1)
	go func() {
		err := team.RenderLog(tool, reader, out)
		// A renderer that stops early must unblock a tail still writing.
		_ = reader.CloseWithError(err)
		renderDone <- err
	}()

	ticker := time.NewTicker(teamLogPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stop()
			waitTailStopped(tailDone)
			_ = stream.close()
			<-renderDone
			return nil
		case err := <-tailDone:
			_ = stream.close()
			renderErr := <-renderDone
			if ctx.Err() != nil {
				return nil
			}
			if renderErr != nil {
				return renderErr
			}
			if err != nil {
				return fmt.Errorf("following task %s's log in %s: %w", taskID, t.container.Name, err)
			}
			return nil
		case <-ticker.C:
			done, err := taskTerminal(ctx, t, base, runID, taskID)
			if err != nil {
				stop()
				waitTailStopped(tailDone)
				_ = stream.close()
				<-renderDone
				return err
			}
			if !done {
				continue
			}

			stop()
			waitTailStopped(tailDone)
			offset := stream.seal()
			data, catErr := readTeamLog(ctx, t, t.container.Name, taskID, logPath)
			var writeErr error
			if catErr == nil {
				if offset < int64(len(data)) {
					writeErr = stream.append(data[offset:])
				}
			}
			_ = stream.close()
			renderErr := <-renderDone
			if catErr != nil {
				return catErr
			}
			if renderErr != nil {
				return renderErr
			}
			return writeErr
		}
	}
}

func taskTerminal(ctx context.Context, t *target, base, runID, taskID string) (bool, error) {
	runs, err := teamRuns(ctx, t, base)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Run != runID {
			continue
		}
		for _, task := range run.Tasks {
			if task.ID == taskID {
				return task.Status != team.StatusRunning, nil
			}
		}
		return false, fmt.Errorf("task %s is gone from run %s", taskID, runID)
	}
	return false, fmt.Errorf("run %s is gone from %s", runID, t.container.Name)
}
