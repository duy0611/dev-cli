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
	Wait   func() error
	Kill   func() error
}

// StartFunc starts a long-running command in the container and returns its
// pipes.
type StartFunc func(ctx context.Context, command []string) (*Pipes, error)

// RunFunc runs a short command to completion and returns its stdout. stdin may
// be nil.
type RunFunc func(ctx context.Context, command []string, stdin io.Reader) (string, error)

// closeGrace bounds how long a relay has to exit after its stdin closes before
// it is killed.
const closeGrace = 2 * time.Second

// retryPolicy is how hard a session tries to bring a dropped relay back.
type retryPolicy struct {
	first, max time.Duration // backoff between attempts, doubling from first up to max
	budget     time.Duration // how long one outage may last before the session gives up
	stable     time.Duration // how long a replacement must live before a later drop is a new outage
}

// defaultRetry is sized for a laptop waking from sleep, when the engine's exec
// streams break and may take a minute to settle. The monotonic clock stops
// during macOS sleep, so sleeping through a backoff spends no budget.
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

// Session is a running relay. Socket stays valid across respawns, so the
// agent's SSH_AUTH_SOCK never goes stale.
type Session struct {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK.
	Socket string

	// ctx is cancelled by Close, so an attempt stuck on a hung engine ends too.
	ctx         context.Context
	cancel      context.CancelFunc
	run         RunFunc
	start       StartFunc
	binPath     string
	bin         []byte
	agentSocket string
	retry       retryPolicy

	// mu guards pipes and drops. Whichever of Close and the pump takes the
	// pipes under mu stops them, so a process is never reaped twice.
	mu    sync.Mutex
	pipes *Pipes // nil while the pump is between relays
	drops []Drop

	pumpDone chan struct{}

	closeOne sync.Once
	closeErr error

	// closing is set under mu, so the pump cannot adopt a relay Close has
	// already moved past.
	closing atomic.Bool

	// unexpected is set while the relay is down, and killed when Close had to
	// kill a wedged relay.
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
// EndUnexpected takes precedence over EndKilled.
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

// Drops returns a copy of the drops so far, safe to call while the pump
// records.
func (s *Session) Drops() []Drop {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Drop(nil), s.drops...)
}

// Start installs the relay into the container and pumps agent traffic until the
// session is closed, respawning on the same socket if the exec channel
// collapses. The caller must Close the session.
func Start(ctx context.Context, run RunFunc, start StartFunc, agentSocket string) (*Session, error) {
	arch, err := run(ctx, []string{"uname", "-m"}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading the container's architecture: %w", err)
	}

	bin, err := relaybin.Binary(arch)
	if err != nil {
		return nil, err
	}

	// Named by content, so a container that already has this build skips the
	// copy.
	binPath := fmt.Sprintf("/tmp/.dev-relay-%s", contentTag(bin))

	// Per session, not per container: two shells on one container each run a
	// relay.
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	socket := fmt.Sprintf("/tmp/.dev-agent-%s.sock", suffix)

	return open(ctx, run, start, binPath, bin, socket, agentSocket, defaultRetry)
}

// open is Start with the binary, start function and retry policy supplied, for
// tests.
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
	p, err := s.launch(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	s.pipes = p
	s.pump()
	return s, nil
}

// launch installs the relay if it is missing and starts it on the session's
// socket.
//
// installCtx bounds only the install, where a wedged engine can hang. start
// keeps s.ctx, since the process must outlive any deadline.
func (s *Session) launch(installCtx context.Context) (*Pipes, error) {
	if err := install(installCtx, s.run, s.binPath, s.bin); err != nil {
		return nil, err
	}
	p, err := s.start(s.ctx, []string{s.binPath, s.Socket})
	if err != nil {
		return nil, fmt.Errorf("starting the relay: %w", err)
	}
	return p, nil
}

// pump runs the host half and puts a new relay up each time the exec channel
// collapses, until Close or the retry budget ends it.
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
			err := Host(s.agentSocket, p.Stdout, p.Stdin)

			s.mu.Lock()
			if s.closing.Load() {
				s.mu.Unlock()
				return // Close owns the pipes
			}
			if s.ctx.Err() != nil {
				// Cancelled by the operator, which is not a drop: recording one
				// would write a false "down since" into the audit log.
				s.mu.Unlock()
				return
			}
			now := time.Now()
			s.pipes = nil
			s.unexpected.Store(true)
			d := Drop{At: now}
			// A clean EOF is the usual shape of a collapsed channel, so a drop
			// is recorded whether or not err is set.
			if err != nil {
				d.Err = err.Error()
			}
			s.drops = append(s.drops, d)
			s.mu.Unlock()

			// Reaped before the next starts: an old devcontainer exec can
			// outlive its stream.
			stopPipes(p)

			// A relay that ran a while starts a new outage. One that died at
			// once continues the old one, so an immediate exit is not retried
			// forever.
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

// respawn retries until a replacement is adopted, the budget runs out, or the
// session ends. delay carries across calls so a flapping relay backs off.
func (s *Session) respawn(downSince time.Time, delay *time.Duration) bool {
	for {
		if time.Since(downSince) >= s.retry.budget {
			return false
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(*delay):
		}
		*delay = min(*delay*2, s.retry.max)

		attempt, cancel := context.WithDeadline(s.ctx, downSince.Add(s.retry.budget))
		p, err := s.launch(attempt)
		cancel()
		if err != nil {
			continue
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

// giveUp reports a relay that could not be restarted. The failure is otherwise
// silent: the agent's SSH_AUTH_SOCK is fixed at exec time, so the first sign is
// a commit refusing to sign, long after the cause. It writes to os.Stderr
// because no caller remains to receive an error.
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

// Close stops the relay. Safe to call more than once.
func (s *Session) Close() error {
	s.closeOne.Do(func() {
		// Set under mu before stopping: the pump must see it when Host returns,
		// or it would respawn the relay being shut down.
		s.mu.Lock()
		s.closing.Store(true)
		p := s.pipes
		s.mu.Unlock()

		// nil while the pump is between relays; it reaps its own.
		if p != nil {
			s.killed.Store(stopPipes(p))
		}
		// After the polite stop, so the relay can remove its own socket.
		s.cancel()
		<-s.pumpDone
	})
	return s.closeErr
}

// stopPipes ends one relay process, politely first, and reports whether it had
// to be killed.
func stopPipes(p *Pipes) (killed bool) {
	// Closing stdin is the polite stop. A killed relay cannot remove its
	// socket, so kill only after closeGrace.
	if p.Stdin != nil {
		_ = p.Stdin.Close()
	}

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		if p.Wait != nil {
			// A relay told to stop may exit non-zero; that is not a failure.
			_ = p.Wait()
		}
	}()

	select {
	case <-exited:
	case <-time.After(closeGrace):
		// Wedged, most likely on a half-open connection.
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
	// The path names the content, so existence means this exact build.
	if _, err := run(ctx, []string{"test", "-x", path}, nil); err == nil {
		return nil
	}

	// Moved into place from a temporary name, so a copy that dies midway leaves
	// nothing `test -x` would accept.
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
