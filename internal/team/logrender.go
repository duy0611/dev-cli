package team

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// NewParser returns a fresh parser for a worker tool's output stream.
func NewParser(tool string) (Parser, error) {
	_, parser, err := Worker(tool, WorkerSpec{})
	if err != nil {
		return nil, err
	}
	return parser, nil
}

// RenderLog turns a worker's JSONL log into readable lines, one per event. It
// reads with a bufio.Reader rather than a Scanner: worker lines can run to
// hundreds of kilobytes, and a Scanner's token limit would reject them.
func RenderLog(tool string, r io.Reader, w io.Writer) error {
	parser, err := NewParser(tool)
	if err != nil {
		return err
	}
	if parser == nil {
		return fmt.Errorf("no parser for worker tool %q", tool)
	}

	reader := bufio.NewReader(r)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			for _, event := range parser.Feed([]byte(line)) {
				rendered := Render(event)
				if rendered == "" {
					continue
				}
				if _, err := io.WriteString(w, rendered+"\n"); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
