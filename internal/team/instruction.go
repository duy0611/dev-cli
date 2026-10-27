package team

import (
	"fmt"
	"slices"
	"strings"
)

// roleDuty is what a role does, in the lead's words.
var roleDuty = map[Role]string{
	RoleCoder:      "implements a task and edits files in the project.",
	RoleReviewer:   "reads and reviews a change, never edits.",
	RoleResearcher: "investigates and reports, never edits.",
}

// roleWork is the kind of work a role takes, for the sentence that routes it
// away from the lead's built-in subagents.
var roleWork = map[Role]string{
	RoleCoder:      "coding",
	RoleReviewer:   "reviewing",
	RoleResearcher: "researching",
}

// Instruction returns the standing instruction a lead agent receives. dispatch
// is the absolute in-container path of the dev-team binary.
func Instruction(c Config, dispatch string) string {
	roles := c.Roles()

	var b strings.Builder
	b.WriteString("You are the lead of a team of coding agents.\n\n")
	b.WriteString("You plan, write specs and review. Once the operator approves a spec or plan, do not implement it yourself: break it into tasks with acceptance criteria and hand each task to a worker.\n\n")
	b.WriteString("Your team:\n")
	for _, role := range roles {
		fmt.Fprintf(&b, "- %s (model %s): %s\n", role, c.Models[role], roleDuty[role])
	}

	work := make([]string, 0, len(roles))
	for _, role := range roles {
		work = append(work, roleWork[role])
	}
	fmt.Fprintf(&b, "\nUse these workers for %s — not your built-in subagents: those run at your price.\n", strings.Join(work, ", "))

	if slices.Contains(roles, RoleCoder) {
		b.WriteString("\nAfter a coder task, ")
		if slices.Contains(roles, RoleReviewer) {
			b.WriteString("send the change to the reviewer and run the acceptance checks yourself.\n")
		} else {
			b.WriteString("run the acceptance checks yourself.\n")
		}
	}

	fmt.Fprintf(&b, "\nHand each task to a worker with %s. The task text goes on stdin, never in argv:\n\n", dispatch)
	// A validated config has at least one role; the example must not name an absent one.
	// Column 0 throughout: a quoted heredoc ends only on an unindented EOF, so a
	// lead copying the example verbatim would otherwise swallow what follows.
	fmt.Fprintf(&b, "%s start %s <<'EOF'\n...the task, with acceptance criteria...\nEOF\n\n", dispatch, roles[0])
	b.WriteString("The commands:\n")
	fmt.Fprintf(&b, "  %s start ROLE            # start a task on a role, text on stdin; prints the task id at once\n", dispatch)
	fmt.Fprintf(&b, "  %s start ROLE --retry ID # send the task back to the same worker\n", dispatch)
	fmt.Fprintf(&b, "  %s wait ID               # wait up to 60 s; prints STATUS: running if the task is still going, else its final status and answer\n", dispatch)
	fmt.Fprintf(&b, "  %s result ID             # print a finished task's final answer\n", dispatch)
	fmt.Fprintf(&b, "  %s takeover ID --reason \"...\"  # record that you took the task over yourself\n", dispatch)
	fmt.Fprintf(&b, "  %s list                  # list the tasks in this run with id, role, status and attempts\n", dispatch)

	b.WriteString("\nwait is bounded: when it prints STATUS: running the task is still going, so call wait again; never give up on a task because your tool call timed out, and never treat a timeout as a failed attempt.\n")
	b.WriteString("\nIf a task falls short, send it back to the same worker with the specific problems (failing tests, review findings) on stdin. At most two retries. If the third attempt still falls short, implement it yourself and record the takeover.\n")

	return b.String()
}
