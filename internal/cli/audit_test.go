package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/store"
)

// stubEngine puts a devcontainer and a docker on PATH that succeed, with
// docker reporting one running container for any lookup — enough for start,
// exec, stop and remove to run end to end without an engine.
func stubEngine(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	scripts := map[string]string{
		"devcontainer": "#!/bin/sh\nexit 0\n",
		// `ps --format {{.State}}` is how Status asks; `ps -aq` is how the
		// container's id is found. Both say there is one, and it is running.
		"docker": "#!/bin/sh\ncase \"$*\" in\n" +
			"  *State*) echo running ;;\n" +
			"  ps*) echo abc123 ;;\n" +
			"esac\nexit 0\n",
	}
	for name, body := range scripts {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func auditEntries(t *testing.T, f audit.Filter) []audit.Entry {
	t.Helper()
	path, err := store.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := audit.Read(filepath.Join(filepath.Dir(path), audit.FileName), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func auditBytes(t *testing.T) string {
	t.Helper()
	path, _ := store.DefaultPath()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The invariant the whole log rests on: a resolved setting never reaches it.
// The setting is a literal, so its value is resolved and handed to every
// command below — and must appear in none of the records they write.
func TestAuditLogNeverHoldsASettingValue(t *testing.T) {
	const secret = "sk-SENTINEL-must-never-be-logged"
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEngine(t)
	if err := runWorkspaceSet(a, "", "ANTHROPIC_API_KEY", "literal:"+secret); err != nil {
		t.Fatalf("set: %v", err)
	}

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "api", folder, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runContainerExec(t.Context(), a, "", "api", []string{"env"}, false, nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, argv := range [][]string{
		{"container", "rebuild", "api"},
		{"container", "stop", "api"},
		{"container", "remove", "api"},
	} {
		if err := runIn(t, a, argv...); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}

	log := auditBytes(t)
	if strings.Contains(log, secret) {
		t.Fatalf("the audit log holds a resolved setting:\n%s", log)
	}
	got := map[string]bool{}
	for _, e := range auditEntries(t, audit.Filter{}) {
		got[e.Event] = true
	}
	for _, want := range []string{"create", "start", "exec", "rebuild", "stop", "remove"} {
		if !got[want] {
			t.Errorf("no %q record; log:\n%s", want, log)
		}
	}
}

// An exec record carries what the operator ran and how it ended.
func TestAuditExecRecordsArgvAndExitCode(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEngine(t)
	if err := runContainerCreate(t.Context(), a, "", "api", "", createOpts{noFolder: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runContainerExec(t.Context(), a, "", "api", []string{"go", "test"}, false, nil); err != nil {
		t.Fatalf("exec: %v", err)
	}

	execs := auditEntries(t, audit.Filter{Events: []string{"exec"}})
	if len(execs) != 1 {
		t.Fatalf("got %d exec records, want 1", len(execs))
	}
	e := execs[0]
	if e.Workspace() != "ws" || e.Container() != "api" {
		t.Errorf("record names %s/%s", e.Workspace(), e.Container())
	}
	if joinArgv(e.Fields["argv"]) != "go test" {
		t.Errorf("argv = %v", e.Fields["argv"])
	}
	if e.Fields["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v", e.Fields["exit_code"])
	}
	for _, k := range []string{"started", "ended"} {
		if _, err := time.Parse(time.RFC3339Nano, e.Fields[k].(string)); err != nil {
			t.Errorf("%s = %v: %v", k, e.Fields[k], err)
		}
	}
}

func auditCmd(t *testing.T, a *app, out *bytes.Buffer, argv ...string) (int, string) {
	t.Helper()
	out.Reset()
	root := newRootCmd("test")
	root.AddCommand(newAuditCmd(a))
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs(append([]string{"audit"}, argv...))
	err := root.Execute()
	return exitCodeOf(err), out.String()
}

func TestAuditCommand(t *testing.T) {
	a, out := newTestApp(t)
	a.record("create", "ws", "api", map[string]any{"source_kind": "folder"})
	a.record("exec", "ws", "api", map[string]any{"argv": []string{"ls"}, "exit_code": 2})
	a.record("create", "other", "web", nil)

	code, text := auditCmd(t, a, out)
	if code != 0 || strings.Count(text, "\n") != 3 {
		t.Errorf("all: exit %d, output:\n%s", code, text)
	}
	if !strings.Contains(text, "ws/api") || !strings.Contains(text, "ls, exit 2") {
		t.Errorf("summary missing:\n%s", text)
	}

	_, text = auditCmd(t, a, out, "--workspace", "ws", "--event", "exec")
	if strings.Count(text, "\n") != 1 || !strings.Contains(text, "exec") {
		t.Errorf("filtered:\n%s", text)
	}

	// A removed container still has history, so an unknown name is not 3.
	code, text = auditCmd(t, a, out, "--container", "gone")
	if code != 0 || text != "" {
		t.Errorf("unknown name: exit %d, output %q", code, text)
	}

	_, text = auditCmd(t, a, out, "--json", "--container", "web")
	if !strings.HasPrefix(text, "{") || strings.Count(text, "\n") != 1 {
		t.Errorf("--json:\n%s", text)
	}

	if code, _ := auditCmd(t, a, out, "--since", "yesterday"); code != exitUsage {
		t.Errorf("malformed --since: exit %d, want %d", code, exitUsage)
	}
	if code, text := auditCmd(t, a, out, "--since", "1h"); code != 0 || strings.Count(text, "\n") != 3 {
		t.Errorf("--since 1h: exit %d\n%s", code, text)
	}
}

func TestAuditCommandWithNoLogPrintsNothing(t *testing.T) {
	a, out := newTestApp(t)
	if code, text := auditCmd(t, a, out); code != 0 || text != "" {
		t.Errorf("exit %d, output %q", code, text)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if got, _ := parseSince("2h", now); !got.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("2h = %v", got)
	}
	if got, _ := parseSince("2026-10-01T00:00:00Z", now); got.Day() != 1 {
		t.Errorf("rfc3339 = %v", got)
	}
	for _, bad := range []string{"-1h", "soon", "2026-10-01"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
