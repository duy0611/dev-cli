package relay

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeContainer stands in for a provider's exec channel by running commands on
// this machine instead of in a container. The relay does not know the
// difference: it copies a binary in, runs it, and talks to its pipes.
type fakeContainer struct {
	root string // stands in for the container's filesystem

	mu       sync.Mutex
	commands [][]string
	started  []*exec.Cmd
}

func newFakeContainer(t *testing.T) *fakeContainer {
	t.Helper()
	// Short path: the relay's socket lives beside the binary here, and a unix
	// socket path is capped near 100 bytes.
	root, err := os.MkdirTemp("", "fc")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeContainer{root: root}
	t.Cleanup(func() {
		f.mu.Lock()
		for _, c := range f.started {
			if c.Process != nil {
				_ = c.Process.Kill()
			}
		}
		f.mu.Unlock()
		os.RemoveAll(root)
	})
	return f
}

// translate rewrites the /tmp paths the relay chooses into the fake
// container's own directory, so a test cannot scribble on the real /tmp.
func (f *fakeContainer) translate(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "/tmp/", f.root+"/")
	}
	return out
}

func (f *fakeContainer) run(ctx context.Context, command []string, stdin io.Reader) (string, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()

	args := f.translate(command)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	return string(out), err
}

func (f *fakeContainer) start(ctx context.Context, command []string) (*Pipes, error) {
	args := f.translate(command)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.started = append(f.started, cmd)
	f.mu.Unlock()

	return &Pipes{
		Stdin:  stdin,
		Stdout: stdout,
		Wait:   cmd.Wait,
		Kill:   func() error { return cmd.Process.Kill() },
	}, nil
}

// socketPath maps a container-side socket path back to this machine's.
func (f *fakeContainer) socketPath(s string) string {
	return strings.ReplaceAll(s, "/tmp/", f.root+"/")
}

func (f *fakeContainer) ran(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if len(c) > 0 && c[0] == name {
			return true
		}
	}
	return false
}

// buildRelay compiles cmd/dev-relay for this machine and returns its bytes, so
// the session test exercises the real program rather than a stand-in.
func buildRelay(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out := filepath.Join(t.TempDir(), "dev-relay")
	build := exec.Command("go", "build", "-o", out, "github.com/duy0611/dev-cli/cmd/dev-relay")
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dev-relay: %v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// startWith runs a session against the fake container using a relay built for
// this machine, bypassing the embedded per-architecture copies.
func startWith(t *testing.T, f *fakeContainer, bin []byte, agentSocket string) *Session {
	t.Helper()

	binPath := filepath.Join("/tmp", ".dev-relay-test")
	if err := install(context.Background(), f.run, binPath, bin); err != nil {
		t.Fatal(err)
	}

	suffix, err := randomSuffix()
	if err != nil {
		t.Fatal(err)
	}
	socket := "/tmp/.dev-agent-" + suffix + ".sock"

	pipes, err := f.start(context.Background(), []string{binPath, socket})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = Host(agentSocket, pipes.Stdout, pipes.Stdin) }()

	s := &Session{Socket: socket, pipes: pipes}
	t.Cleanup(func() { _ = s.Close() })
	waitForSocket(t, f.socketPath(socket))
	return s
}

// TestSessionEndToEnd is the whole path: install the real relay binary into a
// stand-in container, run it, and reach an agent through the socket it creates.
func TestSessionEndToEnd(t *testing.T) {
	f := newFakeContainer(t)
	s := startWith(t, f, buildRelay(t), echoServer(t))

	conn, err := dialUnix(f.socketPath(s.Socket))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	want := "through the session"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != want {
		t.Errorf("round trip = %q, want %q", buf, want)
	}
}

// TestSessionCloseRemovesSocket covers the teardown that matters: a session
// that left its socket behind would leave a channel to the operator's agent
// that nobody is watching.
func TestSessionCloseRemovesSocket(t *testing.T) {
	f := newFakeContainer(t)
	s := startWith(t, f, buildRelay(t), echoServer(t))

	path := f.socketPath(s.Socket)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket missing before close: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent, because callers defer it and may also close early.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	waitForGone(t, path)
}

// TestSessionInstallSkipsWhenPresent covers the copy being skipped on a second
// session. Without it every command would push the whole binary through the
// exec channel before doing any work.
func TestSessionInstallSkipsWhenPresent(t *testing.T) {
	f := newFakeContainer(t)
	bin := []byte("#!/bin/sh\nexit 0\n")
	path := "/tmp/.dev-relay-skip"

	if err := install(context.Background(), f.run, path, bin); err != nil {
		t.Fatal(err)
	}
	if !f.ran("sh") {
		t.Fatal("first install should have copied the binary")
	}

	f.mu.Lock()
	f.commands = nil
	f.mu.Unlock()

	if err := install(context.Background(), f.run, path, bin); err != nil {
		t.Fatal(err)
	}
	if f.ran("sh") {
		t.Error("second install copied the binary again")
	}
}

// TestSessionInstallIsAtomic covers a session dying mid-copy: the next one must
// not find a half-written file and try to run it.
func TestSessionInstallIsAtomic(t *testing.T) {
	f := newFakeContainer(t)
	path := "/tmp/.dev-relay-atomic"

	if err := install(context.Background(), f.run, path, []byte("#!/bin/sh\nexit 0\n")); err != nil {
		t.Fatal(err)
	}
	// The temporary name must not survive a completed install, or `test -x`
	// would eventually find one and skip a copy that never finished.
	if _, err := os.Stat(f.socketPath(path + ".part")); err == nil {
		t.Error("the partial file was left behind")
	}
}
