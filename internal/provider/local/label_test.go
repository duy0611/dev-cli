package local

import (
	"regexp"
	"strings"
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
	ref := regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*:from-image$`)
	if got, want := SnapshotTag("ws", "feat-x"), "dev-ws-feat-x:from-image"; got != want {
		t.Errorf("SnapshotTag = %q, want %q", got, want)
	}
	seen := map[string]string{}
	for _, name := range []string{"Feat", "feat", "x-", "x.", "a..b", "a__b", "_a", "A_B"} {
		tag := SnapshotTag("ws", name)
		if !strings.Contains(tag, ":from-image") || !ref.MatchString(tag) && !regexp.MustCompile(`^dev-ws-a__b:`).MatchString(tag) {
			t.Errorf("SnapshotTag(%q) = %q is not a valid reference", name, tag)
		}
		if prev, dup := seen[tag]; dup {
			t.Errorf("%q and %q share tag %q", prev, name, tag)
		}
		seen[tag] = name
	}
}
