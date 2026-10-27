//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubClaude stands in for claude inside the container: the smoke test has no
// credentials. The launcher runs a lead with --append-system-prompt and
// dispatch runs a worker with -p --output-format stream-json, so -p on argv is
// what tells the two apart.
const stubClaude = `#!/bin/sh
for arg in "$@"; do
	case "$arg" in
	-p | --print)
		cat >/dev/null
		printf '%s\n' '{"type":"assistant","message":{"id":"msg_stub_1","role":"assistant","content":[{"type":"text","text":"stub worker finished the task"}]},"parent_tool_use_id":null,"session_id":"stub-session"}'
		printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.0125,"session_id":"stub-session","result":"stub worker finished the task"}'
		exit 0
		;;
	esac
done

instruction=""
previous=""
for arg in "$@"; do
	if [ "$previous" = "--append-system-prompt" ]; then
		instruction=$arg
	fi
	previous=$arg
done

# The instruction names the dispatch binary's absolute in-container path. Using
# that path rather than one of our own also checks the instruction is usable.
team=$(printf '%s\n' "$instruction" | sed -n 's|^[[:space:]]*\(/[^[:space:]]*\) start .*$|\1|p' | head -n 1)
if [ -z "$team" ] || [ ! -x "$team" ]; then
	echo "claude stub: the lead instruction names no runnable dispatch binary (found '$team')" >&2
	exit 1
fi

id=$("$team" start coder <<'EOF'
Write the word hello into greeting.txt, then say what you did.
EOF
)
if [ $? -ne 0 ]; then
	echo "claude stub: starting the coder task failed" >&2
	exit 1
fi
"$team" wait "$id" || exit 1
"$team" result "$id" || exit 1
`

// teamProject is a project whose image carries the stub claude, so the launch's
// presence check finds it and the lead can run without credentials.
func teamProject(t *testing.T) string {
	t.Helper()

	project := t.TempDir()
	dc := filepath.Join(project, ".devcontainer")
	if err := os.MkdirAll(dc, 0o755); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	files := map[string]string{
		"Dockerfile": "FROM " + platformImage + "\n" +
			"COPY claude /usr/local/bin/claude\n" +
			"RUN chmod 0755 /usr/local/bin/claude\n",
		"claude": stubClaude,
		// The context is the folder holding this file, so COPY finds claude
		// beside it even after dev rewrites the document for the state mount.
		"devcontainer.json": `{"name": "dev-smoke-team", "build": {"dockerfile": "Dockerfile", "context": "."}}` + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dc, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing the fixture's %s: %v", name, err)
		}
	}
	return project
}

// The lead stub hands one coder task to the worker stub, waits, and ends. What
// is asserted afterwards is what the host can see: one done task, its log
// rendered rather than raw, and a lead that returned the worker's answer.
func TestSmokeAgentTeam(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-team"
		ws   = "dev-smoke-team-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name+"-state").Run()
	})

	project := teamProject(t)

	dev("provider", "configure", "dev-smoke-team-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-team-local")

	t.Log("creating the container; the first run pulls a base image")
	dev("container", "create", name, "--folder", project)

	t.Log("starting the team; the lead stub dispatches one coder task")
	started := dev("container", "start-agent-team", name,
		"--lead", "claude", "--workers", "claude", "--worker", "coder=stub-model")
	// The lead printed what `dev-team wait` and `result` returned, so this is
	// the worker's answer making it back through the team channel.
	if !strings.Contains(started, "stub worker finished the task") {
		t.Errorf("the lead did not print the worker's answer:\n%s", started)
	}

	rows := teamTaskRows(dev("container", "team", "list", name))
	if len(rows) != 1 {
		t.Fatalf("team list shows %d tasks, want exactly 1:\n%v", len(rows), rows)
	}
	if got := rows[0][1]; got != "coder" {
		t.Errorf("task role = %q, want %q", got, "coder")
	}
	if got := rows[0][2]; got != "done" {
		t.Errorf("task status = %q, want %q", got, "done")
	}

	logs := dev("container", "team", "logs", name)
	if strings.TrimSpace(logs) == "" {
		t.Errorf("team logs printed nothing for a finished task")
	}
	// The log is the worker's stream-json; the command owes the operator
	// readable lines, not the raw records.
	if strings.Contains(logs, `{"type"`) {
		t.Errorf("team logs printed raw stream-json:\n%s", logs)
	}
	if !strings.Contains(logs, "stub worker finished the task") {
		t.Errorf("team logs did not render the worker's message:\n%s", logs)
	}

	// A finished task has nothing left to tail, so --follow is a plain read.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	followed, err := exec.CommandContext(ctx, bin, "container", "team", "logs", name, "--follow").CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("team logs --follow did not return on a finished task")
	}
	if err != nil {
		t.Fatalf("team logs --follow: %v\n%s", err, followed)
	}
	if !strings.Contains(string(followed), "stub worker finished the task") {
		t.Errorf("team logs --follow did not render the worker's message:\n%s", followed)
	}
}

// A lead that cannot carry a standing instruction is refused before anything is
// resolved, so this needs no engine.
func TestStartAgentTeamRefusesAnUnsupportedLead(t *testing.T) {
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)

	_, stderr, code := runCode(t, bin, "container", "start-agent-team", "dev-smoke-team",
		"--lead", "hermes", "--workers", "claude", "--worker", "coder=stub-model")
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (a usage error); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "hermes") {
		t.Errorf("the refusal does not name the lead it refused:\n%s", stderr)
	}
}

// teamTaskRows returns the task rows of `team list`'s table: a row starts with
// an id of the shape the store hands out, "t" and digits.
func teamTaskRows(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && isTaskID(fields[0]) {
			rows = append(rows, fields)
		}
	}
	return rows
}

func isTaskID(id string) bool {
	if len(id) < 2 || id[0] != 't' {
		return false
	}
	for _, r := range id[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// runCode runs dev and returns both streams with the exit code, for the checks
// that assert a refusal's code rather than only its text.
func runCode(t *testing.T, bin string, args ...string) (string, string, int) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("dev %s: %v", strings.Join(args, " "), err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}
