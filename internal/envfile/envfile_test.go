package envfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/provider"
)

// The values must reach the command's environment byte for byte, through the
// same two commands the container runs, and leave no file behind.
func TestRenderRoundTripsThroughSh(t *testing.T) {
	values := map[string]string{
		"PLAIN":   "nsec1abc",
		"TRICKY":  "q'uo\"te $HOME `id` \\ back\nline two  ",
		"EMPTY":   "",
		"TRAILNL": "x\n\n",
	}
	var vars []provider.EnvVar
	for k, v := range values {
		vars = append(vars, provider.EnvVar{Key: k, Value: v})
	}
	path := filepath.Join(t.TempDir(), "env")

	w := exec.Command(WriteCommand(path)[0], WriteCommand(path)[1:]...)
	w.Stdin = bytes.NewReader(Render(vars))
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
		t.Errorf("the env file survived the command's start: %v", err)
	}
}

func TestWrapperKeepsTheCommandArgvIntact(t *testing.T) {
	argv := Wrapper("/tmp/f", []string{"adapter", "--x", "a b"})
	if argv[0] != "sh" || argv[1] != "-c" || argv[3] != "sh" || argv[4] != "/tmp/f" ||
		strings.Join(argv[5:], "|") != "adapter|--x|a b" {
		t.Errorf("argv = %q", argv)
	}
}

// /dev/shm is a tmpfs in docker, podman and Kubernetes pods, so a file left by
// a killed dev never reaches the container's disk.
func TestPathIsRandomAndInMemory(t *testing.T) {
	a, b := Path(), Path()
	if a == b || !strings.HasPrefix(a, "/dev/shm/.dev-env-") {
		t.Errorf("%s %s", a, b)
	}
}

func TestPassWithNoVarsLeavesTheCommandAlone(t *testing.T) {
	called := false
	got, err := Pass(nil, []string{"claude", "-p"}, func([]string, io.Reader) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || !slices.Equal(got, []string{"claude", "-p"}) {
		t.Errorf("Pass = %q, write called %v", got, called)
	}
}

func TestPassWritesTheValuesOnStdinAndWrapsTheCommand(t *testing.T) {
	var writeArgv []string
	var stdin []byte
	got, err := Pass([]provider.EnvVar{{Key: "TOKEN", Value: "s3cret"}}, []string{"claude"}, func(argv []string, in io.Reader) error {
		writeArgv = argv
		stdin, _ = io.ReadAll(in)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(writeArgv, " "), "s3cret") || strings.Contains(strings.Join(got, " "), "s3cret") {
		t.Errorf("a value reached an argv: write %q, command %q", writeArgv, got)
	}
	if string(stdin) != "TOKEN='s3cret'\n" {
		t.Errorf("stdin = %q", stdin)
	}
	path := writeArgv[len(writeArgv)-1]
	if !slices.Equal(writeArgv, WriteCommand(path)) || !slices.Equal(got, Wrapper(path, []string{"claude"})) {
		t.Errorf("write %q and command %q do not share one file", writeArgv, got)
	}
}

func TestPassReportsAFailedWrite(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Pass([]provider.EnvVar{{Key: "A", Value: "b"}}, []string{"x"}, func([]string, io.Reader) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("Pass error = %v, want boom", err)
	}
}

// The file is eval'd, so a key is a shell word and must be a plain name.
func TestPassRefusesAKeyThatIsNotAName(t *testing.T) {
	for _, key := range []string{"", "A B", "A;id", "1A", "A=B"} {
		_, err := Pass([]provider.EnvVar{{Key: key, Value: "v"}}, []string{"x"}, func([]string, io.Reader) error {
			t.Fatalf("wrote for key %q", key)
			return nil
		})
		if err == nil {
			t.Errorf("Pass accepted key %q", key)
		}
	}
}
