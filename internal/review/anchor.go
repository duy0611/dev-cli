package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// snapshotEntry is one hunk as the last round saw it. The body is the
// identity: an edit above shifts the "@@" numbers without changing it.
type snapshotEntry struct {
	File string `json:"file"`
	Hunk string `json:"hunk"`
}

// Snapshot records the file and hunk pairs a diff held, for the next round's
// "changed since your last review" markers.
func Snapshot(d Diff) string {
	entries := []snapshotEntry{}
	for _, f := range d.Files {
		for _, h := range f.Hunks {
			entries = append(entries, snapshotEntry{File: f.Key(), Hunk: h.Body()})
		}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		// Strings cannot fail to marshal; an empty snapshot only means every
		// hunk is shown as changed, which is the safe direction.
		return ""
	}
	return string(b)
}

// inSnapshot reports whether the hunk is present in a snapshot. An unreadable
// snapshot is treated as holding everything, so a corrupt value does not mark
// the whole review as changed.
func inSnapshot(snapshot string, f File, h Hunk) bool {
	if snapshot == "" {
		return true
	}
	var entries []snapshotEntry
	if err := json.Unmarshal([]byte(snapshot), &entries); err != nil {
		return true
	}
	for _, e := range entries {
		if e.File == f.Key() && e.Hunk == h.Body() {
			return true
		}
	}
	return false
}

// Reanchor carries comments onto a freshly collected diff without dropping
// any: a comment whose hunk is gone moves to its file, and one whose file is
// gone moves to the top of the review, each with a note saying where it was.
func Reanchor(d Diff, comments []Comment) []Comment {
	files := make(map[string]File, len(d.Files))
	for _, f := range d.Files {
		files[f.Key()] = f
	}

	out := make([]Comment, 0, len(comments))
	for _, c := range comments {
		switch {
		case c.File == "":
			out = append(out, c)
		case c.Hunk == "":
			f, ok := files[c.File]
			if ok {
				c.Path = f.Path
				out = append(out, c)
				continue
			}
			path := commentPath(c)
			c.Anchor = Anchor{}
			c.Body = "(was on " + path + ") " + c.Body
			out = append(out, c)
		default:
			f, ok := files[c.File]
			if !ok {
				// Capture what the note quotes before replacing the embedded
				// Anchor, which is what holds these fields.
				path, line := commentPath(c), c.Line
				c.Anchor = Anchor{}
				c.Body = fmt.Sprintf("(was on %s:%d) %s", path, line, c.Body)
				out = append(out, c)
				continue
			}
			h, ok := hunkAt(f, c.Hunk, c.Occurrence)
			if !ok {
				line, text := c.Line, c.Text
				c.Anchor = Anchor{File: f.Key(), Path: f.Path}
				c.Body = fmt.Sprintf("(was on line %d: %s) %s", line, text, c.Body)
				out = append(out, c)
				continue
			}
			c.Path = f.Path
			if len(h.Lines) > 0 {
				index := min(max(c.Index, 0), len(h.Lines)-1)
				l := h.Lines[index]
				c.Index = index
				c.Kind = l.Kind
				c.Line = lineNumber(l)
				c.Text = l.Text
			}
			out = append(out, c)
		}
	}
	return out
}

// Stale returns the comments whose file or hunk is no longer in d. A general
// comment is never stale.
func Stale(d Diff, comments []Comment) []Comment {
	files := make(map[string]File, len(d.Files))
	for _, f := range d.Files {
		files[f.Key()] = f
	}

	var out []Comment
	for _, c := range comments {
		switch {
		case c.File == "":
		case c.Hunk == "":
			if _, ok := files[c.File]; !ok {
				out = append(out, c)
			}
		default:
			f, ok := files[c.File]
			if !ok {
				out = append(out, c)
				continue
			}
			if _, ok := hunkAt(f, c.Hunk, c.Occurrence); !ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// hunkAt finds the occurrence-th hunk in f whose body is body.
func hunkAt(f File, body string, occurrence int) (Hunk, bool) {
	n := 0
	for _, h := range f.Hunks {
		if h.Body() != body {
			continue
		}
		if n == occurrence {
			return h, true
		}
		n++
	}
	return Hunk{}, false
}

func commentPath(c Comment) string {
	if c.Path != "" {
		return c.Path
	}
	if _, path, ok := strings.Cut(c.File, "\x00"); ok {
		return path
	}
	return c.File
}
