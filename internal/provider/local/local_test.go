package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// callSeparator marks the end of one recorded call in a stub's log.
const callSeparator = "--- call ---"

// fakeBin writes an executable onto a temp PATH that records its own argv, one
// argument per line, and prints stdout. Driving the real binaries is what the
// smoke test is for; here the question is only what got called.
type fakeBin struct {
	dir string
}

func newFakePath(t *testing.T) *fakeBin {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeBin{dir: dir}
}

// install writes a stub for name that prints stdout and exits with code.
func (f *fakeBin) install(t *testing.T, name, stdout string, code int) {
	t.Helper()
	log := filepath.Join(f.dir, name+".argv")
	// Appended, with a marker between calls: one dev command can run the same
	// binary more than once, and truncating would hide all but the last.
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + shellQuote(log) + "; done\n" +
		"printf '%s\\n' " + shellQuote(callSeparator) + " >> " + shellQuote(log) + "\n"
	if stdout != "" {
		script += "printf '%s' " + shellQuote(stdout) + "\n"
	}
	script += "exit " + strconv.Itoa(code) + "\n"

	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake %s: %v", name, err)
	}
}

// argvAll returns every call the named stub recorded, in order, one slice per
// call. Up runs a second command after the CLI returns, so the last call is not
// always the one under test.
func (f *fakeBin) argvAll(t *testing.T, name string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name+".argv"))
	if err != nil {
		t.Fatalf("fake %s was never called: %v", name, err)
	}
	var out [][]string
	var cur []string
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == callSeparator:
			out, cur = append(out, cur), nil
		case line != "":
			cur = append(cur, line)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// argv returns the arguments of the stub's first call, which is the one a test
// set out to make. Up runs a chown afterwards for a state-persisting container,
// and that must not displace what is being asserted on.
func (f *fakeBin) argv(t *testing.T, name string) []string {
	t.Helper()
	calls := f.argvAll(t, name)
	if len(calls) == 0 {
		t.Fatalf("fake %s was never called", name)
	}
	return calls[0]
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func testContainer() model.Container {
	return model.Container{
		Name:          "demo",
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        "/projects/demo",
		ConfigPath:    "/projects/demo/.devcontainer/devcontainer.json",
	}
}

// calledWith reports whether any of the stub's calls holds want as a
// consecutive run. For a command that runs a binary more than once — Remove
// asks docker for the container id before removing any volume.
func (f *fakeBin) calledWith(t *testing.T, name string, want []string) bool {
	t.Helper()
	for _, argv := range f.argvAll(t, name) {
		if contains(argv, want) {
			return true
		}
	}
	return false
}

// contains reports whether argv holds want as a consecutive run.
func contains(argv, want []string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		ok := true
		for j := range want {
			if argv[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestUpPassesWorkspaceFolderAndIDLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Up(context.Background(), testContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if argv[0] != "up" {
		t.Errorf("argv[0] = %q, want %q", argv[0], "up")
	}
	for _, want := range [][]string{
		{"--workspace-folder", "/projects/demo"},
		{"--id-label", "dev.workspace=ws"},
		{"--id-label", "dev.container=demo"},
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
	if contains(argv, []string{"--remove-existing-container"}) {
		t.Error("plain Up asked for a recreate")
	}
}

// A folderless container still hands the CLI a workspace folder — the
// materialised temporary directory holding its generated config — and the
// same id-labels as a folder container. The provider does not need to know
// there is no project behind it; only the generated document's own
// workspaceMount differs, and that is dcgen's concern, not Up's.
func TestUpPassesWorkspaceFolderAndIDLabelsForAFolderlessContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := folderlessContainer()
	p := &Provider{}
	if err := p.Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	for _, want := range [][]string{
		{"--workspace-folder", c.Source},
		{"--id-label", "dev.workspace=ws"},
		{"--id-label", "dev.container=scratch"},
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
}

// The invariant most likely to break silently: a call that spells the labels
// differently looks up a container that does not exist, and the CLI creates a
// second one rather than failing.
func TestUpAndExecAgreeOnIDLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)
	c := testContainer()
	p := &Provider{}

	if err := p.Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	upLabels := extractLabels(f.argv(t, devcontainerBin))

	if err := p.Exec(context.Background(), c, []string{"true"}, provider.ExecOpts{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	execLabels := extractLabels(f.argv(t, devcontainerBin))

	if len(upLabels) != 2 {
		t.Fatalf("up passed %d id labels, want 2: %v", len(upLabels), upLabels)
	}
	if strings.Join(upLabels, ",") != strings.Join(execLabels, ",") {
		t.Errorf("id labels differ between up (%v) and exec (%v)", upLabels, execLabels)
	}
}

func extractLabels(argv []string) []string {
	var out []string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--id-label" {
			out = append(out, argv[i+1])
		}
	}
	return out
}

func TestRebuildAsksForRecreate(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Rebuild(context.Background(), testContainer(), nil, true); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if argv[0] != "up" {
		t.Errorf("Rebuild called %q; the CLI has no rebuild subcommand, it is flags on up", argv[0])
	}
	for _, want := range []string{"--remove-existing-container", "--build-no-cache"} {
		if !contains(argv, []string{want}) {
			t.Errorf("argv %v is missing %s", argv, want)
		}
	}
}

func TestRebuildWithoutNoCache(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Rebuild(context.Background(), testContainer(), nil, false); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--build-no-cache"}) {
		t.Error("Rebuild(noCache=false) still passed --build-no-cache")
	}
}

func TestExecPassesEnvAndCommand(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	err := p.Exec(context.Background(), testContainer(),
		[]string{"printenv", "GREETING"},
		provider.ExecOpts{
			Env:    []provider.EnvVar{{Key: "GREETING", Value: "hello"}},
			Stdout: io.Discard,
		})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--remote-env", "GREETING=hello"}) {
		t.Errorf("argv %v is missing the remote env", argv)
	}
	// The command has to come last, after every flag, or the CLI reads it as
	// an argument to whichever flag precedes it.
	if got := argv[len(argv)-2:]; got[0] != "printenv" || got[1] != "GREETING" {
		t.Errorf("argv ends with %v, want the command last", got)
	}
}

func TestStatus(t *testing.T) {
	cases := map[string]model.Status{
		"running\n": model.StatusRunning,
		"exited\n":  model.StatusStopped,
		"created\n": model.StatusStopped,
		"":          model.StatusAbsent,
	}
	for out, want := range cases {
		f := newFakePath(t)
		f.install(t, dockerBin, out, 0)

		got, err := (&Provider{}).Status(context.Background(), testContainer())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != want {
			t.Errorf("docker said %q: Status = %q, want %q", out, got, want)
		}
	}
}

func TestStatusFiltersOnBothLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "running\n", 0)

	if _, err := (&Provider{}).Status(context.Background(), testContainer()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	argv := f.argv(t, dockerBin)
	for _, want := range [][]string{
		{"--filter", "label=dev.workspace=ws"},
		{"--filter", "label=dev.container=demo"},
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
	// -a, or a stopped container reads as absent and `start` creates a second.
	if !contains(argv, []string{"-a"}) {
		t.Errorf("argv %v does not include stopped containers", argv)
	}
}

func TestStopAndRemoveAreNoOpsWhenAbsent(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "", 0) // no container id

	p := &Provider{}
	c := testContainer()
	if err := p.Stop(context.Background(), c); err != nil {
		t.Errorf("Stop on an absent container: %v", err)
	}
	if err := p.Remove(context.Background(), c); err != nil {
		t.Errorf("Remove on an absent container: %v", err)
	}
}

func TestRemoveForcesWhenPresent(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// docker is asked for the container id first, so the removal is a later call.
	if !f.calledWith(t, dockerBin, []string{"rm", "-f", "abc123"}) {
		t.Errorf("docker calls %v did not force-remove the container", f.argvAll(t, dockerBin))
	}
}

func TestMissingBinaryIsReportedByName(t *testing.T) {
	// An empty PATH: neither binary is reachable.
	t.Setenv("PATH", t.TempDir())

	err := (&Provider{}).Up(context.Background(), testContainer(), nil)
	if err == nil {
		t.Fatal("Up with no devcontainer CLI succeeded")
	}
	if !strings.Contains(err.Error(), devcontainerBin) {
		t.Errorf("error %q does not name the missing binary", err)
	}
}

// The config path is passed explicitly rather than left to the CLI's own
// lookup, because a generated configuration lives in a temporary directory the
// CLI would never find, and because being explicit costs nothing for a
// project-owned one.
func TestUpPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := testContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("up argv missing --config %s: %v", c.ConfigPath, argv)
	}
}

func TestExecPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := testContainer()
	err := (&Provider{}).Exec(context.Background(), c, []string{"true"},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("exec argv missing --config %s: %v", c.ConfigPath, argv)
	}
}

func folderlessContainer() model.Container {
	return model.Container{
		Name:          "scratch",
		WorkspaceName: "ws",
		SourceKind:    model.SourceNone,
		Source:        "/tmp/dev-config-x",
		ConfigPath:    "/tmp/dev-config-x/.devcontainer/devcontainer.json",
	}
}

// docker rm does not touch a named volume: it is external to the container, so
// without this it survives every remove and accumulates until a disk fills.
func TestRemoveDeletesTheVolumeOfAFolderlessContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), folderlessContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !f.calledWith(t, dockerBin, []string{"volume", "rm", "--force", "dev-ws-scratch"}) {
		t.Errorf("Remove did not delete the volume; docker calls were %v", f.argvAll(t, dockerBin))
	}
}

// A folder container's work is on the host and there is no volume to remove.
// Asking docker to remove one would fail on every remove.
func TestRemoveDoesNotDeleteAVolumeForAFolderContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if contains(f.argv(t, dockerBin), []string{"volume"}) {
		t.Errorf("Remove touched a volume for a folder container: %v", f.argv(t, dockerBin))
	}
}

// stateContainer is a folder container that persists its agents' state.
func stateContainer() model.Container {
	c := testContainer()
	c.PersistState = true
	return c
}

// overrideContainer is a project-owned, state-persisting container whose merged
// configuration materialise has already written.
func overrideContainer() model.Container {
	c := stateContainer()
	c.ConfigPath = "/project/.devcontainer/devcontainer.json"
	c.OverrideConfigPath = "/tmp/dev-override-x/devcontainer.json"
	return c
}

// The merged document reaches the CLI, and --config still names the project's
// own file: both are accepted together, and --config is what keeps a relative
// "dockerfile" anchored to the project rather than to the temporary directory.
func TestUpPassesTheOverrideConfig(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := overrideContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--override-config", c.OverrideConfigPath}) {
		t.Errorf("argv %v is missing the override config", argv)
	}
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("argv %v dropped the project's own --config", argv)
	}
}

// up and exec must carry the *same* merged document. This is the same shape of
// mistake as the id labels: two commands that disagree reach two different
// containers, or one reaches a container with no state volume, and nothing
// reports an error. --override-config exists on both, unlike --mount, which is
// what lets the two paths finally agree.
func TestUpAndExecAgreeOnTheOverrideConfig(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := overrideContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	upArgs := f.argv(t, devcontainerBin)
	execArgs := (&Provider{}).execArgs(c, []string{"true"})

	want := []string{"--override-config", c.OverrideConfigPath}
	if !contains(upArgs, want) {
		t.Errorf("up argv %v is missing %v", upArgs, want)
	}
	if !contains(execArgs, want) {
		t.Errorf("exec argv %v is missing %v", execArgs, want)
	}
}

// --mount is gone. It existed on up and not on exec, which is the asymmetry the
// override removes; leaving it would also make docker refuse the run with
// "duplicate mount destination" now that the merged document names the volume.
func TestUpNeverPassesMount(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	if err := (&Provider{}).Up(context.Background(), overrideContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--mount"}) {
		t.Errorf("up still passes --mount: %v", f.argv(t, devcontainerBin))
	}
}

// A generated document already names the volume in its own mounts, so
// materialise builds no override for it and neither flag appears.
func TestUpPassesNoOverrideForAGeneratedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := stateContainer()
	c.GeneratedConfig = `{"name":"demo","mounts":["source=dev-ws-demo-state,target=/var/dev-state,type=volume"]}`
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	for _, flag := range []string{"--override-config", "--mount"} {
		if contains(argv, []string{flag}) {
			t.Errorf("up passed %s for a generated container: %v", flag, argv)
		}
	}
}

// Nothing to merge, nothing to pass.
func TestUpPassesNoOverrideWhenStateIsOff(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	if err := (&Provider{}).Up(context.Background(), testContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--override-config"}) {
		t.Errorf("Up passed an override for a container that persists no state: %v",
			f.argv(t, devcontainerBin))
	}
}

func TestExecNeverPassesMount(t *testing.T) {
	argv := (&Provider{}).execArgs(overrideContainer(), []string{"true"})
	if contains(argv, []string{"--mount"}) {
		t.Errorf("exec argv carries --mount: %v", argv)
	}
}

// The volume outlives the container, so nothing else would ever remove it.
func TestRemoveDeletesTheStateVolume(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), stateContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want := []string{"volume", "rm", "--force", "dev-ws-demo-state"}
	if !f.calledWith(t, dockerBin, want) {
		t.Errorf("Remove did not delete the state volume; docker calls were %v",
			f.argvAll(t, dockerBin))
	}
}

// A project-owned container has no postCreateCommand dev may add to, so up
// claims the directory itself. Guarded by a writability test: it runs on every
// up, and a recursive chown over an agent's accumulated history is not worth
// repeating once the volume is already the remote user's.
func TestUpClaimsTheStateDirForAProjectOwnedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	if err := (&Provider{}).Up(context.Background(), stateContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	calls := f.argvAll(t, devcontainerBin)
	if len(calls) != 2 {
		t.Fatalf("devcontainer was called %d times, want up then the chown: %v", len(calls), calls)
	}
	last := strings.Join(calls[1], " ")
	for _, want := range []string{"exec", "chown", "/var/dev-state", "-w"} {
		if !strings.Contains(last, want) {
			t.Errorf("the follow-up call %q is missing %q", last, want)
		}
	}
}

// A generated configuration chowns the directory in its own postCreateCommand,
// so doing it here as well would be a second answer to the same question.
func TestUpDoesNotClaimTheStateDirForAGeneratedContainer(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := stateContainer()
	c.GeneratedConfig = `{"name":"demo"}`
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if calls := f.argvAll(t, devcontainerBin); len(calls) != 1 {
		t.Errorf("devcontainer was called %d times, want only up: %v", len(calls), calls)
	}
}

// The overlay is local-only. k8s builds its own pod spec and ignores a
// document's mounts, so it must not be handed a merged document — and a
// project file that does not parse must not block a k8s command.
func TestLocalProviderOverridesConfig(t *testing.T) {
	if _, ok := any(&Provider{}).(provider.ConfigOverrider); !ok {
		t.Error("local provider does not implement provider.ConfigOverrider")
	}
}
