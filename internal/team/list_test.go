package team

import (
	"strings"
	"testing"
)

// taskRow splits one formatted line into its columns.
func taskRow(t *testing.T, line string) []string {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) != 7 {
		t.Fatalf("row %q has %d columns, want 7", line, len(fields))
	}
	return fields
}

func TestFormatTasks(t *testing.T) {
	cost := 0.0123
	tasks := []Task{
		{ID: "t1", Role: RoleCoder, Status: StatusDone, Attempts: 1, Collected: true, CostUSD: &cost},
		{ID: "t2", Role: RoleReviewer, Status: StatusFailed, Attempts: 2},
		{ID: "t3", Role: RoleResearcher, Status: StatusRunning, Attempts: 1, TookOver: true,
			TakeoverReason: "the lead finished the review itself"},
	}

	lines := strings.Split(strings.TrimSuffix(FormatTasks(tasks), "\n"), "\n")
	if len(lines) != len(tasks)+1 {
		t.Fatalf("output has %d lines, want a header and %d rows:\n%s",
			len(lines), len(tasks), FormatTasks(tasks))
	}
	wantHeader := []string{"ID", "ROLE", "STATUS", "ATTEMPTS", "COST", "TAKEOVER", "UNCOLLECTED"}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != strings.Join(wantHeader, " ") {
		t.Errorf("header = %q, want %q", got, wantHeader)
	}

	for i, tt := range []struct {
		line string
		want []string
	}{
		{"t1", []string{"t1", "coder", "done", "1", "$0.0123", "-", "-"}},
		{"t2", []string{"t2", "reviewer", "failed", "2", "-", "-", "yes"}},
		{"t3", []string{"t3", "researcher", "running", "1", "-", "yes", "-"}},
	} {
		got := taskRow(t, lines[i+1])
		if strings.Join(got, " ") != strings.Join(tt.want, " ") {
			t.Errorf("%s: row = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestFormatTasksEmpty(t *testing.T) {
	if got := FormatTasks(nil); got != "no tasks\n" {
		t.Errorf("FormatTasks(nil) = %q, want %q", got, "no tasks\n")
	}
}
