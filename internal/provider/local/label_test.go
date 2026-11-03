package local

import (
	"regexp"
	"testing"
)

func TestVolumeName(t *testing.T) {
	if got, want := VolumeName("ws", "scratch"), "dev-ws-scratch"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}

func TestFolderVolumeName(t *testing.T) {
	a := FolderVolumeName("ws", "c", "/src/a b")
	if a != FolderVolumeName("ws", "c", "/src/a b") {
		t.Error("not stable")
	}
	if a == FolderVolumeName("ws", "c", "/src/a c") || !regexp.MustCompile(`^dev-ws-c-f-[0-9a-f]{8}$`).MatchString(a) {
		t.Errorf("FolderVolumeName = %q", a)
	}
}

func TestSnapshotTag(t *testing.T) {
	ref := regexp.MustCompile(`^dev-copy-[0-9a-f]{16}:from-image$`)
	seen := map[string]string{}
	for _, c := range [][2]string{{"a-b", "c"}, {"a", "b-c"}, {"ws", "Feat"}, {"ws", "feat"}, {"ws", "x-"}, {"ws", "_a"}} {
		tag := SnapshotTag(c[0], c[1])
		if !ref.MatchString(tag) {
			t.Errorf("SnapshotTag(%q, %q) = %q is not a valid reference", c[0], c[1], tag)
		}
		key := c[0] + "/" + c[1]
		if prev, dup := seen[tag]; dup {
			t.Errorf("%q and %q share tag %q", prev, key, tag)
		}
		seen[tag] = key
	}
}
