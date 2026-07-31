package postgres

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/migrations"
)

func TestSchemaMigrationsApplied(t *testing.T) {
	ctx := context.Background()
	std := stdlib.OpenDBFromPool(adminStore.pool)
	defer std.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, std, migrations.FS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	defer provider.Close()
	statuses, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("no migrations found")
	}
	for _, s := range statuses {
		if s.State != goose.StateApplied {
			t.Errorf("migration %s state = %s, want applied", filepath.Base(s.Source.Path), s.State)
		}
	}
	dbv, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if dbv != latest {
		t.Errorf("database version = %d, want %d", dbv, latest)
	}
}

func TestSchemaStatusDeterministic(t *testing.T) {
	ctx := context.Background()
	std := stdlib.OpenDBFromPool(adminStore.pool)
	defer std.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, std, migrations.FS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	defer provider.Close()
	first, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	second, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("status length changed: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].State != second[i].State {
			t.Errorf("migration %d state changed between calls", i)
		}
	}
}

func TestCheckSchemaCompatibleOK(t *testing.T) {
	if err := adminStore.CheckSchemaCompatible(context.Background()); err != nil {
		t.Fatalf("CheckSchemaCompatible: %v", err)
	}
}

func TestCheckSchemaCompatibleMismatch(t *testing.T) {
	ctx := context.Background()
	// Simulate a database that is ahead of the binary: an unknown future
	// version row.
	if _, err := adminStore.pool.Exec(ctx,
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, TRUE)"); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminStore.pool.Exec(ctx, "DELETE FROM goose_db_version WHERE version_id = 9999")
	})
	err := adminStore.CheckSchemaCompatible(ctx)
	if err == nil {
		t.Fatal("CheckSchemaCompatible succeeded with mismatched schema, want error")
	}
	if !strings.Contains(err.Error(), "9999") || !strings.Contains(err.Error(), "schema version mismatch") {
		t.Errorf("error should name both versions: %v", err)
	}
}

func TestRuntimeRoleCannotAlterSchema(t *testing.T) {
	ctx := context.Background()
	_, err := rtStore.pool.Exec(ctx, "CREATE TABLE forbidden (id INTEGER)")
	if err == nil {
		t.Fatal("runtime role created a table, want permission denied")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRuntimeRoleCanUseTables(t *testing.T) {
	// The runtime role must be able to do DML (it runs the worker), just not
	// DDL.
	c := mustCreateClient(t, rtStore, "runtime-client")
	if c.ID == 0 {
		t.Error("runtime role insert into clients produced id 0")
	}
}
