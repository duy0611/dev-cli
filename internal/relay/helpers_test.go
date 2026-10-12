package relay

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func dialUnix(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// captureStderr redirects os.Stderr for the test and returns a func reporting
// what has been written. Tests using it must not run in parallel: os.Stderr is
// global.
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
		// Closing the writer ends the reader; waiting keeps the goroutine from
		// outliving the test.
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

// waitFor polls until cond holds, failing with msg if it never does.
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

// waitForGone waits for path to disappear. Teardown is asynchronous, so a bare
// Stat races.
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

// roundTrip sends msg through the socket at path and checks it comes back
// intact. It returns an error, not a failure, so a caller can poll while a
// relay starts.
func roundTrip(path, msg string) error {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return fmt.Errorf("round trip = %q, want %q", buf, msg)
	}
	return nil
}
