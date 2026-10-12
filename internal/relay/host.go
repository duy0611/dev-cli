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
// missing. Failing here beats the container's socket answering nothing.
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

// Host runs the host half: it joins each framed connection from the container
// to a fresh dial of the agent. It returns when the exec channel ends.
func Host(agentSocket string, in io.Reader, out io.Writer) error {
	m := newMux(out)

	var wg sync.WaitGroup
	err := readFrames(in, m, func(id uint32) {
		// Registered before the goroutine starts: a data frame for an
		// unregistered channel is dropped.
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
		// Close the channel now, or the client waits on a connection nothing
		// answers.
		_ = m.send(frame{kind: kindClose, channel: id})
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = m.send(frame{kind: kindClose, channel: id}) }()

	go func() {
		_, _ = io.Copy(conn, p)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

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
