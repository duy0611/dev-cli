package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Status is how a task ended, or StatusRunning while it is in progress.
type Status string

const (
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed-out"
	StatusTurnLimit Status = "turn-limit"
)

// Task is one worker's task, as <run>/tasks/<id>/task.json records it.
type Task struct {
	ID             string    `json:"id"`
	Role           Role      `json:"role"`
	Tool           string    `json:"tool,omitempty"`
	Model          string    `json:"model,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	Status         Status    `json:"status"`
	Attempts       int       `json:"attempts"`
	RetryOf        string    `json:"retry_of,omitempty"`
	Pid            int       `json:"pid,omitempty"`
	CostUSD        *float64  `json:"cost_usd,omitempty"`
	Turns          int       `json:"turns,omitempty"`
	FilesChanged   []string  `json:"files_changed,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at,omitzero"`
	Collected      bool      `json:"collected,omitempty"`
	TookOver       bool      `json:"took_over,omitempty"`
	TakeoverReason string    `json:"takeover_reason,omitempty"`
}

// Run is a team run's folder: config.json and tasks/<id>, each holding the
// prompt, record, log and result.
type Run struct {
	Dir string
}

const (
	configFile = "config.json"
	tasksDir   = "tasks"
	runsDir    = "runs"
	taskRecord = "task.json"
	taskPrompt = "task.md"
	runStamp   = "20060102T150405Z"
)

// Base is the in-container directory runs live under: persistent state when
// the launch asked for it, tmpfs otherwise.
func Base(persistState bool) string {
	if persistState {
		return "/var/dev-state/dev-team"
	}
	return "/tmp/dev-team"
}

// BinPath is the in-container path of the dispatch binary.
func BinPath(base string) string {
	return base + "/bin/dev-team"
}

// RunID names a run after its container and launch time in UTC. The stamp is
// last so ListRuns can order ids without opening them.
func RunID(container string, at time.Time) string {
	return container + "-" + at.UTC().Format(runStamp)
}

// TaskDir is where one task's prompt, record, log and result live.
func (r Run) TaskDir(id string) string {
	return path.Join(r.Dir, tasksDir, id)
}

// LogPath is the worker stream one task records. The id is validated here
// because it can come from a record the container wrote.
func (r Run) LogPath(id string) (string, error) {
	if err := validTaskID(id); err != nil {
		return "", err
	}
	return path.Join(r.TaskDir(id), taskLog), nil
}

// ConfigPath is where the run keeps the config.json dispatch reads.
func (r Run) ConfigPath() string {
	return path.Join(r.Dir, configFile)
}

// Config reads the run's config.json.
func (r Run) Config() (Config, error) {
	var cfg Config
	data, err := os.ReadFile(r.ConfigPath())
	if err != nil {
		return Config{}, fmt.Errorf("read run config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse run config: %w", err)
	}
	return cfg, nil
}

// NewTask allocates the next task id and writes its prompt and running record.
func (r Run) NewTask(role Role, text string) (Task, error) {
	id, dir, err := r.createTaskDir()
	if err != nil {
		return Task{}, err
	}
	if err := writeAtomic(dir, taskPrompt, []byte(text)); err != nil {
		return Task{}, fmt.Errorf("write task %s prompt: %w", id, err)
	}

	task := Task{
		ID:        id,
		Role:      role,
		Status:    StatusRunning,
		Attempts:  1,
		StartedAt: time.Now().UTC(),
	}
	// A bare run folder has no config.json until the launcher writes one, and
	// an unreadable one is the launcher's problem: a task must still be created.
	if cfg, err := r.Config(); err == nil {
		task.Tool = cfg.Workers
		task.Model = cfg.Models[role]
	}
	if err := r.Save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// createTaskDir returns a task id no other caller holds. mkdir is the lock: an
// id that already exists makes it fail, so concurrent goroutines or processes
// cannot share one.
func (r Run) createTaskDir() (id, dir string, err error) {
	tasks := path.Join(r.Dir, tasksDir)
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		return "", "", fmt.Errorf("create tasks dir: %w", err)
	}
	for n := 1; ; n++ {
		id = "t" + strconv.Itoa(n)
		dir = path.Join(tasks, id)
		err := os.Mkdir(dir, 0o755)
		switch {
		case err == nil:
			return id, dir, nil
		case os.IsExist(err):
			continue
		default:
			return "", "", fmt.Errorf("create task dir %s: %w", id, err)
		}
	}
}

// Load reads one task record.
func (r Run) Load(id string) (Task, error) {
	data, err := os.ReadFile(path.Join(r.TaskDir(id), taskRecord))
	if err != nil {
		return Task{}, fmt.Errorf("read task %s: %w", id, err)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, fmt.Errorf("parse task %s: %w", id, err)
	}
	return task, nil
}

// Save writes the task record to its task.json.
func (r Run) Save(task Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task %s: %w", task.ID, err)
	}
	if err := writeAtomic(r.TaskDir(task.ID), taskRecord, append(data, '\n')); err != nil {
		return fmt.Errorf("save task %s: %w", task.ID, err)
	}
	return nil
}

// List returns the run's tasks in numeric id order.
func (r Run) List() ([]Task, error) {
	entries, err := os.ReadDir(path.Join(r.Dir, tasksDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []Task{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	type taskRef struct {
		num  int
		name string
	}
	refs := make([]taskRef, 0, len(entries))
	for _, entry := range entries {
		num, ok := taskNumber(entry.Name())
		if !ok || !entry.IsDir() {
			continue
		}
		refs = append(refs, taskRef{num: num, name: entry.Name()})
	}
	// Numeric, not lexical: ids run t1, t2, ... t10, and the log reader wants
	// them in the order the work started.
	slices.SortFunc(refs, func(a, b taskRef) int {
		return a.num - b.num
	})

	tasks := make([]Task, 0, len(refs))
	for _, ref := range refs {
		// A dir without a readable record is a NewTask that died between mkdir
		// and Save; it must not fail the whole listing.
		task, err := r.Load(ref.name)
		if err != nil {
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// taskNumber returns the n in a "t<n>" task id.
func taskNumber(id string) (int, bool) {
	if len(id) < 2 || id[0] != 't' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// LookupRun returns the run folder id names under base. Run ids can come from
// the dispatch binary, so only a single path element may become a path.
func LookupRun(base, id string) (Run, error) {
	if !singlePathElement(id) {
		return Run{}, fmt.Errorf("invalid run id %q: want a single path element", id)
	}
	return Run{Dir: path.Join(base, runsDir, id)}, nil
}

// singlePathElement rejects anything that a path join or a log line could give
// a second meaning: separators, traversal, control characters and empty names.
func singlePathElement(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r == '/' || r == '\\' || r < ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// ListRuns lists run ids under base/runs, newest first; runs live directly in
// base/runs/<runid>.
func ListRuns(base string) ([]string, error) {
	entries, err := os.ReadDir(path.Join(base, runsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	runs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			runs = append(runs, entry.Name())
		}
	}
	slices.SortFunc(runs, func(a, b string) int {
		at, aok := runTime(a)
		bt, bok := runTime(b)
		if aok && bok {
			if cmp := bt.Compare(at); cmp != 0 {
				return cmp
			}
		}
		return strings.Compare(a, b)
	})
	return runs, nil
}

// runTime reads the UTC stamp RunID leaves at the end of a run id.
func runTime(id string) (time.Time, bool) {
	if len(id) < len(runStamp) {
		return time.Time{}, false
	}
	at, err := time.Parse(runStamp, id[len(id)-len(runStamp):])
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// writeAtomic replaces dir/name in one rename, so a reader (the host's team
// list, a worker polling for takeover) never parses a half-written file.
func writeAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path.Join(dir, name))
}
