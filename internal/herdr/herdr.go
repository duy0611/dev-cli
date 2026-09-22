// Package herdr registers a checkout with Herdr, when Herdr is running.
//
// Everything here is optional. Herdr is a view onto a checkout that exists
// either way, so a failure to open that view is warned about and never stops
// the command that made the checkout.
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const herdrBin = "herdr"

// Available reports whether Herdr can be asked to do anything.
//
// Two checks, not one. The binary being on PATH says nothing about the daemon:
// an installed Herdr whose server is not running would turn every call into a
// hang or a failure, on a path where neither is the operator's problem.
func Available(ctx context.Context) bool {
	if _, err := exec.LookPath(herdrBin); err != nil {
		return false
	}
	return exec.CommandContext(ctx, herdrBin, "status", "server").Run() == nil
}

// Open registers an existing checkout as a Herdr workspace and returns its ID.
//
// The checkout is made by git first and handed over afterwards, rather than
// letting `herdr worktree create` do both: the git work must not depend on an
// optional tool.
//
// An unparsable answer is not an error. The ID is only needed to close the
// workspace later, and a missing one costs a stale entry in a sidebar — far
// less than failing a command whose real work already succeeded.
func Open(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, herdrBin,
		"worktree", "open", "--path", path).Output()
	if err != nil {
		return "", fmt.Errorf("herdr worktree open: %w", err)
	}

	var res struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(string(out))), &res); err != nil {
		return "", nil
	}
	return res.WorkspaceID, nil
}

// Close removes the Herdr workspace. Herdr state only: the checkout is deleted
// by git, which is the division Herdr's own commands draw.
//
// --force because the checkout is being deleted regardless, so a prompt about
// it would be a question with one answer.
func Close(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil // herdr was absent or declined when this was created
	}
	if out, err := exec.CommandContext(ctx, herdrBin,
		"worktree", "remove", "--workspace", workspaceID, "--force").
		CombinedOutput(); err != nil {
		return fmt.Errorf("herdr worktree remove: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// firstJSONLine returns the first line that looks like a JSON object, so a
// progress line printed before the result does not break the decode.
func firstJSONLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}
