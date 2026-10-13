package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitHub's real host keys, as ssh-keyscan returned them.
const githubKeys = `github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
`

// forgedKey is a fresh ed25519 key presented as github.com's.
func forgedKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(pub))
	return "github.com " + fields[0] + " " + fields[1] + "\n"
}

// runTrustGitHub runs the in-container script with ssh-keyscan answering scan,
// and returns the resulting known_hosts.
func runTrustGitHub(t *testing.T, scan string) (knownHosts string, err error) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	home, bin := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(bin, "scan"), scan)
	writeFile(t, filepath.Join(bin, "ssh-keyscan"), "#!/bin/sh\ncat '"+filepath.Join(bin, "scan")+"'\n")
	if err := os.Chmod(filepath.Join(bin, "ssh-keyscan"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{"-c", trustGitHubScript, "trust"}, githubFingerprints...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = &exec.ExitError{Stderr: out}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	return string(b), err
}

func TestTrustGitHubWritesTheVerifiedKeys(t *testing.T) {
	got, err := runTrustGitHub(t, githubKeys)
	if err != nil {
		t.Fatalf("script failed: %s", err.(*exec.ExitError).Stderr)
	}
	if got != githubKeys {
		t.Errorf("known_hosts =\n%s\nwant\n%s", got, githubKeys)
	}
}

func TestTrustGitHubRefusesAForgedKey(t *testing.T) {
	got, err := runTrustGitHub(t, forgedKey(t))
	if err == nil {
		t.Error("the script succeeded with no key matching GitHub's fingerprints")
	}
	if got != "" {
		t.Errorf("a forged key reached known_hosts:\n%s", got)
	}
}

// A key GitHub has not published is left out; the published ones still go in.
func TestTrustGitHubDropsAnUnpublishedKey(t *testing.T) {
	forged := forgedKey(t)
	got, _ := runTrustGitHub(t, githubKeys+forged)
	if strings.Contains(got, strings.Fields(forged)[2]) {
		t.Errorf("an unpublished key reached known_hosts:\n%s", got)
	}
	if got != githubKeys {
		t.Errorf("known_hosts =\n%s\nwant GitHub's published keys", got)
	}
}
