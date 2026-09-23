package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateWorktree records a checkout against its container.
func (s *Store) CreateWorktree(w model.Worktree) error {
	_, err := s.db.Exec(
		`INSERT INTO worktrees
		   (workspace_name, container_name, repo, branch, path, herdr_workspace, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.WorkspaceName, w.ContainerName, w.Repo, w.Branch, w.Path,
		w.HerdrWorkspace, nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("recording the worktree for %s: %w", w.ContainerName, err)
	}
	return nil
}

// GetWorktree returns the checkout a container was created on, or ErrNotFound
// when the container is not worktree-backed.
func (s *Store) GetWorktree(workspace, container string) (model.Worktree, error) {
	row := s.db.QueryRow(
		`SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
		 FROM worktrees WHERE workspace_name = ? AND container_name = ?`,
		workspace, container)
	return scanWorktree(row)
}

// ListWorktrees returns one workspace's checkouts, or every workspace's when
// workspace is empty.
func (s *Store) ListWorktrees(workspace string) ([]model.Worktree, error) {
	query := `SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
	          FROM worktrees`
	args := []any{}
	if workspace != "" {
		query += ` WHERE workspace_name = ?`
		args = append(args, workspace)
	}
	query += ` ORDER BY workspace_name, container_name`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", err)
	}
	defer rows.Close()

	var out []model.Worktree
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWorktree forgets a checkout. The container row is untouched: removing
// the container is a separate step, and the cascade covers the other order.
func (s *Store) DeleteWorktree(workspace, container string) error {
	res, err := s.db.Exec(
		`DELETE FROM worktrees WHERE workspace_name = ? AND container_name = ?`,
		workspace, container)
	if err != nil {
		return fmt.Errorf("deleting the worktree for %s: %w", container, err)
	}
	return requireOneRow(res, ErrNotFound)
}

func scanWorktree(sc scanner) (model.Worktree, error) {
	var (
		w         model.Worktree
		createdAt string
	)
	if err := sc.Scan(&w.WorkspaceName, &w.ContainerName, &w.Repo, &w.Branch,
		&w.Path, &w.HerdrWorkspace, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Worktree{}, ErrNotFound
		}
		return model.Worktree{}, fmt.Errorf("reading the worktree: %w", err)
	}

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Worktree{}, fmt.Errorf("reading the worktree for %s: bad created_at %q: %w",
			w.ContainerName, createdAt, err)
	}
	w.CreatedAt = t
	return w, nil
}
