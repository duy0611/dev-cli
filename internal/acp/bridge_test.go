package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func run(t *testing.T, b *Bridge, toAgent bool, in string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	var err error
	if toAgent {
		err = b.ToAgent(&out, strings.NewReader(in))
	} else {
		err = b.ToClient(&out, strings.NewReader(in))
	}
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(out.Bytes(), &msg); err != nil {
		t.Fatalf("output is not one JSON object: %q", out.String())
	}
	return msg
}

func TestInitializeStripsClientCapabilities(t *testing.T) {
	b := New(nil, "/workspaces/app", nil)
	msg := run(t, b, true, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"auth":{"terminal":true}}}}`+"\n")
	caps := msg["params"].(map[string]any)["clientCapabilities"].(map[string]any)
	_, fs := caps["fs"]
	_, term := caps["terminal"]
	_, authTerm := caps["auth"].(map[string]any)["terminal"]
	if fs || term || authTerm {
		t.Errorf("capabilities not stripped: %v", caps)
	}
}

func TestSessionNewMapsCwdAndDropsMCP(t *testing.T) {
	var log bytes.Buffer
	b := New([]Root{{Host: "/Users/me/src/app", Container: "/workspaces/app"}}, "/workspaces/app", &log)
	msg := run(t, b, true, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/Users/me/src/app/sub","mcpServers":[{"name":"x","command":"/bin/x","args":[],"env":[]}]}}`+"\n")
	p := msg["params"].(map[string]any)
	if p["cwd"] != "/workspaces/app/sub" {
		t.Errorf("cwd = %v", p["cwd"])
	}
	if len(p["mcpServers"].([]any)) != 0 {
		t.Errorf("mcpServers kept: %v", p["mcpServers"])
	}
	if !strings.Contains(log.String(), "dropping 1 MCP") {
		t.Errorf("log = %q", log.String())
	}
}

func TestSessionNewUnmappedCwdFallsBack(t *testing.T) {
	b := New([]Root{{Host: "/Users/me/src/app", Container: "/workspaces/app"}}, "/workspaces/app", nil)
	msg := run(t, b, true, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/Users/me","mcpServers":[]}}`+"\n")
	if got := msg["params"].(map[string]any)["cwd"]; got != "/workspaces/app" {
		t.Errorf("cwd = %v", got)
	}
}

func TestAgentPathsMapBackToHost(t *testing.T) {
	b := New([]Root{{Host: "/Users/me/src/app", Container: "/workspaces/app"}}, "", nil)
	msg := run(t, b, false, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"locations":[{"path":"/workspaces/app/x.go"}],"text":"see file:///workspaces/app/y.go, not /workspaces/apple"}}}`+"\n")
	u := msg["params"].(map[string]any)["update"].(map[string]any)
	if got := u["locations"].([]any)[0].(map[string]any)["path"]; got != "/Users/me/src/app/x.go" {
		t.Errorf("path = %v", got)
	}
	if got := u["text"]; got != "see file:///Users/me/src/app/y.go, not /workspaces/apple" {
		t.Errorf("text = %v", got)
	}
}

func TestNonJSONPassesThrough(t *testing.T) {
	var out bytes.Buffer
	if err := New(nil, "", nil).ToClient(&out, strings.NewReader("not json\n")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "not json\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestLargeIDsSurvive(t *testing.T) {
	var out bytes.Buffer
	if err := New(nil, "", nil).ToAgent(&out, strings.NewReader(`{"id":9007199254740993,"method":"x"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9007199254740993") {
		t.Errorf("out = %q", out.String())
	}
}

var appRoots = []Root{{Host: "/Users/me/src/app", Container: "/workspaces/app"}}

func TestInitializeKeepsTheOtherCapabilities(t *testing.T) {
	msg := run(t, New(nil, "", nil), true, `{"id":0,"method":"initialize","params":{"clientCapabilities":{"fs":{},"auth":{"terminal":true,"x":1},"_meta":{"k":true}}}}`+"\n")
	caps := msg["params"].(map[string]any)["clientCapabilities"].(map[string]any)
	if _, ok := caps["fs"]; ok {
		t.Error("fs kept")
	}
	if auth := caps["auth"].(map[string]any); auth["x"] != float64(1) || len(auth) != 1 {
		t.Errorf("auth = %v", auth)
	}
	if caps["_meta"] == nil {
		t.Error("unrelated capability lost")
	}
}

func TestInitializeWithoutCapabilities(t *testing.T) {
	msg := run(t, New(nil, "", nil), true, `{"id":0,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
	if _, ok := msg["params"].(map[string]any)["clientCapabilities"]; ok {
		t.Error("a capability block was invented")
	}
}

func TestSessionMethodsAllMapTheirCwd(t *testing.T) {
	for _, m := range []string{"session/new", "session/load", "session/resume"} {
		b := New(appRoots, "/workspaces/app", nil)
		msg := run(t, b, true, `{"id":1,"method":"`+m+`","params":{"sessionId":"s","cwd":"/Users/me/src/app/sub","mcpServers":[{"name":"a"},{"name":"b"}]}}`+"\n")
		p := msg["params"].(map[string]any)
		if p["cwd"] != "/workspaces/app/sub" || len(p["mcpServers"].([]any)) != 0 {
			t.Errorf("%s: params = %v", m, p)
		}
	}
}

func TestSessionOtherMethodsKeepTheirCwd(t *testing.T) {
	b := New(appRoots, "/workspaces/app", nil)
	msg := run(t, b, true, `{"id":1,"method":"session/prompt","params":{"cwd":"/elsewhere","mcpServers":[1]}}`+"\n")
	p := msg["params"].(map[string]any)
	if p["cwd"] != "/elsewhere" || len(p["mcpServers"].([]any)) != 1 {
		t.Errorf("params = %v", p)
	}
}

func TestFallbackAndDropAreLogged(t *testing.T) {
	var log bytes.Buffer
	b := New(appRoots, "/workspaces/app", &log)
	run(t, b, true, `{"id":1,"method":"session/new","params":{"cwd":"/Users/me","mcpServers":[{},{}],"additionalDirectories":["/Users/me/other"]}}`+"\n")
	for _, want := range []string{"/Users/me is not a folder", "using /workspaces/app", "dropping 2 MCP", "dropping additional directory /Users/me/other"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestAdditionalDirectoriesMapOrDrop(t *testing.T) {
	roots := append([]Root{{Host: "/Users/me/lib", Container: "/workspaces/lib"}}, appRoots...)
	b := New(roots, "/workspaces/app", nil)
	msg := run(t, b, true, `{"id":1,"method":"session/new","params":{"cwd":"/Users/me/src/app","mcpServers":[],"additionalDirectories":["/Users/me/lib/x","/Users/me/gone","/Users/me/src/app"]}}`+"\n")
	dirs := msg["params"].(map[string]any)["additionalDirectories"].([]any)
	if len(dirs) != 2 || dirs[0] != "/workspaces/lib/x" || dirs[1] != "/workspaces/app" {
		t.Errorf("dirs = %v", dirs)
	}
}

func TestClientPathsMapWholePathsOnly(t *testing.T) {
	b := New(appRoots, "", nil)
	msg := run(t, b, true, `{"id":1,"method":"session/prompt","params":{"prompt":[{"type":"text","text":"edit /Users/me/src/app/a.go and file:///Users/me/src/app/b.go but not /Users/me/src/apple or /x/Users/me/src/app, see /Users/me/src/app."}]}}`+"\n")
	text := msg["params"].(map[string]any)["prompt"].([]any)[0].(map[string]any)["text"]
	want := "edit /workspaces/app/a.go and file:///workspaces/app/b.go but not /Users/me/src/apple or /x/Users/me/src/app, see /workspaces/app."
	if text != want {
		t.Errorf("text = %v", text)
	}
}

func TestRewriteIsSinglePass(t *testing.T) {
	b := New([]Root{{Host: "/a", Container: "/b"}, {Host: "/b", Container: "/c"}}, "", nil)
	msg := run(t, b, true, `{"method":"x","params":{"p":"/a/x /b/y"}}`+"\n")
	if got := msg["params"].(map[string]any)["p"]; got != "/b/x /c/y" {
		t.Errorf("p = %v", got)
	}
}

func TestLongestRootWins(t *testing.T) {
	b := New([]Root{{Host: "/h", Container: "/c"}, {Host: "/h/deep", Container: "/d"}}, "", nil)
	msg := run(t, b, true, `{"method":"x","params":{"p":"/h/deep/f"}}`+"\n")
	if got := msg["params"].(map[string]any)["p"]; got != "/d/f" {
		t.Errorf("p = %v", got)
	}
}

func TestAgentResultsMapBackToo(t *testing.T) {
	b := New(appRoots, "", nil)
	msg := run(t, b, false, `{"id":1,"result":{"cwd":"/workspaces/app","list":["file:///workspaces/app/a"]}}`+"\n")
	r := msg["result"].(map[string]any)
	if r["cwd"] != "/Users/me/src/app" || r["list"].([]any)[0] != "file:///Users/me/src/app/a" {
		t.Errorf("result = %v", r)
	}
}

func TestNonObjectJSONPassesThrough(t *testing.T) {
	var out bytes.Buffer
	in := "[1, 2]\n  {\"a\" :1} trailing\n"
	if err := New(appRoots, "", nil).ToAgent(&out, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if out.String() != in {
		t.Errorf("out = %q", out.String())
	}
}

func TestNumbersSurviveExactly(t *testing.T) {
	var out bytes.Buffer
	in := `{"id":18446744073709551615,"params":{"a":1.0,"b":1e3,"c":0.1000000000000000055511151231257827}}` + "\n"
	if err := New(nil, "", nil).ToClient(&out, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"18446744073709551615", `"a":1.0`, `"b":1e3`, "0.1000000000000000055511151231257827"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s lost: %s", want, out.String())
		}
	}
}

func TestLastLineWithoutNewlineIsForwarded(t *testing.T) {
	var out bytes.Buffer
	if err := New(nil, "", nil).ToAgent(&out, strings.NewReader(`{"method":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"method":"x"`) {
		t.Errorf("out = %q", out.String())
	}
}

func TestTraceRecordsRewrittenMessages(t *testing.T) {
	var trace bytes.Buffer
	b := New(appRoots, "", nil)
	b.Trace = &trace
	run(t, b, true, `{"method":"x","params":{"p":"/Users/me/src/app"}}`+"\n")
	if !strings.HasPrefix(trace.String(), "-> ") || !strings.Contains(trace.String(), "/workspaces/app") {
		t.Errorf("trace = %q", trace.String())
	}
}

func TestSessionPathsAreRewrittenOnce(t *testing.T) {
	b := New([]Root{{Host: "/a", Container: "/b"}, {Host: "/b", Container: "/c"}}, "", nil)
	msg := run(t, b, true, `{"method":"session/new","params":{"cwd":"/a/x","additionalDirectories":["/a/y"],"note":"/a/z"}}`+"\n")
	p := msg["params"].(map[string]any)
	if p["cwd"] != "/b/x" {
		t.Errorf("cwd = %v, want /b/x", p["cwd"])
	}
	if dirs := p["additionalDirectories"].([]any); len(dirs) != 1 || dirs[0] != "/b/y" {
		t.Errorf("additionalDirectories = %v, want [/b/y]", dirs)
	}
	if p["note"] != "/b/z" {
		t.Errorf("note = %v", p["note"])
	}
}

func TestAgentPathsMatchTheLongestContainerPrefix(t *testing.T) {
	b := New([]Root{{Host: "/h1", Container: "/w"}, {Host: "/h2", Container: "/w/deep"}}, "", nil)
	msg := run(t, b, false, `{"result":{"p":"/w/deep/f /w/g"}}`+"\n")
	if got := msg["result"].(map[string]any)["p"]; got != "/h2/f /h1/g" {
		t.Errorf("p = %v", got)
	}
}
