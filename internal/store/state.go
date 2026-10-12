package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// activeWorkspaceKey is the app_state row holding the workspace that container
// commands act on when no --workspace is given.
const activeWorkspaceKey = "active_workspace"

// ActiveWorkspace returns the active workspace name, or "" when none is set.
// Unset is not an error.
func (s *Store) ActiveWorkspace() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, activeWorkspaceKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the active workspace: %w", err)
	}
	return v, nil
}

// SetActiveWorkspace records which workspace is active. The workspace must
// exist.
func (s *Store) SetActiveWorkspace(name string) error {
	if _, err := s.GetWorkspace(name); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO app_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		activeWorkspaceKey, name)
	if err != nil {
		return fmt.Errorf("setting the active workspace: %w", err)
	}
	return nil
}

// ClearActiveWorkspace forgets the active workspace. Called when it is deleted,
// so the next command does not report a name the operator never typed.
func (s *Store) ClearActiveWorkspace() error {
	if _, err := s.db.Exec(`DELETE FROM app_state WHERE key = ?`, activeWorkspaceKey); err != nil {
		return fmt.Errorf("clearing the active workspace: %w", err)
	}
	return nil
}

// --- small shared helpers -----------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// requireOneRow turns "the DELETE matched nothing" into a not-found error.
func requireOneRow(res sql.Result, missing error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking affected rows: %w", err)
	}
	if n == 0 {
		return missing
	}
	return nil
}

// isUniqueViolation reports whether err is a unique-constraint failure. It
// matches the message because the driver's error type is not a stable API.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}
