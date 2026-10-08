package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/store"
)

// stubMergedConfig puts a devcontainer on PATH whose read-configuration
// reports merged as the merged configuration, and which logs every call.
func stubMergedConfig(t *testing.T, merged string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$1\" in read-configuration) cat '" + filepath.Join(dir, "merged.json") + "';; esac\nexit 0\n"
	writeFile(t, filepath.Join(dir, "merged.json"), `{"mergedConfiguration":`+merged+`}`)
	writeFile(t, filepath.Join(dir, "devcontainer"), script)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// A configuration that reaches the host is refused at create, exit 2, and
// leaves no row behind — the refusal costs nothing.
func TestCreateRefusesAConfigurationThatReachesTheHost(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubMergedConfig(t, `{"privileged": true, "mounts": ["source=/var/run/docker.sock,target=/s,type=bind"]}`)

	err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{})
	if exitCodeOf(err) != exitUsage {
		t.Fatalf("exit %d, want %d: %v", exitCodeOf(err), exitUsage, err)
	}
	for _, want := range []string{"privileged", "docker.sock", "--allow-privileged"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	st, _ := a.store()
	if _, err := st.GetContainer("ws", "api"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a row was written for a refused container: %v", err)
	}
	if strings.Contains(callsIn(t, log), "up ") {
		t.Errorf("up ran for a refused container:\n%s", callsIn(t, log))
	}
	refused := auditEntries(t, audit.Filter{Events: []string{"refused"}})
	if len(refused) != 1 || refused[0].Fields["reason"] != "escape" {
		t.Errorf("refused records = %+v", refused)
	}
}

func TestAllowPrivilegedCreatesItAnyway(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubMergedConfig(t, `{"privileged": true}`)

	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{allowPrivileged: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c := containerRow(t, a, "api"); !c.AllowPrivileged {
		t.Error("AllowPrivileged not recorded")
	}
	// Allowed means not asked: the merged configuration is not even read.
	if strings.Contains(callsIn(t, log), "read-configuration") {
		t.Errorf("an allowed container was still checked:\n%s", callsIn(t, log))
	}
}

// A configuration that has come to ask for the host since create is refused at
// rebuild, before the old container is touched.
func TestRebuildRefusesAConfigurationThatNowReachesTheHost(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	dir := filepath.Dir(stubMergedConfig(t, `{}`))
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, filepath.Join(dir, "merged.json"), `{"mergedConfiguration":{"runArgs":["--pid=host"]}}`)

	err := runIn(t, a, "container", "rebuild", "api")
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "--pid=host") {
		t.Fatalf("rebuild: exit %d, %v", exitCodeOf(err), err)
	}
	if strings.Contains(callsIn(t, filepath.Join(dir, "calls")), "--remove-existing-container") {
		t.Error("the container was rebuilt anyway")
	}
}

// --no-start records the container with no engine involved; the first start
// is where the check happens instead.
func TestNoStartDefersTheCheckToStart(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubMergedConfig(t, `{"privileged": true}`)

	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create --no-start: %v", err)
	}
	err := a.start(t.Context(), "ws", containerRow(t, a, "api"))
	if exitCodeOf(err) != exitUsage {
		t.Errorf("start: exit %d, want %d: %v", exitCodeOf(err), exitUsage, err)
	}
}

// A generated configuration is dev's own and holds nothing privileged, so the
// CLI is not asked about it.
func TestGeneratedConfigIsNotChecked(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubMergedConfig(t, `{"privileged": true}`)

	if err := runContainerCreate(t.Context(), a, "", "api", "", createOpts{noFolder: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(callsIn(t, log), "read-configuration") {
		t.Errorf("a generated configuration was read:\n%s", callsIn(t, log))
	}
}

// A compose project's privileges live in the compose file.
func TestCreateRefusesAPrivilegedComposeService(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := projectWithConfig(t)
	writeFile(t, filepath.Join(folder, ".devcontainer", "compose.yml"),
		"services:\n  app:\n    image: x\n    privileged: true\n")
	stubMergedConfig(t, `{"dockerComposeFile": "compose.yml", "service": "app"}`)

	err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{})
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "compose.yml service app") {
		t.Fatalf("exit %d, %v", exitCodeOf(err), err)
	}
}
