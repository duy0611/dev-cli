package store

import "fmt"

// SetTeamTier saves or replaces the model a tier stands for under a tool.
func (s *Store) SetTeamTier(tool, tier, model string) error {
	_, err := s.db.Exec(
		`INSERT INTO team_tiers (tool, tier, model)
		 VALUES (?, ?, ?)
		 ON CONFLICT(tool, tier) DO UPDATE SET model = excluded.model`,
		tool, tier, model)
	if err != nil {
		return fmt.Errorf("setting team tier %s for %s: %w", tier, tool, err)
	}
	return nil
}

func (s *Store) UnsetTeamTier(tool, tier string) error {
	res, err := s.db.Exec(
		`DELETE FROM team_tiers WHERE tool = ? AND tier = ?`, tool, tier)
	if err != nil {
		return fmt.Errorf("unsetting team tier %s for %s: %w", tier, tool, err)
	}
	return requireOneRow(res, ErrNotFound)
}

// ListTeamTiers returns a tool's saved tiers as tier -> model; empty, never
// nil, when it has none.
func (s *Store) ListTeamTiers(tool string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT tier, model FROM team_tiers WHERE tool = ?`, tool)
	if err != nil {
		return nil, fmt.Errorf("listing team tiers for %s: %w", tool, err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var tier, model string
		if err := rows.Scan(&tier, &model); err != nil {
			return nil, fmt.Errorf("reading team tiers for %s: %w", tool, err)
		}
		out[tier] = model
	}
	return out, rows.Err()
}
