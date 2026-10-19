package review

import (
	"strings"
	"testing"
)

const messageDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+// added
 context
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -10,2 +10,2 @@
 package b
-old
+new
`

func TestMessageContainsEveryComment(t *testing.T) {
	d := mustDiff(t, messageDiff)
	b := d.Files[1]

	general := Comment{Body: "keep it small"}
	whole := Comment{Anchor: Anchor{File: b.Key(), Path: b.Path}, Body: "whole comment"}
	line := Comment{Anchor: anchorOn(t, d, 0, 0, 1), Body: "why?"}

	// Given out of order: the message numbers comments in diff order.
	msg := Message("api", d, []Comment{general, whole, line})

	for _, want := range []string{
		"Do not commit",
		"keep it small",
		"## 1. a.go:2 (added line)",
		"## 2. b.go (whole file)",
		" package a",
		"+// added",
		"why?",
		"whole comment",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q:\n%s", want, msg)
		}
	}
	if strings.Index(msg, "## 1.") > strings.Index(msg, "## 2.") {
		t.Errorf("comments are not numbered in diff order:\n%s", msg)
	}
	if strings.Index(msg, "## 1.") > strings.Index(msg, "why?") ||
		strings.Index(msg, "## 2.") > strings.Index(msg, "whole comment") {
		t.Errorf("a comment body is not under its heading:\n%s", msg)
	}
}

func TestMessageMarksTheLine(t *testing.T) {
	d := mustDiff(t, messageDiff)
	line := Comment{Anchor: anchorOn(t, d, 0, 0, 1), Body: "why?"}

	msg := Message("api", d, []Comment{line})
	lines := strings.Split(msg, "\n")
	for i, l := range lines {
		if l == "+// added" {
			if i+1 >= len(lines) || lines[i+1] != ">> why?" {
				t.Errorf("line after the commented line is %q, want the >> marker", lines[i+1])
			}
			return
		}
	}
	t.Fatalf("message has no commented line:\n%s", msg)
}

func TestMessageRemovedLine(t *testing.T) {
	d := mustDiff(t, messageDiff)
	removed := Comment{
		Anchor: Anchor{
			File: d.Files[1].Key(), Path: d.Files[1].Path,
			Hunk: d.Files[1].Hunks[0].Body(), Occurrence: 0,
			Index: 1, Kind: Removed, Line: 7, Text: "old",
		},
		Body: "why remove this?",
	}

	msg := Message("api", d, []Comment{removed})
	if !strings.Contains(msg, "## 1. b.go:7 (removed line, was 7)") {
		t.Errorf("message has no removed-line heading:\n%s", msg)
	}
}
