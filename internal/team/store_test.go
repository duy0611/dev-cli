package team

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewTaskIDsSequential(t *testing.T) {
	run := Run{Dir: t.TempDir()}

	first := mustNewTask(t, run, RoleCoder, "do the thing")
	second := mustNewTask(t, run, RoleReviewer, "review it")

	if first.ID != "t1" || second.ID != "t2" {
		t.Fatalf("ids = %q, %q, want t1, t2", first.ID, second.ID)
	}
	if first.Role != RoleCoder || second.Role != RoleReviewer {
		t.Errorf("roles = %q, %q, want coder, reviewer", first.Role, second.Role)
	}
	if first.Status != StatusRunning {
		t.Errorf("status = %q, want %q", first.Status, StatusRunning)
	}
	if first.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", first.Attempts)
	}
	if first.StartedAt.IsZero() || first.StartedAt.Location() != time.UTC {
		t.Errorf("started at = %v, want a UTC timestamp", first.StartedAt)
	}
	if d := time.Since(first.StartedAt); d < 0 || d > time.Minute {
		t.Errorf("started at = %v, want about now", first.StartedAt)
	}
	if first.Tool != "" || first.Model != "" {
		t.Errorf("tool, model = %q, %q, want empty without a config", first.Tool, first.Model)
	}

	prompt, err := os.ReadFile(path.Join(run.TaskDir("t1"), "task.md"))
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if string(prompt) != "do the thing" {
		t.Errorf("task.md = %q, want the text verbatim", prompt)
	}

	t.Run("tool and model come from the run config", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		cfg := Config{
			Lead:     Tool{ID: "claude"},
			Workers:  "opencode",
			Models:   map[Role]string{RoleCoder: "a/b"},
			MaxTurns: DefaultMaxTurns,
			Timeout:  DefaultTimeout,
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		if err := os.WriteFile(path.Join(run.Dir, "config.json"), data, 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}

		coder := mustNewTask(t, run, RoleCoder, "x")
		if coder.Tool != "opencode" || coder.Model != "a/b" {
			t.Errorf("coder tool, model = %q, %q, want opencode, a/b", coder.Tool, coder.Model)
		}
		reviewer := mustNewTask(t, run, RoleReviewer, "y")
		if reviewer.Tool != "opencode" || reviewer.Model != "" {
			t.Errorf("reviewer tool, model = %q, %q, want opencode, empty", reviewer.Tool, reviewer.Model)
		}
	})

	t.Run("prompt is written verbatim at any size", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		text := strings.Repeat("a prompt line\n", 1<<16)
		task := mustNewTask(t, run, RoleCoder, text)
		got, err := os.ReadFile(path.Join(run.TaskDir(task.ID), "task.md"))
		if err != nil {
			t.Fatalf("read task.md: %v", err)
		}
		if string(got) != text {
			t.Errorf("task.md is %d bytes, want the %d byte prompt verbatim", len(got), len(text))
		}
	})
}

func TestSaveIsAtomicAndRoundTrips(t *testing.T) {
	run := Run{Dir: t.TempDir()}
	task := mustNewTask(t, run, RoleCoder, "job")
	started := task.StartedAt
	ended := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	zero, cost := 0.0, 0.25

	cases := []struct {
		name string
		cost *float64
	}{
		{"nil cost stays nil", nil},
		{"zero cost stays zero", &zero},
		{"cost round-trips", &cost},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			task.CostUSD = tt.cost
			task.Status = StatusDone
			task.SessionID = "session-1"
			task.Turns = 4
			task.FilesChanged = []string{"a.go", "b.go"}
			task.EndedAt = ended
			if err := run.Save(task); err != nil {
				t.Fatalf("Save: %v", err)
			}

			data, err := os.ReadFile(path.Join(run.TaskDir(task.ID), "task.json"))
			if err != nil {
				t.Fatalf("read task.json: %v", err)
			}
			if !json.Valid(data) {
				t.Fatalf("task.json is not valid JSON: %q", data)
			}

			got, err := run.Load(task.ID)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.ID != task.ID || got.Role != task.Role || got.Status != task.Status || got.Attempts != task.Attempts {
				t.Errorf("loaded %+v, want id, role, status, attempts of %+v", got, task)
			}
			if got.SessionID != "session-1" || got.Turns != 4 {
				t.Errorf("session, turns = %q, %d, want session-1, 4", got.SessionID, got.Turns)
			}
			if !slices.Equal(got.FilesChanged, []string{"a.go", "b.go"}) {
				t.Errorf("files changed = %v, want [a.go b.go]", got.FilesChanged)
			}
			if !got.StartedAt.Equal(started) || !got.EndedAt.Equal(ended) {
				t.Errorf("started, ended = %v, %v, want %v, %v", got.StartedAt, got.EndedAt, started, ended)
			}
			switch {
			case tt.cost == nil && got.CostUSD != nil:
				t.Errorf("cost = %v, want nil", *got.CostUSD)
			case tt.cost != nil && got.CostUSD == nil:
				t.Errorf("cost = nil, want %v", *tt.cost)
			case tt.cost != nil && *got.CostUSD != *tt.cost:
				t.Errorf("cost = %v, want %v", *got.CostUSD, *tt.cost)
			}
		})
	}

	entries, err := os.ReadDir(run.TaskDir(task.ID))
	if err != nil {
		t.Fatalf("read task dir: %v", err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	if want := []string{"task.json", "task.md"}; !slices.Equal(names, want) {
		t.Errorf("task dir holds %v, want %v: a save must not leave a temp file", names, want)
	}
	for _, name := range names {
		info, err := os.Stat(path.Join(run.TaskDir(task.ID), name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("%s mode = %o, want 644", name, perm)
		}
	}

	t.Run("a reader never sees a partial record", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		want := mustNewTask(t, run, RoleCoder, "job")
		done := make(chan struct{})
		errc := make(chan error, 1)
		go func() {
			defer close(done)
			task := want
			for i := range 100 {
				task.Turns = i
				task.FilesChanged = make([]string, 100)
				if err := run.Save(task); err != nil {
					errc <- err
					return
				}
			}
		}()

		var readErr error
	loop:
		for {
			select {
			case <-done:
				break loop
			default:
				got, err := run.Load(want.ID)
				if err != nil {
					readErr = err
					break loop
				}
				if got.ID != want.ID {
					readErr = fmt.Errorf("loaded task %q during a save of %q", got.ID, want.ID)
					break loop
				}
			}
		}
		<-done
		if readErr != nil {
			t.Fatalf("Load during Save: %v", readErr)
		}
		select {
		case err := <-errc:
			t.Fatalf("Save: %v", err)
		default:
		}
	})

	t.Run("a task dir that does not exist is an error", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		if err := run.Save(Task{ID: "t9"}); err == nil {
			t.Fatal("Save into a missing task dir = nil error")
		}
	})
}

func TestListOrder(t *testing.T) {
	run := Run{Dir: t.TempDir()}

	tasks, err := run.List()
	if err != nil {
		t.Fatalf("List with no tasks dir: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("List = %v, want empty", tasks)
	}

	for range 12 {
		mustNewTask(t, run, RoleCoder, "task")
	}
	tasks, err = run.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(tasks))
	for i, task := range tasks {
		got[i] = task.ID
	}
	want := make([]string, 12)
	for i := range want {
		want[i] = "t" + strconv.Itoa(i+1)
	}
	if !slices.Equal(got, want) {
		t.Errorf("List ids = %v, want numeric id order %v", got, want)
	}
}

func TestListSkipsUnreadableTasks(t *testing.T) {
	run := Run{Dir: t.TempDir()}
	mustNewTask(t, run, RoleCoder, "first")
	mustNewTask(t, run, RoleCoder, "second")

	tasksDir := path.Join(run.Dir, "tasks")
	if err := os.Mkdir(path.Join(tasksDir, "t3"), 0o755); err != nil {
		t.Fatalf("mkdir t3: %v", err)
	}
	if err := os.Mkdir(path.Join(tasksDir, "t4"), 0o755); err != nil {
		t.Fatalf("mkdir t4: %v", err)
	}
	if err := os.WriteFile(path.Join(tasksDir, "t4", "task.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt task.json: %v", err)
	}
	if err := os.WriteFile(path.Join(tasksDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	if err := os.Mkdir(path.Join(tasksDir, "scratch"), 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	if err := os.WriteFile(path.Join(tasksDir, "scratch", "task.json"), []byte(`{"id":"scratch"}`), 0o644); err != nil {
		t.Fatalf("write scratch task.json: %v", err)
	}

	tasks, err := run.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(tasks))
	for i, task := range tasks {
		got[i] = task.ID
	}
	if want := []string{"t1", "t2"}; !slices.Equal(got, want) {
		t.Errorf("List ids = %v, want %v", got, want)
	}
}

func TestListRunsNewestFirst(t *testing.T) {
	base := t.TempDir()

	got, err := ListRuns(base)
	if err != nil {
		t.Fatalf("ListRuns with no runs dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListRuns = %v, want empty", got)
	}

	runsDir := path.Join(base, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	old := RunID("alpha", time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))
	mid := RunID("alpha", time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC))
	newer := RunID("beta", time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	for _, id := range []string{old, newer, mid} {
		if err := os.Mkdir(path.Join(runsDir, id), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", id, err)
		}
	}
	if err := os.WriteFile(path.Join(runsDir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray.txt: %v", err)
	}

	got, err = ListRuns(base)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if want := []string{newer, mid, old}; !slices.Equal(got, want) {
		t.Errorf("ListRuns = %v, want newest first %v", got, want)
	}

	t.Run("ids without a stamp fall back to name order", func(t *testing.T) {
		base := t.TempDir()
		runsDir := path.Join(base, "runs")
		if err := os.MkdirAll(runsDir, 0o755); err != nil {
			t.Fatalf("mkdir runs: %v", err)
		}
		for _, id := range []string{"zeta", "alpha"} {
			if err := os.Mkdir(path.Join(runsDir, id), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", id, err)
			}
		}
		got, err := ListRuns(base)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if want := []string{"alpha", "zeta"}; !slices.Equal(got, want) {
			t.Errorf("ListRuns = %v, want name order %v", got, want)
		}
	})
}

func TestRunID(t *testing.T) {
	// The container clock may not be UTC; the id always carries UTC.
	at := time.Date(2026, 10, 26, 10, 15, 0, 0, time.FixedZone("PDT", -7*60*60))
	if got, want := RunID("dev-app", at), "dev-app-20261026T171500Z"; got != want {
		t.Errorf("RunID = %q, want %q", got, want)
	}
	utc := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, want := RunID("box", utc), "box-20260102T030405Z"; got != want {
		t.Errorf("RunID = %q, want %q", got, want)
	}
}

func TestBaseDependsOnPersistState(t *testing.T) {
	if got := Base(true); got != "/var/dev-state/dev-team" {
		t.Errorf("Base(true) = %q, want /var/dev-state/dev-team", got)
	}
	if got := Base(false); got != "/tmp/dev-team" {
		t.Errorf("Base(false) = %q, want /tmp/dev-team", got)
	}
	if got := BinPath(Base(true)); got != "/var/dev-state/dev-team/bin/dev-team" {
		t.Errorf("BinPath(Base(true)) = %q, want /var/dev-state/dev-team/bin/dev-team", got)
	}
	if got := BinPath(Base(false)); got != "/tmp/dev-team/bin/dev-team" {
		t.Errorf("BinPath(Base(false)) = %q, want /tmp/dev-team/bin/dev-team", got)
	}
}

func TestConfig(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		if _, err := run.Config(); err == nil {
			t.Fatal("Config on a run without config.json = nil error")
		} else if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("error = %q, want it to name config.json", err)
		}
	})

	t.Run("reads config.json", func(t *testing.T) {
		run := Run{Dir: t.TempDir()}
		want := Config{
			Lead:     Tool{ID: "claude", Model: "opus"},
			Workers:  "opencode",
			Models:   map[Role]string{RoleCoder: "a/b", RoleReviewer: "c/d"},
			MaxTurns: 7,
			Timeout:  90 * time.Minute,
		}
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		if err := os.WriteFile(path.Join(run.Dir, "config.json"), data, 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}

		got, err := run.Config()
		if err != nil {
			t.Fatalf("Config: %v", err)
		}
		if got.Lead != want.Lead || got.Workers != want.Workers || got.MaxTurns != want.MaxTurns || got.Timeout != want.Timeout {
			t.Errorf("Config = %+v, want %+v", got, want)
		}
		if !maps.Equal(got.Models, want.Models) {
			t.Errorf("models = %v, want %v", got.Models, want.Models)
		}
	})
}

func TestConcurrentNewTaskDistinctIDs(t *testing.T) {
	run := Run{Dir: t.TempDir()}
	const perGoroutine = 20

	results := make(chan Task, 2*perGoroutine)
	errs := make(chan error, 2*perGoroutine)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				task, err := run.NewTask(RoleCoder, "concurrent")
				if err != nil {
					errs <- err
					return
				}
				results <- task
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("NewTask: %v", err)
	}
	seen := make(map[string]bool, 2*perGoroutine)
	for task := range results {
		if seen[task.ID] {
			t.Errorf("id %s handed out twice", task.ID)
		}
		seen[task.ID] = true
	}
	for i := 1; i <= 2*perGoroutine; i++ {
		if id := "t" + strconv.Itoa(i); !seen[id] {
			t.Errorf("id %s was never handed out", id)
		}
	}
	if len(seen) != 2*perGoroutine {
		t.Errorf("handed out %d ids, want %d", len(seen), 2*perGoroutine)
	}
}

func mustNewTask(t *testing.T, run Run, role Role, text string) Task {
	t.Helper()
	task, err := run.NewTask(role, text)
	if err != nil {
		t.Fatalf("NewTask(%s): %v", role, err)
	}
	return task
}

func TestLookupRunRejectsNonPathElements(t *testing.T) {
	for _, id := range []string{"api-20261026T120000Z", "t1x", "T1"} {
		run, err := LookupRun("/base", id)
		if err != nil {
			t.Errorf("LookupRun(%q) = %v, want it accepted as one path element", id, err)
			continue
		}
		if want := path.Join("/base", "runs", id); run.Dir != want {
			t.Errorf("LookupRun(%q).Dir = %q, want %q", id, run.Dir, want)
		}
	}
	for _, id := range []string{"", ".", "..", "a/b", "../t1", "a\nb"} {
		if _, err := LookupRun("/base", id); err == nil {
			t.Errorf("LookupRun(%q) accepted a value that can leave runs/", id)
		}
	}
}

func TestLogPathRejectsInvalidTaskIDs(t *testing.T) {
	run := Run{Dir: "/base/runs/api-20261026T120000Z"}
	for _, id := range []string{"t1", "t10"} {
		got, err := run.LogPath(id)
		if err != nil {
			t.Errorf("LogPath(%q) = %v, want it accepted", id, err)
			continue
		}
		if want := path.Join(run.Dir, "tasks", id, "log.jsonl"); got != want {
			t.Errorf("LogPath(%q) = %q, want %q", id, got, want)
		}
	}
	for _, id := range []string{"", ".", "..", "a/b", "../t1", "t1x", "T1", "t1\n", "t", "t0", "t01"} {
		if _, err := run.LogPath(id); err == nil {
			t.Errorf("LogPath(%q) accepted an id that is not t plus digits", id)
		}
	}
}

func TestTaskRetryOfAndCwdRoundTrip(t *testing.T) {
	run := Run{Dir: t.TempDir()}
	first := mustNewTask(t, run, RoleCoder, "first")
	second := mustNewTask(t, run, RoleCoder, "second")
	second.RetryOf = first.ID
	second.Cwd = "/work/project"
	if err := run.Save(second); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := run.Load(second.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RetryOf != first.ID {
		t.Errorf("retry of = %q, want %q", got.RetryOf, first.ID)
	}
	if got.Cwd != "/work/project" {
		t.Errorf("cwd = %q, want /work/project", got.Cwd)
	}

	data, err := os.ReadFile(path.Join(run.TaskDir(second.ID), "task.json"))
	if err != nil {
		t.Fatalf("read task.json: %v", err)
	}
	for _, field := range []string{`"retry_of"`, `"cwd"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("task.json = %s, want it to carry %s", data, field)
		}
	}

	third := mustNewTask(t, run, RoleReviewer, "third")
	if third.RetryOf != "" || third.Cwd != "" {
		t.Errorf("fresh task has retry_of %q, cwd %q, want both empty", third.RetryOf, third.Cwd)
	}
}
