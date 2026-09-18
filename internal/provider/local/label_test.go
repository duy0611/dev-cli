package local

import "testing"

// The volume outlives the container and is removed by name. A second spelling
// anywhere would leak it: `docker volume rm` would miss, and the next create
// would mount a volume nothing else refers to.
func TestVolumeName(t *testing.T) {
	if got, want := VolumeName("ws", "scratch"), "dev-ws-scratch"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}
