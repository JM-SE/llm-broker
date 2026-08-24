package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/google/uuid"
)

// h00C1Snapshot is deliberately a database projection rather than a copy of
// production structs. It includes every durable table in the task and
// authorization graph, including child rows and timestamps. The JSON
// projections are ordered in SQL so equality is an exact row snapshot.
type h00C1Snapshot struct {
	Clients      string
	Policies     string
	Capabilities string
	Grants       string
	Tasks        string
	Payloads     string
	Attempts     string
	Results      string
}

func h00C1CanonicalJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("canonicalize H00 C1 JSON: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal canonical H00 C1 JSON: %v", err)
	}
	return string(encoded)
}

func h00C1SnapshotFor(t *testing.T, clientID, workloadID int64) h00C1Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	return h00C1Snapshot{
		Clients: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.id), '[]'::jsonb)::text
			FROM (
				SELECT id, name, credential_identifier,
				       encode(credential_digest, 'hex') AS credential_digest,
				       enabled, created_at, updated_at
				FROM clients WHERE id = $1
			) q`, clientID),
		Policies: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.id), '[]'::jsonb)::text
			FROM (
				SELECT id, name, priority_class, max_active_tasks, max_attempts,
				       retry_delays_seconds, max_output_tokens, allowed_generation,
				       required_features, workload_timeout_seconds,
				       payload_retention_seconds, result_retention_seconds,
				       metadata_retention_seconds, encode(topology_digest, 'hex') AS topology_digest,
				       created_at, updated_at
				FROM workload_policies WHERE id = $1
			) q`, workloadID),
		Capabilities: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.capability), '[]'::jsonb)::text
			FROM (
				SELECT workload_id, capability
				FROM workload_policy_capabilities WHERE workload_id = $1
			) q`, workloadID),
		Grants: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.client_id, q.workload_id), '[]'::jsonb)::text
			FROM (
				SELECT client_id, workload_id, granted_at
				FROM client_workload_grants
				WHERE client_id = $1 AND workload_id = $2
			) q`, clientID, workloadID),
		Tasks: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.id), '[]'::jsonb)::text
			FROM (
				SELECT id, client_id, workload_id, capability, idempotency_key,
				       encode(request_hash, 'hex') AS request_hash, state, attempts,
				       lease_until, started_at, finished_at, cancel_requested_at,
				       retry_available_at, created_at, updated_at
				FROM tasks WHERE client_id = $1 AND workload_id = $2
			) q`, clientID, workloadID),
		Payloads: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.task_id), '[]'::jsonb)::text
			FROM (
				SELECT p.task_id, p.payload, p.created_at
				FROM task_payloads p
				JOIN tasks t ON t.id = p.task_id
				WHERE t.client_id = $1 AND t.workload_id = $2
			) q`, clientID, workloadID),
		Attempts: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.task_id, q.attempt), '[]'::jsonb)::text
			FROM (
				SELECT a.task_id, a.attempt, a.worker_id, a.state,
				       a.error_class, a.error_code, a.started_at, a.finished_at
				FROM task_attempts a
				JOIN tasks t ON t.id = a.task_id
				WHERE t.client_id = $1 AND t.workload_id = $2
			) q`, clientID, workloadID),
		Results: h00C1JSONProjection(t, ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.task_id), '[]'::jsonb)::text
			FROM (
				SELECT r.task_id, r.content, r.finish_reason,
				       r.usage_prompt_tokens, r.usage_completion_tokens,
				       r.usage_total_tokens, r.created_at
				FROM task_results r
				JOIN tasks t ON t.id = r.task_id
				WHERE t.client_id = $1 AND t.workload_id = $2
			) q`, clientID, workloadID),
	}
}

func h00C1JSONProjection(t *testing.T, ctx context.Context, query string, args ...any) string {
	t.Helper()
	var value string
	if err := adminStore.pool.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		t.Fatalf("H00 C1 snapshot query: %v", err)
	}
	return value
}

func h00C1ChangedTables(before, after h00C1Snapshot) []string {
	changed := make([]string, 0, 8)
	if before.Clients != after.Clients {
		changed = append(changed, "clients")
	}
	if before.Policies != after.Policies {
		changed = append(changed, "policies")
	}
	if before.Capabilities != after.Capabilities {
		changed = append(changed, "capabilities")
	}
	if before.Grants != after.Grants {
		changed = append(changed, "grants")
	}
	if before.Tasks != after.Tasks {
		changed = append(changed, "tasks")
	}
	if before.Payloads != after.Payloads {
		changed = append(changed, "payloads")
	}
	if before.Attempts != after.Attempts {
		changed = append(changed, "attempts")
	}
	if before.Results != after.Results {
		changed = append(changed, "results")
	}
	return changed
}

func h00C1AssertChangedTables(t *testing.T, before, after h00C1Snapshot, want ...string) {
	t.Helper()
	got := h00C1ChangedTables(before, after)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("H00 C1 changed durable tables = %v, want exactly %v", got, want)
	}
}

type h00C1TaskRow struct {
	ID         uuid.UUID
	State      string
	Attempts   int
	Capability string
	Key        string
	Hash       string
	Payload    string
}

func h00C1TaskRows(t *testing.T, clientID, workloadID int64) []h00C1TaskRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := adminStore.pool.Query(ctx, `
		SELECT t.id, t.state, t.attempts, t.capability, t.idempotency_key,
		       encode(t.request_hash, 'hex'), p.payload::text
		FROM tasks t
		JOIN task_payloads p ON p.task_id = t.id
		WHERE t.client_id = $1 AND t.workload_id = $2
		ORDER BY t.created_at, t.id`, clientID, workloadID)
	if err != nil {
		t.Fatalf("H00 C1 task rows: %v", err)
	}
	defer rows.Close()
	var out []h00C1TaskRow
	for rows.Next() {
		var row h00C1TaskRow
		if err := rows.Scan(&row.ID, &row.State, &row.Attempts, &row.Capability,
			&row.Key, &row.Hash, &row.Payload); err != nil {
			t.Fatalf("scan H00 C1 task row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate H00 C1 task rows: %v", err)
	}
	return out
}

func h00C1Count(t *testing.T, query string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var count int
	if err := adminStore.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("H00 C1 count query: %v", err)
	}
	return count
}

// h00C1AdvisoryState observes one exact PostgreSQL advisory key. PostgreSQL
// represents a bigint advisory key as classid/objid halves in pg_locks.
func h00C1AdvisoryState(t *testing.T, key int64) (granted, waiting int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	const query = `
		SELECT count(*) FILTER (WHERE granted), count(*) FILTER (WHERE NOT granted)
		FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1
		  AND classid::bigint * 4294967296 + objid::bigint = $1`
	if err := adminStore.pool.QueryRow(ctx, query, key).Scan(&granted, &waiting); err != nil {
		t.Fatalf("inspect H00 C1 advisory key %d: %v", key, err)
	}
	return granted, waiting
}

func waitForH00C1AdvisoryState(t *testing.T, key int64, wantGranted, wantWaiting int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		granted, waiting := h00C1AdvisoryState(t, key)
		if granted == wantGranted && waiting == wantWaiting {
			return
		}
		runtime.Gosched()
	}
	granted, waiting := h00C1AdvisoryState(t, key)
	t.Fatalf("H00 C1 advisory key %d state = granted=%d waiting=%d, want granted=%d waiting=%d",
		key, granted, waiting, wantGranted, wantWaiting)
}

// h00C1ForcedPolicyCollisionKey is the package-private PostgreSQL test seam
// used by the collision matrix. It deliberately obtains the exact production
// policy-domain key for a slug and holds it in a separate production
// transaction; no exported lock or mutation bypass is introduced.
func h00C1ForcedPolicyCollisionKey(t *testing.T, slug string) int64 {
	t.Helper()
	key, err := policy.PolicyLockKey(slug)
	if err != nil {
		t.Fatalf("H00 C1 collision key for %q: %v", slug, err)
	}
	return key
}
