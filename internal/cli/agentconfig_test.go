package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigChoice(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agents.yaml")
	writeFile(t, file, "version: 1\n")

	if got, err := agentConfigChoice("", false); err != nil || got != "" {
		t.Errorf("no flags = %q, %v", got, err)
	}
	if got, err := agentConfigChoice("", true); err != nil || got != agentConfigNone {
		t.Errorf("--no-agent-config = %q, %v", got, err)
	}
	got, err := agentConfigChoice(file, false)
	want, _ := filepath.EvalSymlinks(file)
	if err != nil || got != want {
		t.Errorf("--agent-config = %q, %v; want %q", got, err, want)
	}
	if _, err := agentConfigChoice(file, true); exitCodeOf(err) != exitUsage {
		t.Errorf("both flags: exit %d, want %d", exitCodeOf(err), exitUsage)
	}
	if _, err := agentConfigChoice(file+".gone", false); exitCodeOf(err) != exitNotFound {
		t.Errorf("missing file: exit %d, want %d", exitCodeOf(err), exitNotFound)
	}
}

func TestAgentConfigFile(t *testing.T) {
	project := t.TempDir()
	c := model.Container{SourceKind: model.SourceFolder, Source: project}

	if got, err := agentConfigFile(c); err != nil || got != "" {
		t.Errorf("project without a file = %q, %v", got, err)
	}

	def := filepath.Join(project, ".devcontainer", "agents.yaml")
	writeFile(t, def, "version: 1\n")
	if got, _ := agentConfigFile(c); got != def {
		t.Errorf("default = %q, want %q", got, def)
	}

	c.AgentConfig = agentConfigNone
	if got, _ := agentConfigFile(c); got != "" {
		t.Errorf("none = %q", got)
	}

	c.AgentConfig = filepath.Join(t.TempDir(), "gone.yaml")
	if _, err := agentConfigFile(c); exitCodeOf(err) != exitNotFound {
		t.Errorf("vanished explicit file: exit %d, want %d", exitCodeOf(err), exitNotFound)
	}

	folderless := model.Container{SourceKind: model.SourceNone}
	if got, err := agentConfigFile(folderless); err != nil || got != "" {
		t.Errorf("folderless = %q, %v", got, err)
	}
}

func TestMarkAgentConfig(t *testing.T) {
	project := t.TempDir()
	c := model.Container{SourceKind: model.SourceFolder, Source: project}
	if err := markAgentConfig(&c); err != nil || c.AgentConfigPending {
		t.Errorf("no file: pending=%v err=%v", c.AgentConfigPending, err)
	}

	writeFile(t, filepath.Join(project, ".devcontainer", "agents.yaml"), "version: 1\nclaude:\n  plugins: [a@b]\n")
	if err := markAgentConfig(&c); err != nil || !c.AgentConfigPending {
		t.Errorf("valid file: pending=%v err=%v", c.AgentConfigPending, err)
	}

	writeFile(t, filepath.Join(project, ".devcontainer", "agents.yaml"), "version: 1\ncodex: {}\n")
	err := markAgentConfig(&c)
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "agents.yaml") {
		t.Errorf("bad file: exit %d, err %v", exitCodeOf(err), err)
	}
}

// stubDevcontainer logs each call as one line, and fails for any call
// containing failOn.
func stubDevcontainer(t *testing.T, failOn string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$1\" in read-configuration) echo '{\"mergedConfiguration\":{}}'; exit 0;; esac\n"
	if failOn != "" {
		script += "case \"$*\" in *'" + failOn + "'*) echo boom >&2; exit 1;; esac\n"
	}
	script += "exit 0\n"
	writeFile(t, filepath.Join(dir, "devcontainer"), script)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func callsIn(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// projectWithAgents is a project with a devcontainer.json and an agents.yaml
// beside a local skill.
func projectWithAgents(t *testing.T, agents string) string {
	t.Helper()
	folder := projectWithConfig(t)
	writeFile(t, filepath.Join(folder, ".devcontainer", "agents.yaml"), agents)
	writeFile(t, filepath.Join(folder, ".devcontainer", "skills", "ours", "SKILL.md"), "# ours\n")
	return folder
}

const claudeAgents = `version: 1
skills:
  - path: ./skills/ours
claude:
  plugins: [superpowers@official]
  mcp:
    ctx:
      command: npx
`

func containerRow(t *testing.T, a *app, name string) model.Container {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateAppliesTheProjectsAgentsFile(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder := projectWithAgents(t, claudeAgents)

	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	calls := callsIn(t, log)
	for _, want := range []string{
		"claude plugin install --scope user superpowers@official",
		"claude mcp add-json --scope user ctx",
		"/skills/ours",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("no call containing %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(out.String(), "agents.yaml: claude: plugin superpowers@official") {
		t.Errorf("no progress line:\n%s", out)
	}
	if c := containerRow(t, a, "api"); c.AgentConfigPending {
		t.Error("a finished apply left the flag set")
	}
}

func TestCreateNoStartDefersTheApplyToStart(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder := projectWithAgents(t, claudeAgents)

	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(callsIn(t, log), "claude plugin") {
		t.Fatal("--no-start applied the file with no container to apply it to")
	}
	c := containerRow(t, a, "api")
	if !c.AgentConfigPending {
		t.Fatal("--no-start did not record the apply as owed")
	}

	if err := a.start(t.Context(), "ws", c); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(callsIn(t, log), "claude plugin install") {
		t.Error("the first start did not apply the file")
	}
	if containerRow(t, a, "api").AgentConfigPending {
		t.Error("the flag survived a finished apply")
	}

	before := strings.Count(callsIn(t, log), "claude plugin install")
	if err := a.start(t.Context(), "ws", containerRow(t, a, "api")); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if strings.Count(callsIn(t, log), "claude plugin install") != before {
		t.Error("a start with nothing owed applied the file again")
	}
}

func TestAFailingStepExitsOneAndStaysPending(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubDevcontainer(t, "plugin install")
	folder := projectWithAgents(t, claudeAgents)

	err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{})
	if err == nil {
		t.Fatal("create succeeded past a failed install")
	}
	if exitCodeOf(err) != exitError {
		t.Errorf("exit %d, want %d", exitCodeOf(err), exitError)
	}
	for _, want := range []string{"claude: plugin superpowers@official", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if !containerRow(t, a, "api").AgentConfigPending {
		t.Error("a failed apply cleared the flag, so no start would retry it")
	}
}

func TestAnAgentThatIsNotInstalledIsSkipped(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "command -v hermes")
	folder := projectWithAgents(t, "version: 1\nhermes:\n  mcp:\n    x:\n      command: x\n")

	var err error
	stderr := captureStderr(t, func() {
		err = runContainerCreate(t.Context(), a, "", "api", folder, createOpts{})
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stderr, "hermes is not installed") {
		t.Errorf("no warning:\n%s", stderr)
	}
	// Not HERMES_HOME: that rides --remote-env on every exec, the probe
	// included.
	if strings.Contains(callsIn(t, log), "config.yaml") {
		t.Error("ran a hermes step anyway")
	}
}

func TestAMalformedAgentsFileFailsBeforeAnythingIsCreated(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder := projectWithAgents(t, "version: 1\ncodex: {}\n")

	err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{})
	if exitCodeOf(err) != exitUsage {
		t.Fatalf("exit %d (%v), want %d", exitCodeOf(err), err, exitUsage)
	}
	if calls := callsIn(t, log); calls != "" {
		t.Errorf("the engine was reached:\n%s", calls)
	}
	st, _ := a.store()
	if _, err := st.GetContainer("ws", "api"); err == nil {
		t.Error("a row was written for a rejected create")
	}
}

func TestNoAgentConfigIgnoresTheProjectFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder := projectWithAgents(t, claudeAgents)

	if err := runContainerCreate(t.Context(), a, "", "api", folder,
		createOpts{noAgentConfig: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Not a bare "claude": CLAUDE_CONFIG_DIR rides --remote-env on every up.
	if strings.Contains(callsIn(t, log), "claude plugin") {
		t.Error("--no-agent-config applied the project file")
	}
	if c := containerRow(t, a, "api"); c.AgentConfig != agentConfigNone || c.AgentConfigPending {
		t.Errorf("row = %q %v", c.AgentConfig, c.AgentConfigPending)
	}
}

func TestAgentConfigFlagGivesAFolderlessContainerAFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubDevcontainer(t, "")
	elsewhere := projectWithAgents(t, claudeAgents)
	file := filepath.Join(elsewhere, ".devcontainer", "agents.yaml")

	opts := createOpts{noFolder: true, noStart: true, agentConfig: file}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "scratch")
	want, _ := filepath.EvalSymlinks(file)
	if c.AgentConfig != want || !c.AgentConfigPending {
		t.Errorf("row = %q %v, want %q true", c.AgentConfig, c.AgentConfigPending, want)
	}
}

func TestCreateAgentConfigFlagErrors(t *testing.T) {
	code, _ := execute(t, "container", "create", "x", "--no-folder",
		"--agent-config", "a.yaml", "--no-agent-config")
	if code != exitUsage {
		t.Errorf("both flags: exit %d, want %d", code, exitUsage)
	}
	code, _ = execute(t, "container", "create", "x", "--no-folder",
		"--agent-config", filepath.Join(t.TempDir(), "gone.yaml"))
	if code != exitNotFound {
		t.Errorf("missing file: exit %d, want %d", code, exitNotFound)
	}
}

func TestContainerOnAWorktreeRecordsTheAgentConfigChoice(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	worktreeWithContainer(t, a, "feat", repo, filepath.Join(t.TempDir(), "wt"),
		createOpts{noAgentConfig: true})
	if c := containerRow(t, a, "feat"); c.AgentConfig != agentConfigNone {
		t.Errorf("AgentConfig = %q, want %q", c.AgentConfig, agentConfigNone)
	}
}

// runIn drives the real command tree against an existing test app.
func runIn(t *testing.T, a *app, argv ...string) error {
	t.Helper()
	root := newRootCmd("test")
	root.AddCommand(newContainerCmd(a))
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs(argv)
	return root.Execute()
}

func TestRebuildReappliesTheFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder := projectWithAgents(t, claudeAgents)
	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, filepath.Join(folder, ".devcontainer", "agents.yaml"),
		"version: 1\nclaude:\n  plugins: [second@official]\n")

	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	calls := callsIn(t, log)
	rebuildAt := strings.Index(calls, "--remove-existing-container")
	if rebuildAt < 0 {
		t.Fatalf("no rebuild:\n%s", calls)
	}
	if !strings.Contains(calls[rebuildAt:], "claude plugin install --scope user second@official") {
		t.Errorf("rebuild did not apply the edited file:\n%s", calls[rebuildAt:])
	}
}

func TestRebuildWithAMissingAgentConfigFailsBeforeRebuilding(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	file := filepath.Join(t.TempDir(), "agents.yaml")
	writeFile(t, file, "version: 1\nclaude:\n  plugins: [a@b]\n")

	opts := createOpts{noFolder: true, noStart: true, agentConfig: file}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	err := runIn(t, a, "container", "rebuild", "scratch")
	if exitCodeOf(err) != exitNotFound {
		t.Fatalf("exit %d (%v), want %d", exitCodeOf(err), err, exitNotFound)
	}
	if strings.Contains(callsIn(t, log), "--remove-existing-container") {
		t.Error("the container was recreated before the missing file was noticed")
	}
}

// projectWithOpencodeConfig is a project whose agents.yaml points at a V2
// opencode document.
func projectWithOpencodeConfig(t *testing.T) (folder, file string) {
	t.Helper()
	folder = projectWithConfig(t)
	file = filepath.Join(folder, ".devcontainer", "opencode.json")
	writeFile(t, file, `{"model":"litellm/first"}`)
	writeFile(t, filepath.Join(folder, ".devcontainer", "agents.yaml"), `version: 1
opencode:
  config_file: ./opencode.json
  plugins: [superpowers@git+https://github.com/obra/superpowers.git]
`)
	return folder, file
}

func TestCreateSeedsTheOpencodeConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder, _ := projectWithOpencodeConfig(t)

	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// One read and one write of the file.
	if got := strings.Count(callsIn(t, log), "opencode.json"); got < 2 {
		t.Errorf("the config was touched %d times:\n%s", got, callsIn(t, log))
	}
	if !strings.Contains(out.String(), "opencode: config, plugins and mcp in opencode.json") {
		t.Errorf("no progress line:\n%s", out)
	}
}

func TestCreateNoStartSeedsTheOpencodeConfigOnFirstStart(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder, _ := projectWithOpencodeConfig(t)

	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(callsIn(t, log), "opencode.json") {
		t.Fatal("--no-start applied the config with no container to apply it to")
	}

	if err := a.start(t.Context(), "ws", containerRow(t, a, "api")); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := strings.Count(callsIn(t, log), "opencode.json"); got < 2 {
		t.Errorf("the first start touched the config %d times:\n%s", got, callsIn(t, log))
	}
}

func TestEditingTheOpencodeConfigWaitsForARebuild(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder, file := projectWithOpencodeConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := strings.Count(callsIn(t, log), "opencode.json")

	writeFile(t, file, `{"model":"litellm/second"}`)
	if err := a.start(t.Context(), "ws", containerRow(t, a, "api")); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := strings.Count(callsIn(t, log), "opencode.json"); got != before {
		t.Errorf("an ordinary start re-applied the config: %d -> %d", before, got)
	}

	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := strings.Count(callsIn(t, log), "opencode.json"); got <= before {
		t.Errorf("rebuild did not read the edited config: %d -> %d", before, got)
	}
}

func TestRebuildWithAMissingOpencodeConfigFailsBeforeRebuilding(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubDevcontainer(t, "")
	folder, file := projectWithOpencodeConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	err := runIn(t, a, "container", "rebuild", "api")
	if exitCodeOf(err) != exitUsage {
		t.Fatalf("exit %d (%v), want %d", exitCodeOf(err), err, exitUsage)
	}
	if !strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("error %q does not name the config file", err)
	}
	if strings.Contains(callsIn(t, log), "--remove-existing-container") {
		t.Error("the container was recreated before the missing config was noticed")
	}
}

func TestUndefinedOpencodeEnvRefWarns(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubDevcontainer(t, "")
	folder := projectWithConfig(t)
	writeFile(t, filepath.Join(folder, ".devcontainer", "opencode.json"),
		`{"providers":{"litellm":{"settings":{"apiKey":"{env:OPENCODE_TEST_KEY}"}}}}`)
	writeFile(t, filepath.Join(folder, ".devcontainer", "agents.yaml"),
		"version: 1\nopencode:\n  config_file: ./opencode.json\n")

	var err error
	stderr := captureStderr(t, func() {
		err = runContainerCreate(t.Context(), a, "", "api", folder, createOpts{})
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, want := range []string{"OPENCODE_TEST_KEY", "no workspace setting defines"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning does not mention %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "${") {
		t.Errorf("the warning prints the MCP syntax for a config_file reference:\n%s", stderr)
	}
}

func TestRebuildOfAProjectThatDroppedItsFileClearsThePendingFlag(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubDevcontainer(t, "")
	folder := projectWithAgents(t, claudeAgents)
	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(filepath.Join(folder, ".devcontainer", "agents.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if containerRow(t, a, "api").AgentConfigPending {
		t.Error("the flag outlived the file it was owed for")
	}
}
