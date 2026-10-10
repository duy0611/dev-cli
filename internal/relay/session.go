package relay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/duy0611/dev-cli/internal/relaybin"
)

// Pipes are a started in-container command's streams.
type Pipes struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	// Wait blocks until the command exits.
	Wait func() error
	// Kill ends the command without waiting for it to finish.
	Kill func() error
}

// StartFunc starts a long-running command in the container and returns its
// pipes. Supplied by the provider, because only it knows whether that means the
// devcontainer CLI or kubectl.
type StartFunc func(ctx context.Context, command []string) (*Pipes, error)

// RunFunc runs a short command to completion and returns its stdout. stdin may
// be nil.
type RunFunc func(ctx context.Context, command []string, stdin io.Reader) (string, error)

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

// install copies the relay into the container unless it is already there.
func install(ctx context.Context, run RunFunc, path string, bin []byte) error {
	// `test -x` rather than a version check: the path already names the
	// content, so a file that exists at it is this exact build.
	if _, err := run(ctx, []string{"test", "-x", path}, nil); err == nil {
		return nil
	}

	// Written to a temporary name and moved into place, so a session that dies
	// mid-copy cannot leave a half-written binary that the next one would find
	// with `test -x` and try to run.
	tmp := path + ".part"
	script := fmt.Sprintf("cat > %s && chmod 700 %s && mv %s %s", tmp, tmp, tmp, path)
	if _, err := run(ctx, []string{"sh", "-c", script}, strings.NewReader(string(bin))); err != nil {
		return fmt.Errorf("installing the relay into the container: %w", err)
	}
	return nil
}

// contentTag is a short stable name for a build of the relay.
func contentTag(bin []byte) string {
	sum := sha256.Sum256(bin)
	return hex.EncodeToString(sum[:6])
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
