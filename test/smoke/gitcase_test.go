//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSmokeGitGuardCaseVariants checks whether a guarded container reaches the
// host's .git/config and hooks through a name that differs only in case.
//
// The guard's mounts sit on exact paths. Inside the container Linux treats
// .git/CONFIG and .GIT as names of their own, so they miss those mounts and
// fall through to the writable .git and workspace mounts beneath. A host whose
// filesystem ignores case — macOS by default — resolves them back to the real
// .git/config and .git, which would put the container's write in the host's
// file with no host action at all. Reported for Docker Desktop in
// AI-Progress-Agent/claude-container#11; this checks the guard dev builds.
//
// On a podman machine the file variant got through — .git/CONFIG landed in
// the host's .git/config — while every directory variant hit a mount: .GIT/config
// was refused read-only, .git/HOOKS wrote into the container's hooks copy, and
// mv .GIT was refused busy. Consistent with Linux keeping one dentry per
// directory, so a case variant of a directory resolves to the mount point,
// while a file may have several and a variant of one misses its mount.
//
// Skipped on a case-sensitive host, where none of these names can reach the
// real files and a pass would prove nothing.
func TestSmokeGitGuardCaseVariants(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	project := gitFixture(t)
	if _, err := os.Stat(filepath.Join(project, ".GIT")); err != nil {
		t.Skip("the host filesystem is case-sensitive; a case variant cannot reach .git here")
	}

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	const name = "dev-smoke-gitguard-case"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	t.Log("creating a guarded container; the first run pulls an image")
	dev("container", "create", name, "--folder", project)

	ws := "/workspaces/" + filepath.Base(project)
	hostConfig := filepath.Join(project, ".git", "config")
	hostHooks := filepath.Join(project, ".git", "hooks")

	// try runs script in the container and reports whether it succeeded
	// there. Whether the write reached the host is checked separately: a
	// write can succeed into a file of its own on the container side.
	try := func(label, script string) bool {
		t.Helper()
		out, err := exec.Command(bin, "container", "exec", name, "--", "sh", "-c", script).CombinedOutput()
		result := "succeeded in the container"
		if err != nil {
			result = "refused: " + strings.TrimSpace(string(out))
		}
		t.Logf("%s: %s", label, result)
		return err == nil
	}
	hostConfigHas := func(marker string) bool {
		t.Helper()
		b, err := os.ReadFile(hostConfig)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(b), marker)
	}

	// The exact name first: if this gets through, the guard never took hold
	// and nothing below means anything.
	if try(".git/config (control)", "echo '# dev-case-probe-control' >> "+ws+"/.git/config") {
		t.Fatal("the exact .git/config was writable; the guard never took hold")
	}

	// A comment line, so a probe that lands leaves the host's config valid.
	for i, rel := range []string{".git/CONFIG", ".GIT/config"} {
		marker := fmt.Sprintf("# dev-case-probe-%d", i)
		wrote := try(rel, "echo '"+marker+"' >> "+ws+"/"+rel)
		if hostConfigHas(marker) {
			t.Errorf("a write to %s reached the host's .git/config (succeeded in the container: %v)", rel, wrote)
		}
	}

	// Names git never runs, so a probe that lands in the host's real hooks
	// directory still executes nothing.
	for _, rel := range []string{".git/HOOKS/dev-case-probe-a", ".GIT/hooks/dev-case-probe-b"} {
		wrote := try(rel, "echo probe > "+ws+"/"+rel)
		if _, err := os.Stat(filepath.Join(hostHooks, filepath.Base(rel))); err == nil {
			t.Errorf("a write to %s landed in the host's .git/hooks rather than the container's copy (succeeded in the container: %v)", rel, wrote)
		}
	}

	// Last, because if it works the host's .git has moved for everything
	// after it. Registered after the container's removal, so it runs first.
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(project, ".git")); err != nil {
			_ = os.Rename(filepath.Join(project, ".GIT.old"), filepath.Join(project, ".git"))
		}
	})
	wrote := try("mv .GIT .GIT.old", "mv "+ws+"/.GIT "+ws+"/.GIT.old")
	if _, err := os.Stat(hostConfig); err != nil {
		t.Errorf("renaming .GIT in the container moved the host's .git (succeeded in the container: %v): %v", wrote, err)
	}
}
