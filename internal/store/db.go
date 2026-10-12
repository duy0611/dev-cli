// Package store is the persistence layer, backed by SQLite.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	// Pure-Go SQLite, so the build needs no C toolchain.
	_ "modernc.org/sqlite"
)

// Store owns the database handle. One per process.
type Store struct {
	db *sql.DB
}

// DefaultPath returns the database location, honouring DEV_STATE.
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

// Open connects to the database at path, creating it and applying pending
// migrations. The directory is 0700 and the file 0600, since the settings table
// names secrets.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the state directory: %w", err)
	}

	// Takes the write lock at transaction start, so concurrent invocations wait
	// instead of failing with SQLITE_BUSY partway through.
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one writer at a time; a pool only turns contention into
	// errors.
	db.SetMaxOpenConns(1)

	// Off by default in SQLite, and ON DELETE CASCADE depends on it.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// Only takes effect on creation; the file must exist first, hence after
	// migrate.
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
