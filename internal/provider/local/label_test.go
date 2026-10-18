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
