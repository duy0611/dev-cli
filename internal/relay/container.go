package relay

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ownershipInterval is how often a relay checks that its socket path still
// leads to it. A var so tests can shorten it; ten seconds is quick enough
// that a stale relay does not linger, and a stat that often costs nothing.
var ownershipInterval = 10 * time.Second

// Serve runs the in-container half: it listens on socketPath and forwards every
// connection over the framed stream to the host, which holds the real agent.
//
// This is what the embedded binary runs. It returns when the stream ends, which
// is how the session is stopped — the host closes its side and every pending
// connection fails rather than hanging.
func Serve(socketPath string, in io.Reader, out io.Writer) error {
	// The socket must not be readable by other users on the host side of a
	// shared mount, and inside a container it is the only guard there is: any
	// process that can open it can ask the operator's agent to sign. 0700 on
	// the directory and 0600 on the socket is the same posture ssh-agent uses
	// for its own.
	if err := os.MkdirAll(dirOf(socketPath), 0o700); err != nil {
		return fmt.Errorf("creating the socket directory: %w", err)
	}
	// A leftover socket from a killed session would make Listen fail with
	// "address already in use"; nothing is listening on it, so removing it is
	// safe and is what lets a session start after a crash.
	_ = os.Remove(socketPath)

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

	m := newMux(out)

	// Accepting stops when the stream ends: closing the listener makes the
	// blocked Accept return, which is the only way to interrupt it.
	done := make(chan struct{})
	go func() {
		<-done
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	var nextID atomic.Uint32

	// Serve returns exactly when the host closes the stream, so the read loop
	// is what it waits on.
	readerDone := make(chan error, 1)
	go func() { readerDone <- readFrames(in, m, nil) }()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed, or the stream ended
			}
			id := nextID.Add(1)
			// Registered before the goroutine starts, for the same reason the
			// host end does it in its reader: the agent's reply can arrive
			// before the goroutine is scheduled, and a data frame for an
			// unregistered channel is dropped.
			p := m.add(id)

			wg.Add(1)
			go func() {
				defer wg.Done()
				serveConn(m, id, p, conn)
			}()
		}
	}()

	// The interval is read here, not in the goroutine, so a test restoring it
	// cannot race a watcher that has not been scheduled yet.
	superseded := make(chan struct{})
	go watchOwnership(socketPath, mine, ownershipInterval, done, superseded)

	select {
	case err = <-readerDone:
	case <-superseded:
		// The path leads to another relay now, or to nothing. No client can
		// reach this one again, and its stream may never end — a host whose
		// exec channel broke does not always close the far side — so standing
		// down is the only way it stops before the container does.
		err = nil
	}
	close(done)
	m.shutdown()
	wg.Wait()
	if errors.Is(err, io.EOF) {
		// The host closing the stream is how a session ends, not a failure.
		return nil
	}
	return err
}

// serveConn carries one accepted connection over the stream. The channel is
// already registered; p is the pipe its inbound data arrives on.
func serveConn(m *mux, id uint32, p *pipe, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	defer m.remove(id)

	if err := m.send(frame{kind: kindOpen, channel: id}); err != nil {
		return
	}
	// Tell the far side this connection is done even when it ends in an error,
	// or the host would leak a dial per failed connection.
	defer func() { _ = m.send(frame{kind: kindClose, channel: id}) }()

	// Host to client.
	go func() {
		_, _ = io.Copy(conn, p)
		// The agent has said all it will; closing the write half lets a client
		// that is waiting for EOF finish instead of blocking.
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	// Client to host.
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if sendErr := m.send(frame{
				kind:    kindData,
				channel: id,
				payload: buf[:n],
			}); sendErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// watchOwnership closes superseded once path no longer leads to mine.
func watchOwnership(path string, mine os.FileInfo, every time.Duration,
	done <-chan struct{}, superseded chan<- struct{}) {
	t := time.NewTicker(every)
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

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
