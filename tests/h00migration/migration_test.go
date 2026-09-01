package h00migration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
	"github.com/JM-SE/llm-broker/migrations"
)

var (
	stagedAdminURL       string
	stagedProvisionerURL string
	stagedRuntimeURL     string
	stagedDatabaseName   string
	stagedAdmin          *postgres.Store
	stagedAdminDB        *sql.DB
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	provisionerURL := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || provisionerURL == "" || runtimeURL == "" {
		message := "H00 staged migration tests require TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL"
		if os.Getenv("LLM_BROKER_REQUIRE_TEST_DATABASE") == "1" {
			fmt.Fprintln(os.Stderr, message)
			os.Exit(1)
		}
		fmt.Println(message + "; skipped")
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	options := postgres.Options{MaxConns: 4, ConnectTimeout: 5 * time.Second}

	baseAdmin, err := postgres.Open(ctx, adminURL, options)
	if err != nil {
		setupFailure("admin connection", err)
	}
	stagedDatabaseName = fmt.Sprintf("llm_broker_h00_%s", strings.ReplaceAll(uuid.NewString(), "-", "")[:12])
	baseAdminDB, err := testsupport.OpenDatabase(adminURL)
	if err != nil {
		baseAdmin.Close()
		setupFailure("admin SQL bridge", err)
	}
	if _, err := baseAdminDB.ExecContext(ctx,
		fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{stagedDatabaseName}.Sanitize())); err != nil {
		_ = baseAdminDB.Close()
		baseAdmin.Close()
		setupFailure("create staged database", err)
	}
	_ = baseAdminDB.Close()
	baseAdmin.Close()

	stagedAdminURL, err = databaseURL(adminURL, stagedDatabaseName)
	if err != nil {
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("staged admin URL", err)
	}
	stagedProvisionerURL, err = databaseURL(provisionerURL, stagedDatabaseName)
	if err != nil {
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("staged provisioner URL", err)
	}
	stagedRuntimeURL, err = databaseURL(runtimeURL, stagedDatabaseName)
	if err != nil {
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("staged runtime URL", err)
	}

	stagedAdmin, err = postgres.Open(ctx, stagedAdminURL, options)
	if err != nil {
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("staged admin connection", err)
	}
	stagedAdminDB, err = testsupport.OpenDatabase(stagedAdminURL)
	if err != nil {
		stagedAdmin.Close()
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("staged admin SQL bridge", err)
	}
	if _, err := stagedAdminDB.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		_ = stagedAdminDB.Close()
		stagedAdmin.Close()
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("reset staged schema", err)
	}
	if err := migrateTo(ctx, stagedAdminDB, 3); err != nil {
		stagedAdmin.Close()
		cleanupDatabase(adminURL, stagedDatabaseName)
		setupFailure("migrate through 00003", err)
	}

	code := m.Run()
	_ = stagedAdminDB.Close()
	stagedAdmin.Close()
	if err := cleanupDatabase(adminURL, stagedDatabaseName); err != nil {
		fmt.Fprintf(os.Stderr, "H00 staged migration cleanup: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func setupFailure(step string, err error) {
	fmt.Fprintf(os.Stderr, "H00 staged migration setup failed (%s): %v\n", step, err)
	os.Exit(1)
}

func databaseURL(raw, databaseName string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = "/" + databaseName
	u.RawPath = ""
	return u.String(), nil
}

func cleanupDatabase(adminURL, databaseName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := testsupport.OpenDatabase(adminURL)
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()",
		databaseName); err != nil {
		return err
	}
	_, err = admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{databaseName}.Sanitize()))
	return err
}

func migrateTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	_, migrateErr := provider.UpTo(ctx, version)
	if migrateErr != nil {
		return migrateErr
	}
	return nil
}

type legacyFixture struct {
	clientID   int64
	workloadID int64
	taskID     uuid.UUID
}

func TestH00StagedMigrationPreservesLegacyAmbiguity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetStagedToVersion(t, ctx, 3)
	fixture := seedLegacyFixture(t, ctx)
	if err := migrateTo(ctx, stagedAdminDB, 4); err != nil {
		t.Fatalf("migrate 00004: %v", err)
	}

	var latestState string
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT state FROM task_attempts WHERE task_id=$1 AND attempt=2`, fixture.taskID).Scan(&latestState); err != nil {
		t.Fatalf("read migrated latest attempt: %v", err)
	}
	if latestState != "authorized" {
		t.Fatalf("legacy latest attempt = %q, want authorized", latestState)
	}
	var priorState string
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT state FROM task_attempts WHERE task_id=$1 AND attempt=1`, fixture.taskID).Scan(&priorState); err != nil {
		t.Fatalf("read prior attempt: %v", err)
	}
	if priorState != "failed" {
		t.Fatalf("prior attempt = %q, want failed", priorState)
	}

	var topologyIsNull bool
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT topology_digest IS NULL FROM workload_policies WHERE id=$1", fixture.workloadID).Scan(&topologyIsNull); err != nil {
		t.Fatalf("read legacy topology: %v", err)
	}
	if !topologyIsNull {
		t.Fatal("legacy workload topology_digest is not NULL")
	}
	var capabilityChildren int
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT count(*) FROM workload_policy_capabilities WHERE workload_id=$1", fixture.workloadID).Scan(&capabilityChildren); err != nil {
		t.Fatalf("count capability children: %v", err)
	}
	if capabilityChildren != 0 {
		t.Fatalf("legacy capability children = %d, want zero", capabilityChildren)
	}
	var grantCount int
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT count(*) FROM client_workload_grants WHERE client_id=$1 AND workload_id=$2",
		fixture.clientID, fixture.workloadID).Scan(&grantCount); err != nil {
		t.Fatalf("read legacy grant: %v", err)
	}
	if grantCount != 1 {
		t.Fatalf("legacy grant count = %d, want one", grantCount)
	}

	var defaultIsNull bool
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT column_default IS NULL
		FROM information_schema.columns
		WHERE table_schema='public' AND table_name='workload_policies'
		  AND column_name='workload_timeout_seconds'`).Scan(&defaultIsNull); err != nil {
		t.Fatalf("read timeout default: %v", err)
	}
	if !defaultIsNull {
		t.Fatal("workload timeout still has a default after 00004")
	}
	assertRejected(t, ctx,
		"UPDATE workload_policies SET workload_timeout_seconds=0 WHERE id=$1", fixture.workloadID)
	assertRejected(t, ctx,
		"UPDATE workload_policies SET workload_timeout_seconds=3601 WHERE id=$1", fixture.workloadID)
	if _, err := stagedAdminDB.ExecContext(ctx,
		"UPDATE workload_policies SET workload_timeout_seconds=1 WHERE id=$1", fixture.workloadID); err != nil {
		t.Fatalf("timeout lower bound rejected: %v", err)
	}
	if _, err := stagedAdminDB.ExecContext(ctx,
		"UPDATE workload_policies SET workload_timeout_seconds=3600 WHERE id=$1", fixture.workloadID); err != nil {
		t.Fatalf("timeout upper bound rejected: %v", err)
	}
	if _, err := stagedAdminDB.ExecContext(ctx,
		"UPDATE workload_policies SET workload_timeout_seconds=300 WHERE id=$1", fixture.workloadID); err != nil {
		t.Fatalf("restore timeout: %v", err)
	}

	assertH00Catalog(t, ctx, stagedAdminDB)
	runtime, err := postgres.Open(ctx, stagedRuntimeURL, postgres.Options{MaxConns: 2, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("staged runtime connection: %v", err)
	}
	defer runtime.Close()
	provisioner, err := postgres.Open(ctx, stagedProvisionerURL, postgres.Options{MaxConns: 2, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("staged provisioner connection: %v", err)
	}
	defer provisioner.Close()
	if err := provisioner.Ping(ctx); err != nil {
		t.Fatalf("staged provisioner ping: %v", err)
	}

	if _, err := stagedAdminDB.ExecContext(ctx,
		"UPDATE tasks SET lease_until=now()-interval '1 second' WHERE id=$1", fixture.taskID); err != nil {
		t.Fatalf("expire authorized task lease: %v", err)
	}
	items, err := runtime.ListUnresolvedRecovery(ctx)
	if err != nil {
		t.Fatalf("list unresolved recovery: %v", err)
	}
	var found *postgres.RecoveryItem
	for i := range items {
		if items[i].Task.ID == fixture.taskID {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expired authorized task missing from unresolved recovery")
	}
	if found.Phase != postgres.AttemptPhaseAuthorized || found.SafeToRequeue ||
		!found.LeaseExpired || found.ErrorCode != "authorized_expired" {
		t.Fatalf("expired authorized recovery = %+v, want ambiguous/non-requeueable", *found)
	}
	if ambiguous, err := runtime.HasUnresolvedIndeterminate(ctx); err != nil {
		t.Fatalf("check unresolved indeterminate: %v", err)
	} else if !ambiguous {
		t.Fatal("expired authorized task did not block as unresolved indeterminate")
	}

	var taskState string
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT state FROM tasks WHERE id=$1", fixture.taskID).Scan(&taskState); err != nil {
		t.Fatalf("read task after recovery listing: %v", err)
	}
	if taskState != "running" {
		t.Fatalf("expired authorized task state = %q, want running", taskState)
	}
}

func seedLegacyFixture(t *testing.T, ctx context.Context) legacyFixture {
	t.Helper()
	p := stagedAdminDB
	var fixture legacyFixture
	if err := p.QueryRowContext(ctx, `
		INSERT INTO clients (name, credential_identifier, credential_digest)
		VALUES ('h00-legacy-client', 'h00legacy001', decode(repeat('41', 32), 'hex'))
		RETURNING id`).Scan(&fixture.clientID); err != nil {
		t.Fatalf("seed legacy client: %v", err)
	}
	if err := p.QueryRowContext(ctx, `
		INSERT INTO workload_policies (
			name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, workload_timeout_seconds,
			payload_retention_seconds, result_retention_seconds,
			metadata_retention_seconds)
		VALUES ('h00-legacy-workload', 'normal', 10, 3, ARRAY[1,5], 2048,
			'{}'::jsonb, ARRAY[]::text[], 300, 3600, 86400, 2592000)
		RETURNING id`).Scan(&fixture.workloadID); err != nil {
		t.Fatalf("seed legacy policy: %v", err)
	}
	if _, err := p.ExecContext(ctx, `
		INSERT INTO client_workload_grants (client_id, workload_id)
		VALUES ($1, $2)`, fixture.clientID, fixture.workloadID); err != nil {
		t.Fatalf("seed legacy grant: %v", err)
	}
	if err := p.QueryRowContext(ctx, `
		INSERT INTO tasks (
			client_id, workload_id, capability, idempotency_key, request_hash,
			state, attempts, lease_until, started_at)
		VALUES ($1, $2, 'instruct', 'h00-legacy-task', decode(repeat('42', 32), 'hex'),
			'running', 2, now()-interval '1 minute', now()-interval '2 minutes')
		RETURNING id`, fixture.clientID, fixture.workloadID).Scan(&fixture.taskID); err != nil {
		t.Fatalf("seed legacy task: %v", err)
	}
	if _, err := p.ExecContext(ctx,
		"INSERT INTO task_payloads (task_id, payload) VALUES ($1, '{}'::jsonb)", fixture.taskID); err != nil {
		t.Fatalf("seed legacy payload: %v", err)
	}
	workerID := uuid.New()
	if _, err := p.ExecContext(ctx, `
		INSERT INTO task_attempts (task_id, attempt, worker_id, state, error_class, error_code, finished_at)
		VALUES ($1, 1, $2, 'failed', 'transient_unsent', 'legacy_safe_unsent', now()-interval '30 seconds')`,
		fixture.taskID, workerID); err != nil {
		t.Fatalf("seed legacy prior attempt: %v", err)
	}
	if _, err := p.ExecContext(ctx, `
		INSERT INTO task_attempts (task_id, attempt, worker_id, state, started_at)
		VALUES ($1, 2, $2, 'claimed', now()-interval '90 seconds')`,
		fixture.taskID, workerID); err != nil {
		t.Fatalf("seed legacy latest attempt: %v", err)
	}
	return fixture
}

func assertRejected(t *testing.T, ctx context.Context, statement string, args ...any) {
	t.Helper()
	if _, err := stagedAdminDB.ExecContext(ctx, statement, args...); err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
	}
}
