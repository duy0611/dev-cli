package team

import (
	"bytes"
	"fmt"
	"text/tabwriter"
)

// RunTasks is one run with its tasks: the JSON `dev-team list --all-runs
// --json` prints and the host decodes. The tags are the wire contract between
// the two.
type RunTasks struct {
	Run   string `json:"run"`
	Tasks []Task `json:"tasks"`
}

// FormatTasks renders a run's tasks as the table the host prints for
// `dev container team list`. It returns the text so the caller owns the
// output stream.
func FormatTasks(tasks []Task) string {
	if len(tasks) == 0 {
		return "no tasks\n"
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tROLE\tSTATUS\tATTEMPTS\tCOST\tTAKEOVER\tUNCOLLECTED")
	for _, task := range tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			task.ID, task.Role, task.Status, task.Attempts, formatCost(task.CostUSD),
			yesOrDash(task.TookOver), yesOrDash(uncollected(task)))
	}
	_ = tw.Flush()
	return buf.String()
}

// formatCost renders a task's cost, or a dash when the worker has not reported
// one.
func formatCost(usd *float64) string {
	if usd == nil {
		return "-"
	}
	return fmt.Sprintf("$%.4f", *usd)
}

func yesOrDash(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

// uncollected reports a finished task whose result no wait has collected yet;
// a running one has nothing to collect.
func uncollected(task Task) bool {
	return task.Status != StatusRunning && !task.Collected
}
