package review

import (
	"strings"
	"testing"
)

const shiftedOneLineDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -5,2 +5,3 @@
 package a
+// added
 context
`

const originalOneLineDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+// added
 context
`

func anchorOn(t *testing.T, d Diff, fileIndex, hunkIndex, lineIndex int) Anchor {
	t.Helper()
	f := d.Files[fileIndex]
	h := f.Hunks[hunkIndex]
	if lineIndex >= len(h.Lines) {
		t.Fatalf("hunk %d has %d lines, want index %d", hunkIndex, len(h.Lines), lineIndex)
	}
	l := h.Lines[lineIndex]
	// Occurrence counts hunks sharing this body, as ParseReview records it.
	occurrence := 0
	for _, earlier := range f.Hunks[:hunkIndex] {
		if earlier.Body() == h.Body() {
			occurrence++
		}
	}
	return Anchor{
		File: f.Key(), Path: f.Path, Hunk: h.Body(), Occurrence: occurrence,
		Index: lineIndex, Kind: l.Kind, Line: lineNumber(l), Text: l.Text,
	}
}

func TestReanchorKeepsAnUnchangedHunk(t *testing.T) {
	old := mustDiff(t, originalOneLineDiff)
	now := mustDiff(t, shiftedOneLineDiff)
	c := Comment{Anchor: anchorOn(t, old, 0, 0, 1), Body: "why?"}

	got := Reanchor(now, []Comment{c})
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].Hunk != c.Hunk || got[0].Occurrence != 0 || got[0].Index != 1 {
		t.Errorf("anchor = %+v, want the same hunk and index", got[0].Anchor)
	}
	if got[0].Line != 6 {
		t.Errorf("Line = %d, want 6 from the shifted hunk header", got[0].Line)
	}
	if got[0].Body != "why?" {
		t.Errorf("Body = %q, want it untouched", got[0].Body)
	}
}

func TestReanchorMovedHunkToFile(t *testing.T) {
	old := mustDiff(t, originalOneLineDiff)
	now := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 package a
-// added
 context
`)
	c := Comment{Anchor: anchorOn(t, old, 0, 0, 1), Body: "why?"}

	got := Reanchor(now, []Comment{c})
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].Hunk != "" {
		t.Errorf("Hunk = %q, want a whole-file comment", got[0].Hunk)
	}
	if got[0].File != now.Files[0].Key() {
		t.Errorf("File = %q, want the existing file", got[0].File)
	}
	if !strings.HasPrefix(got[0].Body, "(was on line 2: // added) ") {
		t.Errorf("Body = %q, want the moved-line prefix", got[0].Body)
	}
}

func TestReanchorGoneFileToGeneral(t *testing.T) {
	old := mustDiff(t, originalOneLineDiff)
	now := mustDiff(t, `diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1 +1 @@
-x
+y
`)
	c := Comment{Anchor: anchorOn(t, old, 0, 0, 1), Body: "why?"}
	c.Line = 42

	got := Reanchor(now, []Comment{c})
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].File != "" {
		t.Errorf("File = %q, want the general anchor", got[0].File)
	}
	if !strings.HasPrefix(got[0].Body, "(was on a.go:42) ") {
		t.Errorf("Body = %q, want the gone-file prefix", got[0].Body)
	}
}

func TestReanchorIdenticalHunks(t *testing.T) {
	old := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 same
-a
+b
@@ -9,2 +9,2 @@
 same
-a
+b
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
-a
+b
`)
	c := Comment{Anchor: anchorOn(t, old, 0, 1, 2), Body: "second"}

	got := Reanchor(now, []Comment{c})
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].Occurrence != 1 {
		t.Errorf("Occurrence = %d, want the second identical hunk", got[0].Occurrence)
	}
	if got[0].Line != 12 {
		t.Errorf("Line = %d, want 12 from the shifted second hunk", got[0].Line)
	}
}

func TestStale(t *testing.T) {
	old := mustDiff(t, `diff --git a/a.go b/a.go
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
-old b
+new b
`)
	now := mustDiff(t, `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -5,2 +5,3 @@
 package a
+// added
 context
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1 +1 @@
-old b
+changed b
`)

	general := Comment{Body: "general"}
	whole := Comment{Anchor: Anchor{File: old.Files[0].Key(), Path: old.Files[0].Path}}
	kept := Comment{Anchor: anchorOn(t, old, 0, 0, 1)}
	moved := Comment{Anchor: anchorOn(t, old, 1, 0, 1)}
	gone := Comment{Anchor: Anchor{
		File: "gone\x00gone.go", Path: "gone.go", Hunk: "body",
		Kind: Added, Line: 3, Text: "x",
	}}

	got := Stale(now, []Comment{general, whole, kept, moved, gone})
	if len(got) != 2 {
		t.Fatalf("got %d stale comments, want 2: %+v", len(got), got)
	}
	if got[0].File != moved.File {
		t.Errorf("first stale = %q, want the moved hunk's file", got[0].File)
	}
	if got[1].File != gone.File {
		t.Errorf("second stale = %q, want the gone file", got[1].File)
	}
}

func TestSnapshotIgnoresHeaderNumbers(t *testing.T) {
	old := mustDiff(t, originalOneLineDiff)
	now := mustDiff(t, shiftedOneLineDiff)

	if Snapshot(old) != Snapshot(now) {
		t.Errorf("snapshots differ after only the @@ numbers moved:\n%s\n%s", Snapshot(old), Snapshot(now))
	}
	if !inSnapshot(Snapshot(old), now.Files[0], now.Files[0].Hunks[0]) {
		t.Error("the shifted hunk is not in the old snapshot")
	}
}
