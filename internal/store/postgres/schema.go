package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/JM-SE/llm-broker/migrations"
	"github.com/pressly/goose/v3"
)

// ConfiguredWorkloadTopology identifies the immutable local membership and
// digest expected by one API or worker process.
type ConfiguredWorkloadTopology struct {
	Slug   string
	Digest [32]byte
}

// ListUnmanagedWorkloads returns durable workload rows that are not part of
// the process-local configured membership. They are intentionally nonblocking,
// but startup callers log them so operators can see stale retained policy.
func (s *Store) ListUnmanagedWorkloads(ctx context.Context, configured []string) ([]string, error) {
	configuredSet := make(map[string]struct{}, len(configured))
	for _, slug := range configured {
		configuredSet[slug] = struct{}{}
	}
	rows, err := s.pool.Query(ctx, `SELECT name FROM workload_policies ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list unmanaged workloads: %w", err)
	}
	defer rows.Close()
	var unmanaged []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan unmanaged workload: %w", err)
		}
		if _, ok := configuredSet[name]; !ok {
			unmanaged = append(unmanaged, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unmanaged workloads: %w", err)
	}
	return unmanaged, nil
}

// CheckSchemaCompatible verifies that the database migration version matches
// the version embedded in this binary. It returns an error naming both
// versions when they differ, so a stale binary cannot silently run against a
// newer (or older) schema. Migrations are never applied automatically: an
// operator runs `llm-broker migrate up` explicitly.
func (s *Store) CheckSchemaCompatible(ctx context.Context) error {
	std := s.migrationDB()
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

// migrationDB is the private bridge used only by schema compatibility checks.
// Raw pool access is intentionally not part of the Store API.
func (s *Store) migrationDB() *sql.DB {
	return stdlib.OpenDBFromPool(s.pool)
}

// CheckConfiguredTopology fences startup without comparing mutable policy
// values. Every configured workload must be present, applied, and equal to
// the local secret-free topology digest; unmanaged rows are ignored here.
func (s *Store) CheckConfiguredTopology(ctx context.Context, configured []ConfiguredWorkloadTopology) error {
	items := append([]ConfiguredWorkloadTopology(nil), configured...)
	sort.Slice(items, func(i, j int) bool { return items[i].Slug < items[j].Slug })
	for _, item := range items {
		var digest []byte
		err := s.pool.QueryRow(ctx,
			`SELECT topology_digest FROM workload_policies WHERE name=$1`, item.Slug).Scan(&digest)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("configured workload is missing")
		}
		if err != nil {
			return fmt.Errorf("read configured workload state: %w", err)
		}
		if len(digest) != len(item.Digest) || !bytes.Equal(digest, item.Digest[:]) {
			return fmt.Errorf("configured workload topology is not provisioned")
		}
	}
	return nil
}
