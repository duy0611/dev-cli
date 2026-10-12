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

// Available reports whether Herdr can be asked to do anything. The binary on
// PATH says nothing about the daemon, and a stopped one hangs every call.
func Available(ctx context.Context) bool {
	if _, err := exec.LookPath(herdrBin); err != nil {
		return false
	}
	return exec.CommandContext(ctx, herdrBin, "status", "server").Run() == nil
}

// Open registers an existing checkout as a Herdr workspace and returns its ID.
// An unparsable answer is not an error: a missing ID only leaves a stale
// sidebar entry.
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

// Close removes the Herdr workspace. It does not touch the checkout, which git
// deletes.
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

// firstJSONLine returns the first line that looks like a JSON object, skipping
// any progress output.
func firstJSONLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}
