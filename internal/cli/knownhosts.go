package cli

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// trustGitHubScript adds github.com to the remote user's known_hosts unless an
// entry is already there. Scanned into a temporary file first, so a failed scan
// appends nothing.
const trustGitHubScript = `grep -q '^github\.com ' ~/.ssh/known_hosts 2>/dev/null && exit 0
mkdir -p ~/.ssh && chmod 700 ~/.ssh
keys=$(mktemp) || exit 1
trap 'rm -f "$keys"' EXIT
ssh-keyscan github.com > "$keys" && [ -s "$keys" ] && cat "$keys" >> ~/.ssh/known_hosts`

// trustGitHub lets ssh to github.com work in the container without a prompt.
// ~/.ssh is on the container filesystem, so every up runs it again. A failure
// only warns: no network or no ssh-keyscan must not stop a start.
func trustGitHub(ctx context.Context, a *app, p provider.Provider, c model.Container) {
	var stderr bytes.Buffer
	err := p.Exec(ctx, c, []string{"sh", "-c", trustGitHubScript},
		provider.ExecOpts{Stdout: io.Discard, Stderr: &stderr})
	if err != nil {
		warnf(a, "could not add github.com to ~/.ssh/known_hosts in container %s: %v: %s",
			c.Name, err, strings.TrimSpace(stderr.String()))
	}
}
