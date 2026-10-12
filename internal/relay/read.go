package relay

import (
	"io"
)

// readFrames dispatches inbound frames to their channels until the stream ends.
// onOpen handles a kindOpen; the container end passes nil.
func readFrames(r io.Reader, m *mux, onOpen func(id uint32)) error {
	for {
		f, err := readFrame(r)
		if err != nil {
			return err
		}

		switch f.kind {
		case kindOpen:
			if onOpen != nil {
				onOpen(f.channel)
			}

		case kindData:
			// Data for a closed channel is dropped: close and data can cross on
			// the wire.
			if p, ok := m.lookup(f.channel); ok {
				p.write(f.payload)
			}

		case kindClose:
			m.remove(f.channel)
		}
	}
}
