package relay

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

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

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer func() { _ = ln.Close() }()
	defer func() { _ = os.Remove(socketPath) }()

	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("securing %s: %w", socketPath, err)
	}

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

	err = <-readerDone
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
