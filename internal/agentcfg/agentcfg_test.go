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

// One repository, several skills: paths expands into one Skill per path, in
// order, each sharing the entry's git and ref.
func TestSkillPathsExpand(t *testing.T) {
	s, err := Load(write(t, `version: 1
skills:
  - git: https://github.com/obra/superpowers
    ref: v6.4.1
    paths:
      - skills/brainstorming
      - ./skills/writing-plans
`, nil))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.View("claude")
	want := []Skill{
		{Name: "brainstorming", Git: "https://github.com/obra/superpowers", Ref: "v6.4.1", Path: "skills/brainstorming"},
		{Name: "writing-plans", Git: "https://github.com/obra/superpowers", Ref: "v6.4.1", Path: "skills/writing-plans"},
	}
	if !slices.EqualFunc(v.Skills, want, func(a, b Skill) bool {
		return a.Name == b.Name && a.Git == b.Git && a.Ref == b.Ref && a.Path == b.Path && a.Archive == nil
	}) {
		t.Errorf("skills = %+v", v.Skills)
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
		{"skill path and paths", "version: 1\nskills:\n  - git: https://x/r\n    path: a\n    paths: [b]\n", "not both"},
		{"skill empty paths", "version: 1\nskills:\n  - git: https://x/r\n    paths: []\n", "paths"},
		{"skill paths on local", "version: 1\nskills:\n  - paths: [./s]\n", "git"},
		{"skill paths escapes repo", "version: 1\nskills:\n  - git: https://x/r\n    paths: [a, ../up]\n", "inside the repository"},
		{"duplicate skill in paths", "version: 1\nskills:\n  - git: https://x/r\n    paths: [a/s, b/s]\n", "duplicate"},
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

// The sample in docs/ is what an operator copies, and it claims to show every
// key the file accepts. It has to stay loadable as the format changes.
func TestTheSampleLoads(t *testing.T) {
	s, err := Load(filepath.Join("..", "..", "docs", "agents.sample.yaml"))
	if err != nil {
		t.Fatalf("docs/agents.sample.yaml no longer loads: %v", err)
	}
	for _, id := range agents {
		if _, ok := s.View(id); !ok {
			t.Errorf("the sample says nothing to %s", id)
		}
	}
}
