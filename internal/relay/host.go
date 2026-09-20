package relay

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

// AgentSocket returns the host's SSH agent socket, or an error naming what is
// missing.
//
// Reported by name because the alternative is discovered much later and much
// less clearly: without an agent the container's socket exists but answers
// nothing, and ssh-add prints "error fetching identities: communication with
// agent failed", which says nothing about the host at all.
func AgentSocket() (string, error) {
	path := os.Getenv("SSH_AUTH_SOCK")
	if path == "" {
		return "", errors.New("no SSH agent on this host: SSH_AUTH_SOCK is not set")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("SSH_AUTH_SOCK points at %s, which cannot be used: %w", path, err)
	}
	return path, nil
}

// Host runs the host half: it reads framed connections from the container and
// joins each one to a fresh dial of the operator's agent.
//
// in and out are the container process's stdout and stdin — the exec channel,
// whichever provider established it. Returns when that channel ends.
func Host(agentSocket string, in io.Reader, out io.Writer) error {
	m := newMux(out)

	var wg sync.WaitGroup
	err := readFrames(in, m, func(id uint32) {
		// Registered here, in the reader, rather than inside the goroutine
		// below. The client writes its request immediately after connecting, so
		// the data frame can arrive before a goroutine has had a chance to run
		// — and a data frame for an unregistered channel is dropped, which
		// leaves the client waiting for a reply to a request the agent never
		// saw.
		p := m.add(id)

		wg.Add(1)
		go func() {
			defer wg.Done()
			hostConn(m, id, p, agentSocket)
		}()
	})

	m.shutdown()
	wg.Wait()
	if errors.Is(err, io.EOF) {
		// The container end closing is how a session ends.
		return nil
	}
	return err
}

// hostConn dials the agent for one channel and copies in both directions. The
// channel is already registered; p is the pipe its inbound data arrives on.
func hostConn(m *mux, id uint32, p *pipe, agentSocket string) {
	defer m.remove(id)

	conn, err := net.Dial("unix", agentSocket)
	if err != nil {
		// Tell the container so its client fails now. Without this the client
		// would wait on a connection nothing is ever going to answer.
		_ = m.send(frame{kind: kindClose, channel: id})
		return
	}
	defer conn.Close()
	defer func() { _ = m.send(frame{kind: kindClose, channel: id}) }()

	// Container to agent.
	go func() {
		_, _ = io.Copy(conn, p)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	// Agent to container.
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
