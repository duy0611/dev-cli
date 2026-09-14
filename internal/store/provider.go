package store

import (
	"database/sql"
	"errors"
	"fmt"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
)

// PutProvider creates the provider or updates its kind and config. `provider
// configure` is deliberately idempotent: re-running it with a changed kind is
// how a provider is corrected, and erroring instead would mean deleting and
// recreating one that workspaces already reference.
func (s *Store) PutProvider(p model.Provider) error {
	config := p.Config
	if config == "" {
		config = "{}"
	}
	_, err := s.db.Exec(
		`INSERT INTO providers (name, kind, config, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET kind = excluded.kind, config = excluded.config`,
		p.Name, string(p.Kind), config, nowString())
	if err != nil {
		return fmt.Errorf("saving provider %s: %w", p.Name, err)
	}
	return nil
}

// DeleteProvider removes a provider.
//
// The foreign key from workspaces would refuse this anyway; the caller checks
// first so the operator gets told which workspaces are in the way rather than a
// constraint name.
func (s *Store) DeleteProvider(name string) error {
	res, err := s.db.Exec(`DELETE FROM providers WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("deleting provider %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

func (s *Store) GetProvider(name string) (model.Provider, error) {
	row := s.db.QueryRow(
		`SELECT name, kind, config, created_at FROM providers WHERE name = ?`, name)
	return scanProvider(row)
}

func (s *Store) ListProviders() ([]model.Provider, error) {
	rows, err := s.db.Query(
		`SELECT name, kind, config, created_at FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}
	defer rows.Close()

	var out []model.Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// scanner is what *sql.Row and *sql.Rows have in common, so one scan function
// serves both the Get and the List.
type scanner interface{ Scan(dest ...any) error }

func scanProvider(sc scanner) (model.Provider, error) {
	var (
		p       model.Provider
		kind    string
		created string
	)
	if err := sc.Scan(&p.Name, &kind, &p.Config, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Provider{}, ErrNotFound
		}
		return model.Provider{}, fmt.Errorf("reading provider: %w", err)
	}
	p.Kind = model.ProviderKind(kind)

	t, err := parseTime(created)
	if err != nil {
		return model.Provider{}, fmt.Errorf("reading provider %s: bad created_at %q: %w", p.Name, created, err)
	}
	p.CreatedAt = t
	return p, nil
}
