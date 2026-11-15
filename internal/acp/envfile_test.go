package acp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":      `''`,
		"abc":   `'abc'`,
		"it's":  `'it'\''s'`,
		"a b$c": `'a b$c'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// The values must reach the adapter's environment byte for byte, through the
// same two commands the container runs, and leave no file behind.
func TestEnvFileRoundTripsThroughSh(t *testing.T) {
	values := map[string]string{
		"PLAIN":   "nsec1abc",
		"TRICKY":  "q'uo\"te $HOME `id` \\ back\nline two  ",
		"EMPTY":   "",
		"TRAILNL": "x\n\n",
	}
	var vars []Var
	for k, v := range values {
		vars = append(vars, Var{Key: k, Value: v})
	}
	path := filepath.Join(t.TempDir(), "env")

	w := exec.Command(WriteCommand(path)[0], WriteCommand(path)[1:]...)
	w.Stdin = bytes.NewReader(EnvFile(vars))
	if out, err := w.CombinedOutput(); err != nil {
		t.Fatalf("writer: %v: %s", err, out)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("env file: %v %v", fi, err)
	}
	// Created exclusively: a second write must not follow or replace it.
	w = exec.Command(WriteCommand(path)[0], WriteCommand(path)[1:]...)
	w.Stdin = strings.NewReader("")
	if err := w.Run(); err == nil {
		t.Error("the writer overwrote an existing file")
	}

	argv := Wrapper(path, []string{"sh", "-c", `printf '%s\0' "$PLAIN" "$TRICKY" "$EMPTY" "$TRAILNL"`})
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(out), "\x00")
	want := []string{values["PLAIN"], values["TRICKY"], values["EMPTY"], values["TRAILNL"], ""}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the env file survived the adapter's start: %v", err)
	}
}

func TestWrapperKeepsTheAdapterArgvIntact(t *testing.T) {
	argv := Wrapper("/tmp/f", []string{"adapter", "--x", "a b"})
	if argv[0] != "sh" || argv[1] != "-c" || argv[3] != "sh" || argv[4] != "/tmp/f" ||
		strings.Join(argv[5:], "|") != "adapter|--x|a b" {
		t.Errorf("argv = %q", argv)
	}
}

func TestEnvPathIsRandom(t *testing.T) {
	a, b := EnvPath(), EnvPath()
	if a == b || !strings.HasPrefix(a, "/tmp/.dev-acp-") {
		t.Errorf("%s %s", a, b)
	}
}
