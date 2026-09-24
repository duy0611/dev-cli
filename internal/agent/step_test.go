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
	// The URL, ref and each name and subdirectory arrive as positional
	// arguments, never spliced into the script, since agents.yaml is not dev's
	// to trust.
	if !slices.Equal(withRef[3:], []string{"sh", "https://x/sp", "v1", "brainstorming", "skills/brainstorming"}) {
		t.Errorf("args = %v", withRef[3:])
	}
	if !strings.Contains(withRef[2], `--branch "$2"`) || strings.Contains(noRef[2], "--branch") {
		t.Errorf("--branch handling: %q / %q", withRef[2], noRef[2])
	}
	if strings.Contains(withRef[2], "https://x/sp") {
		t.Error("the URL was spliced into the script")
	}
}

// Skills from one repository at one ref share a clone, wherever they sit in
// the list; a different ref of the same repository is a different checkout.
func TestSkillStepsCloneEachRepositoryOnce(t *testing.T) {
	steps := skillSteps("claude", "${D:-$HOME/.claude}/skills", []agentcfg.Skill{
		{Name: "a", Git: "https://x/sp", Ref: "v1", Path: "skills/a"},
		{Name: "ours", Archive: []byte("tar")},
		{Name: "old", Git: "https://x/sp", Ref: "v0", Path: "skills/old"},
		{Name: "b", Git: "https://x/sp", Ref: "v1", Path: "skills/b"},
	})
	var descs []string
	for _, s := range steps {
		descs = append(descs, s.Desc)
	}
	want := []string{"claude: skills a, b", "claude: skill ours", "claude: skill old"}
	if !slices.Equal(descs, want) {
		t.Fatalf("steps = %v, want %v", descs, want)
	}
	if !slices.Equal(steps[0].Cmd[3:], []string{"sh", "https://x/sp", "v1", "a", "skills/a", "b", "skills/b"}) {
		t.Errorf("args = %v", steps[0].Cmd[3:])
	}
	if n := strings.Count(steps[0].Cmd[2], "git clone"); n != 1 {
		t.Errorf("script clones %d times", n)
	}
}
