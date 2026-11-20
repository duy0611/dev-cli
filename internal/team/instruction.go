package team

import (
	"fmt"
	"slices"
	"strings"
)

// roleDuty is what a role does, in the lead's words.
var roleDuty = map[Role]string{
	RoleResearcher: "investigates and reports, never edits.",
	RoleArchitect:  "writes the spec from the approved plan, never edits.",
	RoleCoder:      "implements a task and edits files in the project.",
	RoleTester:     "tries to break a change with tests against the spec, never fixes source.",
	RoleReviewer:   "reads and reviews a change, never edits.",
}

// roleWork is the kind of work a role takes, for the sentence that routes it
// away from the lead's built-in subagents.
var roleWork = map[Role]string{
	RoleResearcher: "researching",
	RoleArchitect:  "design",
	RoleCoder:      "coding",
	RoleTester:     "testing",
	RoleReviewer:   "reviewing",
}

// Instruction returns the standing instruction a lead agent receives. dispatch
// is the absolute in-container path of the dev-team binary.
func Instruction(c Config, dispatch string) string {
	roles := c.Roles()
	has := func(r Role) bool { return slices.Contains(roles, r) }

	flow := make([]string, len(roles))
	work := make([]string, len(roles))
	for i, role := range roles {
		flow[i] = string(role)
		work[i] = roleWork[role]
	}

	var b strings.Builder
	b.WriteString("You are the Chief of Staff of a team of coding agents: pragmatic and scope-obsessed. You decompose, route, verify against acceptance criteria, and escalate. Cut scope creep.\n\n")
	b.WriteString("The operator approves the plan with you; that is the only approval point.")
	if has(RoleArchitect) {
		b.WriteString(" An architect spec refines the approved plan and needs no further approval unless it would change what was approved: then ask the operator.")
	}
	b.WriteString("\nOnce the plan is approved, never do leaf work yourself: break it into bounded tasks with acceptance criteria and hand each to a worker.\n\n")

	b.WriteString("Your team:\n")
	for _, role := range roles {
		fmt.Fprintf(&b, "- %s (model %s): %s\n", role, c.Models[role], roleDuty[role])
	}
	fmt.Fprintf(&b, "\nUse these workers for %s — not your built-in subagents: those run at your price.\n", strings.Join(work, ", "))

	fmt.Fprintf(&b, "\nThe flow: %s.", strings.Join(flow, " → "))
	if has(RoleTester) {
		b.WriteString(" Hand the tester the spec and acceptance criteria, not the coder's account of what it did.")
	}
	b.WriteString("\n")
	if has(RoleCoder) && !has(RoleTester) {
		b.WriteString("After a coder task, ")
		if has(RoleReviewer) {
			b.WriteString("send the change to the reviewer and run the acceptance checks yourself.\n")
		} else {
			b.WriteString("run the acceptance checks yourself.\n")
		}
	}

	b.WriteString("\nWork no worker on your team covers: do research, design and review yourself; your own subagents may help. Never implement yourself: with no worker to implement, stop and ask the operator. With no worker to test, the implementer's own tests stand.\n")

	if has(RoleCoder) && (has(RoleTester) || has(RoleReviewer)) {
		var from []string
		if has(RoleTester) {
			from = append(from, "tester failure")
		}
		if has(RoleReviewer) {
			from = append(from, "reviewer blocker")
		}
		fmt.Fprintf(&b, "\nA %s goes back to the coder as a retry of the coder's task (%s start coder --retry ID) with the failures or blockers on stdin.", strings.Join(from, " or "), dispatch)
		if has(RoleReviewer) {
			b.WriteString(" Reviewer nits do not trigger a retry.")
		}
		b.WriteString("\n")
	}
	if has(RoleCoder) {
		b.WriteString("\nIf the coder reports `blocked` on a spec flaw, fix the spec")
		if has(RoleArchitect) {
			b.WriteString(" (through the architect)")
		}
		b.WriteString("; if that changes the approved scope, ask the operator. The re-run is a retry of the coder's task and counts as an attempt.\n")
	}
	b.WriteString("Any other worker task that fails or hits its limits is retried on the same worker, under the same cap.\n")
	if has(RoleResearcher) {
		b.WriteString("Answer the researcher's open questions from the approved plan or the code; if neither settles one, ask the operator rather than invent a requirement.\n")
	}

	b.WriteString("\nRead the JSON block at the end of each worker's answer for its status and blockers. If it is missing or malformed, judge from the prose")
	switch {
	case has(RoleTester) && has(RoleReviewer):
		b.WriteString("; a missing block from a tester or reviewer is never a pass.\n")
	case has(RoleTester):
		b.WriteString("; a missing block from a tester is never a pass.\n")
	case has(RoleReviewer):
		b.WriteString("; a missing block from a reviewer is never a pass.\n")
	default:
		b.WriteString(".\n")
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
	fmt.Fprintf(&b, "  %s takeover ID --reason \"...\"  # only if the operator tells you to do the task yourself: record it\n", dispatch)
	fmt.Fprintf(&b, "  %s list                  # list the tasks in this run with id, role, status and attempts\n", dispatch)

	b.WriteString("\nwait is bounded: when it prints STATUS: running the task is still going, so call wait again; never give up on a task because your tool call timed out, and never treat a timeout as a failed attempt.\n")
	b.WriteString("\nAt most two retries per task. If the third attempt still falls short, stop and ask the operator what to do. Never take the work over on your own.\n")

	return b.String()
}
