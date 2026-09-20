package relay

import (
	"encoding/binary"
	"fmt"
	"io"
)

// The exec channel is a single pair of pipes, but a container can hold several
// agent connections at once — `git fetch --all` runs one ssh per remote, and
// ssh keeps its connection open for the length of the handshake. So the stream
// is framed and the connections are multiplexed over it, each with an id.
//
// Without this, a second connection's bytes would interleave with the first's
// and both would see a corrupt agent protocol.

// frameKind says what a frame carries.
type frameKind uint8

const (
	// kindOpen announces a new connection. No payload.
	kindOpen frameKind = 1
	// kindData carries bytes belonging to one connection.
	kindData frameKind = 2
	// kindClose says one connection is finished. No payload.
	kindClose frameKind = 3
)

// maxPayload bounds a single frame's payload.
//
// Agent messages are small — a signature request is a few hundred bytes — so
// this is far above what is needed. It exists so that a corrupt or hostile
// length field cannot make the reader allocate an arbitrary amount of memory.
const maxPayload = 1 << 20

// header is the fixed part of every frame: kind, channel id, payload length.
const headerLen = 1 + 4 + 4

type frame struct {
	kind    frameKind
	channel uint32
	payload []byte
}

// writeFrame writes one frame. Callers must serialise their own access: a
// partial write interleaved with another goroutine's would corrupt the stream.
func writeFrame(w io.Writer, f frame) error {
	if len(f.payload) > maxPayload {
		return fmt.Errorf("frame payload %d exceeds %d", len(f.payload), maxPayload)
	}
	var hdr [headerLen]byte
	hdr[0] = byte(f.kind)
	binary.BigEndian.PutUint32(hdr[1:5], f.channel)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))

	// Header and payload in one write where possible, so a reader on the other
	// end is never handed a header whose payload has not been sent yet.
	buf := make([]byte, 0, headerLen+len(f.payload))
	buf = append(buf, hdr[:]...)
	buf = append(buf, f.payload...)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one frame, returning io.EOF when the stream ends cleanly
// between frames.
func readFrame(r io.Reader) (frame, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		// A clean EOF on the header boundary is the stream ending, not an
		// error: the far side closed between frames, which is how a session
		// finishes.
		return frame{}, err
	}

	f := frame{
		kind:    frameKind(hdr[0]),
		channel: binary.BigEndian.Uint32(hdr[1:5]),
	}
	n := binary.BigEndian.Uint32(hdr[5:9])
	if n > maxPayload {
		return frame{}, fmt.Errorf("frame payload %d exceeds %d", n, maxPayload)
	}
	if n > 0 {
		f.payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.payload); err != nil {
			// Mid-frame EOF is a truncated stream, which is an error however
			// the header read was treated.
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return frame{}, err
		}
	}
	return f, nil
}
