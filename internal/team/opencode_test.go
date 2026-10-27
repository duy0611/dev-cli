package team

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	opencodeFixtureSession = "ses_e86276dc2ffeI3PY0EjSw3gBLr"
	opencodeFixtureCost    = 0.001748664
	opencodeFixtureEdit    = "/tmp/ocfix/note2.txt"
)

// The argv test proves the task route: WorkerCmd.Stdin is true and no element
// can carry the text; TestOpencodeSupervise proves the text arrives byte for
// byte.
func TestOpencodeArgvAndEnv(t *testing.T) {
	tests := []struct {
		name  string
		spec  WorkerSpec
		agent string
		want  []string
	}{
		{
			name:  "coder",
			spec:  WorkerSpec{Role: RoleCoder, Model: "litellm/deepseek-v4.1-flash", MaxTurns: 7},
			agent: "coder",
			want: []string{
				"opencode", "run", "--standalone", "--auto", "--format", "json",
				"-m", "litellm/deepseek-v4.1-flash", "--agent", "coder",
			},
		},
		{
			name:  "coder without a model",
			spec:  WorkerSpec{Role: RoleCoder, MaxTurns: 2},
			agent: "coder",
			want: []string{
				"opencode", "run", "--standalone", "--auto", "--format", "json",
				"--agent", "coder",
			},
		},
		{
			name:  "reviewer is read-only",
			spec:  WorkerSpec{Role: RoleReviewer, Model: "m", MaxTurns: 4},
			agent: "reviewer",
			want: []string{
				"opencode", "run", "--standalone", "--auto", "--format", "json",
				"-m", "m", "--agent", "reviewer",
			},
		},
		{
			name:  "researcher is read-only",
			spec:  WorkerSpec{Role: RoleResearcher, Model: "m", MaxTurns: 4},
			agent: "researcher",
			want: []string{
				"opencode", "run", "--standalone", "--auto", "--format", "json",
				"-m", "m", "--agent", "researcher",
			},
		},
		{
			name:  "resume",
			spec:  WorkerSpec{Role: RoleCoder, Model: "m", SessionID: "ses_abc", MaxTurns: 3},
			agent: "coder",
			want: []string{
				"opencode", "run", "--standalone", "--auto", "--format", "json",
				"-m", "m", "--agent", "coder", "-s", "ses_abc",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, parser, err := Worker("opencode", tt.spec)
			if err != nil {
				t.Fatalf("Worker: %v", err)
			}
			if parser == nil {
				t.Fatal("Worker returned no parser")
			}
			if !cmd.Stdin {
				t.Error("Stdin = false, want the task fed on stdin")
			}
			if !slices.Equal(cmd.Argv, tt.want) {
				t.Errorf("argv = %q, want %q", cmd.Argv, tt.want)
			}

			cfg, raw := opencodeEnvConfig(t, cmd.Env)
			agent, ok := cfg.Agents[tt.agent]
			if !ok {
				t.Fatalf("config %s does not define agent %q", raw, tt.agent)
			}
			if agent.Mode != "primary" {
				t.Errorf("agent mode = %q, want primary", agent.Mode)
			}
			steps := 0
			if agent.Steps != nil {
				steps = *agent.Steps
			}
			if steps != tt.spec.MaxTurns+1 {
				t.Errorf("steps = %d, want %d (the MaxTurns+1 backstop)", steps, tt.spec.MaxTurns+1)
			}

			var denied []string
			for _, rule := range agent.Permissions {
				if rule.Effect != "deny" {
					t.Errorf("permission = %+v, want only deny rules", rule)
				}
				if rule.Resource != "*" {
					t.Errorf("permission = %+v, want resource *", rule)
				}
				denied = append(denied, rule.Action)
			}
			readOnly := tt.spec.Role == RoleReviewer || tt.spec.Role == RoleResearcher
			if readOnly {
				want := []string{"edit", "write", "shell", "subagent", "execute"}
				if !slices.Equal(denied, want) {
					t.Errorf("denied actions = %q, want %q", denied, want)
				}
			} else if len(denied) != 0 {
				t.Errorf("denied actions = %q, want none for the coder", denied)
			}
		})
	}

	t.Run("unsupported tool", func(t *testing.T) {
		cmd, parser, err := Worker("codex", WorkerSpec{Role: RoleCoder, Model: "M"})
		if err == nil {
			t.Fatal("Worker accepted a tool it cannot run")
		}
		if !strings.Contains(err.Error(), "codex") {
			t.Errorf("error = %q, want it to name the tool", err)
		}
		if cmd.Argv != nil || parser != nil {
			t.Errorf("Worker returned %+v, %v with the error", cmd, parser)
		}
	})
}

type testOpencodePermission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

type testOpencodeAgent struct {
	Mode        string                   `json:"mode"`
	Steps       *int                     `json:"steps"`
	Permissions []testOpencodePermission `json:"permissions"`
}

type testOpencodeConfig struct {
	Agents map[string]testOpencodeAgent `json:"agents"`
}

func opencodeEnvConfig(t *testing.T, env []string) (testOpencodeConfig, string) {
	t.Helper()
	raw := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, opencodeConfigEnv+"="); ok {
			raw = value
		}
	}
	if raw == "" {
		t.Fatalf("env %q has no %s", env, opencodeConfigEnv)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", opencodeConfigEnv, err, raw)
	}
	if _, deprecated := top["agent"]; deprecated {
		t.Errorf("%s uses the deprecated agent key: %s", opencodeConfigEnv, raw)
	}
	if strings.Contains(raw, `"permission"`) {
		t.Errorf("%s uses the deprecated permission key: %s", opencodeConfigEnv, raw)
	}
	var cfg testOpencodeConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("decode %s: %v\n%s", opencodeConfigEnv, err, raw)
	}
	return cfg, raw
}

func opencodeFixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "opencode-stream.jsonl"))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	return p
}

func feedFixture(t *testing.T, p Parser, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		events = append(events, p.Feed(line)...)
	}
	return events
}

func TestOpencodeParserFixture(t *testing.T) {
	p := newOpencodeParser()
	events := feedFixture(t, p, opencodeFixturePath(t))

	got := p.Outcome()
	if got.Final != "DONE" {
		t.Errorf("Final = %q, want %q", got.Final, "DONE")
	}
	if got.SessionID != opencodeFixtureSession {
		t.Errorf("SessionID = %q, want %q", got.SessionID, opencodeFixtureSession)
	}
	if got.CostUSD == nil {
		t.Fatalf("CostUSD = nil, want the sum of both step_finish costs")
	}
	if diff := math.Abs(*got.CostUSD - opencodeFixtureCost); diff > 1e-12 {
		t.Errorf("CostUSD = %v, want %v", *got.CostUSD, opencodeFixtureCost)
	}
	if got.Turns != 2 {
		t.Errorf("Turns = %d, want 2 step_start events", got.Turns)
	}
	if want := []string{opencodeFixtureEdit}; !slices.Equal(got.Edited, want) {
		t.Errorf("Edited = %q, want %q", got.Edited, want)
	}
	if got.Failed {
		t.Error("Failed = true, want false")
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if want := []string{"text", "edit", "text", "result"}; !slices.Equal(kinds, want) {
		t.Errorf("event kinds = %q, want %q", kinds, want)
	}
	if rendered := Render(events[1]); rendered != "edit: write "+opencodeFixtureEdit {
		t.Errorf("Render(edit) = %q, want a readable edit line", rendered)
	}
}

func TestOpencodeParserSurvivesGarbage(t *testing.T) {
	p := newOpencodeParser()

	garbage := [][]byte{
		[]byte(`{"type":"step_start","part":`),
		[]byte("not json at all"),
		bytes.Repeat([]byte("x"), 200<<10),
	}
	for _, line := range garbage {
		events := p.Feed(line)
		if len(events) != 1 || events[0].Kind != "other" {
			t.Errorf("Feed(%.40q) = %+v, want one other event", line, events)
		}
	}
	got := p.Outcome()
	if got.Turns != 0 || got.Final != "" || got.CostUSD != nil || got.Failed || len(got.Edited) != 0 {
		t.Errorf("Outcome after garbage = %+v, want an untouched outcome", got)
	}

	// ReadBytes hands over the last line without its newline.
	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1"}`))
	events := p.Feed([]byte(`{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"DONE"}}`))
	if len(events) != 1 || events[0].Kind != "text" || events[0].Text != "DONE" {
		t.Fatalf("Feed(text) = %+v, want one text event", events)
	}
	if got := p.Outcome(); got.Final != "DONE" || got.Turns != 1 || got.SessionID != "ses_1" {
		t.Errorf("Outcome = %+v, want the later text line counted", got)
	}
}

func TestOpencodeParserTurnsLive(t *testing.T) {
	p := newOpencodeParser()
	steps := []string{
		`{"type":"step_start","sessionID":"ses_1"}`,
		`{"type":"step_start","sessionID":"ses_1"}`,
	}
	for i, line := range steps {
		p.Feed([]byte(line))
		if got := p.Outcome().Turns; got != i+1 {
			t.Errorf("running Turns = %d after %d step_start events, want %d", got, i+1, i+1)
		}
	}
}

func TestOpencodeParserWithoutFinalFinish(t *testing.T) {
	p := newOpencodeParser()
	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1"}`))
	p.Feed([]byte(`{"type":"tool_use","sessionID":"ses_1","part":{"tool":"write","state":{"status":"completed","input":{"path":"/tmp/oc/one.txt"}}}}`))
	p.Feed([]byte(`{"type":"step_finish","sessionID":"ses_1","part":{"reason":"tool-calls","cost":0.25}}`))
	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1"}`))
	p.Feed([]byte(`{"type":"text","sessionID":"ses_1","part":{"text":"DONE"}}`))

	got := p.Outcome()
	if got.Final != "DONE" {
		t.Errorf("Final = %q, want DONE without a closing step_finish", got.Final)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.25 {
		t.Errorf("CostUSD = %v, want the partial 0.25", got.CostUSD)
	}
	if got.Turns != 2 {
		t.Errorf("Turns = %d, want 2", got.Turns)
	}
	if want := []string{"/tmp/oc/one.txt"}; !slices.Equal(got.Edited, want) {
		t.Errorf("Edited = %q, want %q", got.Edited, want)
	}

	t.Run("no step_finish at all", func(t *testing.T) {
		p := newOpencodeParser()
		p.Feed([]byte(`{"type":"step_start"}`))
		p.Feed([]byte(`{"type":"text","part":{"text":"DONE"}}`))
		if cost := p.Outcome().CostUSD; cost != nil {
			t.Errorf("CostUSD = %v, want nil when no step_finish reported a cost", *cost)
		}
	})
}

func TestOpencodeParserFinalIsFromLastStep(t *testing.T) {
	p := newOpencodeParser()
	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1"}`))
	p.Feed([]byte(`{"type":"text","sessionID":"ses_1","part":{"text":"I'll write the file now."}}`))
	p.Feed([]byte(`{"type":"step_finish","sessionID":"ses_1","part":{"reason":"tool-calls","cost":0.25}}`))
	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1"}`))
	p.Feed([]byte(`{"type":"tool_use","sessionID":"ses_1","part":{"tool":"write","state":{"status":"completed","input":{"path":"/tmp/oc/one.txt"}}}}`))
	p.Feed([]byte(`{"type":"step_finish","sessionID":"ses_1","part":{"reason":"tool-calls","cost":0.5}}`))

	if got := p.Outcome().Final; got != "" {
		t.Errorf("Final = %q, want empty: the last step ended on a tool call", got)
	}
}

func TestOpencodeParserTolerant(t *testing.T) {
	p := newOpencodeParser()

	// One bad field must not discard the line or the turn it carries.
	p.Feed([]byte(`{"type":"step_start","sessionID":42}`))
	if got := p.Outcome(); got.Turns != 1 || got.SessionID != "" {
		t.Errorf("after a numeric sessionID: turns = %d, session = %q, want 1 and empty", got.Turns, got.SessionID)
	}

	if events := p.Feed([]byte(`{"type":"text","part":"not an object"}`)); len(events) != 1 || events[0].Kind != "other" {
		t.Errorf("Feed(text with a string part) = %+v, want one other event", events)
	}

	p.Feed([]byte(`{"type":"step_start","sessionID":"ses_1","part":{"snapshot":7}}`))
	p.Feed([]byte(`{"type":"text","sessionID":"ses_1","part":{"text":"DONE"},"timestamp":{"bad":true}}`))

	// A cost of the wrong type is ignored, not fatal.
	if events := p.Feed([]byte(`{"type":"step_finish","part":{"reason":"tool-calls","cost":"free"}}`)); len(events) != 0 {
		t.Errorf("Feed(step_finish with a string cost) = %+v, want no events", events)
	}

	// A tool input that is not an object has no path, so it is a plain tool call.
	events := p.Feed([]byte(`{"type":"tool_use","part":{"tool":"write","state":{"status":"completed","input":"not an object"}}}`))
	if len(events) != 1 || events[0].Kind != "tool" || events[0].Tool != "write" {
		t.Errorf("Feed(tool_use with a string input) = %+v, want one tool event", events)
	}

	got := p.Outcome()
	if got.Turns != 2 || got.Final != "DONE" || got.SessionID != "ses_1" {
		t.Errorf("Outcome = %+v, want turns 2, DONE, the surviving session", got)
	}
	if got.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil for a malformed cost", *got.CostUSD)
	}
	if len(got.Edited) != 0 {
		t.Errorf("Edited = %q, want none without a path", got.Edited)
	}
	if got.Failed {
		t.Error("Failed = true, want false")
	}
}

func TestOpencodeParserErrorEvent(t *testing.T) {
	p := newOpencodeParser()
	events := p.Feed([]byte(`{"type":"error","timestamp":1,"sessionID":"ses_1","error":{"type":"unknown","message":"Agent not found: \"coder\""}}`))
	if len(events) != 1 || events[0].Kind != "other" {
		t.Fatalf("Feed(error) = %+v, want one other event", events)
	}
	got := p.Outcome()
	if !got.Failed {
		t.Error("Failed = false, want true for an error event")
	}
	if got.SessionID != "ses_1" {
		t.Errorf("SessionID = %q, want ses_1", got.SessionID)
	}
}

func TestOpencodeParserFailReason(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "message",
			line: `{"type":"error","timestamp":1782345600000,"error":{"type":"unknown","message":"Invalid model reference: deepseek-v4.1-flash"}}`,
			want: "Invalid model reference: deepseek-v4.1-flash",
		},
		{
			name: "no error field",
			line: `{"type":"error"}`,
			want: "",
		},
		{
			name: "error field is not an object",
			line: `{"type":"error","error":"boom"}`,
			want: "",
		},
		{
			name: "empty message",
			line: `{"type":"error","error":{"type":"unknown","message":""}}`,
			want: "",
		},
		{
			name: "message is not a string",
			line: `{"type":"error","error":{"type":"unknown","message":42}}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newOpencodeParser()
			events := p.Feed([]byte(tt.line))
			if len(events) != 1 || events[0].Kind != "other" {
				t.Errorf("Feed(%s) = %+v, want one other event", tt.line, events)
			}
			got := p.Outcome()
			if !got.Failed {
				t.Error("Failed = false, want true for an error event")
			}
			if got.FailReason != tt.want {
				t.Errorf("FailReason = %q, want %q", got.FailReason, tt.want)
			}
		})
	}
}

func writeOpencodeStub(t *testing.T, dir, body string) {
	t.Helper()
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("write opencode stub: %v", err)
	}
}

func TestOpencodeSupervise(t *testing.T) {
	cfg := testConfig(map[Role]string{RoleCoder: "litellm/deepseek-v4.1-flash"})
	cfg.Workers = "opencode"
	h := newHarness(t, cfg)
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	argvFile := filepath.Join(t.TempDir(), "argv")
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("TEAM_TEST_STDIN", stdinFile)
	t.Setenv("TEAM_TEST_ARGV", argvFile)
	t.Setenv("TEAM_TEST_ENV", envFile)
	t.Setenv("TEAM_TEST_FIXTURE", opencodeFixturePath(t))
	writeOpencodeStub(t, h.bin, `cat > "$TEAM_TEST_STDIN"
printf '%s\n' "$@" > "$TEAM_TEST_ARGV"
env > "$TEAM_TEST_ENV"
cat "$TEAM_TEST_FIXTURE"
`)

	text := "- leading dash\n--help\n$(touch /tmp/dev-team-opencode-should-not-exist) `id`\n\nsecond para"
	task := h.task(RoleCoder, text, t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	got := h.load(task.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want %q", got.Status, StatusDone)
	}
	if got.SessionID != opencodeFixtureSession {
		t.Errorf("session id = %q, want %q", got.SessionID, opencodeFixtureSession)
	}
	if got.CostUSD == nil || math.Abs(*got.CostUSD-opencodeFixtureCost) > 1e-12 {
		t.Errorf("cost = %v, want %v", got.CostUSD, opencodeFixtureCost)
	}
	if got.Turns != 2 {
		t.Errorf("turns = %d, want 2", got.Turns)
	}
	if answer := h.result(task.ID); answer != "DONE" {
		t.Errorf("result.md = %q, want DONE", answer)
	}
	fixture, err := os.ReadFile(opencodeFixturePath(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if log := h.log(task.ID); !bytes.Equal(log, fixture) {
		t.Errorf("log.jsonl is %d bytes, want the %d byte fixture verbatim", len(log), len(fixture))
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if string(stdin) != text {
		t.Errorf("stdin = %q, want the task text byte for byte", stdin)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	for _, want := range []string{"run", "--standalone", "--auto", "--format", "json", "-m", "litellm/deepseek-v4.1-flash", "--agent", "coder"} {
		if !slices.Contains(strings.Split(strings.TrimSpace(string(argv)), "\n"), want) {
			t.Errorf("argv = %q, want %q", argv, want)
		}
	}
	if strings.Contains(string(argv), "leading dash") {
		t.Errorf("argv = %q, want the task text only on stdin", argv)
	}

	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read recorded env: %v", err)
	}
	var recorded []string
	for _, line := range strings.Split(string(env), "\n") {
		if strings.HasPrefix(line, opencodeConfigEnv+"=") {
			recorded = append(recorded, line)
		}
	}
	config, raw := opencodeEnvConfig(t, recorded)
	agent, ok := config.Agents["coder"]
	if !ok {
		t.Fatalf("worker env config %s does not define the coder agent", raw)
	}
	steps := 0
	if agent.Steps != nil {
		steps = *agent.Steps
	}
	if steps != cfg.MaxTurns+1 {
		t.Errorf("steps = %d, want %d", steps, cfg.MaxTurns+1)
	}
}

func TestOpencodeSuperviseTurnLimit(t *testing.T) {
	cfg := testConfig(map[Role]string{RoleCoder: "m"})
	cfg.Workers = "opencode"
	cfg.MaxTurns = 2
	h := newHarness(t, cfg)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("TEAM_TEST_PID", pidFile)
	writeOpencodeStub(t, h.bin, `cat > /dev/null
echo $$ > "$TEAM_TEST_PID"
printf '%s\n' '{"type":"step_start","sessionID":"ses_1"}'
printf '%s\n' '{"type":"step_start","sessionID":"ses_1"}'
printf '%s\n' '{"type":"step_start","sessionID":"ses_1"}'
sleep 300
`)

	task := h.task(RoleCoder, "loop", t.TempDir())
	if err := h.supervise(task.ID); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	got := h.load(task.ID)
	if got.Status != StatusTurnLimit {
		t.Errorf("status = %q, want %q", got.Status, StatusTurnLimit)
	}
	if got.Turns != 3 {
		t.Errorf("turns = %d, want the 3 step_start events", got.Turns)
	}
	if answer := h.result(task.ID); !strings.Contains(answer, "hit the turn limit") {
		t.Errorf("result.md = %q, want it to say hit the turn limit", answer)
	}
	assertGone(t, readPid(t, pidFile), "worker")
}
