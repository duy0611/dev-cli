package cli

import (
	"bytes"
	"strings"
	"testing"
)

// execute runs the command tree against argv and returns the exit code, as
// Execute does.
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

// Cobra's inherited root RunE would print help and exit 0 for a mistyped group
// subcommand.
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

func TestStartAgentIsTheCommandName(t *testing.T) {
	if code, out := execute(t, "container", "start-agent"); code != exitUsage {
		t.Errorf("start-agent with no NAME exited %d, want %d\n%s", code, exitUsage, out)
	}
	if code, _ := execute(t, "container", "agent", "api"); code != exitUsage {
		t.Errorf("the old `container agent` spelling still resolves, exit %d", code)
	}
}
