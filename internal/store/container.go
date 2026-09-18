package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateContainer records a container. The name has to be free within the
// workspace; the same name in another workspace is fine, and is the point of
// workspaces.
func (s *Store) CreateContainer(c model.Container) error {
	_, err := s.db.Exec(
		`INSERT INTO containers
		   (name, workspace_name, source_kind, source, config_path, generated_config, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.WorkspaceName, string(c.SourceKind), c.Source, c.ConfigPath,
		c.GeneratedConfig, nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("creating container %s: %w", c.Name, err)
	}
	return nil
}

func (s *Store) GetContainer(workspace, name string) (model.Container, error) {
	row := s.db.QueryRow(
		`SELECT name, workspace_name, source_kind, source, config_path, generated_config, created_at
		 FROM containers WHERE workspace_name = ? AND name = ?`, workspace, name)
	return scanContainer(row)
}

// ListContainers returns the containers in one workspace, or in every workspace
// when workspace is empty.
func (s *Store) ListContainers(workspace string) ([]model.Container, error) {
	query := `SELECT name, workspace_name, source_kind, source, config_path, generated_config, created_at
	          FROM containers`
	args := []any{}
	if workspace != "" {
		query += ` WHERE workspace_name = ?`
		args = append(args, workspace)
	}
	query += ` ORDER BY workspace_name, name`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	defer rows.Close()

	var out []model.Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteContainer(workspace, name string) error {
	res, err := s.db.Exec(
		`DELETE FROM containers WHERE workspace_name = ? AND name = ?`, workspace, name)
	if err != nil {
		return fmt.Errorf("deleting container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

func scanContainer(sc scanner) (model.Container, error) {
	var (
		c         model.Container
		kind      string
		createdAt string
	)
	if err := sc.Scan(&c.Name, &c.WorkspaceName, &kind, &c.Source, &c.ConfigPath,
		&c.GeneratedConfig, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Container{}, ErrNotFound
		}
		return model.Container{}, fmt.Errorf("reading container: %w", err)
	}
	c.SourceKind = model.SourceKind(kind)

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Container{}, fmt.Errorf("reading container %s: bad created_at %q: %w", c.Name, createdAt, err)
	}
	c.CreatedAt = t
	return c, nil
}

// UpdateContainerConfig replaces a container's generated configuration.
func (s *Store) UpdateContainerConfig(workspace, name, config string) error {
	res, err := s.db.Exec(
		`UPDATE containers SET generated_config = ?
		 WHERE workspace_name = ? AND name = ?`, config, workspace, name)
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}
