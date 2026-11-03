package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/guard"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// copyEngine is a stub docker and devcontainer on a temporary PATH. The
// container named "web" exists and is the only one the engine reports, so the
// new container always looks absent until a test says otherwise. Behaviour is
// switched by the environment the scripts read.
type copyEngine struct {
	calls  string
	merged string
}

func stubCopyEngine(t *testing.T, merged string) *copyEngine {
	t.Helper()
	dir := t.TempDir()
	e := &copyEngine{calls: filepath.Join(dir, "calls"), merged: filepath.Join(dir, "merged.json")}
	writeFile(t, e.merged, `{"mergedConfiguration":`+merged+`}`)
	tagged := filepath.Join(dir, "tagged")
	writeFile(t, filepath.Join(dir, "devcontainer"), "#!/bin/sh\nprintf 'devcontainer %s\\n' \"$*\" >> '"+e.calls+"'\n"+
		"case \"$1\" in read-configuration) cat '"+e.merged+"';; esac\n"+
		"[ \"$1\" = up ] && [ -n \"$STUB_FAIL_UP\" ] && { echo boom >&2; exit 1; }\nexit 0\n")
	writeFile(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> '"+e.calls+"'\n"+
		"case \"$*\" in\n"+
		"  *'dev.container=web'*'{{.State}}'*|*'{{.State}}'*'dev.container=web'*) [ -n \"$STUB_RUNNING\" ] && echo running || echo exited; exit 0;;\n"+
		"  ps*'dev.container=web'*) [ -z \"$STUB_NO_CONTAINER\" ] && echo abc123; exit 0;;\n"+
		"  'inspect --type container'*) echo sha256:srcimage; exit 0;;\n"+
		"  'image inspect --format {{.Id}} sha256:srcimage') [ -z \"$STUB_NO_IMAGE\" ]; exit $?;;\n"+
		"  'image inspect'*) [ -e '"+tagged+"' ] && echo sha256:srcimage; exit 0;;\n"+
		"  tag*) : > '"+tagged+"'; exit 0;;\n"+
		"  'volume inspect'*) [ -z \"$STUB_NO_VOLUME\" ]; exit $?;;\n"+
		"  run*) [ -n \"$STUB_FAIL_RUN\" ] && { echo boom >&2; exit 1; }; exit 0;;\n"+
		"esac\nexit 0\n")
	for _, n := range []string{"devcontainer", "docker"} {
		if err := os.Chmod(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return e
}

func (e *copyEngine) log(t *testing.T) string { return callsIn(t, e.calls) }

// index is the position of the first log line containing s, or -1.
func (e *copyEngine) index(t *testing.T, s string) int {
	for i, l := range strings.Split(e.log(t), "\n") {
		if strings.Contains(l, s) {
			return i
		}
	}
	return -1
}

// copySource is a generated, folderless source that persists state.
func copySource(t *testing.T, a *app) {
	t.Helper()
	createSource(t, a, "web", createOpts{tools: []string{"go"}})
}

func copyCreate(t *testing.T, a *app, name string, opts createOpts) error {
	t.Helper()
	opts.from, opts.noFolder = "web", true
	return runContainerCreate(t.Context(), a, "", name, "", opts)
}

func assertNoRow(t *testing.T, a *app, name string) {
	t.Helper()
	st, _ := a.store()
	if _, err := st.GetContainer("ws", name); err == nil {
		t.Errorf("container %s was written", name)
	}
}

func assertUsage(t *testing.T, err error, contains ...string) {
	t.Helper()
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitUsage)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("error %q does not mention %q", err, c)
		}
	}
}

func TestCopyRefusesBadLists(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)

	for label, list := range map[string][]string{
		"empty":        {""},
		"unknown":      {"image", "volume"},
		"trailing gap": {"image", ""},
	} {
		t.Run(label, func(t *testing.T) {
			assertUsage(t, copyCreate(t, a, "feat", createOpts{copy: list}), "--copy")
			assertNoRow(t, a, "feat")
		})
	}
	if strings.Contains(e.log(t), "docker tag") {
		t.Error("a refused list touched the engine")
	}
}

func TestCopyNeedsFrom(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	err := runContainerCreate(t.Context(), a, "", "feat", "", createOpts{noFolder: true, generate: true, copy: []string{"state"}})
	assertUsage(t, err, "--from")
	assertNoRow(t, a, "feat")
}

func TestCopyAcceptsDuplicates(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state", "state"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
}

// insertRow writes a row directly, for sources a create would not make.
func insertRow(t *testing.T, a *app, c model.Container) {
	t.Helper()
	st, _ := a.store()
	c.WorkspaceName = "ws"
	if err := st.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
}

func TestCopyRefusesAnIsolatedSource(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceFolder, Source: "/x",
		GeneratedConfig: "{}", Mode: model.ModeIsolated, PersistState: true})

	err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}})
	assertUsage(t, err, "isolated")
	assertNoRow(t, a, "feat")
}

func TestCopyRefusesAnIsolatedNewContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceFolder, Source: "/x",
		ConfigPath: "/x/.devcontainer/devcontainer.json", Mode: model.ModeGuarded, PersistState: true})

	err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"state"}, mode: model.ModeIsolated})
	assertUsage(t, err, "isolated")
	assertNoRow(t, a, "feat")
}

func TestCopyAllowsLegacyAndUnprotectedSources(t *testing.T) {
	for _, mode := range []model.Mode{model.ModeLegacy, model.ModeUnprotected, model.ModeGuarded, model.ModeNone} {
		t.Run(string(mode), func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			stubCopyEngine(t, `{}`)
			insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceNone,
				GeneratedConfig: "{}", Mode: mode, PersistState: true})
			// "{}" has no tools; ToolsOf reads it as an empty list.
			if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, noStart: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCopyRefusesK8s(t *testing.T) {
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "ws", "kp", false); err != nil {
		t.Fatal(err)
	}
	stubCopyEngine(t, `{}`)
	// A row from before modes need not say isolated; the provider check stands
	// in for it.
	insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceNone,
		GeneratedConfig: "{}", Mode: model.ModeNone, PersistState: true})

	err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}})
	assertUsage(t, err, "k8s")
	assertNoRow(t, a, "feat")

	insertRow(t, a, model.Container{Name: "iso", SourceKind: model.SourceNone,
		GeneratedConfig: "{}", Mode: model.ModeIsolated, PersistState: true})
	err = runContainerCreate(t.Context(), a, "", "feat", "", createOpts{from: "iso", noFolder: true, copy: []string{"state"}})
	assertUsage(t, err, "isolated")
}

func TestPlainFromStillWorksOnK8sWithAProjectSource(t *testing.T) {
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "ws", "kp", false); err != nil {
		t.Fatal(err)
	}
	insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceFolder, Source: "/x",
		ConfigPath: "/x/.devcontainer/devcontainer.json", Mode: model.ModeIsolated, PersistState: false})

	err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", noStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if c := containerRow(t, a, "feat"); c.PersistState || c.GeneratedConfig != "" || c.ConfigPath == "" {
		t.Errorf("row = %+v, want the folder's own config and the source's persist-state", c)
	}
}

func TestCopyStateNeedsPersistentStateOnBothSides(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	createSource(t, a, "web", createOpts{})
	createSource(t, a, "off", createOpts{noPersistState: true})

	err := runContainerCreate(t.Context(), a, "", "feat", "",
		createOpts{from: "off", noFolder: true, copy: []string{"state"}})
	assertUsage(t, err, "persist")
	assertNoRow(t, a, "feat")

	assertUsage(t, copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, noPersistState: true}), "--no-persist-state")
	assertNoRow(t, a, "feat")
}

func TestCopyImageDoesNotNeedPersistentState(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	createSource(t, a, "web", createOpts{noPersistState: true})
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"image"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
}

func composeProject(t *testing.T) string {
	t.Helper()
	folder := projectWithConfig(t)
	writeFile(t, filepath.Join(folder, ".devcontainer", "devcontainer.json"),
		`{"dockerComposeFile": "compose.yml", "service": "app"}`)
	return folder
}

func TestCopyRefusesComposeOnEitherSide(t *testing.T) {
	for _, list := range [][]string{{"image"}, {"state"}} {
		t.Run(list[0]+" target", func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			stubCopyEngine(t, `{}`)
			insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceFolder, Source: "/x",
				ConfigPath: filepath.Join(projectWithConfig(t), ".devcontainer", "devcontainer.json"),
				Mode:       model.ModeGuarded, PersistState: true})
			err := runContainerCreate(t.Context(), a, "", "feat", composeProject(t),
				createOpts{from: "web", copy: list, noStart: true})
			assertUsage(t, err, "compose")
			assertNoRow(t, a, "feat")
		})
		t.Run(list[0]+" source", func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			stubCopyEngine(t, `{}`)
			insertRow(t, a, model.Container{Name: "web", SourceKind: model.SourceFolder, Source: "/x",
				ConfigPath: filepath.Join(composeProject(t), ".devcontainer", "devcontainer.json"),
				Mode:       model.ModeGuarded, PersistState: true})
			err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
				createOpts{from: "web", copy: list, noStart: true})
			assertUsage(t, err, "compose")
			assertNoRow(t, a, "feat")
		})
	}
}

func TestCopyImageRefusesAnyTools(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	err := copyCreate(t, a, "feat", createOpts{copy: []string{"image"}, tools: []string{"+node"}})
	assertUsage(t, err, "--tools")
	assertNoRow(t, a, "feat")
}

func TestCopyStateAllowsTools(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, tools: []string{"+node"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyRefusesASourceWithoutAContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	t.Setenv("STUB_NO_CONTAINER", "1")

	// State alone needs the image for its helper, so it is refused too.
	for _, list := range [][]string{{"image"}, {"state"}} {
		err := copyCreate(t, a, "feat", createOpts{copy: list})
		assertUsage(t, err, "start web once")
		assertNoRow(t, a, "feat")
	}
}

func TestCopyRefusesASourceWithoutItsImage(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	t.Setenv("STUB_NO_IMAGE", "1")

	err := copyCreate(t, a, "feat", createOpts{copy: []string{"image"}})
	assertUsage(t, err, "--copy image")
	assertNoRow(t, a, "feat")
}

func TestCopyStateRefusesASourceWithoutAVolume(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)
	t.Setenv("STUB_NO_VOLUME", "1")

	err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}})
	assertUsage(t, err, "state volume")
	assertNoRow(t, a, "feat")
	if e.index(t, "volume create") >= 0 {
		t.Error("a volume was created for a refused copy")
	}
}

func TestCopyImageAndStateRunInOrderThenStart(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)

	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"image", "state"}}); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	tag, vol, run, up := e.index(t, "docker tag sha256:srcimage dev-ws-feat:from-image"),
		e.index(t, "docker volume create dev-ws-feat-state"),
		e.index(t, "docker run --rm --pull=never"),
		e.index(t, "devcontainer up")
	if tag < 0 || vol < 0 || run < 0 || up < 0 || tag >= vol || vol >= run || run >= up {
		t.Fatalf("order tag=%d volume=%d run=%d up=%d:\n%s", tag, vol, run, up, e.log(t))
	}
	if !strings.Contains(e.log(t), "sha256:srcimage -a /from/. /to/") {
		t.Errorf("the helper did not use the source's image:\n%s", e.log(t))
	}
	if containerRow(t, a, "feat").Name != "feat" {
		t.Error("no row")
	}
}

func TestCopyStateAloneTagsNothing(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	if e.index(t, "docker tag") >= 0 {
		t.Errorf("state alone tagged an image:\n%s", e.log(t))
	}
	if e.index(t, "docker run --rm --pull=never") < 0 || e.index(t, "devcontainer up") >= 0 {
		t.Errorf("want a copy and no start:\n%s", e.log(t))
	}
}

func TestCopyNoStartCopiesButDoesNotStart(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"image"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	if e.index(t, "docker tag") < 0 || e.index(t, "devcontainer up") >= 0 {
		t.Errorf("log:\n%s", e.log(t))
	}
}

func TestFailedCopyLeavesNothingBehind(t *testing.T) {
	for label, env := range map[string]string{"copy": "STUB_FAIL_RUN", "start": "STUB_FAIL_UP"} {
		t.Run(label, func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			e := stubCopyEngine(t, `{}`)
			copySource(t, a)
			t.Setenv(env, "1")

			err := copyCreate(t, a, "feat", createOpts{copy: []string{"image", "state"}})
			if err == nil {
				t.Fatal("create succeeded")
			}
			assertNoRow(t, a, "feat")
			if e.index(t, "docker volume rm --force dev-ws-feat-state") < 0 {
				t.Errorf("the copy was not discarded:\n%s", e.log(t))
			}
			if e.index(t, "dev-ws-web-state") >= 0 && e.index(t, "volume rm --force dev-ws-web") >= 0 ||
				e.index(t, "rm -f abc123") >= 0 {
				t.Errorf("the source was touched:\n%s", e.log(t))
			}
			_ = containerRow(t, a, "web")
		})
	}
}

func TestCopyStateWarnsWhenTheSourceIsRunning(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	var errw bytes.Buffer
	a.errw = &errw
	t.Setenv("STUB_RUNNING", "1")

	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	if got := errw.String(); !strings.Contains(got, "running") || !strings.Contains(got, "dev container stop web") {
		t.Errorf("stderr = %q", got)
	}
}

func TestCopyStateDoesNotWarnForAStoppedSource(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	var errw bytes.Buffer
	a.errw = &errw
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errw.String(), "dev container stop") {
		t.Errorf("stderr = %q", errw.String())
	}
}

func TestCreateAuditRecordsFromAndCopy(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{copy: []string{"state", "image"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range auditEntries(t, audit.Filter{Events: []string{"create"}}) {
		if e.Container() != "feat" {
			continue
		}
		found = true
		if e.Fields["from"] != "web" {
			t.Errorf("from = %v", e.Fields["from"])
		}
		got, _ := e.Fields["copy"].([]any)
		if len(got) != 2 || got[0] != "image" || got[1] != "state" {
			t.Errorf("copy = %v, want [image state]", e.Fields["copy"])
		}
	}
	if !found {
		t.Error("no create entry for feat")
	}
}

func TestPlainFromTouchesNoEngineCopyStep(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{}`)
	copySource(t, a)
	if err := copyCreate(t, a, "feat", createOpts{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"docker tag", "volume create", "docker run", "docker rmi"} {
		if e.index(t, s) >= 0 {
			t.Errorf("plain --from ran %q:\n%s", s, e.log(t))
		}
	}
	for _, en := range auditEntries(t, audit.Filter{Events: []string{"create"}}) {
		if en.Container() == "feat" {
			if _, ok := en.Fields["copy"]; ok {
				t.Errorf("a plain --from recorded copy: %v", en.Fields)
			}
		}
	}
}

// --- project-owned sources ----------------------------------------------------

// projectSource creates and starts a project-owned container, so it has a
// recorded digest.
func projectSource(t *testing.T, a *app, folder string) {
	t.Helper()
	if err := runContainerCreate(t.Context(), a, "", "web", folder, createOpts{}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if containerRow(t, a, "web").ConfigDigest == "" {
		t.Fatal("the source recorded no digest")
	}
}

func TestFromAProjectOwnedSourceUsesTheTargetsOwnConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{"image":"ubuntu"}`)
	projectSource(t, a, projectWithAgents(t, claudeAgents))
	src := containerRow(t, a, "web")

	target := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "feat", target,
		createOpts{from: "web", noStart: true, noAgentConfig: false}); err != nil {
		t.Fatal(err)
	}
	c := containerRow(t, a, "feat")
	if want := filepath.Join(target, ".devcontainer", "devcontainer.json"); c.GeneratedConfig != "" || !samePath(c.ConfigPath, want) {
		t.Errorf("row = %+v", c)
	}
	if c.ConfigPath == src.ConfigPath {
		t.Error("the new container uses the source's config")
	}
	if c.PersistState != src.PersistState || c.AgentConfig != src.AgentConfig {
		t.Errorf("persist/agent = %v/%q, want the source's %v/%q", c.PersistState, c.AgentConfig, src.PersistState, src.AgentConfig)
	}
}

func TestFromAProjectOwnedSourceRefusals(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{"image":"ubuntu"}`)
	projectSource(t, a, projectWithConfig(t))

	t.Run("target without a devcontainer", func(t *testing.T) {
		err := runContainerCreate(t.Context(), a, "", "feat", t.TempDir(), createOpts{from: "web", noStart: true})
		assertUsage(t, err, "project's config", "web")
		assertNoRow(t, a, "feat")
	})
	t.Run("--generate cannot stand in for one", func(t *testing.T) {
		err := runContainerCreate(t.Context(), a, "", "feat", t.TempDir(),
			createOpts{from: "web", noStart: true, generate: true})
		assertUsage(t, err)
		assertNoRow(t, a, "feat")
	})
	t.Run("no-folder", func(t *testing.T) {
		err := copyCreate(t, a, "feat", createOpts{noStart: true})
		assertUsage(t, err, "--no-folder")
		assertNoRow(t, a, "feat")
	})
	t.Run("tools", func(t *testing.T) {
		err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
			createOpts{from: "web", noStart: true, tools: []string{"+node"}})
		assertUsage(t, err, "--tools")
		assertNoRow(t, a, "feat")
	})
}

func TestFromAGeneratedSourceStillRefusesAFolderWithAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{}`)
	copySource(t, a)
	err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", noStart: true, copy: []string{"state"}})
	assertUsage(t, err, "shadow")
}

func TestCopyImageMatchesProjectConfigsIgnoringOverlays(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{"image":"ubuntu","containerEnv":{"A":"1"}}`)
	projectSource(t, a, projectWithConfig(t))

	// The overlays the new container is given differ from the source's, as two
	// worktrees' always do; only the image-deciding entries are compared.
	if err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"image"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
	if containerRow(t, a, "feat").ConfigDigest != "" {
		t.Error("--no-start recorded a digest at create")
	}
}

func TestCopyImageRefusesDifferingConfigs(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{"image":"ubuntu"}`)
	projectSource(t, a, projectWithConfig(t))
	writeFile(t, e.merged, `{"mergedConfiguration":{"image":"debian"}}`)

	err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"image"}, noStart: true})
	assertUsage(t, err, "image", "drop --copy image")
	assertNoRow(t, a, "feat")
	if e.index(t, "docker tag") >= 0 {
		t.Error("tagged despite the mismatch")
	}
}

func TestCopyImageRefusesAChangedDockerfile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{"build":{"dockerfile":"Dockerfile"}}`)
	src := projectWithConfig(t)
	writeFile(t, filepath.Join(src, ".devcontainer", "Dockerfile"), "FROM ubuntu\n")
	projectSource(t, a, src)

	same := projectWithConfig(t)
	writeFile(t, filepath.Join(same, ".devcontainer", "Dockerfile"), "FROM ubuntu\n")
	if err := runContainerCreate(t.Context(), a, "", "ok", same,
		createOpts{from: "web", copy: []string{"image"}, noStart: true}); err != nil {
		t.Fatalf("identical Dockerfiles: %v", err)
	}

	edited := projectWithConfig(t)
	writeFile(t, filepath.Join(edited, ".devcontainer", "Dockerfile"), "FROM debian\n")
	err := runContainerCreate(t.Context(), a, "", "feat", edited,
		createOpts{from: "web", copy: []string{"image"}, noStart: true})
	assertUsage(t, err, "Dockerfile")
	assertNoRow(t, a, "feat")
}

func TestCopyImageRefusesASourceWithoutADigest(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCopyEngine(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "web", projectWithConfig(t), createOpts{noStart: true}); err != nil {
		t.Fatal(err)
	}
	err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"image"}, noStart: true})
	assertUsage(t, err, "drop --copy image")
	assertNoRow(t, a, "feat")
}

func TestCopyStateFromAProjectSourceSkipsTheConfigMatch(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{"image":"ubuntu"}`)
	projectSource(t, a, projectWithConfig(t))
	writeFile(t, e.merged, `{"mergedConfiguration":{"image":"debian"}}`)

	if err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"state"}, noStart: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyImageStartsProjectTargetAndRecordsItsDigest(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	e := stubCopyEngine(t, `{"image":"ubuntu"}`)
	projectSource(t, a, projectWithConfig(t))

	if err := runContainerCreate(t.Context(), a, "", "feat", projectWithConfig(t),
		createOpts{from: "web", copy: []string{"image", "state"}}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if e.index(t, "docker tag sha256:srcimage dev-ws-feat:from-image") < 0 {
		t.Errorf("log:\n%s", e.log(t))
	}
	want := containerRow(t, a, "web").ConfigDigestFields
	if got := guard.ImageDiff(guard.ParseRecord("", want),
		guard.ParseRecord("", containerRow(t, a, "feat").ConfigDigestFields)); len(got) != 0 {
		t.Errorf("image entries differ: %v", got)
	}
}

func samePath(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return ra != "" && ra == rb
}
