package k8s

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCommandShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want [][]string
	}{
		{
			// A string is a shell command line, so it has to reach a shell or
			// `npm i && npm test` runs as a single filename.
			name: "string",
			raw:  `"npm install"`,
			want: [][]string{{"sh", "-c", "npm install"}},
		},
		{
			// An array is an argv, run without a shell.
			name: "array",
			raw:  `["npm","install"]`,
			want: [][]string{{"npm", "install"}},
		},
		{
			// Named commands, sorted so a failure is reproducible.
			name: "object",
			raw:  `{"b":"make build","a":["npm","i"]}`,
			want: [][]string{{"npm", "i"}, {"sh", "-c", "make build"}},
		},
		{name: "empty string", raw: `""`, want: nil},
		{name: "empty array", raw: `[]`, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("parseCommand: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if strings.Join(got[i], "\x00") != strings.Join(tc.want[i], "\x00") {
					t.Errorf("command %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseCommandRejectsNonsense(t *testing.T) {
	if _, err := parseCommand(json.RawMessage(`42`)); err == nil {
		t.Error("parseCommand accepted a number")
	}
}

// Features contribute commands of their own, so a merged phase is an array of
// entries and each may be any of the three shapes.
func TestParseAllFlattensEveryContributor(t *testing.T) {
	got, err := parseAll([]json.RawMessage{
		json.RawMessage(`"echo one"`),
		json.RawMessage(`["echo","two"]`),
		json.RawMessage(`{"x":"echo three"}`),
	})
	if err != nil {
		t.Fatalf("parseAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d commands, want 3: %v", len(got), got)
	}
}

// lifecycleStubs installs a kubectl whose `exec` behaviour depends on whether
// the marker file is considered present, and records every command run.
func lifecycleStubs(t *testing.T, markerExists bool) *stubs {
	t.Helper()
	s := newStubs(t)

	marker := "false"
	if markerExists {
		marker = "true"
	}
	s.installScript(t, kubectlBin, `case "$*" in
  *test*dev-lifecycle-done*)
    [ `+marker+` = true ] || exit 1
    ;;
  *"get deployment"*"-o name"*) echo deployment.apps/dev-x ;;
  *"get deployment"*"-o json"*) echo '{"status":{"readyReplicas":1}}' ;;
esac`)
	s.installScript(t, devcontainerBin, `case "$1" in
  read-configuration)
    echo '{"mergedConfiguration":{"workspaceFolder":"/workspaces/api","remoteUser":"node","containerEnv":{},"remoteEnv":{},"onCreateCommands":["echo on"],"updateContentCommands":[],"postCreateCommands":["echo post"],"postStartCommands":["echo start"],"postAttachCommands":["echo attach"]}}'
    ;;
esac`)
	s.install(t, "docker", "", 0)
	return s
}

func TestLifecycleRunsCreateCommandsOnceAndPostStartAlways(t *testing.T) {
	t.Run("first time", func(t *testing.T) {
		s := lifecycleStubs(t, false)
		c := k8sContainer(t)

		if err := testProvider().Up(context.Background(), c, nil); err != nil {
			t.Fatalf("Up: %v", err)
		}
		calls := kubectlCalls(t, s)
		for _, want := range []string{"echo on", "echo post", "echo start"} {
			if !anyCall(calls, want) {
				t.Errorf("%q did not run: %v", want, calls)
			}
		}
		// Nothing attaches, so running postAttach would be inventing an event.
		if anyCall(calls, "echo attach") {
			t.Error("postAttachCommand ran")
		}
		if !anyCall(calls, "touch") || !anyCall(calls, "/home/node"+markerFile) {
			t.Errorf("the marker was not written: %v", calls)
		}
	})

	// A stop and start must not reinstall everything.
	t.Run("marker present", func(t *testing.T) {
		s := lifecycleStubs(t, true)
		c := k8sContainer(t)

		if err := testProvider().Up(context.Background(), c, nil); err != nil {
			t.Fatalf("Up: %v", err)
		}
		calls := kubectlCalls(t, s)
		if anyCall(calls, "echo on") || anyCall(calls, "echo post") {
			t.Errorf("create-time commands re-ran: %v", calls)
		}
		// postStart is per start, by definition.
		if !anyCall(calls, "echo start") {
			t.Errorf("postStartCommand did not run: %v", calls)
		}
	})
}

// A rebuild is a new image, so whatever postCreate installed is gone with it.
func TestRebuildClearsTheMarkerAndReRuns(t *testing.T) {
	s := lifecycleStubs(t, true)
	c := k8sContainer(t)

	if err := testProvider().Rebuild(context.Background(), c, nil, false); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	calls := kubectlCalls(t, s)
	if !anyCall(calls, "rm") || !anyCall(calls, "/home/node"+markerFile) {
		t.Errorf("the marker was not cleared: %v", calls)
	}
}

// Carrying on into a container missing its dependencies produces a confusing
// failure much later.
func TestLifecycleStopsAtTheFirstFailure(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, kubectlBin, `case "$*" in
  *test*dev-lifecycle-done*) exit 1 ;;
  *"echo on"*) echo "boom" >&2; exit 3 ;;
  *"get deployment"*"-o name"*) echo deployment.apps/dev-x ;;
  *"get deployment"*"-o json"*) echo '{"status":{"readyReplicas":1}}' ;;
esac`)
	s.installScript(t, devcontainerBin, `case "$1" in
  read-configuration)
    echo '{"mergedConfiguration":{"workspaceFolder":"/workspaces/api","remoteUser":"node","containerEnv":{},"remoteEnv":{},"onCreateCommands":["echo on"],"updateContentCommands":[],"postCreateCommands":["echo post"],"postStartCommands":["echo start"],"postAttachCommands":[]}}'
    ;;
esac`)
	s.install(t, "docker", "", 0)

	err := testProvider().Up(context.Background(), k8sContainer(t), nil)
	if err == nil {
		t.Fatal("Up succeeded with a failing onCreateCommand")
	}
	if !strings.Contains(err.Error(), "onCreateCommand") {
		t.Errorf("error %q does not name the phase", err)
	}

	calls := kubectlCalls(t, s)
	if anyCall(calls, "echo post") {
		t.Errorf("postCreate ran after onCreate failed: %v", calls)
	}
	// A marker over a failed install makes the next start skip the fix.
	if anyCall(calls, "touch") {
		t.Errorf("the marker was written despite the failure: %v", calls)
	}
}

// The create path has to put the files in before postCreate tries to build
// them.
func TestFirstCreateSyncsBeforeLifecycle(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, kubectlBin, `case "$*" in
  *test*dev-lifecycle-done*) exit 1 ;;
  *"get deployment"*"-o name"*) ;;
  *"get deployment"*"-o json"*) ;;
esac`)
	s.installScript(t, devcontainerBin, `case "$1" in
  read-configuration)
    echo '{"mergedConfiguration":{"workspaceFolder":"/workspaces/api","remoteUser":"node","containerEnv":{},"remoteEnv":{},"onCreateCommands":[],"updateContentCommands":[],"postCreateCommands":["echo post"],"postStartCommands":[],"postAttachCommands":[]}}'
    ;;
esac`)
	s.install(t, "docker", "", 0)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c := k8sContainer(t)
	c.Source = dir

	if err := testProvider().Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	calls := kubectlCalls(t, s)
	tarAt, postAt := -1, -1
	for i, call := range calls {
		if strings.Contains(call, "tar -x") {
			tarAt = i
		}
		if strings.Contains(call, "echo post") && postAt < 0 {
			postAt = i
		}
	}
	if tarAt < 0 {
		t.Fatalf("create did not sync: %v", calls)
	}
	if postAt < 0 {
		t.Fatalf("postCreate did not run: %v", calls)
	}
	if tarAt > postAt {
		t.Errorf("postCreate ran before the files arrived: %v", calls)
	}
}

// A start must not overwrite what the container has been doing.
func TestStartDoesNotSync(t *testing.T) {
	s := lifecycleStubs(t, true)

	if err := testProvider().Up(context.Background(), k8sContainer(t), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if anyCall(kubectlCalls(t, s), "tar -x") {
		t.Errorf("start re-synced the host folder: %v", kubectlCalls(t, s))
	}
}
