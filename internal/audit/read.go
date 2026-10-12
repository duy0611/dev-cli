package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

// Filter selects records. A zero field matches everything.
type Filter struct {
	Workspace string
	Container string
	Events    []string
	Since     time.Time
}

// Entry is one record: the raw line for --json, and the decoded fields.
type Entry struct {
	Raw    []byte
	Time   time.Time
	Event  string
	Fields map[string]any
}

// Workspace and Container read the common fields from the decoded line.
func (e Entry) Workspace() string { s, _ := e.Fields["workspace"].(string); return s }
func (e Entry) Container() string { s, _ := e.Fields["container"].(string); return s }

// Read returns the records in path that match f, oldest first. A missing file
// yields none. A line that does not parse is skipped and reported through
// skipped, so one damaged line does not hide the rest.
func Read(path string, f Filter, skipped func(line int, err error)) ([]Entry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the audit log: %w", err)
	}
	defer file.Close()
	return scan(file, f, skipped)
}

func scan(r io.Reader, f Filter, skipped func(line int, err error)) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	// Allows a little over maxLine so lines from another dev version still
	// read.
	sc.Buffer(make([]byte, 0, 64<<10), 4*maxLine)
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		e, err := parse(raw)
		if err != nil {
			if skipped != nil {
				skipped(n, err)
			}
			continue
		}
		if f.matches(e) {
			out = append(out, e)
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("reading the audit log: %w", err)
	}
	return out, nil
}

func parse(raw []byte) (Entry, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Entry{}, err
	}
	ts, _ := fields["time"].(string)
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return Entry{}, fmt.Errorf("no valid time: %w", err)
	}
	event, _ := fields["event"].(string)
	if event == "" {
		return Entry{}, errors.New("no event")
	}
	// Copied: the scanner reuses its buffer for the next line.
	return Entry{Raw: slices.Clone(raw), Time: t, Event: event, Fields: fields}, nil
}

func (f Filter) matches(e Entry) bool {
	if f.Workspace != "" && e.Workspace() != f.Workspace {
		return false
	}
	if f.Container != "" && e.Container() != f.Container {
		return false
	}
	if len(f.Events) > 0 && !slices.Contains(f.Events, e.Event) {
		return false
	}
	if !f.Since.IsZero() && e.Time.Before(f.Since) {
		return false
	}
	return true
}
