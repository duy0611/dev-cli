package cli

import (
	"bytes"
	"strings"
	"testing"
)

// execute runs the whole command tree against argv and returns the exit code,
// the same way Execute does — so these assert on the interface an operator and
// a script actually see.
func execute(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	t.Setenv("DEV_STATE", t.TempDir())

	var out bytes.Buffer
	a := &app{out: &out}
	t.Cleanup(a.close)

	root := newRootCmd("test")
	root.AddCommand(newProviderCmd(a), newWorkspaceCmd(a), newContainerCmd(a))
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(argv)

	return exitCodeOf(root.Execute()), out.String()
}

// A mistyped subcommand is a malformed request, so it exits 2 like every other
// one (invariant 7).
//
// The trap this guards: cobra inherits the root's RunE — which prints help and
// returns nil — into any command that defines none, so a group would print its
// help and exit 0. A script would read `dev container agnet api` as the agent
// having run, and the container never started.
func TestUnknownSubcommandExitsTwo(t *testing.T) {
	for _, argv := range [][]string{
		{"container", "agnet", "api"},
		{"container", "bogus"},
		{"container", "config", "bogus"},
		{"workspace", "bogus"},
		{"provider", "bogus"},
		{"totally-unknown"},
	} {
		code, out := execute(t, argv...)
		if code != exitUsage {
			t.Errorf("dev %s exited %d, want %d\n%s",
				strings.Join(argv, " "), code, exitUsage, out)
		}
	}
}

// A group with no subcommand is a question, not a mistake: print help and
// succeed, which is what `dev container` has always done.
func TestGroupWithoutSubcommandPrintsHelp(t *testing.T) {
	for _, argv := range [][]string{
		{"container"},
		{"container", "config"},
		{"workspace"},
		{"provider"},
		{},
	} {
		code, out := execute(t, argv...)
		if code != exitOK {
			t.Errorf("dev %s exited %d, want %d\n%s",
				strings.Join(argv, " "), code, exitOK, out)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("dev %s printed no usage:\n%s", strings.Join(argv, " "), out)
		}
	}
}

// The rename is the whole point of the command's new spelling, and a stale
// reference anywhere would be found by an operator rather than a test.
func TestStartAgentIsTheCommandName(t *testing.T) {
	if code, out := execute(t, "container", "start-agent"); code != exitUsage {
		t.Errorf("start-agent with no NAME exited %d, want %d\n%s", code, exitUsage, out)
	}
	// The old spelling is gone, not aliased: one operator, no deprecations.
	if code, _ := execute(t, "container", "agent", "api"); code != exitUsage {
		t.Errorf("the old `container agent` spelling still resolves, exit %d", code)
	}
}
