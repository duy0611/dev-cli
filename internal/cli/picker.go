package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/duy0611/dev-cli/internal/dcgen"
)

// errPickCancelled is returned when the operator abandons the picker. The
// caller writes nothing.
var errPickCancelled = errors.New("cancelled")

// pickItem is one row in the picker.
type pickItem struct {
	ID      string
	Summary string
	// Note is "official" or "community", so pulling a third-party image is a
	// visible choice.
	Note string
}

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyToggle
	keyAccept
	keyCancel
)

// decodeKey reads one key from buf, returning the bytes it consumed.
func decodeKey(buf []byte) (key, int) {
	if len(buf) == 0 {
		return keyNone, 0
	}
	switch buf[0] {
	case 0x1b:
		if len(buf) < 3 {
			return keyNone, 0 // incomplete escape sequence
		}
		if buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return keyUp, 3
			case 'B':
				return keyDown, 3
			}
		}
		return keyNone, 3 // some other escape sequence; discard it whole
	case ' ':
		return keyToggle, 1
	case '\r', '\n':
		return keyAccept, 1
	case 0x03, 'q': // Ctrl-C
		return keyCancel, 1
	}
	return keyNone, 1
}

// multiSelect runs the picker over the given streams, so a test can drive it
// over a pipe. Raw mode is the caller's job; see pickTools.
func multiSelect(items []pickItem, in io.Reader, out io.Writer) ([]string, error) {
	chosen := make([]bool, len(items))
	cursor := 0

	fmt.Fprintf(out, "tools (↑↓ move, space toggle, enter accept, ^C cancel)\r\n\r\n")
	draw(out, items, chosen, cursor, false)

	var buf []byte
	readBuf := make([]byte, 16)
	for {
		n, err := in.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		if n == 0 && err != nil {
			// Input ended without an accept: the terminal went away, not an
			// empty selection.
			return nil, errPickCancelled
		}

		for {
			k, consumed := decodeKey(buf)
			if consumed == 0 {
				break // wait for more bytes
			}
			buf = buf[consumed:]

			switch k {
			case keyUp:
				if cursor > 0 {
					cursor--
				}
			case keyDown:
				if cursor < len(items)-1 {
					cursor++
				}
			case keyToggle:
				chosen[cursor] = !chosen[cursor]
			case keyCancel:
				draw(out, items, chosen, cursor, true)
				return nil, errPickCancelled
			case keyAccept:
				draw(out, items, chosen, cursor, true)
				var picked []string
				for i, ok := range chosen {
					if ok {
						picked = append(picked, items[i].ID)
					}
				}
				return picked, nil
			case keyNone:
				// A byte that means nothing here.
			}
			draw(out, items, chosen, cursor, false)
		}
	}
}

// draw rewrites the list in place, moving the cursor up rather than clearing
// the screen.
func draw(out io.Writer, items []pickItem, chosen []bool, cursor int, final bool) {
	var b strings.Builder
	for i, it := range items {
		marker := " "
		if i == cursor && !final {
			marker = ">"
		}
		box := " "
		if chosen[i] {
			box = "x"
		}
		// \r\n, since in raw mode a bare \n does not return the carriage.
		fmt.Fprintf(&b, "%s [%s] %-12s %s (%s)\r\n", marker, box, it.ID, it.Summary, it.Note)
	}
	if !final {
		// Park the cursor at the top of the list for the next redraw.
		fmt.Fprintf(&b, "\x1b[%dA", len(items))
	}
	// A failed terminal write is not worth failing over: the next redraw shows
	// the same state.
	_, _ = io.WriteString(out, b.String())
}

// pickTools runs the picker on the real terminal. The prompt goes to stderr,
// out of captured output.
func pickTools(items []pickItem) ([]string, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("putting the terminal into raw mode: %w", err)
	}
	// Restored even on a panic: a raw-mode terminal leaves the shell unusable.
	// A restore error is dropped rather than losing the operator's selection.
	defer func() { _ = term.Restore(fd, state) }()

	return multiSelect(items, os.Stdin, os.Stderr)
}

// catalogItems adapts the tool catalog to picker rows.
func catalogItems(tools []dcgen.Tool) []pickItem {
	items := make([]pickItem, 0, len(tools))
	for _, t := range tools {
		note := "community"
		if t.Official {
			note = "official"
		}
		items = append(items, pickItem{ID: t.ID, Summary: t.Summary, Note: note})
	}
	return items
}
