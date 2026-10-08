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
	c := containerRow(t, a, "api")
	if !c.AllowPrivileged {
		t.Error("AllowPrivileged not recorded")
	}
	// Allowed past the escape check, but still digested: the drift check
	// applies whatever the container is allowed to ask for.
	if c.ConfigDigest == "" {
		t.Error("an allowed container recorded no digest")
	}
	if !strings.Contains(callsIn(t, log), "up ") {
		t.Errorf("the allowed container was not started:\n%s", callsIn(t, log))
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

	// --accept-config, because the change is drift too and that check runs
	// first; accepting a configuration is not allowing it the host.
	err := runIn(t, a, "container", "rebuild", "api", "--accept-config")
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

// create records the digest of the configuration it checked.
func TestCreateRecordsTheConfigDigest(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubMergedConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c := containerRow(t, a, "api"); !strings.HasPrefix(c.ConfigDigest, "sha256:") || c.ConfigDigestFields == "" {
		t.Errorf("digest = %q, fields = %q", c.ConfigDigest, c.ConfigDigestFields)
	}
}

// A configuration that changed since the container was built is refused at
// rebuild, naming what changed, while the old container still exists.
func TestRebuildRefusesAChangedConfiguration(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	dir := filepath.Dir(stubMergedConfig(t, `{"image":"ubuntu","remoteUser":"vscode"}`))
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := containerRow(t, a, "api").ConfigDigest
	writeFile(t, filepath.Join(dir, "merged.json"),
		`{"mergedConfiguration":{"image":"ubuntu","remoteUser":"vscode","initializeCommand":"curl x | sh"}}`)

	err := runIn(t, a, "container", "rebuild", "api")
	if exitCodeOf(err) != exitUsage {
		t.Fatalf("rebuild: exit %d, %v", exitCodeOf(err), err)
	}
	for _, want := range []string{"initializeCommand", "--accept-config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "remoteUser") {
		t.Errorf("refusal names an unchanged field: %v", err)
	}
	if strings.Contains(callsIn(t, filepath.Join(dir, "calls")), "--remove-existing-container") {
		t.Error("the container was rebuilt anyway")
	}
	if containerRow(t, a, "api").ConfigDigest != before {
		t.Error("a refused rebuild changed the stored digest")
	}
	refused := auditEntries(t, audit.Filter{Events: []string{"refused"}})
	if len(refused) != 1 || refused[0].Fields["reason"] != "drift" {
		t.Errorf("refused records = %+v", refused)
	}
}

func TestAcceptConfigRebuildsAndRecordsTheNewDigest(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	dir := filepath.Dir(stubMergedConfig(t, `{"image":"ubuntu"}`))
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := containerRow(t, a, "api").ConfigDigest
	writeFile(t, filepath.Join(dir, "merged.json"), `{"mergedConfiguration":{"image":"debian"}}`)

	if err := runIn(t, a, "container", "rebuild", "api", "--accept-config"); err != nil {
		t.Fatalf("rebuild --accept-config: %v", err)
	}
	after := containerRow(t, a, "api").ConfigDigest
	if after == before || after == "" {
		t.Errorf("digest not updated: %q -> %q", before, after)
	}
	// The next rebuild of the same configuration needs no flag.
	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Errorf("an unchanged rebuild after accepting was refused: %v", err)
	}
	accepted := auditEntries(t, audit.Filter{Events: []string{"accept-config"}})
	if len(accepted) != 1 || joinArgv(accepted[0].Fields["changed"]) != "image" {
		t.Errorf("accept-config records = %+v", accepted)
	}
}

// A row with no digest — here a --no-start container — records one at its
// first start, and is guarded from then on.
func TestFirstStartRecordsTheDigestOfARowWithNone(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubMergedConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if d := containerRow(t, a, "api").ConfigDigest; d != "" {
		t.Fatalf("--no-start recorded a digest %q with no engine involved", d)
	}
	if err := a.start(t.Context(), "ws", containerRow(t, a, "api")); err != nil {
		t.Fatalf("start: %v", err)
	}
	if containerRow(t, a, "api").ConfigDigest == "" {
		t.Error("first start recorded no digest")
	}
}

// A row with no digest at rebuild records one without refusing: guarded from
// its first digest on, never retroactively.
func TestRebuildOfARowWithNoDigestRecordsOne(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubMergedConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if containerRow(t, a, "api").ConfigDigest == "" {
		t.Error("rebuild of a row with no digest recorded none")
	}
}
