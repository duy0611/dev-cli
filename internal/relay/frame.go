package relay

import (
	"encoding/binary"
	"fmt"
	"io"
)

// The exec channel is one pair of pipes, but a container can hold several agent
// connections at once, so the stream is framed and multiplexed by channel id.

// frameKind says what a frame carries.
type frameKind uint8

const (
	kindOpen  frameKind = 1
	kindData  frameKind = 2
	kindClose frameKind = 3
)

// maxPayload bounds a single frame's payload, so a corrupt length field cannot
// make the reader allocate arbitrarily.
const maxPayload = 1 << 20

// headerLen is kind, channel id and payload length.
const headerLen = 1 + 4 + 4

type frame struct {
	kind    frameKind
	channel uint32
	payload []byte
}

// writeFrame writes one frame. Callers must serialise access to w.
func writeFrame(w io.Writer, f frame) error {
	if len(f.payload) > maxPayload {
		return fmt.Errorf("frame payload %d exceeds %d", len(f.payload), maxPayload)
	}
	var hdr [headerLen]byte
	hdr[0] = byte(f.kind)
	binary.BigEndian.PutUint32(hdr[1:5], f.channel)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))

	// One write, so the reader never sees a header without its payload.
	buf := make([]byte, 0, headerLen+len(f.payload))
	buf = append(buf, hdr[:]...)
	buf = append(buf, f.payload...)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one frame, returning io.EOF when the stream ends between
// frames.
func readFrame(r io.Reader) (frame, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
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
			// EOF mid-frame is a truncated stream, not a clean end.
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return frame{}, err
		}
	}
	return f, nil
}
