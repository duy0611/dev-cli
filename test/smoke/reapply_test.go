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

// SPIKE — throwaway. It answers one question before any design depends on
// it: can a read-only file mount that a host rename has broken be put back in
// a running container, from outside the agent's reach? Nothing in dev calls
// this; the answer decides whether re-applying the git guard's mount at the
// start of every command is a design worth writing.

// reapplyScript bind-mounts whatever file the path names now onto itself and
// makes that read-only. A remount with only "ro" clears nosuid and nodev,
// which a user namespace refuses when the mount underneath carries them
// locked — rootless podman — so the stricter sets are tried in turn and the
// one that took is reported.
const reapplyScript = `mount -o bind /target/file /target/file || exit 1
for o in ro ro,nosuid,nodev ro,nosuid,nodev,noexec; do
  if mount -o remount,bind,$o /target/file 2>/dev/null; then echo opts=$o; exit 0; fi
done
echo 'bound, but the remount to read-only failed' >&2
exit 1`

// mountStateScript reads the mount's state without changing the file. A probe
// that wrote would put its own line into the host file every time the guard
// was down, so it opens the file for appending and writes nothing: open(2)
// fails with EROFS on a read-only mount, and an empty append changes neither
// the content nor the mtime.
//
// Not `test -w`. busybox's test decides from the owner and mode bits and, as
// root, calls every file writable without asking the kernel — so it reports a
// read-only mount as writable, which is what the first run of this spike hit.
const mountStateScript = `echo entries=$(grep -c ' /target/file ' /proc/self/mountinfo)
printf 'content=%s\n' "$(head -n1 /target/file 2>&1)"
if ( : >> /target/file ) 2>/dev/null; then echo openw=yes; else echo openw=no; fi`

type mountState struct {
	entries  string
	content  string
	writable bool
}

func (s mountState) String() string {
	return fmt.Sprintf("mountinfo entries=%s, opens for write=%v, reads %q", s.entries, s.writable, s.content)
}

func readMountState(t *testing.T, name string) mountState {
	t.Helper()
	var s mountState
	for _, line := range strings.Split(engine(t, "exec", name, "sh", "-c", mountStateScript), "\n") {
		switch {
		case strings.HasPrefix(line, "entries="):
			s.entries = strings.TrimPrefix(line, "entries=")
		case strings.HasPrefix(line, "content="):
			s.content = strings.TrimPrefix(line, "content=")
		case line == "openw=yes":
			s.writable = true
		}
	}
	return s
}

// watchMount polls the mount for up to d, logging every change, and stops
// early once stop holds. It reports the last state, whether stop held, and
// whether the file was writable at any point.
func watchMount(t *testing.T, name, label string, d time.Duration, stop func(mountState) bool) (last mountState, matched, everWritable bool) {
	t.Helper()
	start := time.Now()
	for i := 0; time.Since(start) < d; i++ {
		s := readMountState(t, name)
		everWritable = everWritable || s.writable
		if i == 0 || s != last {
			t.Logf("%5.2fs %s: %s", time.Since(start).Seconds(), label, s)
			last = s
		}
		if stop != nil && stop(s) {
			return last, true, everWritable
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last, false, everWritable
}

// reapplyReadOnly tries each way of running the mount from outside the agent,
// in order, and returns the first that worked.
//
// The exec is the simple one, but an exec joins the container's seccomp and
// AppArmor confinement, which can refuse mount(2) whatever capabilities the
// exec is given. The helper is a separate privileged container that enters the
// target's mount namespace and keeps its own, unconfined, profile.
func reapplyReadOnly(t *testing.T, name string) (string, error) {
	t.Helper()
	methods := []struct {
		label string
		args  []string
	}{
		{"exec --privileged", []string{"exec", "--privileged", "-u", "0", name, "sh", "-c", reapplyScript}},
		{"privileged helper + nsenter", []string{"run", "--rm", "--privileged", "--pid=container:" + name,
			roMountImage, "nsenter", "-t", "1", "-m", "--", "sh", "-c", reapplyScript}},
	}
	var failures []string
	for _, m := range methods {
		out, err := exec.Command("docker", m.args...).CombinedOutput()
		t.Logf("re-apply via %s: err=%v\n%s", m.label, err, strings.TrimSpace(string(out)))
		if err == nil {
			return m.label, nil
		}
		failures = append(failures, m.label)
	}
	return "", fmt.Errorf("every method failed: %s", strings.Join(failures, ", "))
}

// hostRenameSave replaces file the way git and most editors save: a new inode
// renamed over the path the mount was made on.
func hostRenameSave(t *testing.T, file, content string) {
	t.Helper()
	tmp := file + ".lock"
	if err := os.WriteFile(tmp, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
}

func TestSmokeReapplyReadOnlyMount(t *testing.T) {
	requireBinaries(t, "docker")

	// Physical path: the engine resolves the source inside its VM (invariant 2).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const name = "dev-smoke-reapply"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// The git guard's shape: the file read-only over a writable mount of its
	// own directory.
	engine(t, "run", "-d", "--name", name,
		"--mount", "type=bind,source="+dir+",target=/target",
		"--mount", "type=bind,source="+file+",target=/target/file,readonly",
		roMountImage, "sleep", "600")

	if s := readMountState(t, name); s.writable || s.content != "v1" {
		t.Fatalf("before any host save: %s; the mount never took hold", s)
	}

	// Two rounds: a re-apply that works once but stacks badly, or that the
	// next host save cannot break and the next re-apply cannot fix, is not a
	// mechanism dev can run on every command.
	for round := 1; round <= 2; round++ {
		want := fmt.Sprintf("v%d", round+1)
		label := fmt.Sprintf("round %d", round)

		hostRenameSave(t, file, want)
		if _, broke, _ := watchMount(t, name, label+", after the host save", 10*time.Second,
			func(s mountState) bool { return s.writable }); !broke {
			t.Fatalf("%s: the host rename never made the file writable; nothing to re-apply", label)
		}

		method, err := reapplyReadOnly(t, name)
		if err != nil {
			t.Fatalf("%s: %v — re-applying is not possible on this engine", label, err)
		}
		t.Logf("%s: re-applied via %s", label, method)

		// Watched rather than read once: on a podman machine the stale mount
		// leaves mountinfo about 2.1s after the host rename, and a mount
		// stacked on it may go with it.
		last, _, everWritable := watchMount(t, name, label+", after the re-apply", 5*time.Second, nil)
		if everWritable {
			t.Errorf("%s: writable again within 5s of the re-apply", label)
		}
		if last.content != want {
			t.Errorf("%s: the container reads %q after the re-apply, want the host's current %q", label, last.content, want)
		}

		// The empty append is what dev would rely on, so a real write confirms
		// it.
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		out := engine(t, "exec", name, "sh", "-c",
			"if (echo probe >> /target/file) 2>/dev/null; then echo wrote; else echo refused; fi")
		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out) != "refused" || string(after) != string(before) {
			t.Errorf("%s: a write from the container got through after the re-apply: %s", label, strings.TrimSpace(out))
		}

		// The agent runs as the container's root without CAP_SYS_ADMIN. If it
		// could undo the mount, re-applying would protect against nothing.
		for _, undo := range []string{"umount /target/file", "mount -o remount,bind,rw /target/file"} {
			out, err := exec.Command("docker", "exec", name, "sh", "-c", undo).CombinedOutput()
			if err == nil {
				t.Errorf("%s: the unprivileged container ran %q", label, undo)
			} else {
				t.Logf("%s: unprivileged %q refused: %s", label, undo, strings.TrimSpace(string(out)))
			}
		}
	}
}
