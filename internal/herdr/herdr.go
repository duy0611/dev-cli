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
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
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

var retryEvery = 500 * time.Millisecond

const nameTimeout = time.Minute

// AgentName turns a container name into a Herdr agent name, which must match
// [a-z][a-z0-9_-]{0,31}. It returns "" for a name that cannot fit.
func AgentName(container string) string {
	name := strings.ReplaceAll(strings.ToLower(container), ".", "-")
	if len(name) > 32 {
		name = name[:32]
	}
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return ""
	}
	return name
}

// ValidAgentName reports whether Herdr accepts name as an agent name.
func ValidAgentName(name string) bool {
	if name == "" || len(name) > 32 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// NameAgent names the agent about to run in the calling Herdr pane, in the
// background, and returns a func that stops trying. Outside a Herdr pane, or
// with no name, it does nothing.
func NameAgent(ctx context.Context, name string) (stop func()) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" || name == "" {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	var wg sync.WaitGroup
	wg.Go(func() {
		// Herdr only renames an agent it has detected, which happens some time
		// after the agent takes the pane's foreground.
		for exec.CommandContext(ctx, herdrBin, "agent", "rename", pane, name).Run() != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryEvery):
			}
		}
	})
	return func() {
		cancel()
		wg.Wait()
	}
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
