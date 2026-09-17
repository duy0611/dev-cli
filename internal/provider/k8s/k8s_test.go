package k8s

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
)

// clusterStubs installs stand-ins for every binary the provider drives.
//
// The devcontainer CLI is stubbed here rather than used for real: one binary
// serves both read-configuration and build, so using the real one for the
// former would mean not seeing the latter. The shape of the configuration this
// stub emits is pinned against the real CLI in build_test.go.
//
// deployExists decides what `get deployment` reports, which is the whole
// difference between a create and a start.
func clusterStubs(t *testing.T, deployExists bool) *stubs {
	t.Helper()
	s := newStubs(t)

	exists := "false"
	if deployExists {
		exists = "true"
	}
	s.installKubectl(t, `case "$*" in
  *"get deployment"*"-o name"*)
    [ `+exists+` = true ] && echo deployment.apps/dev-x
    ;;
  *"get deployment"*"-o json"*)
    [ `+exists+` = true ] && echo '{"status":{"readyReplicas":1}}'
    ;;
esac`)

	s.installScript(t, devcontainerBin, `case "$1" in
  read-configuration)
    echo '{"mergedConfiguration":{"workspaceFolder":"/workspaces/api","remoteUser":"node","containerEnv":{},"remoteEnv":{},"onCreateCommands":[],"updateContentCommands":[],"postCreateCommands":[],"postStartCommands":[],"postAttachCommands":[]}}'
    ;;
esac`)
	withBuildx(t, s)
	return s
}

// kubectlCalls returns each kubectl invocation as one joined string.
func kubectlCalls(t *testing.T, s *stubs) []string {
	t.Helper()
	return s.calls(t, kubectlBin)
}

func devcontainerCalls(t *testing.T, s *stubs) []string {
	t.Helper()
	return s.calls(t, devcontainerBin)
}

// anyCallOf matches on the subcommand rather than anywhere in the line: a
// temp directory called ...does_not_build... otherwise looks like a build.
func anyCallOf(calls []string, subcommand string) bool {
	for _, c := range calls {
		if c == subcommand || strings.HasPrefix(c, subcommand+" ") {
			return true
		}
	}
	return false
}

func anyCall(calls []string, substr string) bool {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func testProvider() *Provider {
	cfg := Config{
		Context: "prod", Namespace: "sandboxes",
		Registry: "reg.example/dev", Platform: "linux/amd64", StorageSize: "20Gi",
	}
	return &Provider{cfg: cfg, kube: newKubectl(cfg)}
}

func k8sContainer(t *testing.T) model.Container {
	t.Helper()
	return model.Container{
		Name: "api", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: t.TempDir(),
	}
}

func TestUpBuildsOnlyWhenTheDeploymentIsAbsent(t *testing.T) {
	t.Run("create builds", func(t *testing.T) {
		s := clusterStubs(t, false)
		c := k8sContainer(t)

		if err := testProvider().Up(context.Background(), c, nil); err != nil {
			t.Fatalf("Up: %v", err)
		}
		calls := devcontainerCalls(t, s)
		if !anyCallOf(calls, "build") {
			t.Fatalf("no build among %v", calls)
		}
		if !anyCall(calls, "--platform linux/amd64") || !anyCall(calls, "--push") {
			t.Errorf("build is missing --platform or --push: %v", calls)
		}
	})

	// A start that rebuilds would cost minutes every time.
	t.Run("start does not build", func(t *testing.T) {
		s := clusterStubs(t, true)
		c := k8sContainer(t)

		if err := testProvider().Up(context.Background(), c, nil); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if calls := devcontainerCalls(t, s); anyCallOf(calls, "build") {
			t.Fatalf("start rebuilt the image: %v", calls)
		}
	})
}

func TestUpAppliesAndWaits(t *testing.T) {
	s := clusterStubs(t, false)
	if err := testProvider().Up(context.Background(), k8sContainer(t), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	calls := kubectlCalls(t, s)
	if !anyCall(calls, "apply --server-side") {
		t.Errorf("no apply in %v", calls)
	}
	// Without the wait, the next command execs into a pod that is still
	// pulling its image.
	if !anyCall(calls, "rollout status") {
		t.Errorf("Up did not wait for readiness: %v", calls)
	}
}

func TestRebuildAlwaysBuilds(t *testing.T) {
	s := clusterStubs(t, true) // already exists, so Up would not build
	if err := testProvider().Rebuild(context.Background(), k8sContainer(t), nil, true); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	calls := devcontainerCalls(t, s)
	if !anyCallOf(calls, "build") {
		t.Fatalf("Rebuild did not build: %v", calls)
	}
	if !anyCall(calls, "--no-cache") {
		t.Errorf("--no-cache was requested but not passed: %v", calls)
	}
}

func TestStopScalesToZero(t *testing.T) {
	s := clusterStubs(t, true)
	c := model.Container{Name: "api", WorkspaceName: "ws"}

	if err := testProvider().Stop(context.Background(), c); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	calls := kubectlCalls(t, s)
	if !anyCall(calls, "scale deployment/"+objectName(c)+" --replicas=0") {
		t.Errorf("Stop did not scale to zero: %v", calls)
	}
	// Deleting would take the PVC's contents with it.
	if anyCall(calls, "delete") {
		t.Errorf("Stop deleted something: %v", calls)
	}
}

// `kubectl scale` returns as soon as the API server records the new count, so
// without the wait `stop` reports success while the container is still running.
func TestStopWaitsForThePodToGo(t *testing.T) {
	s := newStubs(t)
	// A pod that is still terminating on the first look and gone on the second.
	counter := filepath.Join(t.TempDir(), "n")
	s.installKubectl(t, `case "$*" in
  *"get deployment"*"-o name"*) echo deployment.apps/dev-x ;;
  *"get pods"*)
    if [ -f '`+counter+`' ]; then exit 0; fi
    : > '`+counter+`'
    echo pod/dev-x-123
    ;;
esac`)

	c := model.Container{Name: "api", WorkspaceName: "ws"}
	if err := testProvider().Stop(context.Background(), c); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	calls := kubectlCalls(t, s)
	var pollsAfterScale int
	scaled := false
	for _, call := range calls {
		if strings.Contains(call, "scale") {
			scaled = true
			continue
		}
		if scaled && strings.Contains(call, "get pods") {
			pollsAfterScale++
		}
	}
	if !scaled {
		t.Fatalf("Stop did not scale: %v", calls)
	}
	if pollsAfterScale < 2 {
		t.Errorf("Stop polled %d times after scaling; it returned before the pod was gone: %v",
			pollsAfterScale, calls)
	}
}

func TestStopOnAnAbsentDeploymentIsANoOp(t *testing.T) {
	s := clusterStubs(t, false)
	c := model.Container{Name: "api", WorkspaceName: "ws"}

	if err := testProvider().Stop(context.Background(), c); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if anyCall(kubectlCalls(t, s), "scale") {
		t.Error("Stop scaled a deployment that does not exist")
	}
}

func TestRemoveDeletesAllThreeObjects(t *testing.T) {
	s := clusterStubs(t, true)
	c := model.Container{Name: "api", WorkspaceName: "ws"}

	if err := testProvider().Remove(context.Background(), c); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	calls := kubectlCalls(t, s)
	for _, want := range []string{
		"delete deployment " + objectName(c),
		"delete secret " + secretName(c),
		// Left behind, the claim keeps billing for storage nothing references.
		"delete persistentvolumeclaim " + objectName(c),
	} {
		if !anyCall(calls, want) {
			t.Errorf("Remove did not run %q: %v", want, calls)
		}
	}
}

func TestStatus(t *testing.T) {
	c := model.Container{Name: "api", WorkspaceName: "ws"}

	t.Run("absent", func(t *testing.T) {
		clusterStubs(t, false)
		got, err := testProvider().Status(context.Background(), c)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != model.StatusAbsent {
			t.Errorf("Status = %q, want %q", got, model.StatusAbsent)
		}
	})

	t.Run("running", func(t *testing.T) {
		clusterStubs(t, true)
		got, err := testProvider().Status(context.Background(), c)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != model.StatusRunning {
			t.Errorf("Status = %q, want %q", got, model.StatusRunning)
		}
	})

	// Scaled to zero: the object exists but nothing can exec into it.
	t.Run("scaled to zero", func(t *testing.T) {
		s := newStubs(t)
		s.install(t, kubectlBin, `{"status":{}}`, 0)

		got, err := testProvider().Status(context.Background(), c)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != model.StatusStopped {
			t.Errorf("Status = %q, want %q", got, model.StatusStopped)
		}
	})
}

func TestExecTargetsTheDeploymentAndCarriesEnv(t *testing.T) {
	s := clusterStubs(t, true)
	c := k8sContainer(t)

	err := testProvider().Exec(context.Background(), c, []string{"printenv", "GREETING"},
		provider.ExecOpts{
			Env:    []provider.EnvVar{{Key: "GREETING", Value: "hello"}},
			Stdout: io.Discard,
		})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	var call string
	for _, c := range kubectlCalls(t, s) {
		if strings.Contains(c, "exec") {
			call = c
		}
	}
	if call == "" {
		t.Fatalf("no exec call: %v", kubectlCalls(t, s))
	}
	// deployment/, not a pod name: the pod's name changes on every restart.
	if !strings.Contains(call, "deployment/"+objectName(c)) {
		t.Errorf("exec does not target the deployment: %q", call)
	}
	if !strings.Contains(call, "GREETING=hello") {
		t.Errorf("exec did not carry the environment: %q", call)
	}
	// The fixture sets remoteUser, so the command switches user if the pod is
	// root — kubectl exec otherwise runs as whatever the image's USER is.
	if !strings.Contains(call, "su -p") {
		t.Errorf("exec does not honour remoteUser: %q", call)
	}
}

func TestLogsTargetsTheDeployment(t *testing.T) {
	s := clusterStubs(t, true)
	c := model.Container{Name: "api", WorkspaceName: "ws"}

	if err := testProvider().Logs(context.Background(), c, true, io.Discard); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	calls := kubectlCalls(t, s)
	if !anyCall(calls, "logs deployment/"+objectName(c)) || !anyCall(calls, "-f") {
		t.Errorf("Logs call is wrong: %v", calls)
	}
}

// The registry is what makes a provider usable, and its absence otherwise
// surfaces after a build rather than before one.
func TestFactoryRejectsAConfigWithoutARegistry(t *testing.T) {
	_, err := provider.New(model.Provider{Name: "k", Kind: model.KindK8s, Config: "{}"})
	if err == nil {
		t.Fatal("the factory accepted a k8s provider with no registry")
	}
}

func TestFactoryBuildsAConfiguredProvider(t *testing.T) {
	p, err := provider.New(model.Provider{
		Name: "k", Kind: model.KindK8s,
		Config: `{"registry":"reg.example/dev","namespace":"sandboxes"}`,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	if p == nil {
		t.Fatal("provider.New returned nothing")
	}
}
