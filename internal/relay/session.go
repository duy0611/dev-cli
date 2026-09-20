package relay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
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

// Session is a running relay. The agent is reachable inside the container at
// Socket for as long as it is open.
type Session struct {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK.
	Socket string

	pipes    *Pipes
	closeOne sync.Once
	closeErr error
}

// Start installs the relay into the container and runs it, then pumps agent
// traffic between it and the host's agent until the session is closed.
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
	if err := install(ctx, run, binPath, bin); err != nil {
		return nil, err
	}

	// The socket is per session, not per container: two shells attached to one
	// container each run their own relay, and a shared path would mean whichever
	// exited first pulled the socket out from under the other.
	suffix, err := randomSuffix()
	if err != nil {
		return nil, err
	}
	socket := fmt.Sprintf("/tmp/.dev-agent-%s.sock", suffix)

	pipes, err := start(ctx, []string{binPath, socket})
	if err != nil {
		return nil, fmt.Errorf("starting the relay: %w", err)
	}

	// The host half runs until the container half's stdout closes, which
	// happens when the relay exits or the session is closed.
	go func() {
		_ = Host(agentSocket, pipes.Stdout, pipes.Stdin)
	}()

	return &Session{Socket: socket, pipes: pipes}, nil
}

// Close stops the relay. Safe to call more than once, so a caller can defer it
// and still close explicitly on the happy path.
func (s *Session) Close() error {
	s.closeOne.Do(func() {
		// Closing stdin is the polite stop: the relay's read loop ends, it
		// removes its own socket, and it exits. Give that a moment to happen
		// before resorting to a kill — a killed relay cannot run its own
		// cleanup, and the socket would be left behind in a container that
		// outlives the session.
		if s.pipes.Stdin != nil {
			_ = s.pipes.Stdin.Close()
		}

		exited := make(chan struct{})
		go func() {
			defer close(exited)
			if s.pipes.Wait != nil {
				// Discarded on purpose: a relay told to stop may exit non-zero,
				// and reporting that would turn every clean shutdown into a
				// warning.
				_ = s.pipes.Wait()
			}
		}()

		select {
		case <-exited:
		case <-time.After(closeGrace):
			// Wedged, most likely on a half-open connection. Kill it, or the
			// command the operator ran would never return.
			if s.pipes.Kill != nil {
				_ = s.pipes.Kill()
			}
			<-exited
		}

		if s.pipes.Stdout != nil {
			_ = s.pipes.Stdout.Close()
		}
	})
	return s.closeErr
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
