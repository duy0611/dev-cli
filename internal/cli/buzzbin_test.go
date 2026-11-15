package cli

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/buzzcli"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
	"github.com/duy0611/dev-cli/internal/provider/local"
)

const (
	machineAArch64 = 183
	machineX8664   = 62
)

// elfFile writes a 64-byte stand-in for a Linux ELF executable.
func elfFile(t *testing.T, machine uint16) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, "\x7fELF")
	h[4], h[5] = 2, 1
	binary.LittleEndian.PutUint16(h[18:], machine)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "buzz")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setBuzz(t *testing.T, a *app, arm64, amd64 string) {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBuzzBinaries("ws", arm64, amd64); err != nil {
		t.Fatal(err)
	}
}

// buzzTarget resolves c as every command does, on a local workspace "ws".
func buzzTarget(t *testing.T, a *app, c model.Container) *target {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := st.GetWorkspace("ws")
	if err != nil {
		t.Fatal(err)
	}
	c.WorkspaceName = "ws"
	tg, err := a.target(t.Context(), c, ws, &local.Provider{})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	t.Cleanup(tg.release)
	return tg
}

func generatedContainer() model.Container {
	return model.Container{Name: "demo", SourceKind: model.SourceNone, Mode: model.ModeNone,
		GeneratedConfig: `{"name":"demo","mounts":["source=v,target=/x,type=volume"]}`}
}

func projectFile(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, []byte(`{"image":"ubuntu"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuzzMountIsInTheGeneratedConfigForTheMatchingArch(t *testing.T) {
	a, _ := newTestApp(t) // the engine platform is linux/amd64
	seedWorkspace(t, a)
	amd, arm := elfFile(t, machineX8664), elfFile(t, machineAArch64)
	setBuzz(t, a, arm, amd)

	tg := buzzTarget(t, a, generatedContainer())
	if tg.overrideErr != nil {
		t.Fatalf("overrideErr = %v", tg.overrideErr)
	}
	doc := fileText(t, tg.container.ConfigPath)
	want := "source=" + amd + ",target=" + buzzcli.Target + ",type=bind,readonly"
	if !strings.Contains(doc, want) {
		t.Errorf("generated config lacks %q:\n%s", want, doc)
	}
	if strings.Contains(doc, arm) {
		t.Errorf("the arm64 file was mounted into an amd64 container:\n%s", doc)
	}
	if !strings.Contains(doc, "target=/x") {
		t.Errorf("the generated mounts were lost:\n%s", doc)
	}
}

func TestBuzzMountIsInTheOverrideConfigOfAProject(t *testing.T) {
	a, _ := newTestApp(t)
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "linux/arm64")
	seedWorkspace(t, a)
	arm := elfFile(t, machineAArch64)
	setBuzz(t, a, arm, "")

	for _, mode := range []model.Mode{model.ModeUnprotected, model.ModeIsolated} {
		path := projectFile(t)
		tg := buzzTarget(t, a, model.Container{Name: "p", SourceKind: model.SourceFolder,
			Source: filepath.Dir(path), ConfigPath: path, Mode: mode})
		if tg.overrideErr != nil {
			t.Fatalf("%s: overrideErr = %v", mode, tg.overrideErr)
		}
		if tg.container.OverrideConfigPath == "" {
			t.Fatalf("%s: no override config was written", mode)
		}
		want := "source=" + arm + ",target=" + buzzcli.Target + ",type=bind,readonly"
		if doc := fileText(t, tg.container.OverrideConfigPath); !strings.Contains(doc, want) {
			t.Errorf("%s: override config lacks %q:\n%s", mode, want, doc)
		}
	}
}

func TestNoBuzzSettingMeansNoMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	tg := buzzTarget(t, a, generatedContainer())
	if doc := fileText(t, tg.container.ConfigPath); strings.Contains(doc, buzzcli.Target) {
		t.Errorf("a mount appeared with no setting:\n%s", doc)
	}

	// A file for the other architecture only: not an error, not a mount.
	setBuzz(t, a, elfFile(t, machineAArch64), "")
	tg = buzzTarget(t, a, generatedContainer())
	if tg.overrideErr != nil {
		t.Errorf("a file for another architecture was an error: %v", tg.overrideErr)
	}
	if doc := fileText(t, tg.container.ConfigPath); strings.Contains(doc, buzzcli.Target) {
		t.Errorf("a mount appeared for the wrong architecture:\n%s", doc)
	}
}

func TestBuzzFileThatIsMissingOrWrongOnlyFailsWhereAContainerIsCreated(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	wrong := elfFile(t, machineAArch64)
	missing := filepath.Join(t.TempDir(), "gone")

	for name, file := range map[string]string{"wrong architecture": wrong, "missing": missing} {
		setBuzz(t, a, "", file)
		tg := buzzTarget(t, a, generatedContainer())
		if err := tg.requireOverride(a); err != nil {
			t.Errorf("%s: exec, shell and start-agent must proceed: %v", name, err)
		}
		if doc := fileText(t, tg.container.ConfigPath); strings.Contains(doc, buzzcli.Target) {
			t.Errorf("%s: an unusable file was mounted:\n%s", name, doc)
		}
		// Create and rebuild.
		err := tg.gitGuard.requireBuzz()
		if err == nil || !strings.Contains(err.Error(), file) {
			t.Errorf("%s: err = %v, want a refusal naming %s", name, err, file)
		}
		// Start creates only when the engine has no container.
		c := model.Container{Name: "api", WorkspaceName: "ws"}
		for status, wantErr := range map[model.Status]bool{model.StatusAbsent: true, model.StatusStopped: false} {
			fake = &fakeProvider{status: status}
			err := requireBuzzIfCreating(t.Context(), fake, c, tg.gitGuard)
			if (err != nil) != wantErr {
				t.Errorf("%s: start of a %s container: err = %v", name, status, err)
			}
		}
	}
}

func TestBuzzMountIsLeftOutOfTheConfigDigest(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	setBuzz(t, a, "", elfFile(t, machineX8664))

	plan, err := a.planGitGuard(t.Context(), model.Container{WorkspaceName: "ws", Name: "demo"}, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	var in bool
	for _, tgt := range plan.ownTargets() {
		in = in || tgt == buzzcli.Target
	}
	if !in {
		t.Errorf("ownTargets = %v: changing the setting would read as project drift", plan.ownTargets())
	}
}

func TestWorkspaceBuzzStoresResolvedPathsAndSaysWhenItApplies(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	arm, amd := elfFile(t, machineAArch64), elfFile(t, machineX8664)

	if err := runWorkspaceBuzz(a, "ws", &arm, &amd); err != nil {
		t.Fatal(err)
	}
	st, _ := a.store()
	ws, _ := st.GetWorkspace("ws")
	if ws.BuzzArm64 != arm || ws.BuzzAmd64 != amd {
		t.Errorf("stored %q and %q, want %q and %q", ws.BuzzArm64, ws.BuzzAmd64, arm, amd)
	}
	if !strings.Contains(out.String(), "created or rebuilt") {
		t.Errorf("the output does not say when it applies:\n%s", out)
	}

	clear := ""
	if err := runWorkspaceBuzz(a, "ws", nil, &clear); err != nil {
		t.Fatal(err)
	}
	ws, _ = st.GetWorkspace("ws")
	if ws.BuzzArm64 != arm || ws.BuzzAmd64 != "" {
		t.Errorf("after clearing amd64: %q and %q", ws.BuzzArm64, ws.BuzzAmd64)
	}
}

func TestWorkspaceBuzzRefusesAFileThatIsWrongForItsArchitecture(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	arm := elfFile(t, machineAArch64)

	err := runWorkspaceBuzz(a, "ws", nil, &arm) // an arm64 file named for amd64
	if err == nil || !strings.Contains(err.Error(), arm) {
		t.Fatalf("err = %v, want a refusal naming %s", err, arm)
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestWorkspaceBuzzIsRefusedOnKubernetes(t *testing.T) {
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "k", "k8s", k8s.Config{Registry: "ghcr.io/me"}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "kws", "k", false); err != nil {
		t.Fatal(err)
	}
	amd := elfFile(t, machineX8664)

	err := runWorkspaceBuzz(a, "kws", nil, &amd)
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err = %v, want a refusal that says the binary must be in the image", err)
	}
	st, _ := a.store()
	if ws, _ := st.GetWorkspace("kws"); ws.BuzzAmd64 != "" {
		t.Errorf("stored %q on a k8s workspace", ws.BuzzAmd64)
	}
}

func TestLaunchAgentRefusesToCreateAContainerWithoutItsBuzzMount(t *testing.T) {
	for status, wantRefusal := range map[model.Status]bool{model.StatusAbsent: true, model.StatusStopped: false} {
		a := seedFake(t, status)
		tg, err := a.resolve(t.Context(), "", "api")
		if err != nil {
			t.Fatal(err)
		}
		tg.gitGuard.buzzErr = errors.New("buzz file /nowhere/buzz is missing")
		err = a.launchAgent(t.Context(), tg, agent.Agent{ID: "claude"}, []string{"fake"}, launchOpts{event: "acp", binary: "fake"})
		tg.release()
		refused := err != nil && strings.Contains(err.Error(), "/nowhere/buzz")
		if refused != wantRefusal {
			t.Errorf("%s: err = %v, want refusal %v", status, err, wantRefusal)
		}
		if upped := slices.Contains(fake.log, "up"); upped == wantRefusal {
			t.Errorf("%s: up called = %v, log %v", status, upped, fake.log)
		}
	}
}
