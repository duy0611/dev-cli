package local

import "testing"

func TestVolumeName(t *testing.T) {
	if got, want := VolumeName("ws", "scratch"), "dev-ws-scratch"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}
