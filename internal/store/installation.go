package store

import (
	"context"
	"fmt"
)

// InstallationID is this database's installation identifier (migration
// 0072, PR #1044 review F4): minted once, never changed, 16 lowercase hex
// characters. Chat checkpoint refs are scoped by it, so installations sharing
// a repository never see each other's.
func (s *Store) InstallationID(ctx context.Context) (string, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT install_id FROM installation WHERE id = 1`).Scan(&id); err != nil {
		return "", fmt.Errorf("read installation id: %w", err)
	}
	return id, nil
}
