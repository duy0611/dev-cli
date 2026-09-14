package k8s

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
)

func testInput() manifestInput {
	return manifestInput{
		Container: container("ws", "api"),
		Config: Config{
			Namespace:   "sandboxes",
			Registry:    "reg.example/dev",
			Platform:    defaultPlatform,
			StorageSize: "20Gi",
		},
		Dev: DevConfig{
			WorkspaceFolder: "/workspaces/api",
			RemoteUser:      "node",
			ContainerEnv:    map[string]string{"FROM_CONFIG": "yes"},
		},
		Image:    "reg.example/dev/ws-api:latest",
		Env:      []provider.EnvVar{{Key: "GREETING", Value: "hello"}},
		Replicas: 1,
	}
}

// decode renders the manifest and returns the three objects by kind.
func decode(t *testing.T, in manifestInput) map[string]map[string]any {
	t.Helper()

	raw, err := buildManifest(in)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v", err)
	}

	out := map[string]map[string]any{}
	for _, item := range list.Items {
		kind, _ := item["kind"].(string)
		out[kind] = item
	}
	if len(out) != 3 {
		t.Fatalf("manifest has %d distinct kinds, want 3: %v", len(out), out)
	}
	return out
}

func TestHomeDir(t *testing.T) {
	cases := map[string]string{"": "/root", "root": "/root", "node": "/home/node"}
	for user, want := range cases {
		if got := (DevConfig{RemoteUser: user}).HomeDir(); got != want {
			t.Errorf("HomeDir(%q) = %q, want %q", user, got, want)
		}
	}
}

func TestDeploymentCarriesTheThingsThatFailSilently(t *testing.T) {
	objs := decode(t, testInput())
	d := objs["Deployment"]

	spec, _ := d["spec"].(map[string]any)
	if got := spec["replicas"]; got != float64(1) {
		t.Errorf("replicas = %v, want 1", got)
	}

	// RollingUpdate would briefly run two pods against one ReadWriteOnce
	// volume, and the new one would sit unschedulable.
	strategy, _ := spec["strategy"].(map[string]any)
	if strategy["type"] != "Recreate" {
		t.Errorf("strategy = %v, want Recreate", strategy["type"])
	}

	pod, _ := spec["template"].(map[string]any)
	podSpec, _ := pod["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("pod has %d containers, want 1", len(containers))
	}
	c0, _ := containers[0].(map[string]any)

	// The tag never changes, so anything but Always serves a stale image after
	// a rebuild.
	if c0["imagePullPolicy"] != "Always" {
		t.Errorf("imagePullPolicy = %v, want Always", c0["imagePullPolicy"])
	}
	if c0["image"] != "reg.example/dev/ws-api:latest" {
		t.Errorf("image = %v", c0["image"])
	}
	if c0["workingDir"] != "/workspaces/api" {
		t.Errorf("workingDir = %v, want the workspaceFolder", c0["workingDir"])
	}

	// busybox does not accept `sleep infinity`, so the keep-alive has to be a
	// loop.
	cmd, _ := c0["command"].([]any)
	joined := ""
	for _, a := range cmd {
		joined += a.(string) + " "
	}
	if strings.Contains(joined, "sleep infinity") {
		t.Errorf("command %q uses sleep infinity, which busybox rejects", joined)
	}
	if !strings.Contains(joined, "sleep") {
		t.Errorf("command %q does not keep the container alive", joined)
	}
}

// Two subPaths, not one. Without the home mount, anything postCreate writes to
// the home directory is destroyed by the next stop, and "postCreate runs once"
// stops being true.
func TestBothSubPathsAreMounted(t *testing.T) {
	objs := decode(t, testInput())
	mounts := containerField(t, objs, "volumeMounts").([]any)

	if len(mounts) != 2 {
		t.Fatalf("got %d volume mounts, want 2 (workspace and home)", len(mounts))
	}
	seen := map[string]string{}
	for _, m := range mounts {
		mm := m.(map[string]any)
		seen[mm["subPath"].(string)] = mm["mountPath"].(string)
	}
	if seen[subPathWorkspace] != "/workspaces/api" {
		t.Errorf("workspace subPath mounts at %q", seen[subPathWorkspace])
	}
	if seen[subPathHome] != "/home/node" {
		t.Errorf("home subPath mounts at %q, want the remote user's home", seen[subPathHome])
	}
}

func TestEnvComesFromTheSecret(t *testing.T) {
	objs := decode(t, testInput())

	envFrom := containerField(t, objs, "envFrom").([]any)
	if len(envFrom) != 1 {
		t.Fatalf("got %d envFrom sources, want 1", len(envFrom))
	}
	ref := envFrom[0].(map[string]any)["secretRef"].(map[string]any)
	want := secretName(container("ws", "api"))
	if ref["name"] != want {
		t.Errorf("envFrom names %v, want %q", ref["name"], want)
	}

	data := objs["Secret"]["stringData"].(map[string]any)
	if data["GREETING"] != "hello" {
		t.Errorf("resolved setting missing from the Secret: %v", data)
	}
	if data["FROM_CONFIG"] != "yes" {
		t.Errorf("devcontainer.json containerEnv missing from the Secret: %v", data)
	}
}

// Same precedence the local provider gives: an explicit workspace setting beats
// the project's own containerEnv.
func TestWorkspaceSettingsBeatContainerEnv(t *testing.T) {
	in := testInput()
	in.Dev.ContainerEnv = map[string]string{"GREETING": "from-config"}
	in.Env = []provider.EnvVar{{Key: "GREETING", Value: "from-workspace"}}

	data := decode(t, in)["Secret"]["stringData"].(map[string]any)
	if data["GREETING"] != "from-workspace" {
		t.Errorf("GREETING = %v, want the workspace setting to win", data["GREETING"])
	}
}

func TestAllObjectsShareLabelsAndName(t *testing.T) {
	objs := decode(t, testInput())
	c := container("ws", "api")

	for kind, obj := range objs {
		meta := obj["metadata"].(map[string]any)
		got := meta["labels"].(map[string]any)
		for k, v := range labels(c) {
			if got[k] != v {
				t.Errorf("%s: label %s = %v, want %q", kind, k, got[k], v)
			}
		}
		name := meta["name"].(string)
		if !strings.HasPrefix(name, objectName(c)) {
			t.Errorf("%s is named %q, which does not derive from %q", kind, name, objectName(c))
		}
	}
}

func TestOptionalConfigIsOmittedWhenUnset(t *testing.T) {
	objs := decode(t, testInput())
	podSpec := podSpecOf(t, objs)

	if _, ok := podSpec["imagePullSecrets"]; ok {
		t.Error("imagePullSecrets is present with none configured")
	}
	if _, ok := podSpec["serviceAccountName"]; ok {
		t.Error("serviceAccountName is present with none configured")
	}
	if _, ok := objs["PersistentVolumeClaim"]["spec"].(map[string]any)["storageClassName"]; ok {
		t.Error("storageClassName is present with none configured, overriding the cluster default")
	}
}

func TestOptionalConfigIsIncludedWhenSet(t *testing.T) {
	in := testInput()
	in.Config.ImagePullSecret = "regcred"
	in.Config.ServiceAccount = "builder"
	in.Config.StorageClass = "fast"

	objs := decode(t, in)
	podSpec := podSpecOf(t, objs)

	if podSpec["serviceAccountName"] != "builder" {
		t.Errorf("serviceAccountName = %v", podSpec["serviceAccountName"])
	}
	pull := podSpec["imagePullSecrets"].([]any)
	if len(pull) != 1 || pull[0].(map[string]any)["name"] != "regcred" {
		t.Errorf("imagePullSecrets = %v", pull)
	}
	if objs["PersistentVolumeClaim"]["spec"].(map[string]any)["storageClassName"] != "fast" {
		t.Error("storageClassName did not reach the PVC")
	}
}

// Without a changed pod template a rebuild produces no new ReplicaSet, and the
// cluster goes on running the image it already pulled under the same tag.
func TestRebuildAnnotationChangesThePodTemplate(t *testing.T) {
	in := testInput()
	in.RebuiltAt = "2026-09-14T10:00:00Z"

	objs := decode(t, in)
	pod := objs["Deployment"]["spec"].(map[string]any)["template"].(map[string]any)
	ann := pod["metadata"].(map[string]any)["annotations"].(map[string]any)

	if ann[annRebuiltAt] != "2026-09-14T10:00:00Z" {
		t.Errorf("pod annotations = %v, want %s", ann, annRebuiltAt)
	}
}

func TestStoppedDeploymentHasZeroReplicas(t *testing.T) {
	in := testInput()
	in.Replicas = 0

	spec := decode(t, in)["Deployment"]["spec"].(map[string]any)
	if got := spec["replicas"]; got != float64(0) {
		t.Errorf("replicas = %v, want 0", got)
	}
}

// A home inside the workspace would mount one subPath over the other, and which
// wins is not worth leaving to chance.
func TestOverlappingMountsAreRefused(t *testing.T) {
	in := testInput()
	in.Dev.WorkspaceFolder = "/home/node/project"

	if _, err := buildManifest(in); err == nil {
		t.Error("buildManifest accepted a workspaceFolder inside the home directory")
	}
}

func TestMissingInputsAreRefused(t *testing.T) {
	in := testInput()
	in.Image = ""
	if _, err := buildManifest(in); err == nil {
		t.Error("buildManifest accepted an empty image")
	}

	in = testInput()
	in.Dev.WorkspaceFolder = ""
	if _, err := buildManifest(in); err == nil {
		t.Error("buildManifest accepted an empty workspaceFolder")
	}
}

// The real check on the hand-rolled structs: kubectl's own strict validation,
// which catches a misspelled or mistyped field that JSON assertions above would
// happily accept.
//
// It needs a reachable API server despite --dry-run=client, because strict
// validation is done against the server's OpenAPI schema. So it skips without
// one rather than failing — the assertions above still run everywhere.
func TestManifestPassesKubectlValidation(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl is not on PATH")
	}
	if err := exec.Command("kubectl", "cluster-info", "--request-timeout=2s").Run(); err != nil {
		t.Skip("no reachable cluster; strict validation needs the server's schema")
	}

	raw, err := buildManifest(testInput())
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	cmd := exec.Command("kubectl", "apply", "--dry-run=client", "--validate=strict", "-f", "-")
	cmd.Stdin = strings.NewReader(string(raw))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl rejected the manifest: %v\n%s", err, out)
	}
}

func podSpecOf(t *testing.T, objs map[string]map[string]any) map[string]any {
	t.Helper()
	spec := objs["Deployment"]["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)
	return pod["spec"].(map[string]any)
}

func containerField(t *testing.T, objs map[string]map[string]any, field string) any {
	t.Helper()
	containers := podSpecOf(t, objs)["containers"].([]any)
	return containers[0].(map[string]any)[field]
}
