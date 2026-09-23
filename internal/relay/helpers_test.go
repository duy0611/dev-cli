package relay

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func dialUnix(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// captureStderr redirects os.Stderr for the length of the test and returns a
// function reporting what has been written to it so far.
//
// A pipe rather than a writer threaded through the code under test, because
// the unexpected-exit warning goes straight to os.Stderr: it is written from a
// goroutine that has no caller left to hand an error to. A goroutine drains
// the pipe into a buffer instead of the accessor reading it, so that asking
// what has been printed never blocks — a test asserting that *nothing* was
// printed would otherwise wait on a write that is never coming.
//
// These tests must not run in parallel with each other: os.Stderr is process
// global, and none of them call t.Parallel.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w

	var (
		mu   sync.Mutex
		buf  []byte
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		chunk := make([]byte, 1024)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf = append(buf, chunk[:n]...)
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		os.Stderr = old
		// The writer first: it is what makes the reader see EOF and stop. The
		// wait keeps the goroutine from outliving the test and writing into a
		// buffer nobody owns any more.
		_ = w.Close()
		<-done
		_ = r.Close()
	})

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(buf)
	}
}

// waitFor polls until cond holds, failing with msg if it never does. The
// warning it is used for is printed from a goroutine, so there is no moment at
// which a single check would be reliable.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error(msg)
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
