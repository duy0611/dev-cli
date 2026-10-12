package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSigningEnvActuallySignsACommit runs real git with the environment dev
// hands a container. The shape of the config is not enough: its traps produce
// configuration git rejects.
func TestSigningEnvActuallySignsACommit(t *testing.T) {
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	socket := startTestAgent(t)
	env := signingEnv(socket)
	if len(env) == 0 {
		t.Fatal("no signing configuration was produced")
	}

	repo := t.TempDir()
	run := func(extraEnv []string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
		cmd.Env = append(cmd.Env, extraEnv...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "signing@example.invalid"},
		{"config", "user.name", "Signing Test"},
	} {
		if out, err := run(nil, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	var gitEnv []string
	for _, e := range env {
		gitEnv = append(gitEnv, e.Key+"="+e.Value)
	}

	if out, err := run(gitEnv, "commit", "-q", "--allow-empty", "-m", "signed through the relay"); err != nil {
		t.Fatalf("committing with dev's signing configuration: %v\n%s", err, out)
	}

	// The signature is in the commit object, so this needs no
	// allowedSignersFile.
	out, err := run(nil, "cat-file", "commit", "HEAD")
	if err != nil {
		t.Fatalf("reading the commit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BEGIN SSH SIGNATURE") {
		t.Errorf("the commit carries no ssh signature:\n%s", out)
	}
}

// TestSigningEnvVerifiesAgainstAllowedSigners checks the signature is good and
// made by the agent's key.
func TestSigningEnvVerifiesAgainstAllowedSigners(t *testing.T) {
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	socket := startTestAgent(t)
	env := signingEnv(socket)
	if len(env) == 0 {
		t.Fatal("no signing configuration was produced")
	}

	var pub string
	for i, e := range env {
		if e.Value == "user.signingkey" && i+1 < len(env) {
			pub = strings.TrimPrefix(env[i+1].Value, "key::")
		}
	}
	if pub == "" {
		t.Fatal("user.signingkey was not configured")
	}

	repo := t.TempDir()
	const email = "signing@example.invalid"
	allowed := filepath.Join(repo, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(email+" "+pub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(extraEnv []string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
		cmd.Env = append(cmd.Env, extraEnv...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", email},
		{"config", "user.name", "Signing Test"},
		{"config", "gpg.ssh.allowedSignersFile", allowed},
	} {
		if out, err := run(nil, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	var gitEnv []string
	for _, e := range env {
		gitEnv = append(gitEnv, e.Key+"="+e.Value)
	}
	if out, err := run(gitEnv, "commit", "-q", "--allow-empty", "-m", "verified"); err != nil {
		t.Fatalf("committing: %v\n%s", err, out)
	}

	out, err := run(nil, "log", "--format=%G?", "-1")
	if err != nil {
		t.Fatalf("verifying: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(out); got != "G" {
		t.Errorf("git reports signature status %q, want \"G\"", got)
	}
}
