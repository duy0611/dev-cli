// Package review turns a guarded container's uncommitted changes into a file
// the operator comments on, and those comments into one message for the agent.
package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LineKind says what a hunk line does to the file.
type LineKind int

const (
	// Context is a line present in both versions.
	Context LineKind = iota
	// Added is a line the working tree adds.
	Added
	// Removed is a line the working tree deletes.
	Removed
)

// Line is one content line of a hunk. Text has its leading " ", "+" or "-"
// removed.
type Line struct {
	Kind LineKind
	Text string
	Old  int // the old file's number; 0 on Added lines
	New  int // the new file's number; 0 on Removed lines
}

// Hunk is one "@@" block. Raw holds the body exactly as git printed it,
// including "\ No newline at end of file" markers.
type Hunk struct {
	Header string
	Lines  []Line
	Raw    []string
}

// Body returns the hunk's content without its "@@" header, so two hunks are
// the same hunk when only their line numbers moved.
func (h Hunk) Body() string { return strings.Join(h.Raw, "\n") }

// File is one changed file. Folder is the label it came from, empty when the
// container has one folder.
type File struct {
	Folder string
	Path   string   // the new path, or the old path when deleted
	Header []string // "diff --git" through "+++" (or the Binary files line), verbatim
	Hunks  []Hunk
	Note   string // set instead of content for an oversized untracked file
}

// Key identifies a file within a container's folder set.
func (f File) Key() string { return f.Folder + "\x00" + f.Path }

// Diff is a container folder set's uncommitted changes. Skipped names folders
// that are not git repositories.
type Diff struct {
	Files   []File
	Skipped []string
}

// hunkHeaderRE matches the numbers a hunk body is counted from; the trailing
// function text is not needed.
var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParseDiff parses git's unified diff text and labels every file with folder.
func ParseDiff(folder, text string) ([]File, error) {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	var files []File
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "diff --git ") {
			return nil, fmt.Errorf("line %d: expected a diff --git header", i+1)
		}

		f := File{Folder: folder}
		start := i
		i++
		for i < len(lines) &&
			!strings.HasPrefix(lines[i], "@@") &&
			!strings.HasPrefix(lines[i], "diff --git ") {
			i++
		}
		f.Header = lines[start:i]

		for i < len(lines) && strings.HasPrefix(lines[i], "@@") {
			h, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			f.Hunks = append(f.Hunks, h)
			i = next
		}

		path, err := parsePath(f.Header)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", start+1, err)
		}
		f.Path = path
		files = append(files, f)
	}
	return files, nil
}

// parseHunk reads one hunk starting at its "@@" line and returns the index of
// the first line after it.
func parseHunk(lines []string, i int) (Hunk, int, error) {
	m := hunkHeaderRE.FindStringSubmatch(lines[i])
	if m == nil {
		return Hunk{}, 0, fmt.Errorf("line %d: malformed hunk header %q", i+1, lines[i])
	}
	oldLine, _ := strconv.Atoi(m[1])
	newLine, _ := strconv.Atoi(m[2])

	h := Hunk{Header: lines[i]}
	i++
	for i < len(lines) {
		ln := lines[i]
		if strings.HasPrefix(ln, "@@") || strings.HasPrefix(ln, "diff --git ") {
			break
		}
		if ln == "" {
			return Hunk{}, 0, fmt.Errorf("line %d: empty line inside a hunk", i+1)
		}

		h.Raw = append(h.Raw, ln)
		switch ln[0] {
		case '\\':
			// "\ No newline at end of file" is a marker, not a line.
		case ' ':
			h.Lines = append(h.Lines, Line{Kind: Context, Text: ln[1:], Old: oldLine, New: newLine})
			oldLine++
			newLine++
		case '-':
			h.Lines = append(h.Lines, Line{Kind: Removed, Text: ln[1:], Old: oldLine})
			oldLine++
		case '+':
			h.Lines = append(h.Lines, Line{Kind: Added, Text: ln[1:], New: newLine})
			newLine++
		default:
			return Hunk{}, 0, fmt.Errorf("line %d: unexpected line in a hunk: %q", i+1, ln)
		}
		i++
	}
	return h, i, nil
}

// parsePath finds the file's path in its header block. The ---/+++ lines are
// authoritative, since a path may contain spaces; a binary file has no such
// lines, and an oversized untracked file is represented by the diff --git line
// alone.
func parsePath(header []string) (string, error) {
	var oldPath, newPath, binary string
	for _, ln := range header {
		switch {
		case strings.HasPrefix(ln, "--- "):
			oldPath = headerPath(ln, "--- ")
		case strings.HasPrefix(ln, "+++ "):
			newPath = headerPath(ln, "+++ ")
		case strings.HasPrefix(ln, "Binary files "):
			binary = binaryPath(ln)
		}
	}

	if binary != "" {
		return binary, nil
	}
	if newPath != "" && newPath != "/dev/null" {
		return newPath, nil
	}
	if oldPath != "" && oldPath != "/dev/null" {
		return oldPath, nil
	}
	if p := gitHeaderPath(header[0]); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("cannot find the changed file's path")
}

// headerPath strips a "--- " or "+++ " prefix, unquotes git's C-style quoting
// and drops the a/ or b/ side prefix.
func headerPath(line, prefix string) string {
	raw := strings.TrimPrefix(line, prefix)
	quoted := strings.HasPrefix(raw, `"`)
	raw = unquoteDiffPath(raw)
	// git terminates an unquoted path that contains a space with a tab; a
	// quoted one carries its own escaping and no terminator.
	if !quoted {
		raw = strings.TrimSuffix(raw, "\t")
	}
	if strings.HasPrefix(raw, "a/") || strings.HasPrefix(raw, "b/") {
		return raw[2:]
	}
	return raw
}

// binaryPath reads the changed side of "Binary files a/x and b/x differ".
func binaryPath(line string) string {
	rest := strings.TrimSuffix(strings.TrimPrefix(line, "Binary files "), " differ")
	idx := strings.LastIndex(rest, " and ")
	if idx < 0 {
		return ""
	}
	left, right := rest[:idx], rest[idx+len(" and "):]
	if right == "/dev/null" {
		return headerPath(left, "")
	}
	return headerPath(right, "")
}

// gitHeaderPath finds the new side in "diff --git a/P b/P". It is a fallback
// for headers with no ---/+++ lines or Binary line.
func gitHeaderPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if idx := strings.LastIndex(rest, " b/"); idx >= 0 {
		return headerPath(rest[idx+1:], "")
	}
	return ""
}

// unquoteDiffPath decodes git's quoted path form. core.quotepath=false leaves
// non-ASCII unquoted, but tabs, quotes and backslashes still arrive quoted.
func unquoteDiffPath(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
