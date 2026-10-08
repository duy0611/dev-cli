package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	return out
}

func TestRecordIsOneFlatLineWithTheCommonFields(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir, "1.2.3", nil)
	l.Record("exec", "ws", "api", map[string]any{"argv": []string{"ls", "-la"}, "exit_code": 0})

	got := lines(t, filepath.Join(dir, FileName))
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	r := got[0]
	for k, want := range map[string]any{"event": "exec", "workspace": "ws", "container": "api", "dev": "1.2.3"} {
		if r[k] != want {
			t.Errorf("%s = %v, want %v", k, r[k], want)
		}
	}
	// Flat: the event's own fields sit beside the common ones.
	if r["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v, want 0 at the top level", r["exit_code"])
	}
	if _, err := time.Parse(time.RFC3339Nano, r["time"].(string)); err != nil {
		t.Errorf("time %v is not RFC 3339: %v", r["time"], err)
	}
}

// An event field named like a common one must not be able to forge it — a
// record claiming another workspace would make the log lie.
func TestEventFieldsCannotOverwriteCommonFields(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir, "1", nil)
	l.Record("exec", "real", "c", map[string]any{"workspace": "forged", "event": "forged"})

	r := lines(t, filepath.Join(dir, FileName))[0]
	if r["workspace"] != "real" || r["event"] != "exec" {
		t.Errorf("common fields overwritten: %v", r)
	}
}

// The log lives beside the database and says as much about the operator's
// work as the database does, so it gets the same permissions.
func TestFileIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l := Open(dir, "1", nil)
	l.Record("start", "ws", "c", nil)

	info, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	dinfo, _ := os.Stat(dir)
	if perm := dinfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 700", perm)
	}
}

// Several dev processes may write at once — an agent session in one terminal,
// a rebuild in another. Every line must still be one whole record.
func TestConcurrentWritersProduceWholeLines(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			l := Open(dir, "1", nil)
			for i := range each {
				l.Record("exec", "ws", "c", map[string]any{
					"argv": []string{strings.Repeat("x", 2000), string(rune('a' + w))},
					"i":    i,
				})
			}
		})
	}
	wg.Wait()

	if got := len(lines(t, filepath.Join(dir, FileName))); got != writers*each {
		t.Errorf("got %d whole lines, want %d", got, writers*each)
	}
}

func TestOversizedRecordDropsArgvAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir, "1", nil)
	l.Record("exec", "ws", "c", map[string]any{
		"argv":      []string{strings.Repeat("y", 2*maxLine)},
		"exit_code": 1,
	})

	r := lines(t, filepath.Join(dir, FileName))[0]
	if _, ok := r["argv"]; ok {
		t.Error("argv kept in an oversized record")
	}
	if r["truncated"] != true {
		t.Error("truncated not set")
	}
	if r["exit_code"] != float64(1) {
		t.Errorf("other fields lost: %v", r)
	}
}

// A failed write warns once, however many records fail, and never panics.
func TestFailedWriteWarnsOnce(t *testing.T) {
	// A file where the directory should be makes every write fail.
	parent := t.TempDir()
	blocked := filepath.Join(parent, "state")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []error
	l := Open(blocked, "1", func(err error) { warnings = append(warnings, err) })
	l.Record("a", "", "", nil)
	l.Record("b", "", "", nil)

	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
}

func TestNilLogRecordsNothing(t *testing.T) {
	var l *Log
	l.Record("x", "", "", nil) // must not panic
}

func TestReadFilters(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir, "1", nil)
	l.Record("create", "ws", "api", nil)
	l.Record("exec", "ws", "api", nil)
	l.Record("exec", "ws", "web", nil)
	l.Record("exec", "other", "api", nil)

	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 4},
		{"workspace", Filter{Workspace: "ws"}, 3},
		{"container", Filter{Container: "api"}, 3},
		{"both", Filter{Workspace: "ws", Container: "api"}, 2},
		{"event", Filter{Events: []string{"create"}}, 1},
		{"events", Filter{Events: []string{"create", "exec"}}, 4},
		{"future", Filter{Since: time.Now().Add(time.Hour)}, 0},
		{"past", Filter{Since: time.Now().Add(-time.Hour)}, 4},
		{"unknown name", Filter{Container: "gone"}, 0},
	}
	for _, c := range cases {
		got, err := Read(l.Path(), c.f, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d records, want %d", c.name, len(got), c.want)
		}
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), FileName), Filter{}, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("Read = %v, %v; want nothing, no error", got, err)
	}
}

// A damaged line — a disk that filled mid-write — is skipped and reported,
// and does not hide the records after it.
func TestReadSkipsADamagedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	content := `{"time":"2026-10-08T10:00:00Z","event":"create","dev":"1"}
{"time":"2026-10-08T10:01:00Z","event":"ex
{"time":"2026-10-08T10:02:00Z","event":"exec","dev":"1"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var skipped []int
	got, err := Read(path, Filter{}, func(n int, _ error) { skipped = append(skipped, n) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d records, want 2", len(got))
	}
	if len(skipped) != 1 || skipped[0] != 2 {
		t.Errorf("skipped = %v, want [2]", skipped)
	}
}

func TestReadKeepsTheRawLine(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir, "1", nil)
	l.Record("exec", "ws", "c", map[string]any{"exit_code": 3})
	got, _ := Read(l.Path(), Filter{}, nil)
	var m map[string]any
	if err := json.Unmarshal(got[0].Raw, &m); err != nil || m["exit_code"] != float64(3) {
		t.Errorf("raw line %q does not round-trip: %v", got[0].Raw, err)
	}
}
