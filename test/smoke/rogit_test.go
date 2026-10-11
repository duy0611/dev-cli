//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPIKE — throwaway. It answers whether mounting the whole of .git read-only,
// over a writable workspace, closes what the git guard's single-file mounts
// leave open: a host rename of .git/config, and on a case-insensitive host a
// case variant of a guarded name. dev cannot build this layout yet, so the
// test drives the engine directly.
//
// Three things to confirm: the container cannot write anything under .git,
// by any name, before or after host git writes its config; host git keeps
// working normally while the container runs; and git inside the container
// still reads — status, diff, log — while add and commit fail, which is the
// cost of the layout.

// roGitWatch is how long the probe keeps checking after each host git write.
// On a podman machine the single-file guard broke about 1.5s after a host
// rename, so this outlasts the slowest break seen so far with room.
const roGitWatch = 5 * time.Second

func TestSmokeReadOnlyGitDir(t *testing.T) {
	requireBinaries(t, "docker", "git")

	project := gitFixture(t)
	// Physical path: the engine resolves the source inside its VM (invariant 2).
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	hostGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostGit("add", "f.txt")
	hostGit("commit", "-q", "-m", "init")
	caseInsensitive := false
	if _, err := os.Stat(filepath.Join(project, ".GIT")); err == nil {
		caseInsensitive = true
	}

	const name = "dev-smoke-rogit"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// The layout under test: the workspace writable, .git read-only over it.
	engine(t, "run", "-d", "--name", name,
		"--mount", "type=bind,source="+project+",target=/w",
		"--mount", "type=bind,source="+filepath.Join(project, ".git")+",target=/w/.git,readonly",
		roMountImage, "sleep", "600")
	// alpine ships without git; one install, kept for every probe after it.
	t.Log("installing git in the container")
	engine(t, "exec", name, "apk", "add", "--no-cache", "-q", "git")
	// The workspace belongs to the host user, not the container's root, so git
	// refuses it as dubious ownership without this. It goes to root's global
	// config, inside the container, never the project's.
	engine(t, "exec", name, "git", "config", "--global", "--add", "safe.directory", "/w")

	// Each probe appends a marker the host checks for, so a write that
	// succeeds into some other file inside the container is told apart from
	// one that reaches the host's .git. Hook probes use names git never runs.
	type probe struct{ label, script, hostFile, marker string }
	probes := []probe{
		{".git/config", "echo '# rogit-probe-1' >> /w/.git/config", ".git/config", "# rogit-probe-1"},
		{".git/info/attributes", "echo '# rogit-probe-2' >> /w/.git/info/attributes", ".git/info/attributes", "# rogit-probe-2"},
		{".git/hooks/<new>", "echo '# rogit-probe-3' > /w/.git/hooks/rogit-probe", ".git/hooks/rogit-probe", "# rogit-probe-3"},
		{".git/commondir", "echo './' > /w/.git/commondir", ".git/commondir", "./"},
		// Its own section name: the host writes rogit.host below, and a marker
		// of plain "rogit" matched the host's line rather than the container's.
		{"git config inside", "cd /w && git config rogitinside.probe 4", ".git/config", "[rogitinside]"},
	}
	if caseInsensitive {
		probes = append(probes,
			probe{".git/CONFIG", "echo '# rogit-probe-5' >> /w/.git/CONFIG", ".git/config", "# rogit-probe-5"},
			probe{".GIT/config", "echo '# rogit-probe-6' >> /w/.GIT/config", ".git/config", "# rogit-probe-6"},
			probe{".git/HOOKS/<new>", "echo '# rogit-probe-7' > /w/.git/HOOKS/rogit-probe-upper", ".git/hooks/rogit-probe-upper", "# rogit-probe-7"},
		)
	} else {
		t.Log("the host filesystem is case-sensitive; the case-variant probes are skipped")
	}

	// tryAll runs every probe and fails on any that reached the host. A probe
	// that succeeded inside the container without reaching the host is logged:
	// it wrote a file of its own somewhere the host never reads.
	tryAll := func(when string) {
		t.Helper()
		for _, p := range probes {
			out, err := exec.Command("docker", "exec", name, "sh", "-c", p.script).CombinedOutput()
			b, _ := os.ReadFile(filepath.Join(project, p.hostFile))
			reached := strings.Contains(string(b), p.marker)
			switch {
			case reached:
				// The container's own result too, so a marker that matches
				// something the host wrote shows up as a refused write that
				// "reached" the host, not as a silent pass.
				t.Errorf("%s: %s reached the host's %s (succeeded in the container: %v)", when, p.label, p.hostFile, err == nil)
			case err == nil:
				t.Logf("%s: %s succeeded in the container but did not reach the host", when, p.label)
			default:
				t.Logf("%s: %s refused: %s", when, p.label, strings.TrimSpace(string(out)))
			}
		}
	}

	// The cost of the layout: reading works, writing to the repository does not.
	for _, c := range []struct {
		script string
		want   bool
	}{
		{"cd /w && git status --short", true},
		{"cd /w && git log --oneline", true},
		{"cd /w && echo two >> f.txt && git diff --stat", true},
		{"cd /w && git add f.txt", false},
		{"cd /w && git -c user.email=a@b -c user.name=a commit -qam msg", false},
	} {
		out, err := exec.Command("docker", "exec", name, "sh", "-c", c.script).CombinedOutput()
		t.Logf("in the container, %q: ok=%v %s", c.script, err == nil, firstLine(out))
		if (err == nil) != c.want {
			t.Errorf("in the container, %q: ok=%v, want %v", c.script, err == nil, c.want)
		}
	}

	tryAll("before any host write")

	// What breaks the single-file guard, run on the host while the container
	// runs. Each must succeed: the layout is only acceptable if host git keeps
	// working normally beside it.
	// The fixture's branch is whatever init.defaultBranch says on this host.
	branch := strings.TrimSpace(hostGit("rev-parse", "--abbrev-ref", "HEAD"))
	// A change of the host's own, so the commit below does not depend on the
	// container having edited f.txt earlier.
	if err := os.WriteFile(filepath.Join(project, "host.txt"), []byte("host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "rogit.host", "1"},
		{"branch", "rogit-side"},
		{"branch", "-u", branch, "rogit-side"},
		{"add", "host.txt"},
		{"commit", "-q", "-m", "host commit while the container runs"},
	} {
		hostGit(args...)
		t.Logf("host git %s: ok", strings.Join(args, " "))

		// Watched, not probed once: the single-file guard held for about 1.5s
		// after a host rename on a podman machine before it opened.
		start := time.Now()
		for time.Since(start) < roGitWatch {
			out, err := exec.Command("docker", "exec", name, "sh", "-c", "( : >> /w/.git/config ) 2>/dev/null").CombinedOutput()
			if err == nil {
				t.Errorf("%.1fs after host git %s: .git/config opens for write in the container %s",
					time.Since(start).Seconds(), strings.Join(args, " "), out)
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		tryAll("after host git " + strings.Join(args, " "))
	}

	// The container sees the host's new commit through the read-only mount, so
	// the agent's view of history stays current.
	out := engine(t, "exec", name, "sh", "-c", "cd /w && git log --oneline -1")
	if !strings.Contains(out, "host commit while the container runs") {
		t.Errorf("the container does not see the host's commit: %s", out)
	}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return fmt.Sprintf("%.90s", s)
}
