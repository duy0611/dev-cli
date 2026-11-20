package team

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatTasks(t *testing.T) {
	cost := 0.0123
	tasks := []Task{
		{ID: "t1", Role: RoleCoder, Title: "Task 3: saved tiers", Status: StatusDone, Attempts: 1, Collected: true, CostUSD: &cost},
		{ID: "t2", Role: RoleReviewer, Status: StatusFailed, Attempts: 2},
		{ID: "t3", Role: RoleResearcher, Title: "map the CLI", Status: StatusRunning, Attempts: 1, TookOver: true,
			TakeoverReason: "the lead finished the review itself"},
		{ID: "t4", Role: RoleCoder, Title: "Task 3: saved tiers", Status: StatusRunning, Attempts: 2, RetryOf: "t1"},
	}

	want := "ID  ROLE        TITLE                STATUS   ATTEMPTS  COST     TAKEOVER  UNCOLLECTED\n" +
		"t1  coder       Task 3: saved tiers  done     1         $0.0123  -         -\n" +
		"t2  reviewer    -                    failed   2         -        -         yes\n" +
		"t3  researcher  map the CLI          running  1         -        yes       -\n" +
		"t4  coder       Task 3: saved tiers  running  2 (t1)    -        -         -\n"
	if got := FormatTasks(tasks); got != want {
		t.Errorf("FormatTasks =\n%s\nwant\n%s", got, want)
	}
}

// An old record has no title and no retry link; it must still render.
func TestFormatTasksOldRecord(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"id":"t1","role":"coder","status":"done","attempts":1}`), &task); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(FormatTasks([]Task{task}), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	got := strings.Fields(lines[1])
	want := []string{"t1", "coder", "-", "done", "1", "-", "-", "yes"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("row = %q, want %q", got, want)
	}
}

func TestFormatTasksEmpty(t *testing.T) {
	if got := FormatTasks(nil); got != "no tasks\n" {
		t.Errorf("FormatTasks(nil) = %q, want %q", got, "no tasks\n")
	}
}
