# Declared agent configuration — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A project declares its agents' skills, MCP servers and plugins in `.devcontainer/agents.yaml`; `dev` applies that declaration inside the container on `create` (or the first `start` after `create --no-start`) and on `rebuild`, for Claude Code, opencode and hermes.

**Architecture:** `internal/agentcfg` parses and validates the file into a `Spec` and projects it per agent into a `View`. `internal/agent` turns a `View` into ordered `Step` values — plain data: a command, optionally a pre/post check, or a read-modify-write of one config file — and `agent.Apply` runs them through an `Exec` function. `internal/cli` resolves which file a container uses, records that choice and a pending flag on the row, and supplies an `Exec` backed by `Provider.Exec`, so neither provider changes.

**Tech Stack:** Go 1.26, cobra, modernc.org/sqlite, `go.yaml.in/yaml/v3` (new), `github.com/tailscale/hujson` (existing). Shells out to the `devcontainer` CLI / `kubectl` through the existing providers; inside the container, to `claude`, `git`, `tar`, `sh`.

**Spec:** `docs/specs/2026-09-23-agent-config.md`

## Global Constraints

- **Exit codes are interface.** `2` malformed request, `3` named thing does not exist, `1` otherwise. Use `usageError`/`usageErrorf`/`notFoundErrorf` (`internal/cli/errors.go`); `exactArgs`/`noArgs`/`minArgs` never cobra's.
- **Migrations are portable SQL.** Literal defaults, `NOT NULL`, no `AUTOINCREMENT`, no `datetime('now')`.
- **Resolve host paths before storing them.** `xpath.Resolve` for directories; this plan adds `xpath.ResolveFile` for files.
- **Do not write into a project's folder.** `dev` reads `agents.yaml` and never writes it.
- **A setting's spec is never a value.** `${NAME}` in `agents.yaml` is never substituted by `dev`; it is rewritten into each agent's own reference syntax.
- **Shell out, do not reimplement.** No client libraries for the agents; their CLIs or their config files.
- **`internal/cli` orchestrates.** Resolve inputs, call a package, print. The apply loop lives in `internal/agent`.
- **Tests never touch a container engine or the network.** Stub executables on a temp `PATH`; agent-package tests use a fake `Exec` func.
- **Store tests use a temp file, never `:memory:`.**
- **Every non-obvious line carries a comment saying *why*.** Match the surrounding density.
- **Nothing is removed on apply.** A declared named entry or skill directory is replaced; everything else is left alone.
- **Codex is out of scope.** A `codex:` section is an unknown key.
- **Commits:** conventional prefix (`feat:`, `docs:`, `test:`), no `Co-Authored-By: Claude` or other tooling-attribution trailer (`.githooks/commit-msg` rejects it; `make hooks` once per clone).
- After every task: `make lint && make test` pass before the commit.

## Review Focus

1. **An existing `opencode.json` written as JSONC** (comments, trailing commas — opencode accepts them) must merge, not fail the apply. → Task 4, `TestMergeOpencodeAcceptsJSONC`.
2. **An existing hermes `config.yaml` with comments and unrelated keys** must keep both through the merge. → Task 4, `TestMergeHermesKeepsCommentsAndOtherKeys`.
3. **A rebuild re-applies everything over a state volume that already holds the result**, so every merge applied twice must equal it applied once, with no duplicated plugin. → Task 4, `TestMergesAreIdempotent`.
4. **The file `--agent-config` named is deleted before a rebuild** — exit 3 *before* the container is recreated, not after. → Task 7, `TestRebuildWithAMissingAgentConfigFailsBeforeRebuilding`.
5. **A `$` that is not a `${NAME}` reference** (`$HOME`, `$$`, `${1}`) passes through unchanged into opencode's config. → Task 2, `TestRewriteRefsLeavesOtherDollarsAlone`.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/store/migrations/0006_agent_config.sql` | two columns on `containers` |
| `internal/model/model.go` | `Container.AgentConfig`, `Container.AgentConfigPending` |
| `internal/store/container.go` | persist/scan the columns; `SetAgentConfigPending` |
| `internal/xpath/xpath.go` | `ResolveFile` |
| `internal/agentcfg/agentcfg.go` | types, `Load`, validation, `View`, `Refs`, `RewriteRefs` |
| `internal/agentcfg/archive.go` | tar a local skill directory |
| `internal/agent/agent.go` | `Agent.Configure`, `Configurable()` |
| `internal/agent/step.go` | `Step`, `Exec`, `Run`, `Apply` |
| `internal/agent/skills.go` | skill steps shared by every agent |
| `internal/agent/claude.go` | Claude Code steps |
| `internal/agent/opencode.go` | opencode steps and `mergeOpencode` |
| `internal/agent/hermes.go` | hermes steps and `mergeHermes` |
| `internal/cli/agentconfig.go` | choosing the file, loading it, the `Exec` over `Provider.Exec` |
| `internal/cli/container.go`, `internal/cli/worktree.go`, `internal/cli/agent.go` | flags and the three call sites |
| `test/smoke/agentconfig_test.go` | end to end |
| `docs/USAGE.md`, `CLAUDE.md` | operator docs, invariant note |

---

### Task 1: Schema, model and store

**Files:**
- Create: `internal/store/migrations/0006_agent_config.sql`
- Modify: `internal/model/model.go` (the `Container` struct)
- Modify: `internal/store/container.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `model.Container.AgentConfig string`, `model.Container.AgentConfigPending bool`; `func (s *Store) SetAgentConfigPending(workspace, name string, pending bool) error` (returns `ErrNotFound` for a missing row).

- [ ] **Step 1: Write the failing tests** — append to `internal/store/store_test.go`:

```go
// The choice of agents.yaml is fixed at create and read back on every rebuild,
// so it has to survive the round trip exactly.
func TestContainerRoundTripsAgentConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	c := model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder,
		Source: "/src/api", AgentConfig: "/home/me/agents.yaml", AgentConfigPending: true,
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.AgentConfig != c.AgentConfig || !got.AgentConfigPending {
		t.Errorf("got AgentConfig=%q pending=%v, want %q true",
			got.AgentConfig, got.AgentConfigPending, c.AgentConfig)
	}

	if err := s.SetAgentConfigPending("ws", "api", false); err != nil {
		t.Fatalf("SetAgentConfigPending: %v", err)
	}
	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 1 || list[0].AgentConfigPending {
		t.Errorf("pending not cleared: %+v", list)
	}
}

// A container row written without either field reads as "use the project's
// default file" and "nothing owed" — the answer the migration gives rows that
// predate it.
func TestAgentConfigDefaultsToProjectFileAndNothingPending(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceNone,
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.AgentConfig != "" || got.AgentConfigPending {
		t.Errorf("got %q %v, want empty and false", got.AgentConfig, got.AgentConfigPending)
	}
}

func TestSetAgentConfigPendingOnAMissingContainer(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.SetAgentConfigPending("ws", "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/ -run 'AgentConfig' -v`
Expected: FAIL to compile — `unknown field AgentConfig`, `s.SetAgentConfigPending undefined`.

- [ ] **Step 3: Add the migration** — `internal/store/migrations/0006_agent_config.sql`:

```sql
-- Which agents.yaml a container applies, and whether applying it is still owed.
--
-- agent_config stores the operator's choice, never the file's contents: ''
-- means the project's own .devcontainer/agents.yaml when it has one, 'none'
-- means create was told --no-agent-config, anything else is the physical path
-- --agent-config named. Stored because rebuild re-reads the file and has to
-- read the same one without the flag being repeated.
--
-- agent_config_pending is work dev owes the container, not the container's
-- state: set when there is a file to apply and cleared once an apply finishes,
-- so `create --no-start` hands the job to the first start and a failed apply is
-- retried by the next one.
--
-- '' and 0 are the right answers for rows that already exist: nothing was ever
-- applied to them, and nothing will be until they are rebuilt.
--
-- Literal defaults and NOT NULL, so the file replays against Postgres.
ALTER TABLE containers ADD COLUMN agent_config TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN agent_config_pending INTEGER NOT NULL DEFAULT 0;
```

- [ ] **Step 4: Add the model fields** — in `internal/model/model.go`, inside `Container`, directly after `PersistState bool`:

```go
	// AgentConfig says which agents.yaml this container applies: "" for the
	// project's own .devcontainer/agents.yaml when it has one, "none" when
	// create was given --no-agent-config, otherwise the physical path
	// --agent-config named. The choice and never the contents, so an edit to
	// the file takes effect at the next rebuild. Fixed at create, like
	// PersistState, so rebuild reads the same file without the flag.
	AgentConfig string
	// AgentConfigPending says applying that file is still owed: set at create
	// when there is one, cleared once an apply finishes. It is what lets
	// `create --no-start` hand the job to the first start, and a failed apply
	// be retried by the next. Work dev owes, not live container status.
	AgentConfigPending bool
```

- [ ] **Step 5: Persist and scan** — in `internal/store/container.go`:

In `CreateContainer`, replace the statement with:

```go
	_, err := s.db.Exec(
		`INSERT INTO containers
		   (name, workspace_name, source_kind, source, config_path, generated_config,
		    persist_state, agent_config, agent_config_pending, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.WorkspaceName, string(c.SourceKind), c.Source, c.ConfigPath,
		c.GeneratedConfig, boolToInt(c.PersistState), c.AgentConfig,
		boolToInt(c.AgentConfigPending), nowString())
```

In `GetContainer` and `ListContainers`, change the column list `persist_state, created_at` to `persist_state, agent_config, agent_config_pending, created_at` (both queries).

Replace `scanContainer` with:

```go
func scanContainer(sc scanner) (model.Container, error) {
	var (
		c         model.Container
		kind      string
		persist   int
		pending   int
		createdAt string
	)
	if err := sc.Scan(&c.Name, &c.WorkspaceName, &kind, &c.Source, &c.ConfigPath,
		&c.GeneratedConfig, &persist, &c.AgentConfig, &pending, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Container{}, ErrNotFound
		}
		return model.Container{}, fmt.Errorf("reading container: %w", err)
	}
	c.SourceKind = model.SourceKind(kind)
	c.PersistState = persist != 0
	c.AgentConfigPending = pending != 0

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Container{}, fmt.Errorf("reading container %s: bad created_at %q: %w", c.Name, createdAt, err)
	}
	c.CreatedAt = t
	return c, nil
}
```

Append:

```go
// SetAgentConfigPending records whether applying a container's agents.yaml is
// still owed. Set before the first step of an apply and cleared after the last,
// so an apply that fails or is interrupted leaves it set for the next start.
func (s *Store) SetAgentConfigPending(workspace, name string, pending bool) error {
	res, err := s.db.Exec(
		`UPDATE containers SET agent_config_pending = ?
		 WHERE workspace_name = ? AND name = ?`, boolToInt(pending), workspace, name)
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}
```

- [ ] **Step 6: Run to verify they pass**

Run: `go test ./internal/store/ -v -run 'AgentConfig|Migrat'` then `make lint && make test`
Expected: PASS, including the existing migration tests.

- [ ] **Step 7: Commit**

```bash
git add internal/store internal/model
git commit -m "feat(store): record a container's agents.yaml choice and pending apply"
```

---

### Task 2: `internal/agentcfg` — parse, validate, project

**Files:**
- Create: `internal/agentcfg/agentcfg.go`, `internal/agentcfg/archive.go`
- Modify: `internal/xpath/xpath.go` (add `ResolveFile`)
- Modify: `go.mod`, `go.sum`
- Test: `internal/agentcfg/agentcfg_test.go`, `internal/xpath/xpath_test.go`

**Interfaces:**
- Produces:
  - `func xpath.ResolveFile(path string) (string, error)` — physical path of a regular file; the error wraps `fs.ErrNotExist` when it is missing.
  - `const agentcfg.FileName = ".devcontainer/agents.yaml"`
  - `type agentcfg.Skill struct { Name, Git, Ref, Path string; Archive []byte }` — `Archive` non-nil exactly for a local skill; `Path` is the repo subdirectory (`"."` for the root) for a git skill.
  - `type agentcfg.MCPServer struct { Command string; Args []string; Env map[string]string; URL string; Headers map[string]string }` with `func (m MCPServer) Remote() bool`.
  - `type agentcfg.View struct { Skills []Skill; MCP map[string]MCPServer; Plugins []string; Marketplaces map[string]string }`
  - `func agentcfg.Load(path string) (*Spec, error)`
  - `func (s *Spec) View(agent string) (View, bool)` — `false` when nothing in the file reaches that agent, or the agent is not one of `claude`, `hermes`, `opencode`.
  - `func (s *Spec) Refs() []string` — sorted distinct `NAME`s from `${NAME}` in MCP `env`/`headers` values, `args`, `url`.
  - `func agentcfg.RewriteRefs(s string, to func(name string) string) string`

- [ ] **Step 1: Add the dependency**

Run: `go get go.yaml.in/yaml/v3@v3.0.4`
Expected: `go.mod` gains `go.yaml.in/yaml/v3 v3.0.4` (it moves to the direct block once imported; `go mod tidy` at step 7 settles it).

- [ ] **Step 2: Write the failing xpath test** — append to `internal/xpath/xpath_test.go`:

```go
func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agents.yaml")
	if err := os.WriteFile(file, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.yaml")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveFile(link)
	if err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}
	want, _ := filepath.EvalSymlinks(file)
	if got != want {
		t.Errorf("ResolveFile = %q, want %q", got, want)
	}

	if _, err := ResolveFile(dir); err == nil {
		t.Error("ResolveFile accepted a directory")
	}
	if _, err := ResolveFile(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}
}
```

Add `"errors"` and `"io/fs"` to that file's imports if absent.

- [ ] **Step 3: Write the failing agentcfg tests** — `internal/agentcfg/agentcfg_test.go`:

```go
package agentcfg

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write puts an agents.yaml and any extra files under a fresh directory and
// returns the agents.yaml path.
func write(t *testing.T, body string, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range extra {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "agents.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const full = `version: 1
skills:
  - git: https://github.com/obra/superpowers
    ref: v6.4.1
    path: skills/brainstorming
  - path: ./skills/ours
mcp:
  context7:
    command: npx
    args: ["-y", "@upstash/context7-mcp"]
    env:
      CONTEXT7_API_KEY: ${CONTEXT7_API_KEY}
  sentry:
    url: https://mcp.sentry.dev/mcp
    headers:
      Authorization: Bearer ${SENTRY_TOKEN}
claude:
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins: [superpowers@claude-plugins-official]
  mcp:
    context7:
      url: https://example.com/ctx
opencode:
  plugins: [opencode-wakatime]
hermes:
  skills:
    - git: git@github.com:me/hermes-only.git
`

func loadFull(t *testing.T) *Spec {
	t.Helper()
	s, err := Load(write(t, full, map[string]string{"skills/ours/SKILL.md": "# ours\n"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestViewMergesTheTwoLayers(t *testing.T) {
	s := loadFull(t)

	claude, ok := s.View("claude")
	if !ok {
		t.Fatal("claude has a section but no view")
	}
	names := func(sk []Skill) []string {
		var out []string
		for _, s := range sk {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names(claude.Skills); !slices.Equal(got, []string{"brainstorming", "ours"}) {
		t.Errorf("claude skills = %v", got)
	}
	// A per-agent server of the same name replaces the shared one for that agent.
	if !claude.MCP["context7"].Remote() {
		t.Errorf("claude's context7 was not overridden: %+v", claude.MCP["context7"])
	}
	if claude.MCP["sentry"].URL == "" {
		t.Error("claude lost the shared sentry server")
	}
	if claude.Marketplaces["claude-plugins-official"] != "anthropics/claude-plugins-official" {
		t.Errorf("marketplaces = %v", claude.Marketplaces)
	}

	opencode, _ := s.View("opencode")
	if opencode.MCP["context7"].Remote() {
		t.Error("claude's override leaked into opencode")
	}
	if !slices.Equal(opencode.Plugins, []string{"opencode-wakatime"}) {
		t.Errorf("opencode plugins = %v", opencode.Plugins)
	}

	hermes, _ := s.View("hermes")
	if got := names(hermes.Skills); !slices.Equal(got, []string{"brainstorming", "ours", "hermes-only"}) {
		t.Errorf("hermes skills = %v", got)
	}
}

func TestSkillShapes(t *testing.T) {
	s := loadFull(t)
	v, _ := s.View("claude")

	git := v.Skills[0]
	if git.Git == "" || git.Ref != "v6.4.1" || git.Path != "skills/brainstorming" || git.Archive != nil {
		t.Errorf("git skill = %+v", git)
	}

	local := v.Skills[1]
	if local.Git != "" || local.Archive == nil {
		t.Fatalf("local skill = %+v", local)
	}
	tr := tar.NewReader(bytes.NewReader(local.Archive))
	var files []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, h.Name)
		// Host ownership means nothing in the container.
		if h.Uid != 0 || h.Uname != "" {
			t.Errorf("%s carries host ownership: %d %q", h.Name, h.Uid, h.Uname)
		}
	}
	if !slices.Contains(files, "SKILL.md") {
		t.Errorf("archive holds %v, want SKILL.md at its root", files)
	}

	hermes, _ := s.View("hermes")
	if got := hermes.Skills[2]; got.Name != "hermes-only" || got.Path != "." {
		t.Errorf("repo-root git skill = %+v, want name hermes-only path .", got)
	}
}

func TestViewWithNothingForTheAgent(t *testing.T) {
	s, err := Load(write(t, "version: 1\nclaude:\n  plugins: [a@b]\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.View("hermes"); ok {
		t.Error("hermes got a view from a file that says nothing to it")
	}
	if _, ok := s.View("codex"); ok {
		t.Error("an agent agents.yaml has no section for got a view")
	}

	shared, err := Load(write(t, "version: 1\nmcp:\n  x:\n    command: x\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared.View("hermes"); !ok {
		t.Error("a shared server did not reach hermes")
	}
	if _, ok := shared.View("codex"); ok {
		t.Error("a shared server reached codex, which has no adapter")
	}
}

func TestLoadRejects(t *testing.T) {
	skill := map[string]string{"s/SKILL.md": "x", "noskill/README": "x"}
	cases := []struct{ name, body, want string }{
		{"empty", "", "version"},
		{"no version", "skills: []\n", "version is required"},
		{"wrong version", "version: 2\n", "version 2"},
		{"unknown top key", "version: 1\nplugins: []\n", "plugins"},
		{"codex", "version: 1\ncodex: {}\n", "codex"},
		{"hermes plugins", "version: 1\nhermes:\n  plugins: [x]\n", "plugins"},
		{"opencode marketplaces", "version: 1\nopencode:\n  marketplaces: {a: b}\n", "marketplaces"},
		{"mcp both", "version: 1\nmcp:\n  x: {command: a, url: b}\n", "not both"},
		{"mcp neither", "version: 1\nmcp:\n  x: {args: [a]}\n", "command"},
		{"mcp headers on local", "version: 1\nmcp:\n  x: {command: a, headers: {A: b}}\n", "headers"},
		{"mcp env on remote", "version: 1\nmcp:\n  x: {url: a, env: {A: b}}\n", "env"},
		{"mcp unknown key", "version: 1\nmcp:\n  x: {command: a, cwd: /}\n", "cwd"},
		{"mcp bad name", "version: 1\nmcp:\n  'a b': {command: a}\n", "a b"},
		{"skill neither", "version: 1\nskills:\n  - ref: main\n", "git or path"},
		{"skill ref on local", "version: 1\nskills:\n  - path: ./s\n    ref: main\n", "ref"},
		{"skill escapes repo", "version: 1\nskills:\n  - git: https://x/r\n    path: ../up\n", "inside the repository"},
		{"skill no SKILL.md", "version: 1\nskills:\n  - path: ./noskill\n", "SKILL.md"},
		{"skill missing dir", "version: 1\nskills:\n  - path: ./gone\n", "gone"},
		{"skill dot name", "version: 1\nskills:\n  - git: https://x/.\n", "name"},
		{"duplicate skill", "version: 1\nskills:\n  - path: ./s\nclaude:\n  skills:\n    - git: https://x/s\n", "duplicate"},
		{"duplicate key", "version: 1\nmcp:\n  x: {command: a}\n  x: {command: b}\n", "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := write(t, c.body, skill)
			_, err := Load(p)
			if err == nil {
				t.Fatal("Load accepted it")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
			// Every rejection names the file, so a message printed from deep in
			// create still says where to look.
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

func TestLocalSkillRejectsASymlink(t *testing.T) {
	p := write(t, "version: 1\nskills:\n  - path: ./s\n", map[string]string{"s/SKILL.md": "x"})
	if err := os.Symlink("/etc/passwd", filepath.Join(filepath.Dir(p), "s", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "regular files") {
		t.Errorf("err = %v, want a refusal naming regular files", err)
	}
}

func TestRefs(t *testing.T) {
	s := loadFull(t)
	if got := s.Refs(); !slices.Equal(got, []string{"CONTEXT7_API_KEY", "SENTRY_TOKEN"}) {
		t.Errorf("Refs = %v", got)
	}
}

func TestRewriteRefsLeavesOtherDollarsAlone(t *testing.T) {
	to := func(n string) string { return "{env:" + n + "}" }
	cases := map[string]string{
		"Bearer ${TOKEN}":   "Bearer {env:TOKEN}",
		"${A}-${B_2}":       "{env:A}-{env:B_2}",
		"$HOME/x":           "$HOME/x",
		"$$":                "$$",
		"${1}":              "${1}",
		"${A:-default}":     "${A:-default}",
		"no reference here": "no reference here",
	}
	for in, want := range cases {
		if got := RewriteRefs(in, to); got != want {
			t.Errorf("RewriteRefs(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 4: Run to verify they fail**

Run: `go test ./internal/agentcfg/ ./internal/xpath/`
Expected: FAIL to compile — package `agentcfg` has no non-test files; `ResolveFile` undefined.

- [ ] **Step 5: Implement `ResolveFile`** — append to `internal/xpath/xpath.go`:

```go
// ResolveFile is Resolve for a regular file: the physical path, symlinks
// followed. Used for a file dev reads again later, such as the agents.yaml
// --agent-config names, so a later command finds the same file whatever the
// working directory. A missing file wraps fs.ErrNotExist, which the CLI turns
// into exit 3.
func ResolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a file: %s", real)
	}
	return real, nil
}
```

- [ ] **Step 6: Implement agentcfg** — `internal/agentcfg/agentcfg.go`:

```go
// Package agentcfg reads the agents.yaml a project declares its agents' skills,
// MCP servers and plugins in.
//
// It parses and validates and nothing else: no exec, no cobra, no container.
// Turning a declaration into commands is internal/agent's job. The only
// filesystem access is reading the file and the local skills it names, which
// happens here so that a missing skill is a usage error at create rather than a
// failure halfway through an apply.
package agentcfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/xpath"
	"go.yaml.in/yaml/v3"
)

// FileName is where a project declares its agents, relative to its folder.
const FileName = ".devcontainer/agents.yaml"

// agents are the ids this file has a section for. Codex is in the agent
// registry but deliberately not here, so a codex: section is an unknown key
// rather than a declaration nothing applies.
var agents = []string{"claude", "hermes", "opencode"}

// Skill is one SKILL.md directory to place in an agent's skills directory.
type Skill struct {
	// Name is the directory it lands in, validated as a plain name.
	Name string
	// Git, Ref and Path describe a skill cloned inside the container. Path is
	// the subdirectory holding SKILL.md, "." for the repository root.
	Git  string
	Ref  string
	Path string
	// Archive is a local skill as a tar, read on the host at load. Non-nil
	// exactly when the skill is local.
	Archive []byte
}

// MCPServer is one MCP server: local (Command) or remote (URL), never both.
// Values may carry ${NAME} references; see RewriteRefs.
type MCPServer struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Remote reports whether this is a server reached over the network.
func (m MCPServer) Remote() bool { return m.URL != "" }

// View is what one agent receives: the shared layer plus its own section.
type View struct {
	Skills       []Skill
	MCP          map[string]MCPServer
	Plugins      []string
	Marketplaces map[string]string
}

// Spec is a loaded, validated agents.yaml.
type Spec struct {
	shared   section
	sections map[string]*section
}

type section struct {
	skills       []Skill
	mcp          map[string]MCPServer
	plugins      []string
	marketplaces map[string]string
}

// The raw shapes, decoded with KnownFields so that a key an agent does not have
// — hermes.plugins, opencode.marketplaces, codex — is rejected rather than
// ignored. A typo caught at create beats a plugin that silently never arrives.
type rawSkill struct {
	Git  string `yaml:"git"`
	Ref  string `yaml:"ref"`
	Path string `yaml:"path"`
}

type rawClaude struct {
	Marketplaces map[string]string    `yaml:"marketplaces"`
	Plugins      []string             `yaml:"plugins"`
	Skills       []rawSkill           `yaml:"skills"`
	MCP          map[string]MCPServer `yaml:"mcp"`
}

type rawOpencode struct {
	Plugins []string             `yaml:"plugins"`
	Skills  []rawSkill           `yaml:"skills"`
	MCP     map[string]MCPServer `yaml:"mcp"`
}

type rawHermes struct {
	Skills []rawSkill           `yaml:"skills"`
	MCP    map[string]MCPServer `yaml:"mcp"`
}

type rawFile struct {
	// A pointer so that a missing version is told apart from version: 0.
	Version  *int                 `yaml:"version"`
	Skills   []rawSkill           `yaml:"skills"`
	MCP      map[string]MCPServer `yaml:"mcp"`
	Claude   *rawClaude           `yaml:"claude"`
	Opencode *rawOpencode         `yaml:"opencode"`
	Hermes   *rawHermes           `yaml:"hermes"`
}

// Load reads and validates an agents.yaml. Every error names the file.
func Load(file string) (*Spec, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var raw rawFile
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty; it needs at least \"version: 1\"", file)
		}
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if raw.Version == nil {
		return nil, fmt.Errorf("%s: version is required (version: 1)", file)
	}
	if *raw.Version != 1 {
		return nil, fmt.Errorf("%s: version %d is not one this dev reads (version: 1)", file, *raw.Version)
	}

	// Local skill paths are relative to the file, not to the working
	// directory, so --agent-config ~/dotfiles/agents.yaml carries skills that
	// sit next to it.
	base := filepath.Dir(file)
	s := &Spec{sections: map[string]*section{}}
	if s.shared, err = buildSection(file, base, "", raw.Skills, raw.MCP); err != nil {
		return nil, err
	}
	if c := raw.Claude; c != nil {
		sec, err := buildSection(file, base, "claude.", c.Skills, c.MCP)
		if err != nil {
			return nil, err
		}
		for name := range c.Marketplaces {
			if err := xpath.ValidateName(name); err != nil {
				return nil, fmt.Errorf("%s: claude.marketplaces: %w", file, err)
			}
		}
		sec.plugins, sec.marketplaces = c.Plugins, c.Marketplaces
		s.sections["claude"] = &sec
	}
	if o := raw.Opencode; o != nil {
		sec, err := buildSection(file, base, "opencode.", o.Skills, o.MCP)
		if err != nil {
			return nil, err
		}
		sec.plugins = o.Plugins
		s.sections["opencode"] = &sec
	}
	if h := raw.Hermes; h != nil {
		sec, err := buildSection(file, base, "hermes.", h.Skills, h.MCP)
		if err != nil {
			return nil, err
		}
		s.sections["hermes"] = &sec
	}

	// Checked per agent, across both layers: two skills with one name would
	// land in one directory, and whichever ran second would silently win.
	for _, id := range agents {
		v, ok := s.View(id)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, sk := range v.Skills {
			if seen[sk.Name] {
				return nil, fmt.Errorf("%s: duplicate skill %q for %s", file, sk.Name, id)
			}
			seen[sk.Name] = true
		}
	}
	return s, nil
}

func buildSection(file, base, prefix string, skills []rawSkill, mcp map[string]MCPServer) (section, error) {
	var sec section
	for i, r := range skills {
		sk, err := buildSkill(base, r)
		if err != nil {
			return section{}, fmt.Errorf("%s: %sskills[%d]: %w", file, prefix, i, err)
		}
		sec.skills = append(sec.skills, sk)
	}
	for name, m := range mcp {
		if err := validateMCP(name, m); err != nil {
			return section{}, fmt.Errorf("%s: %smcp.%s: %w", file, prefix, name, err)
		}
	}
	sec.mcp = mcp
	return sec, nil
}

func buildSkill(base string, r rawSkill) (Skill, error) {
	switch {
	case r.Git != "":
		p := r.Path
		if p == "" {
			p = "."
		}
		// IsLocal refuses an absolute path and anything climbing out with
		// "..": the path is spliced into a copy inside the container, and a
		// skill must not be able to name a directory outside its clone.
		if !filepath.IsLocal(p) {
			return Skill{}, fmt.Errorf("path %q must stay inside the repository", r.Path)
		}
		p = filepath.ToSlash(filepath.Clean(p))
		name := path.Base(p)
		if p == "." {
			name = strings.TrimSuffix(path.Base(r.Git), ".git")
		}
		if err := validSkillName(name); err != nil {
			return Skill{}, err
		}
		return Skill{Name: name, Git: r.Git, Ref: r.Ref, Path: p}, nil

	case r.Path != "":
		if r.Ref != "" {
			return Skill{}, errors.New("ref only applies to a git skill")
		}
		p := r.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		// Named from the path as written, not as resolved: a symlinked
		// skills/foo is still "foo" to the operator who wrote it.
		name := filepath.Base(filepath.Clean(p))
		if err := validSkillName(name); err != nil {
			return Skill{}, err
		}
		dir, err := xpath.Resolve(p)
		if err != nil {
			return Skill{}, err
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			return Skill{}, fmt.Errorf("%s has no SKILL.md", r.Path)
		}
		archive, err := tarDir(dir)
		if err != nil {
			return Skill{}, err
		}
		return Skill{Name: name, Archive: archive}, nil

	default:
		return Skill{}, errors.New("give git or path")
	}
}

// validSkillName is xpath.ValidateName minus the two names it accepts that
// would be catastrophic here: the name becomes a directory dev deletes and
// replaces, and "." or ".." there is the skills directory or its parent.
func validSkillName(name string) error {
	if name == "." || name == ".." {
		return fmt.Errorf("skill name %q is not a directory name; give a path ending in one", name)
	}
	return xpath.ValidateName(name)
}

func validateMCP(name string, m MCPServer) error {
	if err := xpath.ValidateName(name); err != nil {
		return err
	}
	switch {
	case m.Command != "" && m.URL != "":
		return errors.New("give command or url, not both")
	case m.Command == "" && m.URL == "":
		return errors.New("give command (a local server) or url (a remote one)")
	case m.Command != "" && len(m.Headers) > 0:
		return errors.New("headers only apply to a remote server")
	case m.URL != "" && (len(m.Args) > 0 || len(m.Env) > 0):
		return errors.New("args and env only apply to a local server")
	}
	return nil
}

// View projects the file onto one agent. False when nothing in it reaches that
// agent, which is what spares a container a probe for an agent the file never
// mentions.
func (s *Spec) View(agent string) (View, bool) {
	if !slices.Contains(agents, agent) {
		return View{}, false
	}
	sec := s.sections[agent]
	if sec == nil && len(s.shared.skills) == 0 && len(s.shared.mcp) == 0 {
		return View{}, false
	}
	v := View{MCP: map[string]MCPServer{}}
	v.Skills = append(v.Skills, s.shared.skills...)
	maps.Copy(v.MCP, s.shared.mcp)
	if sec != nil {
		v.Skills = append(v.Skills, sec.skills...)
		// After the shared layer: a per-agent server of the same name
		// replaces it for this agent, the one override the format allows.
		maps.Copy(v.MCP, sec.mcp)
		v.Plugins = sec.plugins
		v.Marketplaces = sec.marketplaces
	}
	return v, true
}

// refPattern is a ${NAME} reference: an environment variable name in braces
// and nothing else. $NAME, ${1} and ${A:-b} are deliberately not matched —
// each agent resolves only the plain form, so rewriting anything more would
// promise a default no agent implements.
var refPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Refs lists the variables the file's MCP servers reference, for warning about
// ones no workspace setting defines.
func (s *Spec) Refs() []string {
	seen := map[string]bool{}
	collect := func(v string) {
		for _, m := range refPattern.FindAllStringSubmatch(v, -1) {
			seen[m[1]] = true
		}
	}
	all := []section{s.shared}
	for _, sec := range s.sections {
		all = append(all, *sec)
	}
	for _, sec := range all {
		for _, m := range sec.mcp {
			collect(m.URL)
			for _, a := range m.Args {
				collect(a)
			}
			for _, v := range m.Env {
				collect(v)
			}
			for _, v := range m.Headers {
				collect(v)
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// RewriteRefs replaces every ${NAME} in s with to(NAME). Adapters use it to
// spell a reference the way their agent resolves one; dev never substitutes a
// value, which is what keeps tokens out of the file, the database and the
// state volume.
func RewriteRefs(s string, to func(name string) string) string {
	return refPattern.ReplaceAllStringFunc(s, func(m string) string {
		return to(refPattern.FindStringSubmatch(m)[1])
	})
}
```

`internal/agentcfg/archive.go`:

```go
package agentcfg

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// tarDir archives a local skill directory, its contents at the archive root.
//
// Regular files and directories only. A symlink would be followed on the host
// or recreated in the container, and either way a skill could carry a pointer
// to something that is not part of it.
func tarDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("%s: only regular files and directories can be copied into a container", p)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		// A host uid means nothing in the container, and extracting as a user
		// who does not own it is what makes tar warn. Zeroed, and extracted
		// with -o so the files belong to whoever runs the extraction.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 7: Run to verify they pass**

Run: `go mod tidy && go test ./internal/agentcfg/ ./internal/xpath/ -v`
Expected: PASS. If a `TestLoadRejects` case fails only on its `want` substring, read the actual yaml.v3 message and adjust the *test's* substring to a word the message genuinely contains (e.g. yaml.v3 reports an unknown field as `field plugins not found in type agentcfg.rawHermes`); do not weaken the file-name assertion.

Run: `make lint && make test`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/agentcfg internal/xpath
git commit -m "feat(agentcfg): parse and validate a project's agents.yaml"
```

---

### Task 3: Steps, the apply loop, skills and Claude Code

**Files:**
- Create: `internal/agent/step.go`, `internal/agent/skills.go`, `internal/agent/claude.go`
- Modify: `internal/agent/agent.go`
- Test: `internal/agent/step_test.go`, `internal/agent/claude_test.go`

**Interfaces:**
- Consumes: `agentcfg.Spec`, `agentcfg.View`, `agentcfg.Skill`, `agentcfg.MCPServer` (Task 2).
- Produces:
  - `Agent.Configure func(v agentcfg.View) ([]Step, error)` — nil for an agent without an adapter (codex).
  - `func agent.Configurable() []Agent` — agents with `Configure`, sorted by ID.
  - `type agent.Step struct { Desc string; Cmd []string; Stdin []byte; Check []string; Done func(stdout []byte) bool; File string; Edit func(current []byte) ([]byte, error) }`
  - `type agent.Exec func(cmd []string, stdin []byte) (stdout []byte, err error)`
  - `func agent.Run(s Step, run Exec) error`
  - `func agent.Apply(spec *agentcfg.Spec, run Exec, progress, warn func(string)) error`
  - unexported, used by Task 4: `func skillSteps(agentID, dir string, skills []agentcfg.Skill) []Step`, `func sortedKeys[V any](m map[string]V) []string`.

- [ ] **Step 1: Write the failing tests** — `internal/agent/step_test.go`:

```go
package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// fakeExec records every command and answers from a table of substring →
// reply. The first matching rule wins; no match succeeds with empty stdout.
type fakeExec struct {
	calls []string
	stdin [][]byte
	rules []rule
}

type rule struct {
	match  string
	stdout string
	err    error
}

func (f *fakeExec) run(cmd []string, stdin []byte) ([]byte, error) {
	line := strings.Join(cmd, " ")
	f.calls = append(f.calls, line)
	f.stdin = append(f.stdin, stdin)
	for _, r := range f.rules {
		if strings.Contains(line, r.match) {
			return []byte(r.stdout), r.err
		}
	}
	return nil, nil
}

func TestRunSkipsAStepItsCheckSaysIsDone(t *testing.T) {
	f := &fakeExec{rules: []rule{{match: "check", stdout: "yes"}}}
	s := Step{Desc: "d", Cmd: []string{"do"}, Check: []string{"check"},
		Done: func(out []byte) bool { return string(out) == "yes" }}
	if err := Run(s, f.run); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.calls, "do") {
		t.Errorf("ran a step already done: %v", f.calls)
	}
}

func TestRunVerifiesAfterwards(t *testing.T) {
	f := &fakeExec{rules: []rule{{match: "check", stdout: "no"}}}
	s := Step{Desc: "claude: marketplace m", Cmd: []string{"do"}, Check: []string{"check"},
		Done: func(out []byte) bool { return string(out) == "yes" }}
	err := Run(s, f.run)
	if err == nil || !strings.Contains(err.Error(), "claude: marketplace m") {
		t.Fatalf("err = %v, want a failure naming the step", err)
	}
	if !slices.Equal(f.calls, []string{"check", "do", "check"}) {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestRunNamesAFailingStep(t *testing.T) {
	f := &fakeExec{rules: []rule{{match: "do", err: errors.New("exit 1: boom")}}}
	err := Run(Step{Desc: "claude: plugin p", Cmd: []string{"do"}}, f.run)
	if err == nil || !strings.Contains(err.Error(), "claude: plugin p") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestRunEditsAFile(t *testing.T) {
	f := &fakeExec{rules: []rule{{match: "cat \"$f\"; fi", stdout: "old"}}}
	s := Step{Desc: "d", File: "${X:-$HOME}/f.json",
		Edit: func(cur []byte) ([]byte, error) { return append(cur, "+new"...), nil }}
	if err := Run(s, f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %v, want a read and a write", f.calls)
	}
	if !strings.Contains(f.calls[0], "f=${X:-$HOME}/f.json") {
		t.Errorf("read did not expand the path expression: %s", f.calls[0])
	}
	if string(f.stdin[1]) != "old+new" {
		t.Errorf("wrote %q, want %q", f.stdin[1], "old+new")
	}
}

func specFrom(t *testing.T, body string) *agentcfg.Spec {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agents.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := agentcfg.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestApplySkipsAnAgentThatIsNotInstalled(t *testing.T) {
	spec := specFrom(t, "version: 1\nmcp:\n  x:\n    command: x\n")
	f := &fakeExec{rules: []rule{{match: "command -v hermes", err: errors.New("exit 1")}}}
	var warned []string
	if err := Apply(spec, f.run, func(string) {}, func(m string) { warned = append(warned, m) }); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "hermes is not installed") {
		t.Errorf("warnings = %v", warned)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "HERMES_HOME") {
			t.Errorf("ran a hermes step anyway: %s", c)
		}
	}
	// The other two still got theirs.
	if !slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "claude mcp add-json") }) {
		t.Errorf("claude was skipped too: %v", f.calls)
	}
}

func TestApplyStopsAtTheFirstFailure(t *testing.T) {
	spec := specFrom(t, "version: 1\nclaude:\n  plugins: [a@m, b@m]\n")
	f := &fakeExec{rules: []rule{{match: "install --scope user a@m", err: errors.New("exit 1")}}}
	if err := Apply(spec, f.run, func(string) {}, func(string) {}); err == nil {
		t.Fatal("Apply succeeded past a failed step")
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "b@m") }) {
		t.Errorf("carried on after the failure: %v", f.calls)
	}
}

func TestConfigurableIsSortedAndLeavesOutCodex(t *testing.T) {
	var ids []string
	for _, a := range Configurable() {
		ids = append(ids, a.ID)
	}
	if !slices.Equal(ids, []string{"claude", "hermes", "opencode"}) {
		t.Errorf("Configurable = %v", ids)
	}
}

func TestSkillStepsForALocalSkill(t *testing.T) {
	steps := skillSteps("claude", "${D:-$HOME/.claude}/skills",
		[]agentcfg.Skill{{Name: "ours", Archive: []byte("tar")}})
	if len(steps) != 1 {
		t.Fatalf("steps = %+v", steps)
	}
	script := steps[0].Cmd[2]
	if !strings.Contains(script, `d=${D:-$HOME/.claude}/skills/ours`) ||
		!strings.Contains(script, `rm -rf "$d"`) || !strings.Contains(script, "tar -xo") {
		t.Errorf("script = %s", script)
	}
	if string(steps[0].Stdin) != "tar" {
		t.Error("the archive is not on stdin")
	}
}

func TestSkillStepsForAGitSkill(t *testing.T) {
	steps := skillSteps("hermes", "${H:-$HOME/.hermes}/skills", []agentcfg.Skill{
		{Name: "brainstorming", Git: "https://x/sp", Ref: "v1", Path: "skills/brainstorming"},
		{Name: "r", Git: "https://x/r", Path: "."},
	})
	withRef, noRef := steps[0].Cmd, steps[1].Cmd
	// The URL, subdirectory and ref arrive as positional arguments, never
	// spliced into the script, since agents.yaml is not dev's to trust.
	if !slices.Equal(withRef[3:], []string{"sh", "https://x/sp", "skills/brainstorming", "v1"}) {
		t.Errorf("args = %v", withRef[3:])
	}
	if !strings.Contains(withRef[2], `--branch "$3"`) || strings.Contains(noRef[2], "--branch") {
		t.Errorf("--branch handling: %q / %q", withRef[2], noRef[2])
	}
	if strings.Contains(withRef[2], "https://x/sp") {
		t.Error("the URL was spliced into the script")
	}
}
```

`internal/agent/claude_test.go`:

```go
package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

func TestClaudeSteps(t *testing.T) {
	v := agentcfg.View{
		Marketplaces: map[string]string{"official": "anthropics/claude-plugins-official"},
		Plugins:      []string{"superpowers@official"},
		MCP: map[string]agentcfg.MCPServer{
			"ctx": {Command: "npx", Args: []string{"-y", "ctx"}, Env: map[string]string{"K": "${K}"}},
			"web": {URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer ${T}"}},
		},
		Skills: []agentcfg.Skill{{Name: "ours", Archive: []byte("t")}},
	}
	steps, err := configureClaude(v)
	if err != nil {
		t.Fatal(err)
	}
	var descs []string
	for _, s := range steps {
		descs = append(descs, s.Desc)
	}
	want := []string{
		"claude: marketplace official",
		"claude: plugin superpowers@official",
		"claude: mcp ctx (clear any old definition)",
		"claude: mcp ctx",
		"claude: mcp web (clear any old definition)",
		"claude: mcp web",
		"claude: skill ours",
	}
	if !slices.Equal(descs, want) {
		t.Fatalf("steps =\n%s\nwant\n%s", strings.Join(descs, "\n"), strings.Join(want, "\n"))
	}

	mkt := steps[0]
	if !slices.Equal(mkt.Cmd, []string{"claude", "plugin", "marketplace", "add", "anthropics/claude-plugins-official"}) {
		t.Errorf("marketplace cmd = %v", mkt.Cmd)
	}
	listed := `[{"name":"official","source":"github"}]`
	if !mkt.Done([]byte(listed)) || mkt.Done([]byte(`[]`)) || mkt.Done([]byte(`not json`)) {
		t.Error("marketplace Done misreads the list")
	}

	if !slices.Equal(steps[1].Cmd, []string{"claude", "plugin", "install", "--scope", "user", "superpowers@official"}) {
		t.Errorf("install cmd = %v", steps[1].Cmd)
	}

	// Removal is allowed to fail — there is nothing to remove the first time.
	if !strings.Contains(steps[2].Cmd[2], "|| true") || steps[2].Cmd[4] != "ctx" {
		t.Errorf("remove cmd = %v", steps[2].Cmd)
	}

	add := steps[3].Cmd
	if !slices.Equal(add[:6], []string{"claude", "mcp", "add-json", "--scope", "user", "ctx"}) {
		t.Errorf("add cmd = %v", add)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(add[6]), &doc); err != nil {
		t.Fatal(err)
	}
	// References pass through: Claude resolves ${K} itself, from the
	// environment dev launches it with.
	if doc["type"] != "stdio" || doc["command"] != "npx" || doc["env"].(map[string]any)["K"] != "${K}" {
		t.Errorf("stdio doc = %v", doc)
	}

	if err := json.Unmarshal([]byte(steps[5].Cmd[6]), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "http" || doc["url"] != "https://x/mcp" {
		t.Errorf("http doc = %v", doc)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/agent/`
Expected: FAIL to compile — `Run`, `Apply`, `Step`, `Configurable`, `skillSteps`, `configureClaude` undefined.

- [ ] **Step 3: Extend the registry** — in `internal/agent/agent.go`, add the import `"github.com/duy0611/dev-cli/internal/agentcfg"`, add a field to `Agent` after `Args`:

```go
	// Configure turns this agent's view of an agents.yaml into the steps
	// that apply it inside a container. Nil for an agent the file has no
	// section for, which is what keeps it out of Configurable.
	Configure func(v agentcfg.View) ([]Step, error)
```

replace the registry with:

```go
var registry = map[string]Agent{
	"claude":   {ID: "claude", Binary: "claude", Configure: configureClaude},
	"opencode": {ID: "opencode", Binary: "opencode", Configure: configureOpencode},
	"codex":    {ID: "codex", Binary: "codex"},
	"hermes":   {ID: "hermes", Binary: "hermes", Configure: configureHermes},
}
```

and append:

```go
// Configurable lists the agents an agents.yaml can configure, sorted, so an
// apply runs in the same order every time and its output reads the same.
func Configurable() []Agent {
	var out []Agent
	for _, id := range IDs() {
		if a := registry[id]; a.Configure != nil {
			out = append(out, a)
		}
	}
	return out
}
```

Task 4 defines `configureOpencode` and `configureHermes`. So this task compiles on its own, add temporary stubs at the bottom of `claude.go` (Task 4 step 3 deletes them):

```go
// Replaced in the next task; here only so the registry compiles.
func configureOpencode(agentcfg.View) ([]Step, error) { return nil, nil }
func configureHermes(agentcfg.View) ([]Step, error)   { return nil, nil }
```

- [ ] **Step 4: Implement steps and the loop** — `internal/agent/step.go`:

```go
package agent

import (
	"fmt"
	"maps"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// Step is one thing to do inside a container to apply an agents.yaml.
//
// Plain data rather than a function that runs itself, so that an adapter is
// tested by looking at what it would do, without a container or a stub. Exactly
// one of Cmd or File is set.
type Step struct {
	// Desc names the step in progress output and in the error when it fails.
	Desc string
	// Cmd runs with Stdin on its standard input when Stdin is non-nil.
	Cmd   []string
	Stdin []byte
	// Check runs before Cmd; if Done says its stdout shows the work is
	// already there, Cmd is skipped. It runs again after Cmd, and a false
	// answer then fails the step: a command that exits 0 without doing what
	// it was asked is still a failure.
	Check []string
	Done  func(stdout []byte) bool
	// File and Edit make the step a read-modify-write of one file. File is a
	// shell expression — "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/…" —
	// so a container without the state volume gets the agent's default
	// location with no branch in dev. A file that does not exist reads as
	// empty.
	File string
	Edit func(current []byte) ([]byte, error)
}

// Exec runs a command inside the container and returns its stdout. An error
// should carry the command's stderr, since it is what the operator will read.
type Exec func(cmd []string, stdin []byte) (stdout []byte, err error)

// Run carries out one step.
func Run(s Step, run Exec) error {
	if s.Check != nil {
		// A failing check is not fatal: the list it reads may not exist yet.
		// Cmd runs, and the check afterwards is the one that has to pass.
		if out, err := run(s.Check, nil); err == nil && s.Done(out) {
			return nil
		}
	}
	if s.File != "" {
		cur, err := run(readFileCmd(s.File), nil)
		if err != nil {
			return fmt.Errorf("%s: reading: %w", s.Desc, err)
		}
		next, err := s.Edit(cur)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Desc, err)
		}
		if _, err := run(writeFileCmd(s.File), next); err != nil {
			return fmt.Errorf("%s: writing: %w", s.Desc, err)
		}
		return nil
	}
	if _, err := run(s.Cmd, s.Stdin); err != nil {
		return fmt.Errorf("%s: %w", s.Desc, err)
	}
	if s.Check != nil {
		if out, err := run(s.Check, nil); err != nil || !s.Done(out) {
			return fmt.Errorf("%s: the command succeeded but the change is not visible afterwards", s.Desc)
		}
	}
	return nil
}

// readFileCmd and writeFileCmd splice File in unquoted, which is safe only
// because File is always built by an adapter from constants and never from
// agents.yaml: it has to be unquoted for ${VAR:-default} to expand. The
// assignment is not word-split, so a $HOME with spaces still works.
func readFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; if [ -f "$f" ]; then cat "$f"; fi`}
}

func writeFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; mkdir -p "$(dirname "$f")" && cat > "$f"`}
}

// Apply runs every configurable agent's steps for spec. An agent the file
// reaches but the container does not have is a warning and a skip: one file
// has to work across containers with different tool sets. Anything that fails
// after that stops the apply.
func Apply(spec *agentcfg.Spec, run Exec, progress, warn func(string)) error {
	for _, ag := range Configurable() {
		v, ok := spec.View(ag.ID)
		if !ok {
			continue
		}
		// `command -v`, the check start-agent makes: a shell builtin, so it
		// works in images that ship no which(1).
		if _, err := run([]string{"sh", "-c", "command -v " + ag.Binary}, nil); err != nil {
			warn(fmt.Sprintf("agents.yaml: %s is not installed in this container; skipping it", ag.ID))
			continue
		}
		steps, err := ag.Configure(v)
		if err != nil {
			return err
		}
		for _, s := range steps {
			progress(s.Desc)
			if err := Run(s, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// sortedKeys orders a map's keys, so the steps built from it come out the same
// on every run.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
```

- [ ] **Step 5: Implement skill steps** — `internal/agent/skills.go`:

```go
package agent

import "github.com/duy0611/dev-cli/internal/agentcfg"

// skillSteps places each skill at <dir>/<name>, replacing whatever that one
// directory held. Directories the file does not name are left alone, so a
// skill the operator added by hand survives an apply.
//
// dir is an adapter's constant shell expression and sk.Name passed
// validSkillName — letters, digits, . _ - and never "." or ".." — so both are
// safe to splice. Everything that came from agents.yaml as free text (a URL, a
// ref, a subdirectory) is passed as a positional argument instead.
func skillSteps(agentID, dir string, skills []agentcfg.Skill) []Step {
	var out []Step
	for _, sk := range skills {
		target := dir + "/" + sk.Name
		desc := agentID + ": skill " + sk.Name
		if sk.Archive != nil {
			out = append(out, Step{
				Desc: desc,
				// -o: do not restore ownership from the archive, so the
				// files belong to the user running the extraction.
				Cmd:   []string{"sh", "-c", `d=` + target + `; rm -rf "$d" && mkdir -p "$d" && tar -xo -C "$d"`},
				Stdin: sk.Archive,
			})
			continue
		}
		branch := ""
		if sk.Ref != "" {
			branch = ` --branch "$3"`
		}
		// Cloned per agent into a temporary directory the trap removes. A
		// shallow clone is cheap, and sharing one across agents would need
		// state carried between steps that are otherwise independent.
		script := `set -e; t=$(mktemp -d); trap 'rm -rf "$t"' EXIT; ` +
			`git clone -q --depth 1` + branch + ` -- "$1" "$t/r"; ` +
			`[ -f "$t/r/$2/SKILL.md" ] || { echo "no SKILL.md at $2 in $1" >&2; exit 1; }; ` +
			`d=` + target + `; rm -rf "$d"; mkdir -p "$(dirname "$d")"; ` +
			`cp -R "$t/r/$2" "$d"; rm -rf "$d/.git"`
		out = append(out, Step{
			Desc: desc,
			Cmd:  []string{"sh", "-c", script, "sh", sk.Git, sk.Path, sk.Ref},
		})
	}
	return out
}
```

Note the positional list always has three entries after `sh`, so `$3` is empty (and unused) when there is no ref; the test in Step 1 checks the with-ref case only. For the no-ref case the trailing `""` is harmless.

- [ ] **Step 6: Implement Claude** — `internal/agent/claude.go` (above the temporary stubs):

```go
package agent

import (
	"encoding/json"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// claudeDir is where Claude Code keeps its configuration: on the state volume
// when the container persists state, its own default otherwise.
const claudeDir = "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

// configureClaude drives Claude's own CLI. enabledPlugins in settings.json
// only enables a plugin that is already installed, so writing settings would
// declare plugins without ever fetching them.
func configureClaude(v agentcfg.View) ([]Step, error) {
	var steps []Step
	for _, name := range sortedKeys(v.Marketplaces) {
		steps = append(steps, Step{
			Desc: "claude: marketplace " + name,
			Cmd:  []string{"claude", "plugin", "marketplace", "add", v.Marketplaces[name]},
			// What `marketplace add` does with one it already knows is
			// undocumented, so it is asked first. Checked again afterwards,
			// which is what catches a key that is not the name the source
			// gives itself — otherwise every rebuild would add it again.
			Check: []string{"claude", "plugin", "marketplace", "list", "--json"},
			Done:  func(out []byte) bool { return listsMarketplace(out, name) },
		})
	}
	for _, p := range v.Plugins {
		// Safe to repeat: installing an installed plugin exits 0. No -y: a
		// plugin whose marketplace declares a command to run at install is
		// refused unattended rather than run without anyone seeing it.
		steps = append(steps, Step{
			Desc: "claude: plugin " + p,
			Cmd:  []string{"claude", "plugin", "install", "--scope", "user", p},
		})
	}
	for _, name := range sortedKeys(v.MCP) {
		doc, err := claudeMCP(v.MCP[name])
		if err != nil {
			return nil, err
		}
		steps = append(steps,
			// add-json refuses a name that exists, so a changed definition
			// needs the old one gone first. Failure is expected the first
			// time, when there is nothing to remove.
			Step{
				Desc: "claude: mcp " + name + " (clear any old definition)",
				Cmd:  []string{"sh", "-c", `claude mcp remove --scope user "$1" >/dev/null 2>&1 || true`, "sh", name},
			},
			Step{
				Desc: "claude: mcp " + name,
				Cmd:  []string{"claude", "mcp", "add-json", "--scope", "user", name, string(doc)},
			},
		)
	}
	return append(steps, skillSteps("claude", claudeDir+"/skills", v.Skills)...), nil
}

// marketplace is the one field dev reads from `marketplace list --json`.
type marketplace struct {
	Name string `json:"name"`
}

// listsMarketplace reports whether the list names this marketplace. Output that
// does not parse reads as "not there", so the step runs and the check after it
// is the one that fails, with the step named.
func listsMarketplace(out []byte, name string) bool {
	var list []marketplace
	if json.Unmarshal(out, &list) != nil {
		return false
	}
	return slices.ContainsFunc(list, func(m marketplace) bool { return m.Name == name })
}

// claudeMCP renders one server as add-json takes it. ${NAME} references pass
// through unchanged: that is Claude's own syntax, resolved when it starts.
func claudeMCP(m agentcfg.MCPServer) ([]byte, error) {
	if m.Remote() {
		return json.Marshal(struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"http", m.URL, m.Headers})
	}
	return json.Marshal(struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}{"stdio", m.Command, m.Args, m.Env})
}
```

- [ ] **Step 7: Run to verify they pass**

Run: `go test ./internal/agent/ -v` then `make lint && make test`
Expected: PASS. `TestApplySkipsAnAgentThatIsNotInstalled` passes with the stub `configureHermes` too, since the skip happens before `Configure` is called.

- [ ] **Step 8: Commit**

```bash
git add internal/agent
git commit -m "feat(agent): turn an agents.yaml into steps, and apply Claude Code's"
```

---

### Task 4: opencode and hermes

**Files:**
- Create: `internal/agent/opencode.go`, `internal/agent/hermes.go`
- Modify: `internal/agent/claude.go` (delete the two temporary stubs)
- Test: `internal/agent/merge_test.go`

**Interfaces:**
- Consumes: `Step`, `skillSteps`, `sortedKeys` (Task 3); `agentcfg.View`, `agentcfg.MCPServer`, `agentcfg.RewriteRefs` (Task 2).
- Produces: `configureOpencode`, `configureHermes` (registered in Task 3); unexported `mergeOpencode(current []byte, plugins []string, servers map[string]agentcfg.MCPServer) ([]byte, error)`, `mergeHermes(current []byte, servers map[string]agentcfg.MCPServer) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests** — `internal/agent/merge_test.go`:

```go
package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"go.yaml.in/yaml/v3"
)

var testServers = map[string]agentcfg.MCPServer{
	"ctx": {Command: "npx", Args: []string{"-y", "ctx", "--key=${K}"}, Env: map[string]string{"K": "${K}", "H": "$HOME"}},
	"web": {URL: "https://x/${REGION}/mcp", Headers: map[string]string{"Authorization": "Bearer ${T}"}},
}

func TestMergeOpencode(t *testing.T) {
	current := []byte(`{"theme": "dark", "plugin": ["mine"], "mcp": {"hand": {"type": "local", "command": ["x"]}}}`)
	out, err := mergeOpencode(current, []string{"opencode-wakatime", "mine"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Theme  string                    `json:"theme"`
		Plugin []string                  `json:"plugin"`
		MCP    map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.Theme != "dark" {
		t.Error("an unowned key did not survive")
	}
	if strings.Join(doc.Plugin, ",") != "mine,opencode-wakatime" {
		t.Errorf("plugin = %v, want the existing one kept and the new one added once", doc.Plugin)
	}
	if doc.MCP["hand"] == nil {
		t.Error("a server added by hand was removed")
	}

	ctx := doc.MCP["ctx"]
	cmd, _ := json.Marshal(ctx["command"])
	if ctx["type"] != "local" || string(cmd) != `["npx","-y","ctx","--key={env:K}"]` {
		t.Errorf("ctx = %v", ctx)
	}
	env := ctx["environment"].(map[string]any)
	if env["K"] != "{env:K}" || env["H"] != "$HOME" {
		t.Errorf("environment = %v", env)
	}

	web := doc.MCP["web"]
	if web["type"] != "remote" || web["url"] != "https://x/{env:REGION}/mcp" ||
		web["headers"].(map[string]any)["Authorization"] != "Bearer {env:T}" {
		t.Errorf("web = %v", web)
	}
}

func TestMergeOpencodeAcceptsJSONC(t *testing.T) {
	current := []byte("{\n  // mine\n  \"theme\": \"dark\",\n}\n")
	out, err := mergeOpencode(current, []string{"p"}, nil)
	if err != nil {
		t.Fatalf("JSONC refused: %v", err)
	}
	if !bytes.Contains(out, []byte(`"theme": "dark"`)) {
		t.Errorf("theme lost:\n%s", out)
	}
}

func TestMergeOpencodeFromNothing(t *testing.T) {
	out, err := mergeOpencode(nil, []string{"p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"plugin"`)) {
		t.Errorf("out = %s", out)
	}
}

func TestMergeOpencodeRejectsAWrongShape(t *testing.T) {
	for _, current := range []string{`[1]`, `{"plugin": "x"}`, `{"mcp": []}`, `{`} {
		if _, err := mergeOpencode([]byte(current), []string{"p"}, testServers); err == nil {
			t.Errorf("accepted %s", current)
		}
	}
}

func TestMergeHermesKeepsCommentsAndOtherKeys(t *testing.T) {
	current := []byte("# my settings\nmodel: hermes-4\nmcp_servers:\n  hand:\n    command: x  # keep me\n")
	out, err := mergeHermes(current, testServers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my settings", "model: hermes-4", "# keep me", "hand:"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("%q lost:\n%s", want, out)
		}
	}
	var doc struct {
		MCP map[string]struct {
			Command string            `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
			URL     string            `yaml:"url"`
			Headers map[string]string `yaml:"headers"`
		} `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	// hermes expands ${NAME} itself when it loads config.yaml.
	if doc.MCP["ctx"].Env["K"] != "${K}" || doc.MCP["web"].Headers["Authorization"] != "Bearer ${T}" {
		t.Errorf("mcp_servers = %+v", doc.MCP)
	}
}

func TestMergeHermesFromNothingAndFromNull(t *testing.T) {
	for _, current := range []string{"", "mcp_servers:\n"} {
		out, err := mergeHermes([]byte(current), testServers)
		if err != nil {
			t.Fatalf("%q: %v", current, err)
		}
		if !bytes.Contains(out, []byte("ctx:")) {
			t.Errorf("%q: out = %s", current, out)
		}
	}
	if _, err := mergeHermes([]byte("mcp_testServers: [1]\n"), testServers); err == nil {
		t.Error("accepted a list where a mapping belongs")
	}
}

// A rebuild re-applies over a volume that already holds the last apply.
func TestMergesAreIdempotent(t *testing.T) {
	once, err := mergeOpencode([]byte(`{"plugin":["a"]}`), []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := mergeOpencode(once, []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("opencode drifted:\n%s\n---\n%s", once, twice)
	}

	h1, err := mergeHermes([]byte("model: x\n"), testServers)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := mergeHermes(h1, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1, h2) {
		t.Errorf("hermes drifted:\n%s\n---\n%s", h1, h2)
	}
}

func TestOpencodeAndHermesSteps(t *testing.T) {
	v := agentcfg.View{Plugins: []string{"p"}, MCP: testServers,
		Skills: []agentcfg.Skill{{Name: "s", Archive: []byte("t")}}}

	o, err := configureOpencode(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(o) != 2 || o[0].File != "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/opencode.json" ||
		!strings.Contains(o[1].Cmd[2], "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/skills/s") {
		t.Errorf("opencode steps = %+v", o)
	}

	h, err := configureHermes(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].File != "${HERMES_HOME:-$HOME/.hermes}/config.yaml" ||
		!strings.Contains(h[1].Cmd[2], "${HERMES_HOME:-$HOME/.hermes}/skills/s") {
		t.Errorf("hermes steps = %+v", h)
	}

	// Nothing to merge means no file is touched at all.
	o, _ = configureOpencode(agentcfg.View{})
	h, _ = configureHermes(agentcfg.View{})
	if len(o) != 0 || len(h) != 0 {
		t.Errorf("empty views produced steps: %+v %+v", o, h)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/agent/ -run 'Merge|OpencodeAndHermes'`
Expected: FAIL to compile — `mergeOpencode`, `mergeHermes` undefined.

- [ ] **Step 3: Delete the stubs** — remove the two temporary functions and their comment from the bottom of `internal/agent/claude.go`.

- [ ] **Step 4: Implement opencode** — `internal/agent/opencode.go`:

```go
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/tailscale/hujson"
)

// opencodeDir is opencode's configuration directory: on the state volume when
// the container persists state, its own default otherwise.
const opencodeDir = "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}"

// configureOpencode merges into opencode.json rather than running a CLI:
// opencode has none for plugins or MCP. Listing a package under plugin is the
// whole job, since opencode installs what that list names when it starts.
func configureOpencode(v agentcfg.View) ([]Step, error) {
	var steps []Step
	if len(v.Plugins) > 0 || len(v.MCP) > 0 {
		steps = append(steps, Step{
			Desc: "opencode: plugins and mcp in opencode.json",
			File: opencodeDir + "/opencode.json",
			Edit: func(cur []byte) ([]byte, error) { return mergeOpencode(cur, v.Plugins, v.MCP) },
		})
	}
	return append(steps, skillSteps("opencode", opencodeDir+"/skills", v.Skills)...), nil
}

// mergeOpencode adds declared plugins and sets declared servers by name,
// leaving every other plugin, server and key where it was — the same per-entry
// promise `claude mcp add-json` makes. Unowned keys are held as raw JSON so
// they round-trip without dev having to understand them.
func mergeOpencode(current []byte, plugins []string, servers map[string]agentcfg.MCPServer) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(current)) > 0 {
		// opencode reads JSONC, so the file may carry comments and trailing
		// commas. Standardize strips them; they do not survive the rewrite,
		// which is the price of a program merging a file whose formatting it
		// does not own. Cloned because Standardize works in place.
		std, err := hujson.Standardize(bytes.Clone(current))
		if err != nil {
			return nil, fmt.Errorf("opencode.json does not parse: %w", err)
		}
		if err := json.Unmarshal(std, &doc); err != nil {
			return nil, fmt.Errorf("opencode.json is not an object: %w", err)
		}
	}

	if len(plugins) > 0 {
		var have []string
		if raw, ok := doc["plugin"]; ok {
			if err := json.Unmarshal(raw, &have); err != nil {
				return nil, fmt.Errorf("opencode.json: plugin is not a list of strings: %w", err)
			}
		}
		for _, p := range plugins {
			if !slices.Contains(have, p) {
				have = append(have, p)
			}
		}
		raw, err := json.Marshal(have)
		if err != nil {
			return nil, err
		}
		doc["plugin"] = raw
	}

	if len(servers) > 0 {
		mcp := map[string]json.RawMessage{}
		if raw, ok := doc["mcp"]; ok {
			if err := json.Unmarshal(raw, &mcp); err != nil {
				return nil, fmt.Errorf("opencode.json: mcp is not an object: %w", err)
			}
		}
		for name, m := range servers {
			raw, err := json.Marshal(opencodeMCP(m))
			if err != nil {
				return nil, err
			}
			mcp[name] = raw
		}
		raw, err := json.Marshal(mcp)
		if err != nil {
			return nil, err
		}
		doc["mcp"] = raw
	}

	// A map marshals with sorted keys, which is what makes a second apply
	// write the same bytes as the first.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// opencodeMCP renders one server in opencode's shape, with ${NAME} rewritten
// to its {env:NAME}. The command itself is not rewritten; the spec recognises
// references only in env, headers, args and url.
func opencodeMCP(m agentcfg.MCPServer) any {
	ref := func(s string) string {
		return agentcfg.RewriteRefs(s, func(n string) string { return "{env:" + n + "}" })
	}
	refs := func(in map[string]string) map[string]string {
		if len(in) == 0 {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			out[k] = ref(v)
		}
		return out
	}
	if m.Remote() {
		return struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"remote", ref(m.URL), refs(m.Headers)}
	}
	// opencode takes the whole argv as one list.
	cmd := []string{m.Command}
	for _, a := range m.Args {
		cmd = append(cmd, ref(a))
	}
	return struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment,omitempty"`
	}{"local", cmd, refs(m.Env)}
}
```

- [ ] **Step 5: Implement hermes** — `internal/agent/hermes.go`:

```go
package agent

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"go.yaml.in/yaml/v3"
)

// hermesDir is HERMES_HOME: on the state volume when the container persists
// state, hermes's own default otherwise.
const hermesDir = "${HERMES_HOME:-$HOME/.hermes}"

// configureHermes merges MCP servers into config.yaml; hermes has no CLI for
// them and no plugins at all. Skills are its extension mechanism.
func configureHermes(v agentcfg.View) ([]Step, error) {
	var steps []Step
	if len(v.MCP) > 0 {
		steps = append(steps, Step{
			Desc: "hermes: mcp_servers in config.yaml",
			File: hermesDir + "/config.yaml",
			Edit: func(cur []byte) ([]byte, error) { return mergeHermes(cur, v.MCP) },
		})
	}
	return append(steps, skillSteps("hermes", hermesDir+"/skills", v.Skills)...), nil
}

// hermesServer is agentcfg.MCPServer with omitempty, so a local server does not
// grow an empty url: and a remote one an empty command:. The same fields, so a
// plain conversion moves between them.
type hermesServer struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// mergeHermes sets declared servers under mcp_servers by name. It edits the
// parsed node tree rather than a decoded map, which is what keeps the
// operator's comments, key order and every other setting. ${NAME} passes
// through: hermes expands it when it loads the file.
func mergeHermes(current []byte, servers map[string]agentcfg.MCPServer) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(current)) > 0 {
		if err := yaml.Unmarshal(current, &doc); err != nil {
			return nil, fmt.Errorf("config.yaml does not parse: %w", err)
		}
	}
	// Empty, or nothing but comments: start from an empty mapping.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("config.yaml is not a mapping")
	}
	mcp, err := mappingUnder(root, "mcp_servers")
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(servers) {
		var val yaml.Node
		if err := val.Encode(hermesServer(servers[name])); err != nil {
			return nil, err
		}
		setKey(mcp, name, &val)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mappingUnder returns the mapping at key in m, creating it when absent. A key
// with no value ("mcp_servers:") parses as null and becomes an empty mapping.
func mappingUnder(m *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		v := m.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			*v = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config.yaml: %s is not a mapping", key)
		}
		return v, nil
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v, nil
}

// setKey replaces key's value in mapping m, or appends the pair.
func setKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}
```

- [ ] **Step 6: Run to verify they pass**

Run: `go test ./internal/agent/ -v` then `make lint && make test`
Expected: PASS. If `TestMergeHermesKeepsCommentsAndOtherKeys` fails only because yaml.v3 moved the `# keep me` line comment on the replaced-or-kept `hand` entry, check whether `hand` was replaced (it must not be — it is not declared); a comment on an *untouched* node must survive.

- [ ] **Step 7: Commit**

```bash
git add internal/agent
git commit -m "feat(agent): merge opencode and hermes MCP servers and plugins into their config"
```

---

### Task 5: CLI — choosing, loading and applying the file

**Files:**
- Create: `internal/cli/agentconfig.go`
- Test: `internal/cli/agentconfig_test.go`

**Interfaces:**
- Consumes: `model.Container.AgentConfig`/`AgentConfigPending`, `store.SetAgentConfigPending` (Task 1); `xpath.ResolveFile`, `agentcfg.Load`, `agentcfg.FileName`, `(*Spec).Refs` (Task 2); `agent.Apply`, `agent.Exec` (Task 3).
- Produces (all in package `cli`):
  - `const agentConfigNone = "none"`
  - `func agentConfigChoice(path string, none bool) (string, error)` — exit 2 for both flags, exit 3 for a missing path.
  - `func agentConfigFile(c model.Container) (string, error)` — `""` when there is none; exit 3 when a stored explicit path has gone.
  - `func loadAgentConfig(c model.Container) (*agentcfg.Spec, error)` — nil spec when none; exit 2 for a bad file.
  - `func markAgentConfig(c *model.Container) error` — validates the file `c.AgentConfig` leads to and sets `c.AgentConfigPending`.
  - `func (a *app) applyAgentConfig(ctx context.Context, p provider.Provider, c model.Container, environ []provider.EnvVar, spec *agentcfg.Spec) error`

- [ ] **Step 1: Write the failing tests** — `internal/cli/agentconfig_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run 'AgentConfig'`
Expected: FAIL to compile — `agentConfigChoice`, `agentConfigFile`, `markAgentConfig`, `agentConfigNone` undefined.

- [ ] **Step 3: Implement** — `internal/cli/agentconfig.go`:

```go
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// agentConfigNone is what a container created with --no-agent-config stores,
// so that a later rebuild does not pick up a project file it opted out of.
const agentConfigNone = "none"

// agentConfigChoice turns create's two flags into what the row stores.
func agentConfigChoice(path string, none bool) (string, error) {
	switch {
	case path != "" && none:
		return "", usageErrorf("--agent-config and --no-agent-config contradict each other")
	case none:
		return agentConfigNone, nil
	case path == "":
		return "", nil
	}
	// Resolved now, so a rebuild run from another directory reads the same
	// file.
	resolved, err := xpath.ResolveFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", notFoundErrorf("agent config %s does not exist", path)
	}
	if err != nil {
		return "", usageError(err)
	}
	return resolved, nil
}

// agentConfigFile returns the agents.yaml c applies, or "" when it has none.
func agentConfigFile(c model.Container) (string, error) {
	switch c.AgentConfig {
	case agentConfigNone:
		return "", nil
	case "":
		// Re-derived every time rather than stored: the project can gain or
		// lose the file, and the next rebuild should see which.
		if c.SourceKind != model.SourceFolder {
			return "", nil
		}
		p := filepath.Join(c.Source, agentcfg.FileName)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
		return "", nil
	}
	// A file the operator named explicitly going missing is not "no file":
	// quietly applying nothing would leave a rebuilt container without what it
	// was created with.
	if _, err := os.Stat(c.AgentConfig); errors.Is(err, fs.ErrNotExist) {
		return "", notFoundErrorf("agent config %s no longer exists; restore it, or remove the container and create it again",
			c.AgentConfig)
	} else if err != nil {
		return "", err
	}
	return c.AgentConfig, nil
}

// loadAgentConfig reads and validates c's agents.yaml; nil when it has none.
func loadAgentConfig(c model.Container) (*agentcfg.Spec, error) {
	file, err := agentConfigFile(c)
	if err != nil || file == "" {
		return nil, err
	}
	spec, err := agentcfg.Load(file)
	if err != nil {
		return nil, usageError(err)
	}
	return spec, nil
}

// markAgentConfig validates the file c.AgentConfig leads to and records
// whether applying it is owed. Called before the row is written, so a typo in
// agents.yaml is exit 2 with nothing created.
func markAgentConfig(c *model.Container) error {
	spec, err := loadAgentConfig(*c)
	if err != nil {
		return err
	}
	c.AgentConfigPending = spec != nil
	return nil
}

// applyAgentConfig applies spec inside the running container. A nil spec only
// clears a pending flag that no longer has anything behind it.
//
// The flag is set before the first step and cleared after the last, so an
// apply that fails or is interrupted is retried by the next start.
func (a *app) applyAgentConfig(ctx context.Context, p provider.Provider, c model.Container,
	environ []provider.EnvVar, spec *agentcfg.Spec) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	if spec == nil {
		if c.AgentConfigPending {
			return st.SetAgentConfigPending(c.WorkspaceName, c.Name, false)
		}
		return nil
	}

	// A warning, not an error: the operator may set it after create, and the
	// agent resolves the reference each time it starts.
	defined := make(map[string]bool, len(environ))
	for _, e := range environ {
		defined[e.Key] = true
	}
	for _, name := range spec.Refs() {
		if !defined[name] {
			warnf(a, "agents.yaml references ${%s}, which no workspace setting defines; "+
				"set it with: dev workspace set %s SPEC", name, name)
		}
	}

	if err := st.SetAgentConfigPending(c.WorkspaceName, c.Name, true); err != nil {
		return err
	}
	// The same environment every other exec gets, so the steps see
	// CLAUDE_CONFIG_DIR and its siblings and write onto the state volume.
	run := func(cmd []string, stdin []byte) ([]byte, error) {
		var stdout, stderr bytes.Buffer
		opts := provider.ExecOpts{Env: environ, Stdout: &stdout, Stderr: &stderr}
		if stdin != nil {
			opts.Stdin = bytes.NewReader(stdin)
		}
		if err := p.Exec(ctx, c, cmd, opts); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, err
		}
		return stdout.Bytes(), nil
	}
	if err := agent.Apply(spec, run,
		func(desc string) { a.printf("agents.yaml: %s\n", desc) },
		func(msg string) { warnf(a, "%s", msg) },
	); err != nil {
		return fmt.Errorf("applying agents.yaml to %s: %w (the container is running; "+
			"the next dev container start retries)", c.Name, err)
	}
	return st.SetAgentConfigPending(c.WorkspaceName, c.Name, false)
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/cli/ -run 'AgentConfig' -v` then `make lint && make test`
Expected: PASS for `go test`. `make lint` may report `applyAgentConfig` as unused, since nothing calls it until Task 6. If it does, do not add a caller or a `nolint` here: go on to Task 6 and commit both tasks together at its end.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/agentconfig.go internal/cli/agentconfig_test.go
git commit -m "feat(cli): choose, validate and apply a container's agents.yaml"
```

---

### Task 6: CLI — flags on `create`, applying on `start` and `start-agent`

**Files:**
- Modify: `internal/cli/container.go` (`createOpts`, `newContainerCreateCmd`, `runContainerCreate`, `(*app).start`)
- Modify: `internal/cli/worktree.go` (`newWorktreeCreateCmd`, `runWorktreeCreate`, `createWorktreeRows`)
- Modify: `internal/cli/agent.go` (`runContainerStartAgent`)
- Test: `internal/cli/agentconfig_test.go` (append)

**Interfaces:**
- Consumes: everything Task 5 produces.
- Produces: `createOpts.agentConfig string`, `createOpts.noAgentConfig bool`; flags `--agent-config PATH` and `--no-agent-config` on `dev container create` and `dev worktree create`.

- [ ] **Step 1: Write the failing tests** — append to `internal/cli/agentconfig_test.go`. They need no imports beyond the ones Task 5 gave the file; `captureStderr`, `requireGit`, `initRepo`, `baseOpts`, `projectWithConfig` and `execute` already exist in the package's tests.

```go
// stubDevcontainer puts a devcontainer on PATH that logs each call as one line
// and succeeds — except a call whose arguments contain failOn, which prints
// "boom" to stderr and exits 1. Up and every exec go through it, so the log is
// the sequence of steps an apply ran.
func stubDevcontainer(t *testing.T, failOn string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n"
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

// projectWithAgents is a project that ships a devcontainer.json and an
// agents.yaml with one local skill beside it.
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
	if strings.Contains(callsIn(t, log), "claude") {
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

	// A second start owes nothing, so it runs nothing.
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
	// included. config.yaml appears only in hermes's own step.
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
	if strings.Contains(callsIn(t, log), "claude") {
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

func TestWorktreeCreateRecordsTheAgentConfigChoice(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	opts := baseOpts(repo, path)
	opts.create.noAgentConfig = true
	if err := runWorktreeCreate(t.Context(), a, "", "feat", opts); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if c := containerRow(t, a, "feat"); c.AgentConfig != agentConfigNone {
		t.Errorf("AgentConfig = %q, want %q", c.AgentConfig, agentConfigNone)
	}
}
```

`TestCreateAgentConfigFlagErrors` runs through `execute`, which has no workspace; the flag checks must therefore come before the workspace lookup in `runContainerCreate` (Step 4 places them there). If the missing-file case exits 3 for the wrong reason (no workspace), the ordering is wrong.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run 'Create|AgentsFile|NotInstalled|FailingStep|WorktreeCreateRecords'`
Expected: FAIL to compile — `createOpts` has no field `agentConfig` / `noAgentConfig`.

- [ ] **Step 3: Add the options and flags** — in `internal/cli/container.go`, add to `createOpts`:

```go
	// agentConfig is --agent-config: an agents.yaml to apply instead of the
	// project's own. noAgentConfig is --no-agent-config: apply none.
	agentConfig   string
	noAgentConfig bool
```

In `newContainerCreateCmd`, append to the `Long` text (before the closing quote of its last line, keeping the `+` chain):

```go
			"\n\nA project's .devcontainer/agents.yaml, when it has one, declares its\n" +
			"agents' skills, MCP servers and plugins; dev applies it inside the\n" +
			"container once it is running, and again on every rebuild. --agent-config\n" +
			"names another file, --no-agent-config applies none.",
```

and register the flags after `--no-persist-state`:

```go
	cmd.Flags().StringVar(&opts.agentConfig, "agent-config", "",
		"apply this agents.yaml instead of the project's .devcontainer/agents.yaml")
	cmd.Flags().BoolVar(&opts.noAgentConfig, "no-agent-config", false,
		"apply no agents.yaml, even if the project has one")
```

In `internal/cli/worktree.go` `newWorktreeCreateCmd`, register the same two flags against `opts.create.agentConfig` / `opts.create.noAgentConfig` after its `--no-persist-state`.

- [ ] **Step 4: Wire `create`** — in `runContainerCreate`, directly after the `--tools only applies with --generate` check and before `a.workspaceName(workspace)`:

```go
	// Before anything else is resolved, so a bad flag or a missing file is
	// reported as itself rather than as whatever fails first after it.
	agentConfig, err := agentConfigChoice(opts.agentConfig, opts.noAgentConfig)
	if err != nil {
		return err
	}
```

The existing `wsName, err := a.workspaceName(workspace)` stays as it is: `wsName` is new, so `:=` still compiles.

In the `model.Container{...}` literal add `AgentConfig: agentConfig,` after `PersistState`, and between the literal and `st.CreateContainer(c)` insert:

```go
	// After the source is known, since the default file lives in it, and
	// before the row is written, so a malformed agents.yaml creates nothing.
	if err := markAgentConfig(&c); err != nil {
		return err
	}
```

- [ ] **Step 5: Wire `worktree create`** — in `runWorktreeCreate`, after the `--path is required` check:

```go
	// Checked before the checkout exists, so a bad flag leaves nothing to roll
	// back. createWorktreeRows repeats it to get the value; it is cheap.
	if _, err := agentConfigChoice(opts.create.agentConfig, opts.create.noAgentConfig); err != nil {
		return err
	}
```

In `createWorktreeRows`, before `st, err := a.store()`:

```go
	agentConfig, err := agentConfigChoice(opts.create.agentConfig, opts.create.noAgentConfig)
	if err != nil {
		return err
	}
```

add `AgentConfig: agentConfig,` to its `model.Container` literal, and before `st.CreateContainer(c)`:

```go
	// A failure here is returned before the row exists, and the caller rolls
	// the checkout back, the same as for a bad devcontainer config.
	if err := markAgentConfig(&c); err != nil {
		return err
	}
```

- [ ] **Step 6: Apply on `start`** — in `(*app).start` in `internal/cli/container.go`, replace

```go
	if err := p.Up(ctx, c, environ); err != nil {
		return err
	}
	a.printf("container %s is running\n", c.Name)
	return nil
```

with

```go
	if err := p.Up(ctx, c, environ); err != nil {
		return err
	}
	a.printf("container %s is running\n", c.Name)

	// Only when owed: at create, after `create --no-start`, or after an apply
	// that failed. A plain start must not re-apply a file whose edits are
	// meant to wait for a rebuild, the same contract devcontainer.json has.
	if !c.AgentConfigPending {
		return nil
	}
	spec, err := loadAgentConfig(c)
	if err != nil {
		return err
	}
	return a.applyAgentConfig(ctx, p, c, environ, spec)
```

- [ ] **Step 7: Apply on `start-agent`** — in `runContainerStartAgent` in `internal/cli/agent.go`, directly after the `if status != model.StatusRunning { ... Up ... }` block and before `ensureAgentPresent`:

```go
	// start-agent can be the first thing to start a container created with
	// --no-start, and the agent it is about to launch is what the file
	// configures — so it honours an owed apply exactly as start does.
	if t.container.AgentConfigPending {
		spec, err := loadAgentConfig(t.container)
		if err != nil {
			return err
		}
		if err := a.applyAgentConfig(ctx, t.provider, t.container, environ, spec); err != nil {
			return err
		}
	}
```

- [ ] **Step 8: Run to verify they pass**

Run: `go test ./internal/cli/ -v -run 'Create|AgentsFile|NotInstalled|FailingStep|WorktreeCreateRecords|AgentConfig'` then `make lint && make test`
Expected: PASS, and every pre-existing CLI test still passes (they either use `noStart` or reach no agents.yaml).

- [ ] **Step 9: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): --agent-config and --no-agent-config; apply agents.yaml on first start"
```

---

### Task 7: CLI — re-apply on `rebuild`

**Files:**
- Modify: `internal/cli/container.go` (`newContainerRebuildCmd`)
- Test: `internal/cli/agentconfig_test.go` (append)

**Interfaces:**
- Consumes: `loadAgentConfig`, `applyAgentConfig` (Task 5); `stubDevcontainer`, `projectWithAgents`, `containerRow`, `claudeAgents` (Task 6 tests).

- [ ] **Step 1: Write the failing tests** — append:

```go
// runIn drives the real command tree against an existing test app, so a test
// can create through runContainerCreate and then rebuild through cobra.
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
	// An edit waits for the rebuild, and the rebuild picks it up.
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
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run 'Rebuild(Reapplies|WithAMissing|OfAProject)' -v`
Expected: FAIL — the rebuild applies nothing, and the missing-file case rebuilds and exits 0.

- [ ] **Step 3: Implement** — in `newContainerRebuildCmd`'s `RunE`, after `environ, err := a.containerEnv(...)` / its error check and before `t.provider.Rebuild(...)`, insert:

```go
			// Loaded before the rebuild, not after it: a malformed file, or
			// an --agent-config file that has since gone, must stop the
			// command while the old container still exists, rather than
			// after it has been replaced by one that cannot be configured.
			spec, err := loadAgentConfig(t.container)
			if err != nil {
				return err
			}
```

and replace

```go
			a.printf("container %s rebuilt\n", t.container.Name)
			return nil
```

with

```go
			a.printf("container %s rebuilt\n", t.container.Name)
			// Every rebuild re-reads the file: it is the moment an edit takes
			// effect, and a container without the state volume has just lost
			// everything the last apply installed.
			return a.applyAgentConfig(cmd.Context(), t.provider, t.container, environ, spec)
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/cli/ -v -run 'Rebuild'` then `make lint && make test`
Expected: PASS, including the existing `TestRebuildTools*` tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): rebuild re-applies agents.yaml, checking the file first"
```

---

### Task 8: Smoke test and documentation

**Files:**
- Create: `test/smoke/agentconfig_test.go`
- Modify: `docs/USAGE.md`, `CLAUDE.md`

- [ ] **Step 1: Write the smoke test** — `test/smoke/agentconfig_test.go`:

```go
//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The declaration end to end: a generated container with Claude Code, a
// project that ships only an agents.yaml, and a rebuild that has to re-apply
// over what the first apply left on the state volume.
func TestSmokeAgentConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-agents"
		ws   = "dev-smoke-agents-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name+"-state").Run()
	})

	// No devcontainer.json, so --generate applies; the agents.yaml beside
	// where one would be is found by default.
	project := t.TempDir()
	for rel, body := range map[string]string{
		".devcontainer/agents.yaml": `version: 1
skills:
  - path: ./skills/hello
mcp:
  echo:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-everything"]
claude:
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins: [superpowers@claude-plugins-official]
`,
		".devcontainer/skills/hello/SKILL.md": "---\nname: hello\ndescription: says hello\n---\nSay hello.\n",
	} {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dev("provider", "configure", "dev-smoke-agents-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-agents-local")

	check := func(when string) {
		t.Helper()
		dev("container", "exec", name, "--", "sh", "-c", `test -f "$CLAUDE_CONFIG_DIR/skills/hello/SKILL.md"`)
		if out := dev("container", "exec", name, "--", "claude", "plugin", "list"); !strings.Contains(out, "superpowers") {
			t.Errorf("%s: the plugin is not installed:\n%s", when, out)
		}
		if out := dev("container", "exec", name, "--", "claude", "mcp", "list"); !strings.Contains(out, "echo") {
			t.Errorf("%s: the MCP server is not configured:\n%s", when, out)
		}
	}

	t.Log("creating; the apply runs once the container is up")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "claude-code")
	check("after create")

	t.Log("rebuilding; every step has to be safe to run over its own result")
	dev("container", "rebuild", name)
	check("after rebuild")
}
```

Run: `go vet -tags smoke ./test/smoke/`
Expected: no output. (`make smoke` itself needs an engine; run it if one is available and record the result in the task report either way.)

- [ ] **Step 2: Document the feature** — in `docs/USAGE.md`:

1. Add to the walkthrough list, after "Keep an agent's plugins across a rebuild":
   `  - [Declare a project's agent setup](#declare-a-projects-agent-setup)`
2. Add the walkthrough section directly after the "Keep an agent's plugins across a rebuild" section:

````markdown
### Declare a project's agent setup

The state volume keeps what you install in one container across its rebuilds.
It does nothing for the next container. To stop typing the same `claude plugin
install` in every new one, declare it in the project:

```yaml
# .devcontainer/agents.yaml
version: 1

skills:                       # every installed agent gets these
  - path: ./skills/our-conventions          # relative to this file
  - git: https://github.com/obra/superpowers
    ref: v6.4.1
    path: skills/brainstorming

mcp:                          # and these
  context7:
    command: npx
    args: ["-y", "@upstash/context7-mcp"]
    env:
      CONTEXT7_API_KEY: ${CONTEXT7_API_KEY}

claude:
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins: [superpowers@claude-plugins-official]
opencode:
  plugins: [opencode-wakatime]
```

```sh
dev workspace set CONTEXT7_API_KEY keychain:context7
dev container create api --folder ~/code/api
```

`create` applies the file once the container is running — Claude Code through
its own CLI, opencode and hermes by merging into `opencode.json` and
`config.yaml` — and `rebuild` applies it again. `start` does not: like a
`devcontainer.json`, an edit waits for a rebuild. After `create --no-start`, the
first `start` applies it.

- **Top level vs. per agent.** `skills` and `mcp` at the top reach every agent
  installed in the container; each agent's own section adds more. `plugins`
  exists only under `claude` and `opencode`, `marketplaces` only under `claude`.
  hermes has no plugins. A per-agent MCP server with a top-level server's name
  replaces it for that agent.
- **`${NAME}` is a reference, never a value.** The file is committed, so `dev`
  never substitutes it; each agent resolves it when it starts, from the
  workspace's settings. A reference no setting defines is a warning.
- **An agent the container lacks is skipped with a warning**, so one file works
  across containers with different tools. A step that fails — a plugin that
  does not exist, a marketplace that cannot be reached — fails the command; the
  container stays running and the next `start` retries.
- **Nothing is removed.** A declared entry or skill directory is replaced; a
  plugin you drop from the file stays installed until the container is
  recreated. Things you added by hand are left alone.
- **`dev` only reads the file.** It never writes into the project.

`--agent-config PATH` applies another file instead — one in your dotfiles, say,
which also gives a `--no-folder` container something to apply — and
`--no-agent-config` applies none. The choice is fixed at create, so `rebuild`
reads the same file without being told again.

Both providers support this; on k8s (experimental) the steps run through
`kubectl exec`.
````

3. In the `### container` synopsis, change the two `create` lines to:

```
dev container create NAME --folder PATH [--generate] [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
dev container create NAME --no-folder [--tools LIST] [--no-persist-state]
                         [--agent-config PATH | --no-agent-config] [--no-start]
```

4. In the `create` flag table, add after `--no-persist-state`:

```markdown
| `--agent-config` | apply this agents.yaml instead of the project's `.devcontainer/agents.yaml` |
| `--no-agent-config` | apply no agents.yaml, even if the project has one |
```

   and after the compose-file paragraph add:

```markdown
A project's `.devcontainer/agents.yaml` is applied once the container is
running, and again on every `rebuild`; see
[Declare a project's agent setup](#declare-a-projects-agent-setup). A malformed
file is exit 2 before anything is created; an `--agent-config` file that does
not exist is exit 3, at create or at a later rebuild.
```

5. In the `### worktree` synopsis add `[--agent-config PATH | --no-agent-config]` to the `create` line group, and add the same two rows to its `create` flag table after `--no-persist-state`.

6. Under `## Troubleshooting`, add:

```markdown
**`agents.yaml: hermes is not installed in this container; skipping it`** —
the file declares something for an agent the image does not have. Add the agent
to the project's `devcontainer.json` (or `--tools hermes` for a generated one)
and rebuild, or ignore it if this container is not meant to run that agent.

**`claude: marketplace NAME: the command succeeded but the change is not
visible afterwards`** — the key under `marketplaces` is not the name the
marketplace gives itself. `dev container exec NAME -- claude plugin marketplace
list` shows the real one; use it as the key.
```

Then in `CLAUDE.md`:

7. In the architecture block, add after the `internal/agent/` line:

```
internal/agentcfg/    reading a project's agents.yaml
```

   and change the `internal/agent/` line to `internal/agent/       which agents exist, how to invoke and configure them`.

8. At the end of invariant 9 (after the `postCreateCommand` paragraph), add:

```markdown
   `.devcontainer/agents.yaml` is the same rule from the other side: `dev`
   reads it and never writes it, and applies what it declares *inside* the
   container, through `Provider.Exec`, onto the state volume — never into the
   project. `create` validates it before the row exists, the row records only
   which file (`agent_config`) and whether an apply is owed
   (`agent_config_pending`), and `rebuild` loads it before recreating the
   container, so a file that has gone or stopped parsing fails while the old
   container still exists.
```

- [ ] **Step 3: Check the docs against the code**

Run: `DEV_STATE=$(mktemp -d) go run ./cmd/dev container create --help` and `DEV_STATE=$(mktemp -d) go run ./cmd/dev worktree create --help`
Expected: both list `--agent-config` and `--no-agent-config` with the wording in the tables above. Fix whichever side disagrees.

Run: `grep -n "agents.yaml\|agent-config" README.md`
Expected: if the README has a feature list, add one line pointing at the walkthrough; if it has none, leave it.

Run: `make lint && make test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add test/smoke/agentconfig_test.go docs/USAGE.md CLAUDE.md README.md
git commit -m "docs: declare a project's agent setup in agents.yaml"
```
