package h00migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// resetStagedToVersion makes each C2 case independent. The database is created
// by TestMain solely for this package and is disposable, so resetting public is
// preferable to carrying state between catalog, direct-SQL, and failure cases.
func resetStagedToVersion(t *testing.T, ctx context.Context, version int64) {
	t.Helper()
	if _, err := stagedAdminDB.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("reset staged schema: %v", err)
	}
	if version > 0 {
		if err := migrateTo(ctx, stagedAdminDB, version); err != nil {
			t.Fatalf("migrate staged database to %d: %v", version, err)
		}
	}
}

// assertH00Catalog is shared by the fresh and v3->H00 paths. It deliberately
// checks PostgreSQL's catalog representation, not the spelling in a migration
// file or cosmetic SQL formatting.
func assertH00Catalog(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	var nullable string
	var defaultValue sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'workload_policies'
		  AND column_name = 'workload_timeout_seconds'`).Scan(&nullable, &defaultValue); err != nil {
		t.Fatalf("read timeout catalog: %v", err)
	}
	if nullable != "NO" || defaultValue.Valid {
		t.Fatalf("timeout column nullable/default = %q/%v, want NO/NULL", nullable, defaultValue)
	}

	var oldCheckCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = 'public.workload_policies'::regclass
		  AND conname = 'workload_policies_workload_timeout_seconds_check'`).Scan(&oldCheckCount); err != nil {
		t.Fatalf("read old timeout check: %v", err)
	}
	if oldCheckCount != 0 {
		t.Fatalf("old timeout check count = %d, want 0", oldCheckCount)
	}
	var timeoutCheckCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.conrelid = 'public.workload_policies'::regclass
		  AND c.contype = 'c'
		  AND a.attname = 'workload_timeout_seconds'`).Scan(&timeoutCheckCount); err != nil {
		t.Fatalf("count timeout checks: %v", err)
	}
	if timeoutCheckCount != 1 {
		t.Fatalf("timeout check count = %d, want exactly one H00 range check", timeoutCheckCount)
	}

	var timeoutDefinition string
	if err := db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(c.oid, true)
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.conrelid = 'public.workload_policies'::regclass
		  AND c.conname = 'workload_policies_timeout_range'
		  AND c.contype = 'c'
		  AND c.conkey = ARRAY[a.attnum]`).Scan(&timeoutDefinition); err != nil {
		t.Fatalf("read timeout range check: %v", err)
	}
	if got := compactCatalogExpression(timeoutDefinition); got != "checkworkload_timeout_seconds>=1andworkload_timeout_seconds<=3600" {
		t.Fatalf("timeout check catalog expression = %q, want exact 1..3600 semantics", timeoutDefinition)
	}

	var attemptDefinition string
	if err := db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(c.oid, true)
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.conrelid = 'public.task_attempts'::regclass
		  AND c.conname = 'task_attempts_state_check'
		  AND c.contype = 'c'
		  AND c.conkey = ARRAY[a.attnum]`).Scan(&attemptDefinition); err != nil {
		t.Fatalf("read attempt state check: %v", err)
	}
	lowerAttempt := strings.ToLower(attemptDefinition)
	for _, state := range []string{"claimed", "authorized", "succeeded", "failed"} {
		if !strings.Contains(lowerAttempt, "'"+state+"'") {
			t.Fatalf("attempt state check = %q, missing %q", attemptDefinition, state)
		}
	}
	if strings.Contains(lowerAttempt, "preflight_passed") {
		t.Fatalf("attempt state check = %q, contains rejected alias", attemptDefinition)
	}

	var keyCount int
	var keys string
	var predicate string
	if err := db.QueryRowContext(ctx, `
		SELECT i.indnkeyatts,
		       array_to_string(array_agg(a.attname ORDER BY k.ord), ','),
		       pg_get_expr(i.indpred, i.indrelid, true)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class table_class ON table_class.oid = i.indrelid
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE c.relname = 'tasks_active_client_workload_idx'
		  AND table_class.relname = 'tasks'
		GROUP BY i.indnkeyatts, i.indpred, i.indrelid`).Scan(&keyCount, &keys, &predicate); err != nil {
		t.Fatalf("read active index catalog: %v", err)
	}
	if keyCount != 2 || keys != "client_id,workload_id" {
		t.Fatalf("active index keys = %q/%d, want client_id,workload_id/2", keys, keyCount)
	}
	assertActivePredicate(t, predicate)
	assertBoundedCountUsesActiveIndex(t, ctx, db)
}

func compactCatalogExpression(expression string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '(' || r == ')' || r == ' ', r == '\t' || r == '\n' || r == '\r':
			return -1
		default:
			return r
		}
	}, strings.ToLower(expression))
}

func assertActivePredicate(t *testing.T, predicate string) {
	t.Helper()
	compact := compactCatalogExpression(predicate)
	// pg_get_expr renders the partial-index predicate as state = ANY(array).
	// Removing only whitespace/redundant parentheses leaves a stable catalog
	// form while retaining the operator and array structure.
	compact = strings.ReplaceAll(compact, "::text[]", "")
	stateLiteral := regexp.MustCompile(`'([a-z_]+)'(?:::text)?`)
	literals := stateLiteral.FindAllStringSubmatch(compact, -1)
	if len(literals) != 2 || literals[0][1] != "queued" || literals[1][1] != "running" {
		t.Fatalf("active index predicate = %q, want queued/running only", predicate)
	}
	withoutLiterals := stateLiteral.ReplaceAllString(compact, "?")
	if withoutLiterals != "state=anyarray[?,?]" {
		t.Fatalf("active index predicate structure = %q, want state=ANY(array[queued,running])", predicate)
	}
}

func assertBoundedCountUsesActiveIndex(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var version string
	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
		t.Fatalf("read PostgreSQL version: %v", err)
	}
	versionNumber, err := strconv.Atoi(version)
	if err != nil || versionNumber < 170000 || versionNumber >= 180000 {
		t.Fatalf("PostgreSQL version = %q, want major version 17 for bounded-count proof", version)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin bounded-count plan transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // SET LOCAL is intentionally scoped to this plan.
	if _, err := tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scan for bounded-count proof: %v", err)
	}
	var rawPlan []byte
	if err := tx.QueryRowContext(ctx, `
		EXPLAIN (FORMAT JSON)
		SELECT count(*)
		FROM tasks
		WHERE client_id = 1 AND workload_id = 1
		  AND state IN ('queued', 'running')`).Scan(&rawPlan); err != nil {
		t.Fatalf("explain bounded active-task count: %v", err)
	}

	var plans []map[string]any
	if err := json.Unmarshal(rawPlan, &plans); err != nil {
		t.Fatalf("decode bounded-count plan JSON: %v", err)
	}
	hasActiveIndex, hasSeqScan := planUsesIndex(plans), planUsesNode(plans, "Seq Scan")
	if !hasActiveIndex || hasSeqScan {
		t.Fatalf("bounded-count plan uses active index=%t seq_scan=%t: %s", hasActiveIndex, hasSeqScan, rawPlan)
	}
}

func planUsesIndex(value any) bool {
	switch node := value.(type) {
	case []map[string]any:
		for _, item := range node {
			if planUsesIndex(item) {
				return true
			}
		}
	case map[string]any:
		if name, ok := node["Index Name"].(string); ok && name == "tasks_active_client_workload_idx" {
			return true
		}
		if children, ok := node["Plans"].([]any); ok && planUsesIndex(children) {
			return true
		}
		for _, child := range node {
			if planUsesIndex(child) {
				return true
			}
		}
	case []any:
		for _, item := range node {
			if planUsesIndex(item) {
				return true
			}
		}
	}
	return false
}

func planUsesNode(value any, want string) bool {
	switch node := value.(type) {
	case map[string]any:
		if nodeType, ok := node["Node Type"].(string); ok && nodeType == want {
			return true
		}
		for _, child := range node {
			if planUsesNode(child, want) {
				return true
			}
		}
	case []any:
		for _, item := range node {
			if planUsesNode(item, want) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range node {
			if planUsesNode(item, want) {
				return true
			}
		}
	}
	return false
}

func TestH00FreshCatalogAndDirectSQLBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetStagedToVersion(t, ctx, 0)
	if err := migrateTo(ctx, stagedAdminDB, 4); err != nil {
		t.Fatalf("fresh migrate to H00: %v", err)
	}
	assertH00Catalog(t, ctx, stagedAdminDB)
	assertDirectSQLBoundaries(t, ctx, stagedAdminDB)
}

func assertDirectSQLBoundaries(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	baseName := fmt.Sprintf("h00-c2-direct-%d", time.Now().UnixNano())

	if _, err := db.ExecContext(ctx, `
		INSERT INTO workload_policies (
			name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, payload_retention_seconds,
			result_retention_seconds, metadata_retention_seconds, topology_digest)
		VALUES ($1, 'normal', 10, 3, ARRAY[1,5], 2048, '{}'::jsonb,
			ARRAY[]::text[], 3600, 86400, 2592000, NULL)`, baseName+"-omitted"); err == nil {
		t.Fatal("timeout omission unexpectedly succeeded")
	}

	for _, timeout := range []int{0, 3601} {
		if _, err := insertDirectPolicy(ctx, db, fmt.Sprintf("%s-timeout-%d", baseName, timeout), timeout, nil); err == nil {
			t.Fatalf("timeout %d unexpectedly succeeded", timeout)
		}
	}
	for _, timeout := range []int{1, 3600} {
		id, err := insertDirectPolicy(ctx, db, fmt.Sprintf("%s-timeout-%d", baseName, timeout), timeout, nil)
		if err != nil {
			t.Fatalf("timeout %d boundary rejected: %v", timeout, err)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM workload_policies WHERE id=$1", id); err != nil {
			t.Fatalf("remove timeout %d boundary row: %v", timeout, err)
		}
	}

	nullID, err := insertDirectPolicy(ctx, db, baseName+"-digest-null", 300, nil)
	if err != nil {
		t.Fatalf("NULL topology digest rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM workload_policies WHERE id=$1", nullID); err != nil {
		t.Fatalf("remove NULL digest row: %v", err)
	}
	fullID, err := insertDirectPolicy(ctx, db, baseName+"-digest-full", 300, bytesOf(32))
	if err != nil {
		t.Fatalf("32-byte topology digest rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM workload_policies WHERE id=$1", fullID); err != nil {
		t.Fatalf("remove 32-byte digest row: %v", err)
	}
	for _, length := range []int{31, 33} {
		if _, err := insertDirectPolicy(ctx, db, fmt.Sprintf("%s-digest-%d", baseName, length), 300, bytesOf(length)); err == nil {
			t.Fatalf("topology digest length %d unexpectedly succeeded", length)
		}
	}

	clientName := baseName + "-attempt-client"
	var clientID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO clients (name, credential_identifier, credential_digest)
		VALUES ($1, $2, $3) RETURNING id`, clientName, "h00c2attm001", bytesOf(32)).Scan(&clientID); err != nil {
		t.Fatalf("insert attempt direct-SQL client: %v", err)
	}
	policyID, err := insertDirectPolicy(ctx, db, baseName+"-attempt-policy", 300, nil)
	if err != nil {
		t.Fatalf("insert attempt direct-SQL policy: %v", err)
	}
	var taskID uuid.UUID
	if err := db.QueryRowContext(ctx, `
		INSERT INTO tasks (client_id, workload_id, capability, idempotency_key,
			request_hash, state, attempts)
		VALUES ($1, $2, 'instruct', $3, $4, 'queued', 0)
		RETURNING id`, clientID, policyID, baseName+"-attempt-key", bytesOf(32)).Scan(&taskID); err != nil {
		t.Fatalf("insert attempt direct-SQL task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM clients WHERE id=$1", clientID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM workload_policies WHERE id=$1", policyID)
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO task_attempts (task_id, attempt, worker_id, state)
		VALUES ($1, 1, $2, 'preflight_passed')`, taskID, uuid.New()); err == nil {
		t.Fatal("attempt alias preflight_passed unexpectedly succeeded")
	}
}

func insertDirectPolicy(ctx context.Context, db *sql.DB, name string, timeout int, digest any) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO workload_policies (
			name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, workload_timeout_seconds,
			payload_retention_seconds, result_retention_seconds,
			metadata_retention_seconds, topology_digest)
		VALUES ($1, 'normal', 10, 3, ARRAY[1,5], 2048, '{}'::jsonb,
			ARRAY[]::text[], $2, 3600, 86400, 2592000, $3)
		RETURNING id`, name, timeout, digest).Scan(&id)
	return id, err
}

func bytesOf(length int) []byte {
	value := make([]byte, length)
	for i := range value {
		value[i] = byte(i + 1)
	}
	return value
}

func TestH00StagedV3OutOfRangeTimeoutBlocksH00Migration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetStagedToVersion(t, ctx, 3)
	var policyID int64
	if err := stagedAdminDB.QueryRowContext(ctx, `
		INSERT INTO workload_policies (
			name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, workload_timeout_seconds,
			payload_retention_seconds, result_retention_seconds,
			metadata_retention_seconds)
		VALUES ('h00-c2-v3-out-of-range', 'normal', 10, 3, ARRAY[1,5], 2048,
			'{}'::jsonb, ARRAY[]::text[], 3601, 3600, 86400, 2592000)
		RETURNING id`).Scan(&policyID); err != nil {
		t.Fatalf("seed v3 timeout=3601 fixture: %v", err)
	}

	if err := migrateTo(ctx, stagedAdminDB, 4); err == nil {
		t.Fatal("H00 migration accepted staged timeout=3601 fixture")
	}
	if got := stagedMigrationVersion(t, ctx); got != 3 {
		t.Fatalf("failed H00 migration version = %d, want 3", got)
	}
	var timeout int
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT workload_timeout_seconds FROM workload_policies WHERE id=$1", policyID).Scan(&timeout); err != nil {
		t.Fatalf("read v3 timeout after rejected migration: %v", err)
	}
	if timeout != 3601 {
		t.Fatalf("v3 timeout after rejected migration = %d, want 3601", timeout)
	}

	var topologyColumn, capabilityTable, activeIndex, oldCheck int
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='workload_policies'
		  AND column_name='topology_digest'`).Scan(&topologyColumn); err != nil {
		t.Fatalf("check topology column after rejected migration: %v", err)
	}
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='workload_policy_capabilities'`).Scan(&capabilityTable); err != nil {
		t.Fatalf("check capability table after rejected migration: %v", err)
	}
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_class WHERE relname='tasks_active_client_workload_idx'`).Scan(&activeIndex); err != nil {
		t.Fatalf("check active index after rejected migration: %v", err)
	}
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conrelid='public.workload_policies'::regclass
		  AND conname='workload_policies_workload_timeout_seconds_check'`).Scan(&oldCheck); err != nil {
		t.Fatalf("check v3 timeout constraint after rejected migration: %v", err)
	}
	if topologyColumn != 0 || capabilityTable != 0 || activeIndex != 0 || oldCheck != 1 {
		t.Fatalf("partial H00 schema after failed migration: topology=%d capabilities=%d index=%d old_timeout_check=%d; want 0/0/0/1",
			topologyColumn, capabilityTable, activeIndex, oldCheck)
	}

	var defaultValue sql.NullString
	if err := stagedAdminDB.QueryRowContext(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema='public' AND table_name='workload_policies'
		  AND column_name='workload_timeout_seconds'`).Scan(&defaultValue); err != nil {
		t.Fatalf("read v3 timeout default after rejected migration: %v", err)
	}
	if !defaultValue.Valid || !strings.Contains(defaultValue.String, "300") {
		t.Fatalf("v3 timeout default after rejected migration = %v, want 300", defaultValue)
	}
}

func stagedMigrationVersion(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var version int64
	if err := stagedAdminDB.QueryRowContext(ctx,
		"SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	return version
}
