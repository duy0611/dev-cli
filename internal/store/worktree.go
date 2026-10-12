package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// CreateWorktree records a checkout `dev worktree create` made.
func (s *Store) CreateWorktree(w model.Worktree) error {
	_, err := s.db.Exec(
		`INSERT INTO worktrees
		   (workspace_name, name, repo, branch, path, herdr_workspace, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.WorkspaceName, w.Name, w.Repo, w.Branch, w.Path,
		w.HerdrWorkspace, nowString())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("recording worktree %s: %w", w.Name, err)
	}
	return nil
}

// GetWorktree returns one recorded checkout, or ErrNotFound.
func (s *Store) GetWorktree(workspace, name string) (model.Worktree, error) {
	row := s.db.QueryRow(
		`SELECT workspace_name, name, repo, branch, path, herdr_workspace, created_at
		 FROM worktrees WHERE workspace_name = ? AND name = ?`,
		workspace, name)
	return scanWorktree(row)
}

// ListWorktrees returns one workspace's checkouts, or every workspace's when
// workspace is empty.
func (s *Store) ListWorktrees(workspace string) ([]model.Worktree, error) {
	query := `SELECT workspace_name, name, repo, branch, path, herdr_workspace, created_at
	          FROM worktrees`
	args := []any{}
	if workspace != "" {
		query += ` WHERE workspace_name = ?`
		args = append(args, workspace)
	}
	query += ` ORDER BY workspace_name, name`

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

// DeleteWorktree forgets a checkout. Containers on it are left alone.
func (s *Store) DeleteWorktree(workspace, name string) error {
	res, err := s.db.Exec(
		`DELETE FROM worktrees WHERE workspace_name = ? AND name = ?`,
		workspace, name)
	if err != nil {
		return fmt.Errorf("deleting worktree %s: %w", name, err)
	}
	return requireOneRow(res, ErrNotFound)
}

func scanWorktree(sc scanner) (model.Worktree, error) {
	var (
		w         model.Worktree
		createdAt string
	)
	if err := sc.Scan(&w.WorkspaceName, &w.Name, &w.Repo, &w.Branch,
		&w.Path, &w.HerdrWorkspace, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Worktree{}, ErrNotFound
		}
		return model.Worktree{}, fmt.Errorf("reading the worktree: %w", err)
	}

	t, err := parseTime(createdAt)
	if err != nil {
		return model.Worktree{}, fmt.Errorf("reading worktree %s: bad created_at %q: %w",
			w.Name, createdAt, err)
	}
	w.CreatedAt = t
	return w, nil
}
