package review

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Anchor says where a comment sits. File is empty for a comment on the whole
// change; Hunk is empty for a comment on the whole file.
type Anchor struct {
	File       string // File.Key()
	Path       string // File.Path, for display
	Hunk       string // Hunk.Body()
	Occurrence int    // which hunk with this body in the file, from 0
	Index      int    // index into Hunk.Lines of the line it sits under
	Kind       LineKind
	Line       int // New for Added/Context, Old for Removed
	Text       string
}

// Comment is one operator comment. Body is the ">>" lines joined by "\n", with
// one leading space stripped from each.
type Comment struct {
	Anchor
	Body string
}

// Previous is the last round sent, zero when none has been.
type Previous struct {
	SentAt   time.Time
	Sent     []Comment
	Snapshot string
}

// EditedError says the saved review file changed a diff line, or put a
// comment where dev cannot tell it from one.
type EditedError struct {
	Line     int
	Indented bool
}

func (e *EditedError) Error() string {
	if e.Indented {
		return fmt.Sprintf("line %d: comments start at column 0", e.Line)
	}
	return fmt.Sprintf("line %d: the diff itself was changed", e.Line)
}

// renderedLine is one line of the review file and where a comment under it
// belongs.
type renderedLine struct {
	text   string
	anchor Anchor
}

// lineSlot identifies the draft comments that belong under one diff line.
type lineSlot struct {
	file, hunk string
	occurrence int
	index      int
}

// Render lays out the review file: how to comment, the last round's comments,
// the draft comments, and the diff with the draft's comments inserted.
func Render(d Diff, draft []Comment, prev Previous) string {
	lines := renderLines(d, draft, prev)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	return b.String()
}

func renderLines(d Diff, draft []Comment, prev Previous) []renderedLine {
	var lines []renderedLine
	add := func(text string, a Anchor) {
		lines = append(lines, renderedLine{text: text, anchor: a})
	}

	add(`# Write comments on lines starting with ">>", under the line they are about.`, Anchor{})
	add("# A comment above the first file is about the whole change.", Anchor{})
	add("#", Anchor{})

	if !prev.SentAt.IsZero() {
		add("# Last review (sent "+prev.SentAt.Format("2006-01-02 15:04")+"):", Anchor{})
		for _, c := range prev.Sent {
			add("#   "+lastRoundSummary(c), Anchor{})
		}
	}
	add("", Anchor{})

	general, whole, atLine := indexDraft(draft)
	used := make([]bool, len(draft))
	emit := func(indices []int) {
		for _, i := range indices {
			used[i] = true
			for _, body := range strings.Split(draft[i].Body, "\n") {
				add(commentLine(body), Anchor{})
			}
		}
	}

	emit(general)
	if len(general) > 0 {
		add("", Anchor{})
	}

	lastFolder := "\x00"
	for _, f := range d.Files {
		if f.Folder != "" && f.Folder != lastFolder {
			add("# folder: "+f.Folder, Anchor{})
			lastFolder = f.Folder
		}

		wholeAnchor := Anchor{File: f.Key(), Path: f.Path}
		for _, header := range f.Header {
			add(header, wholeAnchor)
		}
		if f.Note != "" {
			add("# NOTE: "+f.Note, wholeAnchor)
		}
		emit(whole[f.Key()])

		bodies := map[string]int{}
		for _, h := range f.Hunks {
			// Occurrence says which hunk with this body it is, which is how
			// every later lookup finds it again; an index in the file would
			// read as a different hunk.
			occurrence := bodies[h.Body()]
			bodies[h.Body()]++
			if prev.Snapshot != "" && !inSnapshot(prev.Snapshot, f, h) {
				add("# changed since your last review", wholeAnchor)
			}
			hunkAt := hunkAnchor(f, h, occurrence)
			add(h.Header, hunkAt)
			last := hunkAt
			lineIndex := 0
			for _, raw := range h.Raw {
				if strings.HasPrefix(raw, `\`) || lineIndex >= len(h.Lines) {
					// Markers carry no line of their own; a comment after one
					// belongs to the line before it.
					add(raw, last)
					continue
				}
				l := h.Lines[lineIndex]
				at := Anchor{
					File: f.Key(), Path: f.Path, Hunk: h.Body(), Occurrence: occurrence,
					Index: lineIndex, Kind: l.Kind, Line: lineNumber(l), Text: l.Text,
				}
				add(raw, at)
				emit(atLine[lineSlot{f.Key(), h.Body(), occurrence, lineIndex}])
				last = at
				lineIndex++
			}
		}
	}

	for _, path := range d.Skipped {
		add("# skipped, not a git repository: "+path, Anchor{})
	}

	// Reanchor makes this unreachable; keeping the text visible is still
	// better than dropping a comment if it is ever called without it.
	for i, c := range draft {
		if used[i] {
			continue
		}
		for _, body := range strings.Split(c.Body, "\n") {
			add(commentLine(body), Anchor{})
		}
	}
	return lines
}

// indexDraft groups draft comments by the line they are written under.
func indexDraft(draft []Comment) (general []int, whole map[string][]int, atLine map[lineSlot][]int) {
	whole = map[string][]int{}
	atLine = map[lineSlot][]int{}
	for i, c := range draft {
		switch {
		case c.File == "":
			general = append(general, i)
		case c.Hunk == "":
			whole[c.File] = append(whole[c.File], i)
		default:
			key := lineSlot{c.File, c.Hunk, c.Occurrence, c.Index}
			atLine[key] = append(atLine[key], i)
		}
	}
	return general, whole, atLine
}

func hunkAnchor(f File, h Hunk, occurrence int) Anchor {
	a := Anchor{File: f.Key(), Path: f.Path, Hunk: h.Body(), Occurrence: occurrence}
	if len(h.Lines) > 0 {
		l := h.Lines[0]
		a.Index = 0
		a.Kind = l.Kind
		a.Line = lineNumber(l)
		a.Text = l.Text
	}
	return a
}

func lineNumber(l Line) int {
	if l.Kind == Removed {
		return l.Old
	}
	return l.New
}

func commentLine(body string) string {
	if body == "" {
		return ">>"
	}
	return ">> " + body
}

func lastRoundSummary(c Comment) string {
	switch {
	case c.File == "":
		return "(general)"
	case c.Hunk == "":
		return anchorDisplayPath(c.Anchor)
	default:
		return fmt.Sprintf("%s:%d  %s", anchorDisplayPath(c.Anchor), c.Line, firstLine(c.Body))
	}
}

func anchorDisplayPath(a Anchor) string {
	if folder, path, ok := strings.Cut(a.File, "\x00"); ok && folder != "" {
		return folder + "/" + path
	}
	return a.Path
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ParseReview reads the saved review file back. It refuses a file whose diff
// no longer matches d, so an accidental edit cannot be stored as a comment on
// the wrong line.
func ParseReview(d Diff, text string) ([]Comment, error) {
	baseline := baselineLines(d)
	saved := strings.Split(text, "\n")

	var comments []Comment
	current := Anchor{}
	b := 0
	for i := 0; i < len(saved); i++ {
		line := strings.TrimRight(saved[i], " \t\r")
		switch {
		case line == "":
			// A context line that is a blank line in the file trims to
			// nothing too; matching it keeps a comment under it there rather
			// than on the line above.
			if b < len(baseline) && baseline[b].text == "" {
				current = baseline[b].anchor
				b++
			}
			continue
		case strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, ">>"):
			j := i
			var body []string
			for j < len(saved) {
				l := strings.TrimRight(saved[j], " \t\r")
				if !strings.HasPrefix(l, ">>") {
					break
				}
				body = append(body, strings.TrimPrefix(strings.TrimPrefix(l, ">>"), " "))
				j++
			}
			comments = append(comments, Comment{Anchor: current, Body: strings.Join(body, "\n")})
			i = j - 1
			continue
		}

		if b < len(baseline) && line == baseline[b].text {
			current = baseline[b].anchor
			b++
			continue
		}
		if isIndentedComment(line) {
			return nil, &EditedError{Line: i + 1, Indented: true}
		}
		return nil, &EditedError{Line: i + 1}
	}
	if b != len(baseline) {
		return nil, &EditedError{Line: max(1, len(saved))}
	}
	return comments, nil
}

// baselineLines is the diff part of the file Render writes with no comments.
func baselineLines(d Diff) []renderedLine {
	var out []renderedLine
	for _, rl := range renderLines(d, nil, Previous{}) {
		text := strings.TrimRight(rl.text, " \t\r")
		if strings.HasPrefix(text, "#") {
			continue
		}
		// A blank line that belongs to no file is layout; one with an anchor
		// is a context line and has to line up with the saved file.
		if text == "" && rl.anchor.File == "" {
			continue
		}
		out = append(out, renderedLine{text: text, anchor: rl.anchor})
	}
	return out
}

func isIndentedComment(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return trimmed != line && strings.HasPrefix(trimmed, ">>")
}

// EncodeComments serialises comments for storage; none is the empty string.
func EncodeComments(c []Comment) (string, error) {
	if len(c) == 0 {
		return "", nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encoding review comments: %w", err)
	}
	return string(b), nil
}

// DecodeComments reads what EncodeComments wrote; the empty string has none.
func DecodeComments(s string) ([]Comment, error) {
	if s == "" {
		return nil, nil
	}
	var c []Comment
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil, fmt.Errorf("decoding review comments: %w", err)
	}
	return c, nil
}
