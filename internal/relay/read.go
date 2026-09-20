package relay

import (
	"io"
)

// readFrames dispatches inbound frames onto their channels until the stream
// ends. Both ends use it; they differ only in what a kindOpen means.
//
// onOpen is called for a new channel the far side announced. The container end
// passes nil: it is the one that opens channels, so an inbound open would be a
// protocol error it can safely ignore. The host end passes a function that
// dials the real agent.
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
			// A frame for a channel that has already closed is dropped rather
			// than treated as an error: close and data can cross on the wire,
			// and one late frame is not a reason to tear down every other
			// connection sharing this stream.
			if p, ok := m.lookup(f.channel); ok {
				p.write(f.payload)
			}

		case kindClose:
			m.remove(f.channel)
		}
	}
}
