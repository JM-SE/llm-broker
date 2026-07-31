package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// retentionWorkload returns a policy whose retention windows can be exercised
// with backdated timestamps without waiting.
func retentionWorkload(t *testing.T) (Client, WorkloadPolicy) {
	t.Helper()
	client := mustCreateClient(t, adminStore, "cleanup-client")
	policy, err := adminStore.UpsertWorkloadPolicy(context.Background(), WorkloadPolicy{
		Name:                     "cleanup-workload",
		PriorityClass:            "normal",
		MaxActiveTasks:           250,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1, 5},
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		PayloadRetentionSeconds:  3600,  // 1h
		ResultRetentionSeconds:   86400, // 24h
		MetadataRetentionSeconds: 2592000,
	})
	if err != nil {
		t.Fatalf("upsert retention policy: %v", err)
	}
	return client, policy
}

func finishedTask(t *testing.T, client Client, policy WorkloadPolicy, key string) Task {
	t.Helper()
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, key, testHash(1)).TaskID
	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	if err := adminStore.TransitionTask(ctx, id, domain.TaskRunning, domain.TaskSucceeded); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	// The worker phase (P07) owns the store method that persists results; the
	// fixture inserts a raw result row so cleanup can exercise it.
	if _, err := adminStore.pool.Exec(ctx, `
		INSERT INTO task_results (task_id, content, finish_reason,
		                          usage_prompt_tokens, usage_completion_tokens, usage_total_tokens)
		VALUES ($1, 'ok', 'stop', 10, 20, 30)`, id); err != nil {
		t.Fatalf("insert fixture result: %v", err)
	}
	task, err := adminStore.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return task
}

func backdate(t *testing.T, taskID uuid.UUID, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	tag, err := adminStore.pool.Exec(ctx,
		"UPDATE tasks SET finished_at = now() - ($2 * interval '1 second') WHERE id = $1",
		taskID, int64(age.Seconds()))
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate affected %d rows", tag.RowsAffected())
	}
}

func TestCleanupPurgesPayloadThenResultThenTask(t *testing.T) {
	client, policy := retentionWorkload(t)
	ctx := context.Background()
	task := finishedTask(t, client, policy, "cleanup-key-0001")

	// 2h after finish: payload (1h retention) is gone, task and result remain.
	backdate(t, task.ID, 2*time.Hour)
	counts, err := adminStore.CleanupExpired(ctx, 100)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if counts.Payloads != 1 || counts.Results != 0 || counts.Tasks != 0 {
		t.Errorf("first cleanup = %+v, want payloads=1", counts)
	}
	if _, err := adminStore.GetTaskWithPayload(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("payload survived: %v", err)
	}
	if _, err := adminStore.GetTask(ctx, task.ID); err != nil {
		t.Errorf("task should survive payload purge: %v", err)
	}

	// 2 days after finish: result (24h) is gone too.
	backdate(t, task.ID, 48*time.Hour)
	counts, err = adminStore.CleanupExpired(ctx, 100)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if counts.Payloads != 0 || counts.Results != 1 || counts.Tasks != 0 {
		t.Errorf("second cleanup = %+v, want results=1", counts)
	}
	var hasResult int
	if err := adminStore.pool.QueryRow(ctx,
		"SELECT count(*) FROM task_results WHERE task_id = $1", task.ID).Scan(&hasResult); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if hasResult != 0 {
		t.Error("result survived its retention window")
	}

	// 40 days after finish: metadata (30d) is gone, the whole task disappears.
	backdate(t, task.ID, 40*24*time.Hour)
	counts, err = adminStore.CleanupExpired(ctx, 100)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if counts.Tasks != 1 {
		t.Errorf("third cleanup = %+v, want tasks=1", counts)
	}
	if _, err := adminStore.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("task survived metadata purge: %v", err)
	}
}

func TestCleanupIsBounded(t *testing.T) {
	client, policy := retentionWorkload(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		task := finishedTask(t, client, policy, "bounded-key-000"+string(rune('0'+i)))
		backdate(t, task.ID, 2*time.Hour)
	}
	counts, err := adminStore.CleanupExpired(ctx, 1)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if counts.Payloads != 1 {
		t.Errorf("batch=1 removed %d payloads, want 1", counts.Payloads)
	}
}

func TestCleanupPreservesIdempotency(t *testing.T) {
	client, policy := retentionWorkload(t)
	ctx := context.Background()
	task := finishedTask(t, client, policy, "idem-key-00001")
	backdate(t, task.ID, 2*time.Hour)
	if _, err := adminStore.CleanupExpired(ctx, 100); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// The same key + hash must still replay to the original task ID after the
	// payload is gone: the hash survives on the task row.
	res := mustCreateTask(t, adminStore, client.ID, policy.ID, "idem-key-00001", testHash(1))
	if res.Created {
		t.Fatal("replay after payload purge created a new task")
	}
	if res.TaskID != task.ID {
		t.Errorf("replay returned %s, want original %s", res.TaskID, task.ID)
	}
}
