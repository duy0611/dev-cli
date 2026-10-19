package review

import "testing"

func TestParseDiffModifiedFile(t *testing.T) {
	text := `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@
 package main
 
-func old() {}
+func new() {}
+// added
@@ -10,3 +11,4 @@ func later()
 context
-remove me
+add me
+and me
`

	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	f := files[0]
	if f.Path != "main.go" {
		t.Errorf("Path = %q, want main.go", f.Path)
	}
	if f.Folder != "" {
		t.Errorf("Folder = %q, want empty", f.Folder)
	}
	if len(f.Hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(f.Hunks))
	}

	var firstAdded *Line
	for i := range f.Hunks[1].Lines {
		if f.Hunks[1].Lines[i].Kind == Added {
			firstAdded = &f.Hunks[1].Lines[i]
			break
		}
	}
	if firstAdded == nil {
		t.Fatal("second hunk has no added line")
	}
	if firstAdded.New != 12 {
		t.Errorf("first added line New = %d, want 12", firstAdded.New)
	}
	if firstAdded.Old != 0 {
		t.Errorf("added line Old = %d, want 0", firstAdded.Old)
	}

	var removed *Line
	for i := range f.Hunks[0].Lines {
		if f.Hunks[0].Lines[i].Kind == Removed {
			removed = &f.Hunks[0].Lines[i]
			break
		}
	}
	if removed == nil {
		t.Fatal("first hunk has no removed line")
	}
	if removed.Old != 3 {
		t.Errorf("removed line Old = %d, want 3", removed.Old)
	}
	if removed.New != 0 {
		t.Errorf("removed line New = %d, want 0", removed.New)
	}
	if removed.Text != "func old() {}" {
		t.Errorf("removed line Text = %q, want the text without the '-'", removed.Text)
	}
}

func TestParseDiffPaths(t *testing.T) {
	text := `diff --git a/dir/my file.go b/dir/my file.go
--- a/dir/my file.go	
+++ b/dir/my file.go	
@@ -1 +1 @@
-a
+b
diff --git a/naïve.txt b/naïve.txt
--- a/naïve.txt
+++ b/naïve.txt
@@ -1 +1 @@
-a
+b
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/born.go b/born.go
new file mode 100644
--- /dev/null
+++ b/born.go
@@ -0,0 +1 @@
+hi
`

	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	want := []string{"dir/my file.go", "naïve.txt", "gone.go", "born.go"}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d", len(files), len(want))
	}
	for i, w := range want {
		if files[i].Path != w {
			t.Errorf("files[%d].Path = %q, want %q", i, files[i].Path, w)
		}
	}
}

func TestParseDiffBinary(t *testing.T) {
	text := `diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..abc1234
Binary files /dev/null and b/logo.png differ
`

	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Path != "logo.png" {
		t.Errorf("Path = %q, want logo.png", files[0].Path)
	}
	if len(files[0].Hunks) != 0 {
		t.Errorf("got %d hunks, want none", len(files[0].Hunks))
	}
	if len(files[0].Header) != 4 {
		t.Errorf("Header has %d lines, want 4 (the block through the Binary line)", len(files[0].Header))
	}
}

func TestParseDiffNoNewlineMarker(t *testing.T) {
	text := `diff --git a/x.txt b/x.txt
--- a/x.txt
+++ b/x.txt
@@ -1 +1 @@
-old
\ No newline at end of file
+new
\ No newline at end of file
`

	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(files[0].Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(files[0].Hunks))
	}
	h := files[0].Hunks[0]
	if len(h.Raw) != 4 {
		t.Errorf("Raw has %d lines, want 4", len(h.Raw))
	}
	if len(h.Lines) != 2 {
		t.Errorf("Lines has %d entries, want 2; the marker is not a line", len(h.Lines))
	}
	if h.Raw[1] != `\ No newline at end of file` {
		t.Errorf("Raw[1] = %q, want the marker verbatim", h.Raw[1])
	}
}

func TestHunkBodyIgnoresHeaderNumbers(t *testing.T) {
	text := `diff --git a/x b/x
--- a/x
+++ b/x
@@ -1,2 +1,2 @@
 same
-a
+b
@@ -9,2 +9,2 @@
 same
-a
+b
`

	files, err := ParseDiff("", text)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	hunks := files[0].Hunks
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(hunks))
	}
	if hunks[0].Body() != hunks[1].Body() {
		t.Errorf("bodies differ:\n%q\n%q", hunks[0].Body(), hunks[1].Body())
	}
}

func TestFileKeyIncludesFolder(t *testing.T) {
	a := File{Folder: "app", Path: "a.go"}
	b := File{Folder: "lib", Path: "a.go"}
	if a.Key() == b.Key() {
		t.Errorf("Key() ignores Folder: both are %q", a.Key())
	}
	if a.Key() != "app\x00a.go" {
		t.Errorf("Key() = %q, want app\\x00a.go", a.Key())
	}
}
