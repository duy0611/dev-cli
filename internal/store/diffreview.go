package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// GetDiffReview returns a container's review state. A container that has never
// been reviewed gets the zero value with the names set, not ErrNotFound: the
// absence of a review is not an error.
func (s *Store) GetDiffReview(workspace, name string) (model.DiffReview, error) {
	dr := model.DiffReview{WorkspaceName: workspace, ContainerName: name}
	var sentAt string
	err := s.db.QueryRow(
		`SELECT draft, sent, snapshot, sent_at FROM diff_reviews
		 WHERE workspace_name = ? AND container_name = ?`, workspace, name).
		Scan(&dr.Draft, &dr.Sent, &dr.Snapshot, &sentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return dr, nil
	}
	if err != nil {
		return model.DiffReview{}, fmt.Errorf("reading the diff review for %s: %w", name, err)
	}
	if sentAt != "" {
		t, err := parseTime(sentAt)
		if err != nil {
			return model.DiffReview{}, fmt.Errorf("parsing the diff review's send time for %s: %w", name, err)
		}
		dr.SentAt = t
	}
	return dr, nil
}

// SaveDiffDraft replaces the draft and leaves the last sent round untouched.
func (s *Store) SaveDiffDraft(workspace, name, draft string) error {
	_, err := s.db.Exec(
		`INSERT INTO diff_reviews (workspace_name, container_name, draft)
		 VALUES (?, ?, ?)
		 ON CONFLICT(workspace_name, container_name) DO UPDATE SET draft = excluded.draft`,
		workspace, name, draft)
	if err != nil {
		return fmt.Errorf("saving the diff draft for %s: %w", name, err)
	}
	return nil
}

// MarkDiffSent records the round that was sent, clears the draft and stamps the
// send time.
func (s *Store) MarkDiffSent(workspace, name, sent, snapshot string) error {
	_, err := s.db.Exec(
		`INSERT INTO diff_reviews (workspace_name, container_name, draft, sent, snapshot, sent_at)
		 VALUES (?, ?, '', ?, ?, ?)
		 ON CONFLICT(workspace_name, container_name) DO UPDATE SET
		   draft = '', sent = excluded.sent, snapshot = excluded.snapshot,
		   sent_at = excluded.sent_at`,
		workspace, name, sent, snapshot, nowString())
	if err != nil {
		return fmt.Errorf("recording the sent diff review for %s: %w", name, err)
	}
	return nil
}
