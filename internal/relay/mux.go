package relay

import (
	"io"
	"sync"
)

// mux carries several connections over one framed stream. Both ends use it.
type mux struct {
	// writeMu serialises frame writes to the shared stream.
	writeMu sync.Mutex
	w       io.Writer

	mu       sync.Mutex
	channels map[uint32]*pipe
	closed   bool
}

func newMux(w io.Writer) *mux {
	return &mux{w: w, channels: map[uint32]*pipe{}}
}

// send writes one frame, serialised against every other sender.
func (m *mux) send(f frame) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return writeFrame(m.w, f)
}

// add registers a channel and returns the pipe its inbound data arrives on.
func (m *mux) add(id uint32) *pipe {
	p := newPipe()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		// The stream is gone, so return a closed pipe rather than block
		// forever.
		p.close()
		return p
	}
	m.channels[id] = p
	return p
}

// remove drops a channel and closes its pipe, unblocking whoever reads it.
func (m *mux) remove(id uint32) {
	m.mu.Lock()
	p := m.channels[id]
	delete(m.channels, id)
	m.mu.Unlock()
	if p != nil {
		p.close()
	}
}

// lookup finds a channel's pipe.
func (m *mux) lookup(id uint32) (*pipe, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.channels[id]
	return p, ok
}

// shutdown closes every channel, so connections waiting on the stream fail.
func (m *mux) shutdown() {
	m.mu.Lock()
	m.closed = true
	pipes := make([]*pipe, 0, len(m.channels))
	for id, p := range m.channels {
		pipes = append(pipes, p)
		delete(m.channels, id)
	}
	m.mu.Unlock()
	for _, p := range pipes {
		p.close()
	}
}

// pipe is a buffered byte stream. It is not io.Pipe: an unbuffered write would
// stall the shared frame reader behind one slow connection.
type pipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newPipe() *pipe {
	p := &pipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *pipe) write(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
}

func (p *pipe) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
}

// Read blocks until there is data or the pipe is closed, returning io.EOF once
// closed and drained.
func (p *pipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}
