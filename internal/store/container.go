package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateContainer records a container. The name must be free within its
// workspace.
func (s *Store) CreateContainer(c model.Container) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("creating container %s: %w", c.Name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the commit below succeeds

	_, err = tx.Exec(
		`INSERT INTO containers
		   (name, workspace_name, source_kind, source, config_path, generated_config,
		    persist_state, agent_config, agent_config_pending,
		    git_guard, allow_privileged, config_digest, config_digest_fields,
		    mode, worktree_repo, workspace_path, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.WorkspaceName, string(c.SourceKind), c.Source, c.ConfigPath,
		c.GeneratedConfig, boolToInt(c.PersistState), c.AgentConfig,
		boolToInt(c.AgentConfigPending), boolToInt(c.GitGuard),
		boolToInt(c.AllowPrivileged), c.ConfigDigest, c.ConfigDigestFields,
		string(modeOrLegacy(c.Mode)), c.WorktreeRepo, c.WorkspacePath, nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("creating container %s: %w", c.Name, err)
	}
	if err := insertFolders(tx, c.WorkspaceName, c.Name, c.Folders); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("creating container %s: %w", c.Name, err)
	}
	return nil
}

func insertFolders(tx *sql.Tx, workspace, name string, folders []model.Folder) error {
	for _, f := range folders {
		if _, err := tx.Exec(
			`INSERT INTO container_folders
			   (workspace_name, container_name, host_path, container_path, worktree_repo)
			 VALUES (?, ?, ?, ?, ?)`, workspace, name, f.HostPath, f.ContainerPath, f.WorktreeRepo); err != nil {
			return fmt.Errorf("recording container %s's folder %s: %w", name, f.HostPath, err)
		}
	}
	return nil
}

// SetContainerFolders replaces a container's extra folders.
func (s *Store) SetContainerFolders(workspace, name string, folders []model.Folder) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the commit below succeeds

	var one int
	if err := tx.QueryRow(`SELECT 1 FROM containers WHERE workspace_name = ? AND name = ?`,
		workspace, name).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	if _, err := tx.Exec(`DELETE FROM container_folders WHERE workspace_name = ? AND container_name = ?`,
		workspace, name); err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	if err := insertFolders(tx, workspace, name, folders); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return nil
}

// loadFolders fills in each container's extra folders. Called once the
// container rows are closed: the pool has one connection.
func (s *Store) loadFolders(cs []model.Container) error {
	for i := range cs {
		rows, err := s.db.Query(
			`SELECT host_path, container_path, worktree_repo FROM container_folders
			 WHERE workspace_name = ? AND container_name = ? ORDER BY host_path`,
			cs[i].WorkspaceName, cs[i].Name)
		if err != nil {
			return fmt.Errorf("reading container %s's folders: %w", cs[i].Name, err)
		}
		for rows.Next() {
			var f model.Folder
			if err := rows.Scan(&f.HostPath, &f.ContainerPath, &f.WorktreeRepo); err != nil {
				rows.Close()
				return fmt.Errorf("reading container %s's folders: %w", cs[i].Name, err)
			}
			cs[i].Folders = append(cs[i].Folders, f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("reading container %s's folders: %w", cs[i].Name, err)
		}
	}
	return nil
}

func (s *Store) GetContainer(workspace, name string) (model.Container, error) {
	row := s.db.QueryRow(
		`SELECT name, workspace_name, source_kind, source, config_path, generated_config,
		        persist_state, agent_config, agent_config_pending,
		        git_guard, allow_privileged, config_digest, config_digest_fields,
		        mode, worktree_repo, workspace_path, created_at
		 FROM containers WHERE workspace_name = ? AND name = ?`, workspace, name)
	c, err := scanContainer(row)
	if err != nil {
		return c, err
	}
	one := []model.Container{c}
	if err := s.loadFolders(one); err != nil {
		return model.Container{}, err
	}
	return one[0], nil
}

// ListContainers returns the containers in one workspace, or in every workspace
// when workspace is empty.
func (s *Store) ListContainers(workspace string) ([]model.Container, error) {
	query := `SELECT name, workspace_name, source_kind, source, config_path, generated_config,
	                 persist_state, agent_config, agent_config_pending,
	                 git_guard, allow_privileged, config_digest, config_digest_fields,
		        mode, worktree_repo, workspace_path, created_at
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.loadFolders(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteContainer(workspace, name string) error {
	res, err := s.db.Exec(
		`DELETE FROM containers WHERE workspace_name = ? AND name = ?`, workspace, name)
	if err != nil {
		return fmt.Errorf("deleting container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

// modeOrLegacy returns the mode a row is written with. A container built before
// modes existed has none, and must not be stored as an empty string.
func modeOrLegacy(m model.Mode) model.Mode {
	if m == "" {
		return model.ModeLegacy
	}
	return m
}

func scanContainer(sc scanner) (model.Container, error) {
	var (
		c         model.Container
		kind      string
		persist   int
		pending   int
		guard     int
		allowPriv int
		mode      string
		createdAt string
	)
	if err := sc.Scan(&c.Name, &c.WorkspaceName, &kind, &c.Source, &c.ConfigPath,
		&c.GeneratedConfig, &persist, &c.AgentConfig, &pending,
		&guard, &allowPriv, &c.ConfigDigest, &c.ConfigDigestFields,
		&mode, &c.WorktreeRepo, &c.WorkspacePath, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Container{}, ErrNotFound
		}
		return model.Container{}, fmt.Errorf("reading container: %w", err)
	}
	c.SourceKind = model.SourceKind(kind)
	c.PersistState = persist != 0
	c.AgentConfigPending = pending != 0
	c.GitGuard = guard != 0
	c.AllowPrivileged = allowPriv != 0
	c.Mode = model.Mode(mode)

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

// SetAgentConfigPending records whether applying the container's agents.yaml is
// still owed.
func (s *Store) SetAgentConfigPending(workspace, name string, pending bool) error {
	res, err := s.db.Exec(
		`UPDATE containers SET agent_config_pending = ?
		 WHERE workspace_name = ? AND name = ?`, boolToInt(pending), workspace, name)
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

// SetConfigDigest records the digest of the configuration a container was just
// built from.
func (s *Store) SetConfigDigest(workspace, name, digest, fields string) error {
	res, err := s.db.Exec(
		`UPDATE containers SET config_digest = ?, config_digest_fields = ?
		 WHERE workspace_name = ? AND name = ?`, digest, fields, workspace, name)
	if err != nil {
		return fmt.Errorf("updating container %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}
