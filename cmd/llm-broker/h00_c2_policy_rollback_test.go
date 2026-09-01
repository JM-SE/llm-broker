package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/JM-SE/llm-broker/internal/testsupport"
)

type c2PolicySnapshot struct {
	Parents  string
	Children string
}

// TestH00C2CLIPolicyApplyRollsBackAllPolicies exercises the CLI boundary with
// two lexically ordered policies. The first policy is updated before the
// second policy is created; a disposable rejecting trigger fails the second
// policy's child write. The trigger is a test-only fault injector and is
// removed by cleanup.
func TestH00C2CLIPolicyApplyRollsBackAllPolicies(t *testing.T) {
	requireH00CLIDatabase(t)
	ctx := context.Background()

	initialPath := writeTempConfig(t, c2RollbackConfig(false, 10))
	t.Setenv(configEnvVar, initialPath)
	code, stdout, stderr := runH00CLI(t, "policy", "apply")
	if code != exitOk || stdout != "apply: create=1 update=0 unchanged=0 unmanaged=0 topology-mismatch=0\n" || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("seed C2 policy apply = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	adminDB, err := testsupport.OpenDatabase(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open C2 rollback snapshot database: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	installC2RejectingCapabilityTrigger(t, adminDB)

	before := snapshotC2Policies(t, ctx, adminDB)
	updatedPath := writeTempConfig(t, c2RollbackConfig(true, 11))
	t.Setenv(configEnvVar, updatedPath)
	code, stdout, stderr = runH00CLI(t, "policy", "apply")
	if code != 1 {
		t.Fatalf("rejecting C2 policy apply exit = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("rejecting C2 policy apply stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "h00 C2 rejecting capability child write") {
		t.Fatalf("rejecting C2 policy apply stderr = %q, want rejecting child error", stderr)
	}

	after := snapshotC2Policies(t, ctx, adminDB)
	if after != before {
		t.Fatalf("policy snapshot changed after failed multi-policy apply:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func c2RollbackConfig(includeSecond bool, firstMaxActive int) string {
	second := ""
	if includeSecond {
		second = `,
    "b-c2-policy": {
      "priority_class": "normal",
      "max_active_tasks": 10,
      "max_attempts": 3,
      "retry_delays_seconds": [1, 5],
      "max_output_tokens": 2048,
      "allowed_generation": {},
      "required_features": [],
      "allowed_capabilities": ["reject-cap"],
      "workload_timeout_seconds": 60
    }`
	}
	return fmt.Sprintf(`{
  "version": 2,
  "api": {"listen": "127.0.0.1:8010"},
  "database": {
    "url_env": "TEST_DATABASE_RUNTIME_URL",
    "provisioner_url_env": "TEST_DATABASE_PROVISIONER_URL",
    "migration_url_env": "TEST_DATABASE_URL",
    "max_connections": 4,
    "connect_timeout_seconds": 5
  },
  "retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
  "backends": {"fake": {"type": "fake"}},
  "capabilities": {
    "instruct": {"backend": "fake"},
    "reject-cap": {"backend": "fake"}
  },
  "workloads": {
    "a-c2-policy": {
      "priority_class": "normal",
      "max_active_tasks": %d,
      "max_attempts": 3,
      "retry_delays_seconds": [1, 5],
      "max_output_tokens": 2048,
      "allowed_generation": {},
      "required_features": [],
      "allowed_capabilities": ["instruct"],
      "workload_timeout_seconds": 60
    }%s
  }
}`, firstMaxActive, second)
}

func installC2RejectingCapabilityTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		CREATE OR REPLACE FUNCTION h00_c2_reject_capability_child()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $fn$
		BEGIN
			IF NEW.capability = 'reject-cap' THEN
				RAISE EXCEPTION 'h00 C2 rejecting capability child write';
			END IF;
			RETURN NEW;
		END
		$fn$;
		CREATE TRIGGER h00_c2_reject_capability_child_trigger
		BEFORE INSERT OR UPDATE ON workload_policy_capabilities
		FOR EACH ROW EXECUTE FUNCTION h00_c2_reject_capability_child()`)
	if err != nil {
		t.Fatalf("install C2 rejecting trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `
			DROP TRIGGER IF EXISTS h00_c2_reject_capability_child_trigger
			ON workload_policy_capabilities`)
		_, _ = db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS h00_c2_reject_capability_child()`)
	})
}

func snapshotC2Policies(t *testing.T, ctx context.Context, db *sql.DB) c2PolicySnapshot {
	t.Helper()
	var snapshot c2PolicySnapshot
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE((jsonb_agg(to_jsonb(p) ORDER BY p.id))::text, '[]')
		FROM (
			SELECT id, name, priority_class, max_active_tasks, max_attempts,
				retry_delays_seconds, max_output_tokens, allowed_generation,
				required_features, workload_timeout_seconds,
				payload_retention_seconds, result_retention_seconds,
				metadata_retention_seconds, encode(topology_digest, 'hex') AS topology_digest,
				to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF') AS created_at,
				to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF') AS updated_at
			FROM workload_policies
		) p`).Scan(&snapshot.Parents); err != nil {
		t.Fatalf("snapshot policy parents: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE((jsonb_agg(to_jsonb(c) ORDER BY c.workload_id, c.capability))::text, '[]')
		FROM (
			SELECT workload_id, capability
			FROM workload_policy_capabilities
		) c`).Scan(&snapshot.Children); err != nil {
		t.Fatalf("snapshot policy capability/feature children: %v", err)
	}
	return snapshot
}
