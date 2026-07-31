package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/JM-SE/llm-broker/migrations"
	"github.com/pressly/goose/v3"
)

// CheckSchemaCompatible verifies that the database migration version matches
// the version embedded in this binary. It returns an error naming both
// versions when they differ, so a stale binary cannot silently run against a
// newer (or older) schema. Migrations are never applied automatically: an
// operator runs `llm-broker migrate up` explicitly.
func (s *Store) CheckSchemaCompatible(ctx context.Context) error {
	std := stdlib.OpenDBFromPool(s.pool)
	defer std.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, std, migrations.FS)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	defer provider.Close()
	dbVersion, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		return fmt.Errorf("read embedded schema version: %w", err)
	}
	if dbVersion != latest {
		return fmt.Errorf("schema version mismatch: database is at version %d, binary embeds version %d",
			dbVersion, latest)
	}
	return nil
}
