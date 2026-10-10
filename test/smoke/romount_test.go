//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture's image, so the pull is already cached by the other tests.
const roMountImage = "docker.io/library/alpine:3.20"

// roMountWatch is how long each case watches the mount after the host save.
// On a podman machine masked/rename turned writable 1.5s after the rename and
// the mount left mountinfo at 2.1s, so ten seconds outlasts both with room.
const roMountWatch = 10 * time.Second

// roMountProbe reports, from inside the container, whether the file's mount is
// listed, what its first line reads, and whether a write to it succeeds. The
// content is read before the write, so a write that lands does not change it.
const roMountProbe = `if grep -q ' /target/file ' /proc/self/mountinfo; then echo mounted=yes; else echo mounted=no; fi
printf 'content=%s\n' "$(head -n1 /target/file 2>&1)"
if (echo probe >> /target/file) 2>/dev/null; then echo writable=yes; else echo writable=no; fi`

// TestSmokeReadOnlyFileMount characterises the engine, not dev: what a
// read-only single-file bind mount does when the host replaces the file.
//
// Two layouts, each under both ways a host saves a file. "plain" is the file
// mounted on its own. "masked" is the file mounted read-only over a writable
// mount of its own directory — the git guard's shape. Every case is asserted
// to keep the container from writing the host's file, except masked/rename:
// that is the case TestSmokeGitGuardSurvivesAHostConfigWrite shows opening,
// and it is logged here rather than asserted because it is kernel behaviour
// dev cannot change, so a failure would say nothing new.
func TestSmokeReadOnlyFileMount(t *testing.T) {
	requireBinaries(t, "docker")
	for _, layout := range []string{"plain", "masked"} {
		for _, save := range []string{"in-place", "rename"} {
			t.Run(layout+"/"+save, func(t *testing.T) {
				roMountCase(t, layout, save)
			})
		}
	}
}

func roMountCase(t *testing.T, layout, save string) {
	// Physical path: the engine resolves the source inside its VM, where
	// macOS's /var -> /private/var symlink does not exist (invariant 2).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "dev-smoke-romount-" + layout + "-" + save
	_ = exec.Command("docker", "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	args := []string{"run", "-d", "--name", name}
	if layout == "masked" {
		args = append(args, "--mount", "type=bind,source="+dir+",target=/target")
	}
	args = append(args,
		"--mount", "type=bind,source="+file+",target=/target/file,readonly",
		roMountImage, "sleep", "600")
	engine(t, args...)

	probe := func() (mounted, writable bool, content string) {
		t.Helper()
		out := engine(t, "exec", name, "sh", "-c", roMountProbe)
		for _, line := range strings.Split(out, "\n") {
			switch {
			case line == "mounted=yes":
				mounted = true
			case line == "writable=yes":
				writable = true
			case strings.HasPrefix(line, "content="):
				content = strings.TrimPrefix(line, "content=")
			}
		}
		return mounted, writable, content
	}

	if mounted, writable, content := probe(); !mounted || writable || content != "v1" {
		t.Fatalf("before the host save: mounted=%v writable=%v content=%q; the mount never took hold",
			mounted, writable, content)
	}

	switch save {
	case "in-place":
		// os.WriteFile truncates an existing file, so the inode is the same —
		// what `echo > file` does.
		if err := os.WriteFile(file, []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "rename":
		// What git, `sed -i` and most editors' safe save do: a new inode
		// renamed over the path the mount was made on.
		tmp := file + ".lock"
		if err := os.WriteFile(tmp, []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, file); err != nil {
			t.Fatal(err)
		}
	}

	// Watched for a while rather than read once. On a podman machine a read
	// straight after the save finds masked/rename still protected; it turns
	// writable about 1.5s later while the mount is still listed, and the mount
	// leaves mountinfo after that — so a single early read reports a guard
	// that is about to fail, and mountinfo trails the real state.
	type state struct {
		mounted, writable bool
		seen              string
	}
	describe := func(content string) string {
		switch content {
		case "v2":
			return "v2 (the host's change)"
		case "v1":
			return "v1 (stale)"
		}
		return "unreadable: " + content
	}
	start := time.Now()
	var last state
	everWritable := false
	for i := 0; time.Since(start) < roMountWatch; i++ {
		mounted, writable, content := probe()
		now := state{mounted, writable, describe(content)}
		everWritable = everWritable || writable
		if i == 0 || now != last {
			t.Logf("%5.1fs after the %s save: mount listed=%v, writable=%v, container reads %s",
				time.Since(start).Seconds(), save, now.mounted, now.writable, now.seen)
			last = now
		}
		time.Sleep(500 * time.Millisecond)
	}

	host, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// The probe appends on every write that succeeds, so any line of it on the
	// host means a write got through at some point in the watch.
	reached := strings.Contains(string(host), "probe")
	t.Logf("host file written=%v", reached)

	if layout == "masked" && save == "rename" {
		return
	}
	if everWritable || reached {
		t.Errorf("a read-only mount let the container write after the %s save: writable at some point=%v, host file written=%v",
			save, everWritable, reached)
	}
}

// engine runs the engine CLI and returns its combined output, failing the test
// if it fails.
func engine(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
