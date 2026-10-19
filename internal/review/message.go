package review

import (
	"fmt"
	"sort"
	"strings"
)

// MessageLimit is the largest message sent as one argv element. Linux caps a
// single argument at 128 KiB; this leaves room for the rest of the command.
const MessageLimit = 100 << 10

// Message writes the review for the agent: what to do with it, the general
// comments, then every other comment numbered in diff order with its hunk.
func Message(container string, d Diff, comments []Comment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "These are my review comments on your uncommitted changes in container %s.\n", container)
	b.WriteString("For each comment, answer the question or change the code. Do not commit: I\n")
	b.WriteString("review and commit from the host. When you are done, list every comment by\n")
	b.WriteString("number with what you did (answered, changed, or disagreed) and why.\n")
	b.WriteString("\n")

	var general, numbered []Comment
	for _, c := range comments {
		if c.File == "" {
			general = append(general, c)
			continue
		}
		numbered = append(numbered, c)
	}

	if len(general) > 0 {
		b.WriteString("General:\n")
		for _, c := range general {
			b.WriteString("- " + c.Body + "\n")
		}
		b.WriteString("\n")
	}

	sort.SliceStable(numbered, func(i, j int) bool {
		return lessPosition(messagePosition(d, numbered[i]), messagePosition(d, numbered[j]))
	})

	for i, c := range numbered {
		fmt.Fprintf(&b, "## %d. %s\n", i+1, messageHeading(c))
		b.WriteString("```diff\n")
		for _, line := range messageContext(d, c) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString("```\n")
		b.WriteString(c.Body)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// msgPosition orders comments by file, then hunk, then line; a whole-file
// comment sits before its file's hunks.
type msgPosition struct {
	file, hunk, line int
}

func lessPosition(a, b msgPosition) bool {
	if a.file != b.file {
		return a.file < b.file
	}
	if a.hunk != b.hunk {
		return a.hunk < b.hunk
	}
	return a.line < b.line
}

func messagePosition(d Diff, c Comment) msgPosition {
	last := msgPosition{file: len(d.Files), hunk: len(d.Files), line: 0}
	for i, f := range d.Files {
		if f.Key() != c.File {
			continue
		}
		if c.Hunk == "" {
			return msgPosition{file: i, hunk: -1, line: -1}
		}
		occurrence := 0
		for j, h := range f.Hunks {
			if h.Body() != c.Hunk {
				continue
			}
			if occurrence == c.Occurrence {
				return msgPosition{file: i, hunk: j, line: c.Index}
			}
			occurrence++
		}
		return msgPosition{file: i, hunk: len(f.Hunks), line: c.Index}
	}
	return last
}

func messageHeading(c Comment) string {
	path := anchorDisplayPath(c.Anchor)
	switch {
	case c.Hunk == "":
		return path + " (whole file)"
	case c.Kind == Removed:
		return fmt.Sprintf("%s:%d (removed line, was %d)", path, c.Line, c.Line)
	case c.Kind == Added:
		return fmt.Sprintf("%s:%d (added line)", path, c.Line)
	default:
		return fmt.Sprintf("%s:%d", path, c.Line)
	}
}

// messageContext is the fenced part of one comment: the file header for a
// whole-file comment, otherwise the hunk with the comment's ">>" lines under
// its line.
func messageContext(d Diff, c Comment) []string {
	var f File
	found := false
	for _, candidate := range d.Files {
		if candidate.Key() == c.File {
			f, found = candidate, true
			break
		}
	}
	if !found {
		return nil
	}
	if c.Hunk == "" {
		return append([]string(nil), f.Header...)
	}

	h, ok := hunkAt(f, c.Hunk, c.Occurrence)
	if !ok {
		return nil
	}
	out := []string{h.Header}
	lineIndex := 0
	inserted := false
	for _, raw := range h.Raw {
		out = append(out, raw)
		if strings.HasPrefix(raw, `\`) || lineIndex >= len(h.Lines) {
			continue
		}
		if lineIndex == c.Index {
			out = append(out, commentLines(c.Body)...)
			inserted = true
		}
		lineIndex++
	}
	if !inserted && len(h.Lines) == 0 {
		out = append(out, commentLines(c.Body)...)
	}
	return out
}

func commentLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		out = append(out, commentLine(line))
	}
	return out
}
