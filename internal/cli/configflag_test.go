package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// siblingConfig writes a devcontainer.json in a directory of its own, outside
// any project, and returns its resolved path.
func siblingConfig(t *testing.T, base string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, base)
	writeFile(t, p, `{"image":"ubuntu"}`)
	return p
}

func TestCreateWithConfigOutsideTheProject(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, _ := stubCapturingDevcontainer(t)
	project, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := siblingConfig(t, "devcontainer.json")

	if err := runContainerCreate(t.Context(), a, "", "api", project, createOpts{configFile: cfg}); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "api")
	if c.ConfigPath != cfg || c.Source != project || c.GeneratedConfig != "" {
		t.Errorf("row = source %q config %q generated %q; want %q, %q, none", c.Source, c.ConfigPath, c.GeneratedConfig, project, cfg)
	}
	up := lastUp(t, calls)
	if !strings.Contains(up, "--workspace-folder "+project) || !strings.Contains(up, "--config "+cfg) {
		t.Errorf("up = %q; want --workspace-folder %s and --config %s", up, project, cfg)
	}
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Errorf("create wrote into the project: %v", entries)
	}
}

func TestCreateConfigIsResolvedFromARelativePath(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	cfg := siblingConfig(t, ".devcontainer.json")
	t.Chdir(filepath.Dir(cfg))

	err := runContainerCreate(t.Context(), a, "", "api", t.TempDir(),
		createOpts{configFile: ".devcontainer.json", noStart: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := containerRow(t, a, "api").ConfigPath; got != cfg {
		t.Errorf("ConfigPath = %q, want %q", got, cfg)
	}
}

func TestCreateConfigShadowsTheProjectFileWithAWarning(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	project := projectWithConfig(t)
	cfg := siblingConfig(t, "devcontainer.json")

	var err error
	stderr := captureStderr(t, func() {
		err = runContainerCreate(t.Context(), a, "", "api", project, createOpts{configFile: cfg, noStart: true})
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := containerRow(t, a, "api").ConfigPath; got != cfg {
		t.Errorf("ConfigPath = %q, want %q", got, cfg)
	}
	for _, want := range []string{cfg, filepath.Join(".devcontainer", "devcontainer.json"), "ignored"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning lacks %q:\n%s", want, stderr)
		}
	}
}

func TestCreateConfigRefusals(t *testing.T) {
	cfg := siblingConfig(t, "devcontainer.json")
	wrongName := siblingConfig(t, "config.json")
	missing := filepath.Join(t.TempDir(), "devcontainer.json")

	tests := []struct {
		name string
		opts createOpts
		want int
	}{
		{"generate", createOpts{configFile: cfg, generate: true}, exitUsage},
		{"no-folder", createOpts{configFile: cfg, noFolder: true}, exitUsage},
		{"tools", createOpts{configFile: cfg, tools: []string{"go"}}, exitUsage},
		{"wrong base name", createOpts{configFile: wrongName}, exitUsage},
		{"missing", createOpts{configFile: missing}, exitNotFound},
		{"directory", createOpts{configFile: t.TempDir()}, exitNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			stubCapturingDevcontainer(t)
			tt.opts.noStart = true
			err := runContainerCreate(t.Context(), a, "", "api", folderFor(tt.opts, t), tt.opts)
			if got := exitCodeOf(err); got != tt.want {
				t.Fatalf("exit = %d (%v), want %d", got, err, tt.want)
			}
			st, _ := a.store()
			if _, err := st.GetContainer("ws", "api"); err == nil {
				t.Error("a refused create wrote a row")
			}
		})
	}
}

// folderFor gives a project folder unless the case is --no-folder.
func folderFor(o createOpts, t *testing.T) string {
	if o.noFolder {
		return ""
	}
	return t.TempDir()
}

func TestCreateFromInheritsAnExternalConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	cfg := siblingConfig(t, "devcontainer.json")
	other := siblingConfig(t, "devcontainer.json")

	if err := runContainerCreate(t.Context(), a, "", "api", t.TempDir(),
		createOpts{configFile: cfg, noStart: true}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "copy", t.TempDir(),
		createOpts{from: "api", noStart: true}); err != nil {
		t.Fatalf("create --from: %v", err)
	}
	if got := containerRow(t, a, "copy").ConfigPath; got != cfg {
		t.Errorf("inherited ConfigPath = %q, want %q", got, cfg)
	}

	if err := runContainerCreate(t.Context(), a, "", "over", t.TempDir(),
		createOpts{from: "api", configFile: other, noStart: true}); err != nil {
		t.Fatalf("create --from --config: %v", err)
	}
	if got := containerRow(t, a, "over").ConfigPath; got != other {
		t.Errorf("overridden ConfigPath = %q, want %q", got, other)
	}
}

func TestCreateFromAProjectsOwnConfigIsNotInherited(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	project := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "copy", project,
		createOpts{from: "api", noStart: true}); err != nil {
		t.Fatalf("create --from: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(project, ".devcontainer", "devcontainer.json"))
	if got := containerRow(t, a, "copy").ConfigPath; got != want {
		t.Errorf("ConfigPath = %q, want its own folder's %q", got, want)
	}
}

func TestCreateConfigGuardedStillChecksTheProjectGit(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	requireGit(t)
	root, _ := filepath.EvalSymlinks(initRepo(t))
	cfg := siblingConfig(t, "devcontainer.json")

	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{configFile: cfg}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !containerRow(t, a, "api").GitGuard {
		t.Fatal("GitGuard not set for the project's repository")
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	if up := lastUp(t, calls); !strings.Contains(up, "--workspace-folder "+root) || !strings.Contains(up, "--config "+cfg) {
		t.Errorf("up = %q", up)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, gitdir, true) {
		t.Errorf("project .git is not mounted read-only:\n  %s", strings.Join(mounts, "\n  "))
	}
}

func lastUp(t *testing.T, calls string) string {
	t.Helper()
	var up string
	for _, l := range strings.Split(callsIn(t, calls), "\n") {
		if strings.HasPrefix(l, "up ") {
			up = l
		}
	}
	return up
}

func TestCreateConfigNamingTheProjectsOwnFileDoesNotWarn(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	project := projectWithConfig(t)
	own := filepath.Join(project, ".devcontainer", "devcontainer.json")

	var err error
	stderr := captureStderr(t, func() {
		err = runContainerCreate(t.Context(), a, "", "api", project, createOpts{configFile: own, noStart: true})
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(stderr, "ignored") {
		t.Errorf("warned about the file it uses:\n%s", stderr)
	}
}

func TestCreateFromInheritsANonDefaultConfigInsideTheFolder(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	rel := filepath.Join(".devcontainer", "python", "devcontainer.json")
	mk := func() string {
		d, _ := filepath.EvalSymlinks(t.TempDir())
		writeFile(t, filepath.Join(d, rel), "{}")
		return d
	}
	src, dst := mk(), mk()
	if err := runContainerCreate(t.Context(), a, "", "api", src,
		createOpts{configFile: filepath.Join(src, rel), noStart: true}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "copy", dst,
		createOpts{from: "api", noStart: true}); err != nil {
		t.Fatalf("create --from: %v", err)
	}
	if got, want := containerRow(t, a, "copy").ConfigPath, filepath.Join(dst, rel); got != want {
		t.Errorf("ConfigPath = %q, want rebased %q", got, want)
	}

	err := runContainerCreate(t.Context(), a, "", "bare", t.TempDir(), createOpts{from: "api", noStart: true})
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("a folder lacking the file: exit = %d (%v), want %d", got, err, exitNotFound)
	}
}

func TestCreateFromAnExternalConfigShadowsTheNewFoldersOwnWithAWarning(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	cfg := siblingConfig(t, "devcontainer.json")
	if err := runContainerCreate(t.Context(), a, "", "api", t.TempDir(),
		createOpts{configFile: cfg, noStart: true}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	var err error
	stderr := captureStderr(t, func() {
		err = runContainerCreate(t.Context(), a, "", "copy", projectWithConfig(t), createOpts{from: "api", noStart: true})
	})
	if err != nil {
		t.Fatalf("create --from: %v", err)
	}
	if !strings.Contains(stderr, "ignored") || containerRow(t, a, "copy").ConfigPath != cfg {
		t.Errorf("no shadow warning or wrong path:\n%s", stderr)
	}
}
