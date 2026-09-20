package relay

import (
	"net"
	"os"
	"testing"
	"time"
)

func dialUnix(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// waitForGone waits for a path to disappear. Teardown is asynchronous — Close
// kills the relay and the relay removes its own socket — so a bare Stat right
// after Close would be a race.
func waitForGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%s still exists after close", path)
}
