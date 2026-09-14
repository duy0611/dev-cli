package store

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate applies every migration the database has not seen yet, in filename
// order, each inside its own transaction. A failed migration therefore leaves
// the database on the last version that did apply, rather than half-way through
// one that did not.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version    TEXT PRIMARY KEY,
		   applied_at TEXT NOT NULL
		 )`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations()
	if err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}
		if err := s.applyMigration(name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appliedMigrations() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("reading schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("reading schema_migrations: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func (s *Store) applyMigration(name, body string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("starting migration %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the commit below succeeds

	if _, err := tx.Exec(body); err != nil {
		return fmt.Errorf("applying migration %s: %w", name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		name, nowString()); err != nil {
		return fmt.Errorf("recording migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migration %s: %w", name, err)
	}
	return nil
}

// migrationNames lists the embedded migrations in the order they must run.
// Filenames are zero-padded and sort lexically, which is why they are numbered
// that way.
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
