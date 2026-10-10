package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		_ = os.RemoveAll(root)
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

// testRetry is defaultRetry scaled down so a test sees a whole outage —
// backoff, budget and give-up — in well under a second.
var testRetry = retryPolicy{
	first:  10 * time.Millisecond,
	max:    50 * time.Millisecond,
	budget: 500 * time.Millisecond,
	stable: 200 * time.Millisecond,
}

// startWith runs a session against the fake container using a relay built for
// this machine, bypassing the embedded per-architecture copies.
func startWith(t *testing.T, f *fakeContainer, bin []byte, agentSocket string) *Session {
	t.Helper()
	return startWithOpts(t, f, bin, agentSocket, f.start, testRetry)
}

// startWithOpts is startWith with the start function and retry policy chosen
// by the test, which is how a test makes a respawn fail or flap.
func startWithOpts(t *testing.T, f *fakeContainer, bin []byte, agentSocket string,
	start StartFunc, retry retryPolicy) *Session {
	t.Helper()

	suffix, err := randomSuffix()
	if err != nil {
		t.Fatal(err)
	}
	socket := "/tmp/.dev-agent-" + suffix + ".sock"

	// Through open, exactly as Start does, so these tests exercise the same
	// pump that respawns and reports rather than one beside it.
	s, err := open(context.Background(), f.run, start, "/tmp/.dev-relay-test", bin,
		socket, agentSocket, retry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	waitForSocket(t, f.socketPath(socket))
	return s
}

// dropRelay kills the running relay the way a collapsed exec channel looks
// from the host: the process gone, Host's stdin at EOF, no Close behind it.
func dropRelay(t *testing.T, s *Session) {
	t.Helper()
	s.mu.Lock()
	p := s.pipes
	s.mu.Unlock()
	if p == nil {
		t.Fatal("no relay running to drop")
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
}

// waitForDrops polls until the session has recorded n drops.
func waitForDrops(t *testing.T, s *Session, n int) []Drop {
	t.Helper()
	var d []Drop
	waitFor(t, func() bool { d = s.Drops(); return len(d) >= n },
		fmt.Sprintf("expected %d drops", n))
	return d
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
	defer func() { _ = conn.Close() }()

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

// TestSessionClosePrintsNoWarning is the other half, and the reason the flag
// exists at all: an ordinary shutdown reaches Host the same way, so without it
// every clean session would end by telling the operator it had failed.
func TestSessionClosePrintsNoWarning(t *testing.T) {
	f := newFakeContainer(t)
	stderr := captureStderr(t)
	s := startWith(t, f, buildRelay(t), echoServer(t))

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForGone(t, f.socketPath(s.Socket))

	if got := stderr(); strings.Contains(got, "stopped unexpectedly") {
		t.Errorf("a clean close warned: %q", got)
	}
	if got := s.End(); got != EndClean {
		t.Errorf("End() = %q, want %q", got, EndClean)
	}
}

// TestSessionRecoversAfterDrop is the feature: the exec channel collapses, and
// a new relay comes up on the same path, so the SSH_AUTH_SOCK the agent was
// started with leads somewhere again — with nothing printed, because nothing
// is left for the operator to do.
func TestSessionRecoversAfterDrop(t *testing.T) {
	f := newFakeContainer(t)
	stderr := captureStderr(t)
	s := startWith(t, f, buildRelay(t), echoServer(t))
	path := f.socketPath(s.Socket)

	dropRelay(t, s)

	waitFor(t, func() bool { return roundTrip(path, "after the drop") == nil },
		"the relay never came back on the same path")

	d := waitForDrops(t, s, 1)
	if d[0].Restored.IsZero() {
		t.Error("the drop was not marked restored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.End(); got != EndClean {
		t.Errorf("End() = %q, want %q for a session that recovered", got, EndClean)
	}
	if got := stderr(); strings.Contains(got, "stopped unexpectedly") {
		t.Errorf("a recovered drop warned: %q", got)
	}
}

// TestSessionWarnsWhenRelayCannotReturn: the engine stays unreachable for the
// whole budget. The warning is the only sign before a commit refuses to sign.
func TestSessionWarnsWhenRelayCannotReturn(t *testing.T) {
	f := newFakeContainer(t)
	stderr := captureStderr(t)

	var starts atomic.Int32
	start := func(ctx context.Context, cmd []string) (*Pipes, error) {
		if starts.Add(1) == 1 {
			return f.start(ctx, cmd)
		}
		return nil, errors.New("engine unreachable")
	}
	s := startWithOpts(t, f, buildRelay(t), echoServer(t), start, testRetry)

	dropRelay(t, s)

	waitFor(t, func() bool { return strings.Contains(stderr(), "stopped unexpectedly") },
		"the warning was never printed")
	_ = s.Close()
	if got := s.End(); got != EndUnexpected {
		t.Errorf("End() = %q, want %q", got, EndUnexpected)
	}
	d := s.Drops()
	if len(d) != 1 || !d[0].Restored.IsZero() {
		t.Errorf("drops = %+v, want one that was never restored", d)
	}
}

// TestSessionFlappingRelayGivesUp: a respawn that starts but dies at once — the
// container stopped under it — must spend the budget, not loop forever.
func TestSessionFlappingRelayGivesUp(t *testing.T) {
	f := newFakeContainer(t)
	stderr := captureStderr(t)

	var starts atomic.Int32
	start := func(ctx context.Context, cmd []string) (*Pipes, error) {
		if starts.Add(1) == 1 {
			return f.start(ctx, cmd)
		}
		return f.start(ctx, []string{"true"}) // starts, then exits at once
	}
	s := startWithOpts(t, f, buildRelay(t), echoServer(t), start, testRetry)

	dropRelay(t, s)

	waitFor(t, func() bool { return strings.Contains(stderr(), "stopped unexpectedly") },
		"a flapping relay was retried forever")
	_ = s.Close()
	d := s.Drops()
	if len(d) < 2 {
		t.Fatalf("drops = %d, want the flaps recorded", len(d))
	}
	if !d[len(d)-1].Restored.IsZero() {
		t.Error("the last drop should be the one never restored")
	}
}

// TestSessionCloseDuringBackoff: the operator's command ends while a respawn is
// waiting. Close must not sit out the backoff, or quitting the agent would hang.
func TestSessionCloseDuringBackoff(t *testing.T) {
	f := newFakeContainer(t)
	captureStderr(t)
	slow := retryPolicy{first: time.Hour, max: time.Hour, budget: 2 * time.Hour, stable: time.Minute}
	s := startWithOpts(t, f, buildRelay(t), echoServer(t), f.start, slow)

	dropRelay(t, s)
	waitForDrops(t, s, 1)

	began := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("Close took %v during backoff", took)
	}
	if got := s.End(); got != EndUnexpected {
		t.Errorf("End() = %q, want %q for a session closed while down", got, EndUnexpected)
	}
}

// TestSessionCancelledContextDoesNotWarn: Ctrl-C during an outage ends the
// command; telling the operator commits will not sign, as they leave, is noise.
func TestSessionCancelledContextDoesNotWarn(t *testing.T) {
	f := newFakeContainer(t)
	stderr := captureStderr(t)

	ctx, cancel := context.WithCancel(context.Background())
	suffix, _ := randomSuffix()
	socket := "/tmp/.dev-agent-" + suffix + ".sock"
	slow := retryPolicy{first: time.Hour, max: time.Hour, budget: 2 * time.Hour, stable: time.Minute}
	s, err := open(ctx, f.run, f.start, "/tmp/.dev-relay-test", buildRelay(t), socket, echoServer(t), slow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	waitForSocket(t, f.socketPath(socket))

	dropRelay(t, s)
	waitForDrops(t, s, 1)
	cancel()
	_ = s.Close()

	if got := stderr(); strings.Contains(got, "stopped unexpectedly") {
		t.Errorf("a cancelled command warned: %q", got)
	}
}
