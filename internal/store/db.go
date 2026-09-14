// Package store is the persistence layer: a SQLite database today, the same
// schema against Postgres when the cloud provider lands.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	// Pure-Go SQLite. Chosen over mattn/go-sqlite3 so the build needs no C
	// toolchain and CGO_ENABLED=0 yields one static binary.
	_ "modernc.org/sqlite"
)

// Store owns the database handle. One per process.
type Store struct {
	db *sql.DB
}

// DefaultPath returns the database location, honouring DEV_STATE so tests and
// throwaway runs can point somewhere harmless.
func DefaultPath() (string, error) {
	dir := os.Getenv("DEV_STATE")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating the home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state", "dev")
	}
	return filepath.Join(dir, "dev.db"), nil
}

// Open connects to the database at path, creating it and applying any pending
// migrations. The directory is created 0700 and the file ends up 0600: the
// settings table holds references to secrets, and the container list alone says
// a good deal about what the operator works on.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the state directory: %w", err)
	}

	// _txlock=immediate takes the write lock when a transaction begins rather
	// than on its first write, which is what turns two concurrent `dev`
	// invocations into one waiting on the other instead of one failing partway
	// through with SQLITE_BUSY.
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one writer at a time. Letting database/sql open a pool of
	// connections against it buys nothing and turns lock contention into
	// errors, so hold it to one.
	db.SetMaxOpenConns(1)

	// Foreign keys are off by default in SQLite, which would silently skip the
	// ON DELETE CASCADE that removing a workspace depends on.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// Only meaningful on creation; a no-op afterwards. Applied after migrate so
	// the file certainly exists.
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("tightening permissions on %s: %w", path, err)
	}
	return s, nil
}

// OpenDefault opens the database at DefaultPath.
func OpenDefault() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

func (s *Store) Close() error { return s.db.Close() }
