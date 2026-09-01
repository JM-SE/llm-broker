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

func TestRuntimeRoleCanReadAuthorizationTables(t *testing.T) {
	// Authorization state is provisioner-owned. Runtime may read it but cannot
	// create or mutate identity rows.
	c := mustCreateClient(t, adminStore, "runtime-client")
	got, err := rtStore.GetClientByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("runtime read client: %v", err)
	}
	if got.ID == 0 {
		t.Error("runtime role returned an invalid client id")
	}
}

func TestProvisionerRoleCanMutatePolicyButNotSchema(t *testing.T) {
	ctx := context.Background()
	name := uniqueName("provisioner-policy")
	p := applyPolicy(t, provisionerStore, WorkloadPolicy{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              1,
		RetryDelaysSeconds:       []int{1},
		MaxOutputTokens:          128,
		AllowedGeneration:        []byte(`{}`),
		RequiredFeatures:         []string{},
		AllowedCapabilities:      []string{"instruct"},
		WorkloadTimeoutSeconds:   60,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		TopologyDigest:           make([]byte, 32),
	})
	if p.Name != name || len(p.AllowedCapabilities) != 1 {
		t.Fatalf("unexpected provisioner policy: %+v", p)
	}
	if _, err := provisionerStore.pool.Exec(ctx, "CREATE TABLE provisioner_forbidden (id INTEGER)"); err == nil {
		t.Fatal("provisioner role created a table, want permission denied")
	}
	if _, err := provisionerStore.pool.Exec(ctx,
		"UPDATE workload_policies SET name = $1 WHERE id = $2", uniqueName("renamed"), p.ID); err == nil {
		t.Fatal("provisioner role renamed a workload, want permission denied")
	}
}

func TestRuntimeRoleCannotMutateAuthorizationTables(t *testing.T) {
	ctx := context.Background()
	client := mustCreateClient(t, adminStore, "runtime-mutation-client")
	workload := mustUpsertPolicy(t, adminStore, "runtime-mutation-workload")
	if err := adminStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("grant fixture workload: %v", err)
	}

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"clients", "UPDATE clients SET enabled = false WHERE id = $1", []any{client.ID}},
		{"workload_policies", "UPDATE workload_policies SET max_attempts = 1 WHERE id = $1", []any{workload.ID}},
		{"capabilities", "INSERT INTO workload_policy_capabilities (workload_id, capability) VALUES ($1, 'instruct')", []any{workload.ID}},
		{"grants", "DELETE FROM client_workload_grants WHERE client_id = $1 AND workload_id = $2", []any{client.ID, workload.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rtStore.pool.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Fatalf("runtime role mutated %s", tc.name)
			} else if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
				t.Fatalf("runtime %s mutation error = %v, want permission denied", tc.name, err)
			}
		})
	}
}

func TestRuntimeRoleSequencePrivilegesAreExecutionScoped(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		table   string
		allowed bool
	}{
		{name: "clients", table: "public.clients", allowed: false},
		{name: "workload policies", table: "public.workload_policies", allowed: false},
		{name: "task attempts", table: "public.task_attempts", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var allowed bool
			if err := rtStore.pool.QueryRow(ctx, `
				SELECT has_sequence_privilege(current_user,
					pg_get_serial_sequence($1, 'id'), 'USAGE')`, tc.table).Scan(&allowed); err != nil {
				t.Fatalf("read sequence privilege for %s: %v", tc.table, err)
			}
			if allowed != tc.allowed {
				t.Fatalf("runtime sequence USAGE for %s = %v, want %v", tc.table, allowed, tc.allowed)
			}
		})
	}
}

func TestH00DatabaseConstraintsAndActiveIndex(t *testing.T) {
	ctx := context.Background()
	policy := mustUpsertPolicy(t, adminStore, "h00-constraints")

	assertRejected := func(t *testing.T, statement string, args ...any) {
		t.Helper()
		tx, err := adminStore.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin constraint transaction: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback is expected after failures
		if _, err := tx.Exec(ctx, statement, args...); err == nil {
			t.Fatalf("statement unexpectedly succeeded: %s", statement)
		}
	}

	assertRejected(t, "UPDATE workload_policies SET workload_timeout_seconds = 0 WHERE id = $1", policy.ID)
	assertRejected(t, "UPDATE workload_policies SET workload_timeout_seconds = 3601 WHERE id = $1", policy.ID)
	assertRejected(t, "UPDATE workload_policies SET topology_digest = $1 WHERE id = $2", make([]byte, 31), policy.ID)

	for _, capability := range []any{nil, "", "NotCanonical", "*"} {
		assertRejected(t,
			"INSERT INTO workload_policy_capabilities (workload_id, capability) VALUES ($1, $2)",
			policy.ID, capability)
	}

	tx, err := adminStore.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin valid constraint transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE workload_policies SET workload_timeout_seconds = 1, topology_digest = $1 WHERE id = $2", make([]byte, 32), policy.ID); err != nil {
		t.Fatalf("timeout/digest lower boundary: %v", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE workload_policies SET workload_timeout_seconds = 3600 WHERE id = $1", policy.ID); err != nil {
		t.Fatalf("timeout upper boundary: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO workload_policy_capabilities (workload_id, capability) VALUES ($1, 'instruct')", policy.ID); err != nil {
		t.Fatalf("valid capability: %v", err)
	}
	assertRejectedTx := func(statement string, args ...any) {
		if _, err := tx.Exec(ctx, statement, args...); err == nil {
			t.Fatalf("duplicate capability unexpectedly succeeded")
		}
	}
	assertRejectedTx("INSERT INTO workload_policy_capabilities (workload_id, capability) VALUES ($1, 'instruct')", policy.ID)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback valid constraint transaction: %v", err)
	}

	var keyCount int
	var keys []string
	var predicate string
	if err := adminStore.pool.QueryRow(ctx, `
		SELECT i.indnkeyatts,
		       array_agg(a.attname ORDER BY k.ord),
		       pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class table_class ON table_class.oid = i.indrelid
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE c.relname = 'tasks_active_client_workload_idx'
		  AND table_class.relname = 'tasks'
		GROUP BY i.indnkeyatts, i.indpred, i.indrelid`,
	).Scan(&keyCount, &keys, &predicate); err != nil {
		t.Fatalf("read active index: %v", err)
	}
	if keyCount != 2 || len(keys) != 2 || keys[0] != "client_id" || keys[1] != "workload_id" {
		t.Fatalf("active index keys = %v/%d, want client_id, workload_id", keys, keyCount)
	}
	lowerPredicate := strings.ToLower(predicate)
	if !strings.Contains(lowerPredicate, "queued") || !strings.Contains(lowerPredicate, "running") {
		t.Fatalf("active index predicate = %q, want queued and running", predicate)
	}
}

func TestConfiguredTopologyFence(t *testing.T) {
	ctx := context.Background()
	name := uniqueName("topology-fence")
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = 0x5a
	}
	applyPolicy(t, adminStore, WorkloadPolicy{
		Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
		AllowedGeneration: []byte(`{}`), RequiredFeatures: []string{},
		AllowedCapabilities: []string{"instruct"}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000, TopologyDigest: digest,
	})
	var expected [32]byte
	copy(expected[:], digest)
	if err := adminStore.CheckConfiguredTopology(ctx, []ConfiguredWorkloadTopology{{Slug: name, Digest: expected}}); err != nil {
		t.Fatalf("matching topology rejected: %v", err)
	}
	wrong := expected
	wrong[0]++
	if err := adminStore.CheckConfiguredTopology(ctx, []ConfiguredWorkloadTopology{{Slug: name, Digest: wrong}}); err == nil {
		t.Fatal("mismatched topology accepted")
	}
	if err := adminStore.CheckConfiguredTopology(ctx, []ConfiguredWorkloadTopology{{Slug: uniqueName("missing-topology"), Digest: expected}}); err == nil {
		t.Fatal("missing configured workload accepted")
	}
	unmanagedName := uniqueName("unmanaged-topology")
	applyPolicy(t, adminStore, WorkloadPolicy{
		Name:                     unmanagedName,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              1,
		RetryDelaysSeconds:       []int{1},
		MaxOutputTokens:          128,
		AllowedGeneration:        []byte(`{}`),
		RequiredFeatures:         []string{},
		AllowedCapabilities:      []string{"instruct"},
		WorkloadTimeoutSeconds:   60,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
	})
	if err := adminStore.CheckConfiguredTopology(ctx, []ConfiguredWorkloadTopology{{Slug: name, Digest: expected}}); err != nil {
		t.Fatalf("unmanaged row blocked configured topology fence: %v", err)
	}
	unmanaged, err := adminStore.ListUnmanagedWorkloads(ctx, []string{name})
	if err != nil {
		t.Fatalf("list unmanaged workloads: %v", err)
	}
	seen := false
	for _, candidate := range unmanaged {
		if candidate == unmanagedName {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatalf("unmanaged workloads = %v, want %q", unmanaged, unmanagedName)
	}
}

func TestH00MigrationDownFailsWithoutChangingVersion(t *testing.T) {
	ctx := context.Background()
	std := stdlib.OpenDBFromPool(adminStore.pool)
	defer std.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, std, migrations.FS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	defer provider.Close()

	before, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("read migration version before down: %v", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("read latest migration: %v", err)
	}
	if before != latest {
		t.Fatalf("database version = %d, want latest %d", before, latest)
	}
	if _, err := provider.DownTo(ctx, latest-1); err == nil {
		t.Fatal("forward-only migration down unexpectedly succeeded")
	}
	after, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("read migration version after rejected down: %v", err)
	}
	if after != before {
		t.Fatalf("migration version changed after rejected down: before=%d after=%d", before, after)
	}
	var topologyColumn int
	if err := adminStore.pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='workload_policies'
		  AND column_name='topology_digest'`).Scan(&topologyColumn); err != nil {
		t.Fatalf("check H00 schema after rejected down: %v", err)
	}
	if topologyColumn != 1 {
		t.Fatal("H00 schema changed after rejected down")
	}
}
