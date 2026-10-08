// Package audit appends a record of what dev did to a file on the host.
//
// The file lives beside the database, out of every container's reach, so an
// agent that wants to hide what it did cannot edit the record of it. It is a
// log rather than a table: it only grows, it is read rarely and whole, and it
// must outlive `workspace remove` and the cascade that comes with it — which a
// table hanging off containers would not.
//
// It is accountability, not enforcement. A record that cannot be written warns
// and the command carries on: a full disk must not stop an agent mid-task.
//
// Names, never values. A record carries setting names, argv the operator typed,
// and identifiers — never a resolved setting, an environment value or a token.
// That is invariant 3 extended to one more file, and a test holds it.
package audit

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"time"
)

// FileName is the log's name inside the state directory.
const FileName = "audit.jsonl"

// maxLine bounds one record. Linux guarantees a single O_APPEND write(2) to a
// regular file is not interleaved with another process's, and in practice that
// holds for writes far larger than this; the bound is there so that one record
// carrying a pasted megabyte of argv cannot make the file unreadable line by
// line. A record over it has its argv dropped and says so.
const maxLine = 16 << 10

// Record is one event. Fields that do not apply to an event are left zero and
// omitted from the line.
type Record struct {
	Time      time.Time `json:"time"`
	Event     string    `json:"event"`
	Workspace string    `json:"workspace,omitempty"`
	Container string    `json:"container,omitempty"`
	Dev       string    `json:"dev"`
	User      string    `json:"user,omitempty"`

	// Fields holds the event's own fields. A map rather than a struct per
	// event: the reader treats records as data, and a new event should not
	// need a new type in two places.
	Fields map[string]any `json:"-"`

	Truncated bool `json:"truncated,omitempty"`
}

// MarshalJSON flattens Fields into the record, so a line reads as one object
// rather than one object with the interesting part nested inside it.
func (r Record) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(r.Fields)+7)
	maps.Copy(out, r.Fields)
	// The common fields are written last, so an event field that happens to
	// share a name cannot overwrite them.
	out["time"] = r.Time.UTC().Format(time.RFC3339Nano)
	out["event"] = r.Event
	out["dev"] = r.Dev
	if r.Workspace != "" {
		out["workspace"] = r.Workspace
	}
	if r.Container != "" {
		out["container"] = r.Container
	}
	if r.User != "" {
		out["user"] = r.User
	}
	if r.Truncated {
		out["truncated"] = true
	}
	return json.Marshal(out)
}

// Log appends records to one file.
type Log struct {
	path    string
	version string
	user    string
	// warn reports a failed write. Once per Log: a command that writes several
	// records to a full disk says so once, not once per record.
	warn   func(error)
	warned bool
}

// Open returns a Log appending to FileName in dir. Nothing is opened yet: the
// file is created on the first record, so a command that records nothing
// leaves nothing behind.
func Open(dir, version string, warn func(error)) *Log {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return &Log{
		path:    filepath.Join(dir, FileName),
		version: version,
		user:    name,
		warn:    warn,
	}
}

// Path is where the log is written.
func (l *Log) Path() string { return l.path }

// Record appends one event. It never returns an error: see the package doc.
func (l *Log) Record(event, workspace, container string, fields map[string]any) {
	if l == nil {
		return
	}
	r := Record{
		Time:      time.Now(),
		Event:     event,
		Workspace: workspace,
		Container: container,
		Dev:       l.version,
		User:      l.user,
		Fields:    fields,
	}
	if err := l.append(r); err != nil {
		l.fail(err)
	}
}

func (l *Log) append(r Record) error {
	line, err := encode(r)
	if err != nil {
		return err
	}

	// 0700 to match what store.Open makes, for the same reason: the log says a
	// good deal about what the operator works on. Created here as well as
	// there because the log may be written before the database is opened.
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// One write, so concurrent dev processes never interleave within a line.
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// encode renders a record as one line, dropping argv if the line is too long.
func encode(r Record) ([]byte, error) {
	line, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encoding the audit record: %w", err)
	}
	if len(line) >= maxLine {
		trimmed := make(map[string]any, len(r.Fields))
		for k, v := range r.Fields {
			if k != "argv" {
				trimmed[k] = v
			}
		}
		r.Fields = trimmed
		r.Truncated = true
		if line, err = json.Marshal(r); err != nil {
			return nil, fmt.Errorf("encoding the audit record: %w", err)
		}
	}
	return append(line, '\n'), nil
}

func (l *Log) fail(err error) {
	if l.warned || l.warn == nil {
		return
	}
	l.warned = true
	l.warn(fmt.Errorf("could not write the audit log %s: %w", l.path, err))
}
