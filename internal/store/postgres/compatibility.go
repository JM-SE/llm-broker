package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CompatibilityClient is the deliberately minimal client projection needed by
// a read-only compatibility check. Credential identifiers and digests are not
// selected or exposed.
type CompatibilityClient struct {
	ID      int64
	Enabled bool
}

// GetCompatibilityClientByName reads only identity and enabled state. It is
// intended for provisioner-backed compatibility preflight and performs no
// mutation or credential access.
func (s *Store) GetCompatibilityClientByName(ctx context.Context, name string) (CompatibilityClient, error) {
	var client CompatibilityClient
	err := s.pool.QueryRow(ctx, `SELECT id, enabled FROM clients WHERE name = $1`, name).
		Scan(&client.ID, &client.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompatibilityClient{}, ErrNotFound
	}
	return client, err
}
