package cli

import (
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// runWorkspace drives the real `workspace` command tree, so the flag handling
// in RunE — --ssh-forward given versus absent — is what is under test.
func runWorkspace(t *testing.T, a *app, argv ...string) error {
	t.Helper()
	root := newRootCmd("test")
	root.AddCommand(newWorkspaceCmd(a))
	root.SetArgs(append([]string{"workspace"}, argv...))
	return root.Execute()
}

// seedSource creates providers p and q and a workspace src on p with ssh
// forwarding on and two settings.
func seedSource(t *testing.T, a *app) {
	t.Helper()
	for _, p := range []string{"p", "q"} {
		if err := runProviderConfigure(a, p, "local", k8s.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runWorkspaceInit(a, "src", "p", true); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"A_KEY": "literal:a", "GH_TOKEN": "keychain:gh"} {
		if err := runWorkspaceSet(a, "src", k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func workspaceRow(t *testing.T, a *app, name string) (model.Workspace, []model.Setting) {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := st.GetWorkspace(name)
	if err != nil {
		t.Fatalf("GetWorkspace %s: %v", name, err)
	}
	settings, err := st.ListSettings(name)
	if err != nil {
		t.Fatal(err)
	}
	return ws, settings
}

func TestWorkspaceInitFromCopiesEverything(t *testing.T) {
	a, out := newTestApp(t)
	seedSource(t, a)

	if err := runWorkspace(t, a, "init", "copy", "--from", "src"); err != nil {
		t.Fatalf("init --from: %v", err)
	}
	ws, settings := workspaceRow(t, a, "copy")
	if ws.ProviderName != "p" {
		t.Errorf("provider = %q, want p", ws.ProviderName)
	}
	// Absent from the command line means inherited, not off.
	if !ws.SSHForward {
		t.Error("ssh forwarding was not inherited from the source")
	}
	// Specs, not values: GH_TOKEN must still name the keychain entry.
	want := []model.Setting{{Key: "A_KEY", Spec: "literal:a"}, {Key: "GH_TOKEN", Spec: "keychain:gh"}}
	if len(settings) != 2 || settings[0] != want[0] || settings[1] != want[1] {
		t.Errorf("settings = %v, want %v", settings, want)
	}
	if !strings.Contains(out.String(), "2 setting(s) copied from src") {
		t.Errorf("output %q does not report the copy", out)
	}
}

func TestWorkspaceInitFromHonoursOverrides(t *testing.T) {
	a, _ := newTestApp(t)
	seedSource(t, a)

	if err := runWorkspace(t, a, "init", "copy", "--from", "src", "--provider", "q", "--ssh-forward=false"); err != nil {
		t.Fatalf("init --from with overrides: %v", err)
	}
	ws, settings := workspaceRow(t, a, "copy")
	if ws.ProviderName != "q" {
		t.Errorf("provider = %q, want q", ws.ProviderName)
	}
	if ws.SSHForward {
		t.Error("--ssh-forward=false was overridden by the source")
	}
	if len(settings) != 2 {
		t.Errorf("settings = %v, want both copied", settings)
	}
}

func TestWorkspaceInitFromRefusals(t *testing.T) {
	a, _ := newTestApp(t)
	seedSource(t, a)

	cases := []struct {
		name string
		argv []string
		want int
	}{
		{"missing source", []string{"init", "copy", "--from", "nope"}, exitNotFound},
		{"missing provider override", []string{"init", "copy", "--from", "src", "--provider", "nope"}, exitNotFound},
		{"existing name", []string{"init", "src", "--from", "src"}, exitUsage},
		{"bad name", []string{"init", "no spaces", "--from", "src"}, exitUsage},
		{"neither provider nor from", []string{"init", "copy"}, exitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runWorkspace(t, a, tc.argv...)
			if got := exitCodeOf(err); got != tc.want {
				t.Errorf("exit code = %d (%v), want %d", got, err, tc.want)
			}
		})
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorkspace("copy"); err == nil {
		t.Error("a refused init left a workspace behind")
	}
}
