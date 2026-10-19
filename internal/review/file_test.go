package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustDiff(t *testing.T, text string) Diff {
	t.Helper()
	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	return Diff{Files: files}
}

const twoFileDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+// added
 context
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1 +1 @@
-one
+two
`

func TestRenderParseRoundTrip(t *testing.T) {
	d := mustDiff(t, twoFileDiff)
	a, b := d.Files[0], d.Files[1]
	h := a.Hunks[0]

	draft := []Comment{
		{Body: "overall"},
		{
			Anchor: Anchor{
				File: a.Key(), Path: a.Path, Hunk: h.Body(), Occurrence: 0,
				Index: 1, Kind: Added, Line: 2, Text: "// added",
			},
			Body: "why?",
		},
		{Anchor: Anchor{File: b.Key(), Path: b.Path}, Body: "whole file"},
	}

	got, err := ParseReview(d, Render(d, draft, Previous{}))
	if err != nil {
		t.Fatalf("ParseReview: %v", err)
	}
	if !reflect.DeepEqual(got, draft) {
		t.Errorf("round trip = %+v, want %+v", got, draft)
	}
}

func TestParseReviewGroupsConsecutiveLines(t *testing.T) {
	d := mustDiff(t, twoFileDiff[:strings.Index(twoFileDiff, "diff --git a/b.go")])
	a := d.Files[0]
	h := a.Hunks[0]

	draft := []Comment{
		{
			Anchor: Anchor{File: a.Key(), Path: a.Path, Hunk: h.Body(), Index: 1,
				Kind: Added, Line: 2, Text: "// added"},
			Body: "a\nb",
		},
		{
			Anchor: Anchor{File: a.Key(), Path: a.Path, Hunk: h.Body(), Index: 2,
				Kind: Context, Line: 3, Text: "context"},
			Body: "c",
		},
	}

	got, err := ParseReview(d, Render(d, draft, Previous{}))
	if err != nil {
		t.Fatalf("ParseReview: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	if got[0].Body != "a\nb" {
		t.Errorf("first body = %q, want one comment with a\\nb", got[0].Body)
	}
	if got[1].Body != "c" {
		t.Errorf("second body = %q, want c", got[1].Body)
	}
}

func TestParseReviewAnchors(t *testing.T) {
	d := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
-old
+new
 still
`)
	a := d.Files[0]

	text := `>> general
diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
>> whole
@@ -1,2 +1,2 @@
-old
>> removed
+new
>> added
 still
`
	got, err := ParseReview(d, text)
	if err != nil {
		t.Fatalf("ParseReview: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d comments, want 4", len(got))
	}
	if got[0].Anchor != (Anchor{}) {
		t.Errorf("general anchor = %+v, want the zero anchor", got[0].Anchor)
	}
	if got[1].File != a.Key() || got[1].Hunk != "" {
		t.Errorf("whole-file anchor = %+v, want the file with no hunk", got[1].Anchor)
	}
	if got[2].Kind != Removed || got[2].Line != 1 || got[2].Text != "old" {
		t.Errorf("removed anchor = %+v, want Removed line 1", got[2].Anchor)
	}
	if got[3].Kind != Added || got[3].Line != 1 || got[3].Text != "new" {
		t.Errorf("added anchor = %+v, want Added line 1", got[3].Anchor)
	}
}

func TestParseReviewIgnoresHashLines(t *testing.T) {
	d := mustDiff(t, twoFileDiff[:strings.Index(twoFileDiff, "diff --git a/b.go")])
	a := d.Files[0]

	text := `>> general
diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
# after the header
>> whole
@@ -1,2 +1,3 @@
# before the first line
 package a
+// added
>> line
# a note in the middle
 context
`
	got, err := ParseReview(d, text)
	if err != nil {
		t.Fatalf("ParseReview: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d comments, want 3", len(got))
	}
	if got[1].File != a.Key() || got[1].Hunk != "" {
		t.Errorf("comment after a # line anchored to %+v, want the previous diff line (whole file)", got[1].Anchor)
	}
	if got[2].Kind != Added || got[2].Line != 2 {
		t.Errorf("line comment anchor = %+v, want Added line 2", got[2].Anchor)
	}
}

func TestParseReviewRefusesAnEditedDiffLine(t *testing.T) {
	d := mustDiff(t, twoFileDiff[:strings.Index(twoFileDiff, "diff --git a/b.go")])
	text := Render(d, nil, Previous{})

	lines := strings.Split(text, "\n")
	wantLine := 0
	for i, line := range lines {
		if line == "+// added" {
			lines[i] = "+// changed"
			wantLine = i + 1
			break
		}
	}
	if wantLine == 0 {
		t.Fatal("fixture has no +// added line")
	}

	_, err := ParseReview(d, strings.Join(lines, "\n"))
	var edited *EditedError
	if !errors.As(err, &edited) {
		t.Fatalf("err = %v, want *EditedError", err)
	}
	if edited.Line != wantLine {
		t.Errorf("edited line = %d, want %d", edited.Line, wantLine)
	}
	if edited.Indented {
		t.Error("Indented is set for an edited diff line")
	}
}

func TestParseReviewIndentedComment(t *testing.T) {
	d := mustDiff(t, twoFileDiff[:strings.Index(twoFileDiff, "diff --git a/b.go")])
	text := Render(d, nil, Previous{})
	text = strings.Replace(text, "\n+// added\n", "\n+// added\n >> fix this\n", 1)

	_, err := ParseReview(d, text)
	var edited *EditedError
	if !errors.As(err, &edited) {
		t.Fatalf("err = %v, want *EditedError", err)
	}
	if !edited.Indented {
		t.Error("Indented is false for a comment not at column 0")
	}
	if !strings.Contains(err.Error(), "start at column 0") {
		t.Errorf("Error() = %q, want it to say comments start at column 0", err.Error())
	}
}

func TestParseReviewToleratesEditorNormalisation(t *testing.T) {
	text := "diff --git a/a.go b/a.go\n" +
		"--- a/a.go\n" +
		"+++ b/a.go\n" +
		"@@ -1 +1 @@\n" +
		"-old   \n" +
		"+new   \n"
	d := mustDiff(t, text)
	a := d.Files[0]
	h := a.Hunks[0]
	draft := []Comment{{
		Anchor: Anchor{File: a.Key(), Path: a.Path, Hunk: h.Body(), Index: 0,
			Kind: Added, Line: 1, Text: "new   "},
		Body: "keep it",
	}}
	rendered := Render(d, draft, Previous{})

	// Editors strip trailing whitespace, switch to CRLF and add or drop the
	// final newline. None of that may count as editing a diff line.
	var normalised []string
	for _, line := range strings.Split(rendered, "\n") {
		normalised = append(normalised, strings.TrimRight(line, " \t"))
	}
	crlf := strings.Join(normalised, "\r\n")

	for _, text := range []string{crlf, strings.TrimSuffix(crlf, "\r\n"), crlf + "\r\n"} {
		got, err := ParseReview(d, text)
		if err != nil {
			t.Fatalf("ParseReview(%q): %v", text, err)
		}
		if len(got) != 1 || got[0].Body != "keep it" {
			t.Errorf("comments = %+v, want the one draft comment", got)
		}
	}
}

func TestRenderShowsLastRound(t *testing.T) {
	d := mustDiff(t, twoFileDiff[:strings.Index(twoFileDiff, "diff --git a/b.go")])
	prev := Previous{
		SentAt: time.Date(2026, 10, 19, 14, 32, 0, 0, time.UTC),
		Sent: []Comment{{
			Anchor: Anchor{
				File: "\x00internal/cli/diff.go", Path: "internal/cli/diff.go",
				Hunk: "some hunk", Line: 42,
			},
			Body: "why shell out here instead of reusing gitwt?",
		}},
	}

	out := Render(d, nil, prev)
	if !strings.Contains(out, "# Last review (sent ") {
		t.Errorf("output has no last-review header:\n%s", out)
	}
	if !strings.Contains(out, "#   internal/cli/diff.go:42  why shell out") {
		t.Errorf("output has no last-review summary line:\n%s", out)
	}
}

func TestEncodeDecodeComments(t *testing.T) {
	d := mustDiff(t, twoFileDiff)
	a := d.Files[0]
	h := a.Hunks[0]
	comments := []Comment{
		{Body: "general"},
		{Anchor: Anchor{File: a.Key(), Path: a.Path, Hunk: h.Body(), Index: 1,
			Kind: Added, Line: 2, Text: "// added"}, Body: "line"},
		{Anchor: Anchor{File: a.Key(), Path: a.Path}, Body: "whole"},
	}

	encoded, err := EncodeComments(comments)
	if err != nil {
		t.Fatalf("EncodeComments: %v", err)
	}
	decoded, err := DecodeComments(encoded)
	if err != nil {
		t.Fatalf("DecodeComments: %v", err)
	}
	if !reflect.DeepEqual(decoded, comments) {
		t.Errorf("round trip = %+v, want %+v", decoded, comments)
	}

	empty, err := EncodeComments(nil)
	if err != nil || empty != "" {
		t.Errorf("EncodeComments(nil) = %q, %v; want empty and no error", empty, err)
	}
	nilComments, err := DecodeComments("")
	if err != nil || nilComments != nil {
		t.Errorf("DecodeComments(\"\") = %+v, %v; want nil and no error", nilComments, err)
	}
}

func TestRenderMarksChangedHunks(t *testing.T) {
	old := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 same
-a
+b
@@ -9,2 +9,2 @@
 same
-c
+d
`)
	now := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3,2 +3,2 @@
 same
-a
+b
@@ -11,2 +11,2 @@
 same
-c
+changed
`)

	out := Render(now, nil, Previous{Snapshot: Snapshot(old)})
	if n := strings.Count(out, "# changed since your last review"); n != 1 {
		t.Fatalf("got %d change markers, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "# changed since your last review\n@@ -11,2 +11,2 @@") {
		t.Errorf("the marker is not directly before the changed hunk:\n%s", out)
	}
}

const twoHunkFileDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 package a
-one
+one-new
@@ -10,2 +10,2 @@
 package b
-two
+two-new
`

// commentUnder writes ">> why?" after the first line equal to target.
func commentUnder(t *testing.T, d Diff, target string) []Comment {
	t.Helper()
	var lines []string
	inserted := false
	for _, l := range strings.Split(strings.TrimRight(Render(d, nil, Previous{}), "\n"), "\n") {
		lines = append(lines, l)
		if !inserted && l == target {
			lines = append(lines, ">> why?")
			inserted = true
		}
	}
	if !inserted {
		t.Fatalf("the rendered review has no %q line", target)
	}
	comments, err := ParseReview(d, strings.Join(lines, "\n")+"\n")
	if err != nil {
		t.Fatalf("ParseReview: %v", err)
	}
	return comments
}

func TestSecondHunkCommentSurvivesStale(t *testing.T) {
	d := mustDiff(t, twoHunkFileDiff)
	comments := commentUnder(t, d, "+two-new")
	if len(comments) != 1 {
		t.Fatalf("%d comments, want 1", len(comments))
	}
	if comments[0].Occurrence != 0 {
		t.Errorf("Occurrence = %d, want 0: it is the only hunk with this body", comments[0].Occurrence)
	}
	if stale := Stale(d, comments); len(stale) != 0 {
		t.Errorf("a comment on the second hunk reads as stale: %+v", stale)
	}
	msg := Message("api", d, comments)
	if !strings.Contains(msg, "## 1. a.go:11 (added line)") {
		t.Errorf("message does not name the second hunk's line:\n%s", msg)
	}
	if !strings.Contains(msg, "+two-new\n>> why?") {
		t.Errorf("message put the comment under the wrong line:\n%s", msg)
	}
}

// A context line holding one space is how git shows a blank line in a hunk;
// a comment under it must stay under it.
func TestParseReviewCommentUnderBlankContextLine(t *testing.T) {
	d := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,3 @@
 package a
 
-old
+new
`)
	comments := commentUnder(t, d, " ")
	if len(comments) != 1 {
		t.Fatalf("%d comments, want 1", len(comments))
	}
	if comments[0].Line != 2 || comments[0].Index != 1 {
		t.Errorf("comment = line %d, index %d; want the blank line at line 2, index 1",
			comments[0].Line, comments[0].Index)
	}
	msg := Message("api", d, comments)
	if !strings.Contains(msg, "## 1. a.go:2") {
		t.Errorf("message does not name line 2:\n%s", msg)
	}
	if !strings.Contains(msg, "\n \n>> why?") {
		t.Errorf("message put the comment under the wrong line:\n%s", msg)
	}
}
