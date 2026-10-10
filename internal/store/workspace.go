package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateWorkspace inserts a workspace. Unlike PutProvider this refuses to
// overwrite: a workspace owns containers and settings, so silently rebinding
// one to a different provider would strand them.
func (s *Store) CreateWorkspace(w model.Workspace) error {
	_, err := s.db.Exec(
		`INSERT INTO workspaces (name, provider_name, ssh_forward, created_at)
		 VALUES (?, ?, ?, ?)`,
		w.Name, w.ProviderName, boolToInt(w.SSHForward), nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("creating workspace %s: %w", w.Name, err)
	}
	return nil
}

// CloneWorkspace inserts w and copies every setting of the workspace src onto
// it, returning how many were copied. One transaction, so a failure part-way
// leaves no workspace rather than one holding half its settings — which would
// launch containers missing a token with nothing to say so. Specs are copied
// as specs; nothing is resolved here or anywhere in this layer.
func (s *Store) CloneWorkspace(src string, w model.Workspace) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("cloning workspace %s: %w", src, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the commit below succeeds

	// Checked inside the transaction: the copy below selects by name, and a
	// missing source would otherwise copy nothing and succeed.
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM workspaces WHERE name = ?`, src).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("cloning workspace %s: %w", src, err)
	}

	if _, err := tx.Exec(
		`INSERT INTO workspaces (name, provider_name, ssh_forward, created_at)
		 VALUES (?, ?, ?, ?)`,
		w.Name, w.ProviderName, boolToInt(w.SSHForward), nowString()); err != nil {
		if isUniqueViolation(err) {
			return 0, ErrExists
		}
		return 0, fmt.Errorf("creating workspace %s: %w", w.Name, err)
	}

	res, err := tx.Exec(
		`INSERT INTO workspace_settings (workspace_name, key, spec)
		 SELECT ?, key, spec FROM workspace_settings WHERE workspace_name = ?`,
		w.Name, src)
	if err != nil {
		return 0, fmt.Errorf("copying settings from workspace %s: %w", src, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("copying settings from workspace %s: %w", src, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("cloning workspace %s: %w", src, err)
	}
	return int(n), nil
}

func (s *Store) GetWorkspace(name string) (model.Workspace, error) {
	row := s.db.QueryRow(
		`SELECT name, provider_name, ssh_forward, created_at
		 FROM workspaces WHERE name = ?`, name)
	return scanWorkspace(row)
}

func (s *Store) ListWorkspaces() ([]model.Workspace, error) {
	rows, err := s.db.Query(
		`SELECT name, provider_name, ssh_forward, created_at
		 FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	defer rows.Close()

	var out []model.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkspacesUsing returns the workspaces bound to a provider, in name order.
// What `provider remove` reports instead of a foreign-key error.
func (s *Store) WorkspacesUsing(provider string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT name FROM workspaces WHERE provider_name = ? ORDER BY name`, provider)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces on provider %s: %w", provider, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("listing workspaces on provider %s: %w", provider, err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteWorkspace removes the workspace and, by cascade, its settings and
// container records. The containers themselves are the caller's problem: this
// layer never talks to an engine.
func (s *Store) DeleteWorkspace(name string) error {
	res, err := s.db.Exec(`DELETE FROM workspaces WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("deleting workspace %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

// --- settings ----------------------------------------------------------------

// SetSetting adds or replaces one setting on a workspace.
func (s *Store) SetSetting(workspace, key, spec string) error {
	_, err := s.db.Exec(
		`INSERT INTO workspace_settings (workspace_name, key, spec)
		 VALUES (?, ?, ?)
		 ON CONFLICT(workspace_name, key) DO UPDATE SET spec = excluded.spec`,
		workspace, key, spec)
	if err != nil {
		return fmt.Errorf("setting %s on workspace %s: %w", key, workspace, err)
	}
	return nil
}

func (s *Store) UnsetSetting(workspace, key string) error {
	res, err := s.db.Exec(
		`DELETE FROM workspace_settings WHERE workspace_name = ? AND key = ?`,
		workspace, key)
	if err != nil {
		return fmt.Errorf("unsetting %s on workspace %s: %w", key, workspace, err)
	}
	return requireOneRow(res, ErrNotFound)
}

// ListSettings returns a workspace's settings in key order, so that command
// output and the env passed to a container are both stable.
func (s *Store) ListSettings(workspace string) ([]model.Setting, error) {
	rows, err := s.db.Query(
		`SELECT key, spec FROM workspace_settings WHERE workspace_name = ? ORDER BY key`,
		workspace)
	if err != nil {
		return nil, fmt.Errorf("listing settings for workspace %s: %w", workspace, err)
	}
	defer rows.Close()

	var out []model.Setting
	for rows.Next() {
		var st model.Setting
		if err := rows.Scan(&st.Key, &st.Spec); err != nil {
			return nil, fmt.Errorf("reading settings for workspace %s: %w", workspace, err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanWorkspace(sc scanner) (model.Workspace, error) {
	var (
		w         model.Workspace
		ssh       int
		createdAt string
	)
	if err := sc.Scan(&w.Name, &w.ProviderName, &ssh, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Workspace{}, ErrNotFound
		}
		return model.Workspace{}, fmt.Errorf("reading workspace: %w", err)
	}
	w.SSHForward = ssh != 0

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Workspace{}, fmt.Errorf("reading workspace %s: bad created_at %q: %w", w.Name, createdAt, err)
	}
	w.CreatedAt = t
	return w, nil
}
