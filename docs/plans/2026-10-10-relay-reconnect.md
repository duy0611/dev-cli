# Relay Reconnect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a session's ssh agent relay loses its exec channel mid-session, `dev` brings a new relay up on the *same* socket path, so the agent's `SSH_AUTH_SOCK` stays valid and signing comes back without restarting anything.

**Architecture:** `relay.Session` gains a respawn loop in its pump goroutine: when `Host` returns and nobody called `Close`, it reaps the old process, reinstalls the binary if it is missing, and starts a new one with the same socket argument, retrying with backoff inside a time budget. The in-container relay learns to own its socket path by inode, so a stale relay that outlives its stream can neither delete the replacement's socket nor linger forever. Every drop is recorded on the session and written to the `relay` audit row.

**Tech Stack:** Go 1.26 standard library (`net`, `os`, `os/exec`, `sync`, `context`, `time`). No new dependencies.

**Spec:** No separate spec. This extends `docs/specs/2026-09-19-ssh-agent-relay.md`, and the design is the next section. The motivating incident: on 2026-10-09/10 two long sessions on a sleeping MacBook (`demo-ssh/dev-cli-local`) ended `relay unexpected after 18h6m9s` and `after 22h41m32s`, while the claude process on the second one stayed up. Its own `devcontainer exec` stream survived, but the relay's idle stream did not.

## Design

### Why the same path works

The agent received `SSH_AUTH_SOCK=/tmp/.dev-agent-<suffix>.sock` at exec time and cannot be told a new value. Nothing needs to tell it: `Serve` already removes a leftover file at its path before listening (`internal/relay/container.go:31`), so a new `dev-relay /tmp/.dev-agent-<suffix>.sock` started over a fresh exec takes the path over. The next `ssh`/`git` connection reaches the new relay. Connections in flight at the moment of the drop fail, as they do today.

### The respawn loop (host side, `internal/relay/session.go`)

```
Host(old pipes) returns
  └─ Close called?  ── yes ─▶ return quietly (Close owns the pipes)
        │ no
        ▼
  record Drop{At, Err}; mark unexpected (relay is down)
  reap old process (stdin close → 2s grace → kill)
  same outage or new? (previous relay lived ≥ stable → new outage: reset budget and backoff)
        ▼
  loop: budget spent? → give up (warn, stay unexpected)
        sleep backoff (wakes on Close/ctx cancel)
        install (test -x, copy if missing) + start(binPath, same socket)
        ok → adopt: swap pipes, clear unexpected, Drop.Restored = now → back to Host
```

Policy, `defaultRetry`:

| field    | value | why |
|----------|-------|-----|
| `first`  | 1s    | a broken exec after a wake usually reconnects on the first try |
| `max`    | 15s   | backoff cap: don't hammer an engine that is restarting |
| `budget` | 5m    | Docker Desktop restarting its VM after a wake can take a minute or more |
| `stable` | 1m    | a relay that dies sooner counts as the same outage, so a start that succeeds but exits at once (container stopped) spends the budget instead of looping forever |

The timers use Go's monotonic clock, which on macOS does not advance while the machine sleeps. Sleeping during backoff therefore spends no budget.

**Decided, not left open:**
- **Warnings print only on give-up.** A drop that recovers is recorded in the audit log and prints nothing: the relay sits under a full-screen agent's display, and a line that is already obsolete would only corrupt it.
- **`unexpected` now means "the relay was down when the session ended"** (gave up, or closed mid-outage). A session that dropped and recovered ends `clean` and carries its drops.
- **`Restored` is when the replacement process started**, not when its socket appeared. The gap is milliseconds, and measuring it would need a new frame kind.

### Socket ownership (container side, `internal/relay/container.go`)

Respawning creates a hazard that doesn't exist today. When the host side of the stream breaks, the old in-container relay may keep running for a while: its stdin need not see EOF. Two things then go wrong:

1. **When the old relay finally exits, it deletes the replacement's socket.** It removes its path on exit (`defer os.Remove(socketPath)`), and Go's `UnixListener.Close` also unlinks the path by default. By then the path belongs to the new relay. Signing breaks again, silently.
2. **The old relay never exits.** Its path now leads elsewhere, so nothing will connect to it, and it waits on stdin for the life of the container.

Fix: the relay records the `os.FileInfo` of the socket it created. It turns off `SetUnlinkOnClose` and only removes the path if it still refers to that same file (`os.SameFile`). It also checks every `ownershipInterval` (10s), and if the path is gone or belongs to another relay, it stands down and exits 0. That also self-heals a deleted socket: the relay exits, the host sees EOF, and the respawn recreates the socket.

### Audit

The `relay` row gains `drops: [{at, error?, restored?}]`, present only when there was at least one drop. `dev audit` shows it:

```
relay  clean after 22h41m32s, recovered from 1 drop
relay  unexpected after 18h6m9s, down since 2026-10-09 23:14
```

That answers "when did it die", which the incident couldn't.

## Global Constraints

- `cmd/dev-relay` and `internal/relay` stay standard-library only. The relay binary must not import `internal/provider`, `internal/model` or anything else in the module (`cmd/dev-relay/main.go:9-11`, `Makefile:49-50`).
- `provider` must not import `relay`. Adapters convert types (`internal/provider/local/agent.go`, `internal/provider/k8s/agent.go`).
- Every non-obvious line gets a comment explaining *why*, at the density of `internal/relay/session.go`.
- Commit subjects are Conventional Commits, scope usually the package. No `Co-Authored-By: Claude` or other tooling-attribution line.
- `make lint && make test` passes after every task. `make test` never touches a container engine.
- `docs/USAGE.md` must match the behaviour by the last task.
- Relay tests use `os.Stderr` capture and must not call `t.Parallel()` (`internal/relay/helpers_test.go:24-26`).

## Review Focus

1. **A stale relay outliving its stream deletes the replacement's socket** when it finally exits. Expected: the live socket survives. Pinned by `TestServeLeavesAReplacementInPlace` (Task 1).
2. **A relay that starts and dies at once** (container stopped, `devcontainer exec` failing after launch). Expected: give up within the budget, not loop forever. Pinned by `TestSessionFlappingRelayGivesUp` (Task 2).
3. **The operator's command ends while a respawn is sleeping in backoff.** Expected: `Close` returns at once, with no 15s or 5m hang. Pinned by `TestSessionCloseDuringBackoff` (Task 2).
4. **Ctrl-C during a respawn.** Expected: no "could not be restarted" warning printed over the operator's exit. Pinned by `TestSessionCancelledContextDoesNotWarn` (Task 2).
5. **Drops on a session that never dropped.** Expected: the audit row is byte-for-byte what it is today, with no empty `drops` key. Pinned by `TestRelayDropsFields` (Task 3).

---

### Task 1: The relay owns its socket by inode

**Files:**
- Modify: `internal/relay/container.go` (in `Serve`, lines 19-90; add helpers after `serveConn`)
- Modify: `internal/relay/relay_test.go` (refactor `connect` onto a new `serveAt`, add two tests)
- Modify: `internal/relay/helpers_test.go` (add `roundTrip`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `var ownershipInterval time.Duration` (package var, test-shortenable). Test helpers `serveAt(t, socket, agentSocket string) (served <-chan struct{}, stop func())` and `roundTrip(path, msg string) error`, which Task 2 also uses.

- [ ] **Step 1: Add the test helpers**

In `internal/relay/helpers_test.go`, add `"fmt"`, `"io"` to imports and append:

```go
// roundTrip sends msg through the socket at path and checks it comes back
// intact. An error rather than a failure, so a caller can poll it while a
// relay is coming up.
func roundTrip(path, msg string) error {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return fmt.Errorf("round trip = %q, want %q", buf, msg)
	}
	return nil
}
```

In `internal/relay/relay_test.go`, replace the body of `connect` from `toHost, hostIn := io.Pipe()` through its `t.Cleanup` with a call to `serveAt`, and add `serveAt` below it:

```go
func connect(t *testing.T, agentSocket string) string {
	t.Helper()

	// Short directory: a unix socket path is capped near 100 bytes by the
	// kernel, and a t.TempDir() under a long test name can exceed it. This is
	// the same reason the real thing puts its socket under /tmp.
	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	serveAt(t, socket, agentSocket)
	waitForSocket(t, socket)
	return socket
}

// serveAt joins a container end listening on socket to a host end, with
// in-memory pipes standing in for the exec channel. served closes when Serve
// returns; stop ends the session the way the host really does, by closing the
// container's input. Safe to call twice, and called at cleanup regardless.
func serveAt(t *testing.T, socket, agentSocket string) (served <-chan struct{}, stop func()) {
	t.Helper()
	toHost, hostIn := io.Pipe()       // container stdout -> host
	hostOut, toContainer := io.Pipe() // host stdout -> container

	done := make(chan struct{})
	var host sync.WaitGroup
	host.Add(1)
	go func() { defer close(done); _ = Serve(socket, hostOut, hostIn) }()
	go func() { defer host.Done(); _ = Host(agentSocket, toHost, toContainer) }()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = toContainer.Close()
			_ = hostIn.Close()
			<-done
			host.Wait()
		})
	}
	t.Cleanup(stop)
	return done, stop
}
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/relay/relay_test.go`:

```go
// TestServeLeavesAReplacementInPlace is the hazard respawning creates: the old
// relay can outlive its stream, and when it finally exits it must not take the
// replacement's socket with it — Go's listener unlinks its path on close by
// default, whoever that path belongs to by then.
func TestServeLeavesAReplacementInPlace(t *testing.T) {
	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	agent := echoServer(t)

	_, stopOld := serveAt(t, socket, agent)
	waitForSocket(t, socket)
	first, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}

	serveAt(t, socket, agent)
	waitFor(t, func() bool {
		fi, err := os.Stat(socket)
		return err == nil && !os.SameFile(fi, first)
	}, "the replacement never took the path")

	stopOld()

	if err := roundTrip(socket, "still reachable"); err != nil {
		t.Fatalf("the old relay's exit broke the replacement: %v", err)
	}
}

// TestServeStandsDownWhenReplaced covers the other half: a relay whose path
// now leads elsewhere can never be reached again, and without this it would
// wait on a dead stream for the life of the container.
func TestServeStandsDownWhenReplaced(t *testing.T) {
	old := ownershipInterval
	ownershipInterval = 10 * time.Millisecond
	t.Cleanup(func() { ownershipInterval = old })

	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	agent := echoServer(t)

	servedOld, _ := serveAt(t, socket, agent)
	waitForSocket(t, socket)
	serveAt(t, socket, agent)

	select {
	case <-servedOld:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced relay kept running")
	}
	if err := roundTrip(socket, "the replacement answers"); err != nil {
		t.Fatal(err)
	}
}

// TestServeStandsDownWhenSocketDeleted: a relay whose socket was removed is
// unreachable, and exiting is what lets the host notice and put a new one up.
func TestServeStandsDownWhenSocketDeleted(t *testing.T) {
	old := ownershipInterval
	ownershipInterval = 10 * time.Millisecond
	t.Cleanup(func() { ownershipInterval = old })

	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	served, _ := serveAt(t, socket, echoServer(t))
	waitForSocket(t, socket)
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("a relay with no socket kept running")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/relay/ -run 'TestServe' -v`
Expected: compile failure, `undefined: ownershipInterval`. After temporarily declaring the var, `TestServeLeavesAReplacementInPlace` fails with a dial error (`no such file or directory` or `connection refused`), and the two stand-down tests fail with "kept running".

- [ ] **Step 4: Implement**

In `internal/relay/container.go`, add `"time"` to imports. Above `Serve`:

```go
// ownershipInterval is how often a relay checks that its socket path still
// leads to it. A var so tests can shorten it; ten seconds is quick enough
// that a stale relay does not linger, and a stat that often costs nothing.
var ownershipInterval = 10 * time.Second
```

In `Serve`, replace the block from `ln, err := net.Listen("unix", socketPath)` through `return fmt.Errorf("securing %s: %w", socketPath, err)` with:

```go
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	// Go unlinks a listener's path when it closes, whoever the path belongs to
	// by then. After the host respawns a relay it belongs to the replacement,
	// and this one closing late would pull the live socket out from under the
	// session. removeIfMine does the removal instead, and only of our own.
	ln.SetUnlinkOnClose(false)
	defer func() { _ = ln.Close() }()

	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("securing %s: %w", socketPath, err)
	}

	// The socket as created, so it can be told apart from a replacement at the
	// same path later. The path alone cannot: both relays use it.
	mine, err := os.Stat(socketPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", socketPath, err)
	}
	defer removeIfMine(socketPath, mine)
```

Replace the tail from `err = <-readerDone` to `close(done)` with:

```go
	superseded := make(chan struct{})
	go watchOwnership(socketPath, mine, done, superseded)

	select {
	case err = <-readerDone:
	case <-superseded:
		// The path leads to another relay now, or to nothing. No client can
		// reach this one again, and its stream may never end — a host whose exec
		// channel broke does not always close the far side — so standing down is
		// the only way it stops before the container does.
		err = nil
	}
	close(done)
```

`done` is already closed after the select. Check that `watchOwnership` returns on `done`. Append after `serveConn`:

```go
// watchOwnership closes superseded once socketPath no longer leads to mine.
func watchOwnership(path string, mine os.FileInfo, done <-chan struct{}, superseded chan<- struct{}) {
	t := time.NewTicker(ownershipInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if !isMine(path, mine) {
				close(superseded)
				return
			}
		}
	}
}

// isMine reports whether path still names the socket this relay created.
func isMine(path string, mine os.FileInfo) bool {
	fi, err := os.Stat(path)
	return err == nil && os.SameFile(fi, mine)
}

// removeIfMine removes path only if it is still this relay's socket.
func removeIfMine(path string, mine os.FileInfo) {
	if isMine(path, mine) {
		_ = os.Remove(path)
	}
}
```

Delete the now-replaced `defer func() { _ = os.Remove(socketPath) }()` line.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/relay/ -v`
Expected: PASS, including the existing `TestRelay*` and `TestSession*` tests, which run the rebuilt `cmd/dev-relay`.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/relay/container.go internal/relay/relay_test.go internal/relay/helpers_test.go
git commit -m "fix(relay): remove only the socket the relay created, and stand down once replaced"
```

---

### Task 2: The session respawns a dropped relay

**Files:**
- Modify: `internal/relay/session.go` (Session struct, `Start`, `pump`, `warnIfUnexpected`→`giveUp`, `Close`; add `open`, `launch`, `respawn`, `adopt`, `stopPipes`, `Drops`, `Drop`, `retryPolicy`)
- Modify: `internal/relay/session_test.go` (`startWith` onto `open`; replace `TestSessionWarnsOnUnexpectedExit`; add five tests)

**Interfaces:**
- Consumes: `roundTrip` from Task 1.
- Produces, used by Task 3:
  ```go
  type Drop struct {
      At       time.Time
      Err      string    // empty when the channel simply closed
      Restored time.Time // zero when no replacement came up
  }
  func (s *Session) Drops() []Drop // copy, oldest first; safe any time
  ```
  `End()` keeps its signature and constants; its meaning narrows to "down at the end" for `EndUnexpected`.

- [ ] **Step 1: Refactor the test helper onto the constructor that is about to exist**

In `internal/relay/session_test.go`, add `"errors"`, `"sync/atomic"`, `"time"` to imports and replace `startWith` with:

```go
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
```

Add `"fmt"` to imports as well.

- [ ] **Step 2: Write the failing tests**

Delete `TestSessionWarnsOnUnexpectedExit` and append:

```go
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
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/relay/ -run TestSession -v`
Expected: compile failure, `undefined: retryPolicy`, `undefined: open`, `s.Drops undefined`.

- [ ] **Step 4: Implement**

Replace `internal/relay/session.go` from the `closeGrace` const through the end of `Close` with the following, leaving `install`, `contentTag` and `randomSuffix` as they are.

```go
// closeGrace is how long a relay is given to shut itself down before it is
// killed. Long enough for a process to notice its stdin closed and unlink a
// socket; short enough that a wedged one does not hold up the operator's shell
// noticeably.
const closeGrace = 2 * time.Second

// retryPolicy is how hard a session tries to bring a dropped relay back.
type retryPolicy struct {
	first, max time.Duration // backoff between attempts, doubling from first up to max
	budget     time.Duration // how long one outage may last before the session gives up
	stable     time.Duration // how long a replacement must live before a later drop is a new outage
}

// defaultRetry is sized for the failure that motivated it: a laptop waking
// from sleep, where the engine's exec streams break and Docker Desktop may take
// a minute or more to settle. The clock is monotonic, which on macOS stops
// while the machine sleeps, so sleeping through a backoff spends no budget.
var defaultRetry = retryPolicy{
	first:  time.Second,
	max:    15 * time.Second,
	budget: 5 * time.Minute,
	stable: time.Minute,
}

// Drop is one time the relay's exec channel collapsed under a session.
type Drop struct {
	At       time.Time
	Err      string    // empty when the channel simply closed, which is the usual shape
	Restored time.Time // when the replacement started; zero if none did
}

// Session is a running relay. The agent is reachable inside the container at
// Socket for as long as it is open, across any number of respawns: the path
// never changes, because the agent was handed it once and cannot be told again.
type Session struct {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK.
	Socket string

	// What a respawn needs to put the same relay back. ctx is the session's
	// own, cancelled by Close, so an attempt stuck on a hung engine ends too.
	ctx         context.Context
	cancel      context.CancelFunc
	run         RunFunc
	start       StartFunc
	binPath     string
	bin         []byte
	agentSocket string
	retry       retryPolicy

	// mu guards pipes and drops, and orders closing against the pump's handover
	// of the pipes: whichever of Close and the pump takes them under mu is the
	// one that stops them, so a process is never reaped twice.
	mu    sync.Mutex
	pipes *Pipes // nil while the pump is between relays
	drops []Drop

	pumpDone chan struct{} // closed when the pump goroutine returns

	closeOne sync.Once
	closeErr error

	// closing records that Close was called. Set under mu, so the pump cannot
	// read it as false and then adopt a relay Close has already moved past.
	closing atomic.Bool

	// unexpected records that the relay is down: set at a drop, cleared when a
	// replacement is adopted. Read by End once Close has returned, so it says
	// whether the session ended with signing broken. killed records that Close
	// had to kill a wedged relay.
	unexpected atomic.Bool
	killed     atomic.Bool
}

// How a session ended, as End reports it.
const (
	EndClean      = "clean"      // told to stop, and stopped
	EndKilled     = "killed"     // told to stop, and had to be killed
	EndUnexpected = "unexpected" // the relay was down when the session ended
)

// End reports how the session ended. Meaningful once Close has returned.
//
// Unexpected wins over killed: a session that ended with its relay down is one
// whose commits stopped signing, and that is the fact worth having on record.
// A session that dropped and recovered ends clean; Drops says what happened.
func (s *Session) End() string {
	switch {
	case s.unexpected.Load():
		return EndUnexpected
	case s.killed.Load():
		return EndKilled
	default:
		return EndClean
	}
}

// Drops returns every drop so far, oldest first. A copy, so it is safe to call
// while the pump is still recording.
func (s *Session) Drops() []Drop {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Drop(nil), s.drops...)
}

// Start installs the relay into the container and runs it, then pumps agent
// traffic between it and the host's agent until the session is closed —
// respawning it on the same socket if its exec channel collapses.
//
// The caller must Close the session. Until then the container can ask the
// operator's agent to sign, which is the whole point and also the reason the
// window should be no longer than the command that opened it.
func Start(ctx context.Context, run RunFunc, start StartFunc, agentSocket string) (*Session, error) {
	arch, err := run(ctx, []string{"uname", "-m"}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading the container's architecture: %w", err)
	}

	bin, err := relaybin.Binary(arch)
	if err != nil {
		return nil, err
	}

	// The binary's path is derived from its content, so a container that
	// already has this exact build skips the copy. Without it every `git fetch`
	// would push megabytes through the exec channel before doing any work.
	binPath := fmt.Sprintf("/tmp/.dev-relay-%s", contentTag(bin))

	// The socket is per session, not per container: two shells attached to one
	// container each run their own relay, and a shared path would mean whichever
	// exited first pulled the socket out from under the other.
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	socket := fmt.Sprintf("/tmp/.dev-agent-%s.sock", suffix)

	return open(ctx, run, start, binPath, bin, socket, agentSocket, defaultRetry)
}

// open starts the first relay and the pump that keeps it running. Split from
// Start so tests can supply the binary, the start function and the policy.
func open(ctx context.Context, run RunFunc, start StartFunc, binPath string, bin []byte,
	socket, agentSocket string, retry retryPolicy) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		Socket:      socket,
		ctx:         ctx,
		cancel:      cancel,
		run:         run,
		start:       start,
		binPath:     binPath,
		bin:         bin,
		agentSocket: agentSocket,
		retry:       retry,
		pumpDone:    make(chan struct{}),
	}
	p, err := s.launch()
	if err != nil {
		cancel()
		return nil, err
	}
	s.pipes = p
	s.pump()
	return s, nil
}

// launch installs the relay if it is missing and starts it on the session's
// socket. Install runs on every respawn, not just the first: a container that
// restarted has an empty /tmp, and `test -x` makes the common case one exec.
func (s *Session) launch() (*Pipes, error) {
	if err := install(s.ctx, s.run, s.binPath, s.bin); err != nil {
		return nil, err
	}
	p, err := s.start(s.ctx, []string{s.binPath, s.Socket})
	if err != nil {
		return nil, fmt.Errorf("starting the relay: %w", err)
	}
	return p, nil
}

// pump runs the host half in the background, and puts a new relay up each
// time the exec channel collapses, until Close or the retry budget ends it.
//
// A method rather than an inline goroutine so that the respawn, the warning
// and the thing that triggers them cannot drift apart: every path that starts a
// session goes through here, the tests included.
func (s *Session) pump() {
	go func() {
		defer close(s.pumpDone)

		var downSince time.Time // start of the current outage
		delay := s.retry.first
		for {
			s.mu.Lock()
			p := s.pipes
			s.mu.Unlock()

			up := time.Now()
			// Runs until the container half's stdout closes, which happens when
			// the relay exits or the session is closed.
			err := Host(s.agentSocket, p.Stdout, p.Stdin)

			s.mu.Lock()
			if s.closing.Load() {
				s.mu.Unlock()
				return // an ordinary shutdown; Close owns the pipes
			}
			now := time.Now()
			s.pipes = nil
			s.unexpected.Store(true)
			d := Drop{At: now}
			// Host returns nil on a clean EOF, which is exactly the shape a
			// collapsed channel takes, so a drop is not conditional on err.
			if err != nil {
				d.Err = err.Error()
			}
			s.drops = append(s.drops, d)
			s.mu.Unlock()

			// Reaped before the next one starts: the old devcontainer exec may
			// outlive its stream, and left alone it holds a process per drop.
			stopPipes(p)

			// A relay that held for a while was a real recovery, so this is a
			// new outage with a fresh budget. One that died at once is the same
			// outage continuing — otherwise a start that succeeds and exits
			// immediately would be retried forever.
			if downSince.IsZero() || now.Sub(up) >= s.retry.stable {
				downSince, delay = now, s.retry.first
			}
			if !s.respawn(downSince, &delay) {
				s.giveUp(err)
				return
			}
		}
	}()
}

// respawn retries until a replacement is adopted, the outage outlasts the
// budget, or the session ends. delay carries across calls within one outage,
// so a flapping relay backs off instead of restarting every second.
func (s *Session) respawn(downSince time.Time, delay *time.Duration) bool {
	for {
		if time.Since(downSince) >= s.retry.budget {
			return false
		}
		select {
		case <-s.ctx.Done():
			return false // Close, or the operator's command was cancelled
		case <-time.After(*delay):
		}
		*delay = min(*delay*2, s.retry.max)

		p, err := s.launch()
		if err != nil {
			continue // engine still unreachable; the budget decides when to stop
		}
		return s.adopt(p)
	}
}

// adopt makes p the session's relay, unless Close got there first.
func (s *Session) adopt(p *Pipes) bool {
	s.mu.Lock()
	if s.closing.Load() {
		s.mu.Unlock()
		stopPipes(p)
		return false
	}
	s.pipes = p
	s.unexpected.Store(false)
	s.drops[len(s.drops)-1].Restored = time.Now()
	s.mu.Unlock()
	return true
}

// giveUp reports a relay that could not be brought back.
//
// The failure this exists for is silent by construction: the relay and the
// agent are siblings, so the relay dying disturbs nothing the operator can see.
// SSH_AUTH_SOCK was baked into the agent's environment at exec time and
// commit.gpgsign with it, so the first sign of trouble is a commit refusing to
// sign, often an hour later and nowhere near the cause.
//
// Straight to os.Stderr because there is no caller left to hand an error to —
// this runs in a goroutine the command has already moved past. Silent when the
// session is ending anyway: Close, or the operator interrupting the command.
func (s *Session) giveUp(err error) {
	if s.closing.Load() || s.ctx.Err() != nil {
		return
	}
	detail := ""
	if err != nil {
		detail = ": " + err.Error()
	}
	fmt.Fprintf(os.Stderr,
		"dev: the ssh agent relay stopped unexpectedly%s and could not be restarted; commits will not sign\n",
		detail)
}

// Close stops the relay. Safe to call more than once, so a caller can defer it
// and still close explicitly on the happy path.
func (s *Session) Close() error {
	s.closeOne.Do(func() {
		// Under mu and before anything is stopped: closing stdin is what ends
		// the relay, so the pump can return from Host and check this flag while
		// Close is still in its grace period — and it must see it set, or it
		// would record a drop and respawn the relay being shut down.
		s.mu.Lock()
		s.closing.Store(true)
		p := s.pipes
		s.mu.Unlock()

		// nil when the pump is between relays; it reaps what it holds itself.
		if p != nil {
			s.killed.Store(stopPipes(p))
		}
		// After the polite stop, so the relay gets its chance to remove its own
		// socket; cancelling first would kill it through CommandContext. Wakes a
		// backoff and ends an attempt stuck on a hung engine.
		s.cancel()
		<-s.pumpDone
	})
	return s.closeErr
}

// stopPipes ends one relay process, politely first, and reports whether it had
// to be killed.
func stopPipes(p *Pipes) (killed bool) {
	// Closing stdin is the polite stop: the relay's read loop ends, it removes
	// its own socket, and it exits. Give that a moment to happen before
	// resorting to a kill — a killed relay cannot run its own cleanup, and the
	// socket would be left behind in a container that outlives the session.
	if p.Stdin != nil {
		_ = p.Stdin.Close()
	}

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		if p.Wait != nil {
			// Discarded on purpose: a relay told to stop may exit non-zero, and
			// reporting that would turn every clean shutdown into a warning.
			_ = p.Wait()
		}
	}()

	select {
	case <-exited:
	case <-time.After(closeGrace):
		// Wedged, most likely on a half-open connection. Kill it, or the
		// command the operator ran would never return.
		killed = true
		if p.Kill != nil {
			_ = p.Kill()
		}
		<-exited
	}

	if p.Stdout != nil {
		_ = p.Stdout.Close()
	}
	return killed
}
```

Delete the old `pump(agentSocket string)` and `warnIfUnexpected`. In `TestSessionClosePrintsNoWarning` and every other test, `startWith` now goes through `open`, so they need no other edit.

- [ ] **Step 5: Run the tests, with the race detector**

Run: `go test -race -count=3 ./internal/relay/ -v`
Expected: PASS on all three runs. The respawn path is concurrent by design, and a flaky race here becomes a hung `dev` command in the field.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/relay/session.go internal/relay/session_test.go
git commit -m "feat(relay): restart a dropped relay on the same socket"
```

---

### Task 3: Drops reach the audit log

**Files:**
- Modify: `internal/provider/provider.go:125-133` (add `AgentDrop`, `Drops()` on `AgentSession`)
- Modify: `internal/provider/local/agent.go` (adapter, after line ~115)
- Modify: `internal/provider/k8s/agent.go` (adapter, after line ~97)
- Modify: `internal/cli/sshagent.go:111-124` (record drops; add `relayDrops`)
- Modify: `internal/cli/audit.go:140-145` (summary; add `dropSummary`)
- Test: `internal/cli/sshagent_test.go`, `internal/cli/audit_test.go`

**Interfaces:**
- Consumes: `relay.Drop`, `(*relay.Session).Drops() []relay.Drop` from Task 2.
- Produces:
  ```go
  // package provider
  type AgentDrop struct { At time.Time; Err string; Restored time.Time }
  // AgentSession gains: Drops() []AgentDrop
  // package cli
  func relayDrops(drops []provider.AgentDrop) []map[string]any
  func dropSummary(v any) string
  ```
  Audit field `drops`: list of `{"at": RFC3339Nano UTC, "error"?: string, "restored"?: RFC3339Nano UTC}`, omitted entirely when empty.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/sshagent_test.go`, adding `"time"` and `"reflect"` to imports if absent:

```go
// TestRelayDropsFields covers the record a drop leaves. No drops must mean no
// key at all, so a session that never dropped writes the record it always has.
func TestRelayDropsFields(t *testing.T) {
	if got := relayDrops(nil); got != nil {
		t.Errorf("no drops = %v, want nil", got)
	}

	at := time.Date(2026, 10, 9, 20, 14, 0, 0, time.UTC)
	back := at.Add(3 * time.Second)
	got := relayDrops([]provider.AgentDrop{
		{At: at, Restored: back},
		{At: at.Add(time.Hour), Err: "read |0: file already closed"},
	})
	want := []map[string]any{
		{"at": "2026-10-09T20:14:00Z", "restored": "2026-10-09T20:14:03Z"},
		{"at": "2026-10-09T21:14:00Z", "error": "read |0: file already closed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relayDrops = %v, want %v", got, want)
	}
}
```

Append to `internal/cli/audit_test.go`:

```go
// TestSummariseRelay covers the one line `dev audit` prints for a relay: how
// it ended, and — the question the 2026-10-10 incident could not answer — when
// it went down. Fields arrive as JSON decodes them, so drops is []any of maps.
func TestSummariseRelay(t *testing.T) {
	base := map[string]any{
		"started": "2026-10-09T08:31:37Z",
		"ended":   "2026-10-10T07:13:09Z",
	}
	with := func(end string, drops ...map[string]any) audit.Entry {
		f := map[string]any{"end": end}
		for k, v := range base {
			f[k] = v
		}
		if len(drops) > 0 {
			list := make([]any, len(drops))
			for i, d := range drops {
				list[i] = d
			}
			f["drops"] = list
		}
		return audit.Entry{Event: "relay", Fields: f}
	}
	downAt := time.Date(2026, 10, 9, 20, 14, 0, 0, time.UTC)

	cases := []struct {
		name string
		e    audit.Entry
		want string
	}{
		{"no drops", with("clean"), "clean after 22h41m32s"},
		{"recovered once", with("clean",
			map[string]any{"at": "2026-10-09T20:14:00Z", "restored": "2026-10-09T20:14:03Z"}),
			"clean after 22h41m32s, recovered from 1 drop"},
		{"recovered twice", with("clean",
			map[string]any{"at": "2026-10-09T20:14:00Z", "restored": "2026-10-09T20:14:03Z"},
			map[string]any{"at": "2026-10-09T22:00:00Z", "restored": "2026-10-09T22:00:01Z"}),
			"clean after 22h41m32s, recovered from 2 drops"},
		{"never came back", with("unexpected",
			map[string]any{"at": "2026-10-09T20:14:00Z"}),
			"unexpected after 22h41m32s, down since " + downAt.Local().Format("2006-01-02 15:04")},
	}
	for _, c := range cases {
		if got := summarise(c.e); got != c.want {
			t.Errorf("%s: summarise = %q, want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRelayDropsFields|TestSummariseRelay' -v`
Expected: compile failure, `undefined: relayDrops`, `undefined: provider.AgentDrop`.

- [ ] **Step 3: Implement the interface and adapters**

In `internal/provider/provider.go`, make sure `"time"` is imported, then replace the `AgentSession` interface:

```go
// AgentSession is a live agent relay.
type AgentSession interface {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK. Fixed
	// for the session's life, across any respawn of the relay behind it.
	Socket() string
	Close() error
	// End reports how the session ended — clean, killed or unexpected — once
	// Close has returned. For the audit log.
	End() string
	// Drops reports each time the relay's exec channel collapsed, oldest
	// first. For the audit log.
	Drops() []AgentDrop
}

// AgentDrop is one collapse of a relay's exec channel. A mirror of relay.Drop,
// because this package cannot name the relay package without every provider
// importing it.
type AgentDrop struct {
	At       time.Time
	Err      string    // empty when the channel simply closed
	Restored time.Time // zero when no replacement came up before the session ended
}
```

In **both** `internal/provider/local/agent.go` and `internal/provider/k8s/agent.go`, append after `func (a *agentSession) End() string`:

```go
func (a *agentSession) Drops() []provider.AgentDrop {
	var out []provider.AgentDrop
	for _, d := range a.s.Drops() {
		out = append(out, provider.AgentDrop{At: d.At, Err: d.Err, Restored: d.Restored})
	}
	return out
}
```

- [ ] **Step 4: Implement the record and the summary**

In `internal/cli/sshagent.go`, replace the `a.record("relay", ...)` call inside `stop` with:

```go
		fields := map[string]any{
			"started": started.UTC().Format(time.RFC3339Nano),
			"ended":   time.Now().UTC().Format(time.RFC3339Nano),
			"end":     session.End(),
		}
		if drops := relayDrops(session.Drops()); drops != nil {
			fields["drops"] = drops
		}
		a.record("relay", t.workspace.Name, t.container.Name, fields)
```

and append to the file:

```go
// relayDrops is a session's drops as audit fields, oldest first. Nil when there
// were none, so a relay that never dropped writes the record it always has.
// The error text is the relay's own I/O error and carries no setting value.
func relayDrops(drops []provider.AgentDrop) []map[string]any {
	if len(drops) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(drops))
	for _, d := range drops {
		m := map[string]any{"at": d.At.UTC().Format(time.RFC3339Nano)}
		if d.Err != "" {
			m["error"] = d.Err
		}
		if !d.Restored.IsZero() {
			m["restored"] = d.Restored.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, m)
	}
	return out
}
```

In `internal/cli/audit.go`, change the `"relay"` case to:

```go
	case "relay":
		s := str("end")
		if d := elapsed(str("started"), str("ended")); d != "" {
			s += " after " + d
		}
		return s + dropSummary(e.Fields["drops"])
```

and append:

```go
// dropSummary is the tail of a relay line: nothing for a relay that never
// dropped, how often it came back, or — the fact the session's duration hides —
// when it went down for good.
func dropSummary(v any) string {
	drops, _ := v.([]any)
	if len(drops) == 0 {
		return ""
	}
	last, _ := drops[len(drops)-1].(map[string]any)
	if restored, _ := last["restored"].(string); restored == "" {
		at, _ := last["at"].(string)
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			return ", down since " + t.Local().Format("2006-01-02 15:04")
		}
		return ", down at the end"
	}
	if len(drops) == 1 {
		return ", recovered from 1 drop"
	}
	return fmt.Sprintf(", recovered from %d drops", len(drops))
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/cli/ ./internal/provider/... -v -run 'Relay|Summarise|ForwardAgent'`, then `make test`
Expected: PASS. If anything else implements `provider.AgentSession`, it now fails to compile. Run `grep -rn "End() string" internal/` to find it, and add `Drops` there too.

- [ ] **Step 6: Lint and commit**

```bash
make lint && make test
git add internal/provider/provider.go internal/provider/local/agent.go internal/provider/k8s/agent.go \
        internal/cli/sshagent.go internal/cli/audit.go internal/cli/sshagent_test.go internal/cli/audit_test.go
git commit -m "feat(cli): record relay drops in the audit log"
```

---

### Task 4: USAGE matches

**Files:**
- Modify: `docs/USAGE.md:886-893` (troubleshooting entry), `docs/USAGE.md:779-785` (audit example)

**Interfaces:** none.

- [ ] **Step 1: Rewrite the troubleshooting entry**

Replace the whole `**\`the ssh agent relay stopped unexpectedly; commits will not sign\`**` paragraph with:

```markdown
**`the ssh agent relay stopped unexpectedly and could not be restarted; commits
will not sign`** — the relay's exec channel collapsed while the command was still
running, and no replacement came up within five minutes. `dev` restarts a
dropped relay by itself, on the same socket path, so the `SSH_AUTH_SOCK` your
agent was started with keeps working and nothing needs restarting. A git command
that runs in the gap fails, and the next one works. The usual cause is a laptop
sleeping through a long session or Docker Desktop restarting, and both normally
recover on the first retry. This message means the engine stayed unreachable:
check that the container is still running, then restart the `dev` command.
`dev audit --event relay` shows every drop, as `recovered from 1 drop` or
`down since 2026-10-09 23:14`.
```

- [ ] **Step 2: Extend the audit example**

In the example block under "See what ran, and when", add one line after the existing `relay  clean after 2h12m47s` line, so readers see what a recovered session looks like:

```
2026-10-09 10:13:09  personal/api  relay        clean after 22h41m32s, recovered from 1 drop
```

- [ ] **Step 3: Check nothing else contradicts it**

Run: `grep -n "not recoverable\|different path\|stopped unexpectedly" docs/USAGE.md`
Expected: only the rewritten entry. Fix any other sentence that still says a dropped relay needs a restart.

- [ ] **Step 4: Commit**

```bash
git add docs/USAGE.md
git commit -m "docs: describe relay restarts and the drops audit field"
```

---

## Verification after all tasks

- `make lint && make test`: green.
- `go test -race -count=5 ./internal/relay/`: green, with no "kept running" or hang.
- Manual, on the Mac (no smoke test covers it, since sleep can't be simulated in CI): start `dev container shell` with `--ssh-agent` in tmux, close the lid for 30+ minutes, wake, and run `ssh-add -l` inside. It should list keys within ~15s of waking. `dev audit --event relay` after exiting should show `recovered from N drops`.
- `ps` inside the container after a recovery (`dev container exec NAME -- ps aux | grep dev-relay`): at most one relay per live session within ~10s of the recovery. More means the stale one did not stand down.
