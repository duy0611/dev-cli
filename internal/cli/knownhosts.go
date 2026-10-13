package cli

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// githubFingerprints are GitHub's published SSH host key fingerprints:
// https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints
var githubFingerprints = []string{
	"SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s", // RSA
	"SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM", // ECDSA
	"SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU", // Ed25519
}

// trustGitHubScript adds github.com to the remote user's known_hosts unless an
// entry is already there, keeping only the scanned keys whose fingerprint is
// one of the arguments. ssh-keygen -l reads a file, not a known_hosts line on
// stdin, on every OpenSSH version, so each key is written out to be checked.
const trustGitHubScript = `grep -q '^github\.com ' ~/.ssh/known_hosts 2>/dev/null && exit 0
dir=$(mktemp -d) || exit 1
trap 'rm -rf "$dir"' EXIT
ssh-keyscan github.com > "$dir/scan" 2>/dev/null
: > "$dir/ok"
while IFS= read -r line; do
  case $line in ''|'#'*) continue ;; esac
  printf '%s\n' "$line" > "$dir/key"
  fp=$(ssh-keygen -lf "$dir/key" 2>/dev/null | cut -d' ' -f2)
  for want in "$@"; do
    if [ -n "$fp" ] && [ "$fp" = "$want" ]; then printf '%s\n' "$line" >> "$dir/ok"; break; fi
  done
done < "$dir/scan"
if [ ! -s "$dir/ok" ]; then
  echo "no key ssh-keyscan returned matches GitHub's published fingerprints" >&2
  exit 1
fi
mkdir -p ~/.ssh && chmod 700 ~/.ssh && cat "$dir/ok" >> ~/.ssh/known_hosts`

// trustGitHub lets ssh to github.com work in the container without a prompt.
// ~/.ssh is on the container filesystem, so every up runs it again. A failure
// only warns: no network or no ssh-keyscan must not stop a start.
func trustGitHub(ctx context.Context, a *app, p provider.Provider, c model.Container) {
	var stderr bytes.Buffer
	argv := append([]string{"sh", "-c", trustGitHubScript, "trust-github"}, githubFingerprints...)
	err := p.Exec(ctx, c, argv, provider.ExecOpts{Stdout: io.Discard, Stderr: &stderr})
	if err != nil {
		warnf(a, "github.com was not added to ~/.ssh/known_hosts in container %s: %v: %s",
			c.Name, err, strings.TrimSpace(stderr.String()))
	}
}
