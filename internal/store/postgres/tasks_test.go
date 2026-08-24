package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/domain"
)

func setupTaskEnv(t *testing.T) (Client, WorkloadPolicy) {
	t.Helper()
	client := mustCreateClient(t, adminStore, "task-client")
	policy := mustUpsertPolicy(t, adminStore, "task-workload")
	return client, policy
}

func TestCreateTaskAndIdempotentReplay(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()

	first := mustCreateTask(t, adminStore, client.ID, policy.ID, "replay-key-0001", testHash(1))
	if !first.Created {
		t.Fatal("first creation must be Created")
	}
	second := mustCreateTask(t, adminStore, client.ID, policy.ID, "replay-key-0001", testHash(1))
	if second.Created {
		t.Fatal("replay must not create a second task")
	}
	if second.TaskID != first.TaskID {
		t.Errorf("replay returned different task id: %s vs %s", second.TaskID, first.TaskID)
	}

	tw, err := adminStore.GetTaskWithPayload(ctx, first.TaskID)
	if err != nil {
		t.Fatalf("get task with payload: %v", err)
	}
	if string(tw.Payload) != `{"messages": []}` {
		t.Errorf("payload mismatch: %s", tw.Payload)
	}
	if tw.State != domain.TaskQueued {
		t.Errorf("state = %s, want queued", tw.State)
	}
}

func TestCreateTaskConflictOnDifferentRequest(t *testing.T) {
	client, policy := setupTaskEnv(t)
	mustCreateTask(t, adminStore, client.ID, policy.ID, "conflict-key-001", testHash(1))
	_, err := adminStore.createTaskFixture(context.Background(), CreateTaskParams{
		ClientID:       client.ID,
		WorkloadID:     policy.ID,
		Capability:     "instruct",
		IdempotencyKey: "conflict-key-001",
		RequestHash:    testHash(2),
		Payload:        []byte(`{"messages":[]}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestCreateTaskConcurrentIdentical(t *testing.T) {
	client, policy := setupTaskEnv(t)
	const n = 16
	results := make([]CreateTaskResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = adminStore.createTaskFixture(context.Background(), CreateTaskParams{
				ClientID:       client.ID,
				WorkloadID:     policy.ID,
				Capability:     "instruct",
				IdempotencyKey: "concurrent-key-01",
				RequestHash:    testHash(7),
				Payload:        []byte(`{"messages":[]}`),
			})
		}(i)
	}
	wg.Wait()

	created, replayed := 0, 0
	id := results[0].TaskID
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i].Created {
			created++
		} else {
			replayed++
		}
		if results[i].TaskID != id {
			t.Errorf("goroutine %d got different task id %s", i, results[i].TaskID)
		}
	}
	if created != 1 || replayed != n-1 {
		t.Errorf("created = %d, replayed = %d, want 1 and %d", created, replayed, n-1)
	}
}

func TestCreateTaskConcurrentConflicts(t *testing.T) {
	client, policy := setupTaskEnv(t)
	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, replayed, conflicts := 0, 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Even callers reuse the original request (hash 9), odd callers a
			// different request (hash 1), all with the same key.
			var hash []byte
			if i%2 == 0 {
				hash = testHash(9)
			} else {
				hash = testHash(1)
			}
			res, err := adminStore.createTaskFixture(context.Background(), CreateTaskParams{
				ClientID:       client.ID,
				WorkloadID:     policy.ID,
				Capability:     "instruct",
				IdempotencyKey: "concurrent-conflict-01",
				RequestHash:    hash,
				Payload:        []byte(`{"messages":[]}`),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && res.Created:
				created++
			case err == nil && !res.Created:
				replayed++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("goroutine %d: unexpected result %v %v", i, res, err)
			}
		}(i)
	}
	wg.Wait()
	// Exactly one insert wins the race. The four same-request callers replay,
	// the five different-request callers conflict.
	if created != 1 {
		t.Errorf("created = %d, want exactly 1", created)
	}
	if replayed != 4 || conflicts != 5 {
		t.Errorf("replayed = %d, conflicts = %d, want 4 and 5", replayed, conflicts)
	}
}

func TestStateTransitions(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, "transition-key-01", testHash(3)).TaskID

	// queued -> running sets started_at and lease.
	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	task, err := adminStore.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.StartedAt == nil || task.FinishedAt != nil || task.LeaseUntil == nil {
		t.Errorf("running task timestamps wrong: started=%v finished=%v lease=%v",
			task.StartedAt, task.FinishedAt, task.LeaseUntil)
	}
	if task.LeaseUntil.Before(time.Now()) {
		t.Error("lease_until is already in the past")
	}

	// running -> succeeded sets finished_at and clears the lease.
	if err := adminStore.TransitionTask(ctx, id, domain.TaskRunning, domain.TaskSucceeded); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	task, err = adminStore.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.FinishedAt == nil || task.LeaseUntil != nil {
		t.Errorf("terminal task state wrong: finished=%v lease=%v", task.FinishedAt, task.LeaseUntil)
	}
	if !task.State.Terminal() {
		t.Errorf("state = %s, want terminal", task.State)
	}

	// Terminal states never transition.
	if err := adminStore.TransitionTask(ctx, id, domain.TaskSucceeded, domain.TaskQueued); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("terminal transition: want ErrInvalidTransition, got %v", err)
	}
}

func TestSafeRetryTransitionClearsRunningState(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, "retry-key-00001", testHash(4)).TaskID
	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	// running -> queued is the proven-safe retry path.
	if err := adminStore.TransitionTask(ctx, id, domain.TaskRunning, domain.TaskQueued); err != nil {
		t.Fatalf("running->queued: %v", err)
	}
	task, err := adminStore.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.State != domain.TaskQueued || task.StartedAt != nil || task.LeaseUntil != nil || task.FinishedAt != nil {
		t.Errorf("safe retry left stale timestamps: %+v", task)
	}
}

func TestCancelFromQueued(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, "cancel-key-0001", testHash(5)).TaskID
	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskCancelled); err != nil {
		t.Fatalf("queued->cancelled: %v", err)
	}
	task, err := adminStore.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.FinishedAt == nil || task.StartedAt != nil {
		t.Errorf("cancelled task state wrong: started=%v finished=%v", task.StartedAt, task.FinishedAt)
	}
}

func TestInvalidTransitionRejected(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, "invalid-key-001", testHash(6)).TaskID
	// queued -> succeeded is not allowed by the contract.
	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskSucceeded); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("want ErrInvalidTransition, got %v", err)
	}
	// Unknown task.
	if err := adminStore.TransitionTask(ctx, uuid.New(), domain.TaskQueued, domain.TaskRunning); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// clearQueuedTasks wipes queued tasks so claim tests are deterministic. Safe
// only because the test database is disposable and dedicated.
func clearQueuedTasks(t *testing.T) {
	t.Helper()
	if _, err := adminStore.pool.Exec(context.Background(),
		"DELETE FROM tasks WHERE state = 'queued'"); err != nil {
		t.Fatalf("clear queued tasks: %v", err)
	}
}

func TestClaimOncePerRowUnderConcurrency(t *testing.T) {
	clearQueuedTasks(t)
	client, policy := setupTaskEnv(t)
	const nTasks = 5
	const nClaimers = 12
	var ids []uuid.UUID
	for i := 0; i < nTasks; i++ {
		ids = append(ids, mustCreateTask(t, adminStore, client.ID, policy.ID,
			"claim-key-"+string(rune('a'+i)), testHash(byte(i))).TaskID)
	}

	claims := make([]TaskWithPayload, nClaimers)
	errs := make([]error, nClaimers)
	var wg sync.WaitGroup
	for i := 0; i < nClaimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			workerID := uuid.New()
			claims[i], errs[i] = adminStore.ClaimNextQueued(context.Background(), workerID, 30*time.Second)
		}(i)
	}
	wg.Wait()

	var claimedIDs []uuid.UUID
	for i := 0; i < nClaimers; i++ {
		if errs[i] != nil {
			if errors.Is(errs[i], ErrNoTaskAvailable) {
				continue
			}
			t.Fatalf("claim %d: %v", i, errs[i])
		}
		claimedIDs = append(claimedIDs, claims[i].ID)
	}
	if len(claimedIDs) != nTasks {
		t.Fatalf("claimed %d tasks, want %d", len(claimedIDs), nTasks)
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range claimedIDs {
		if seen[id] {
			t.Fatalf("task %s claimed more than once", id)
		}
		seen[id] = true
		if !contains(ids, id) {
			t.Fatalf("claimed unknown task %s", id)
		}
	}

	// Every claimed task is running with attempt 1 and one attempt row.
	for _, id := range ids {
		task, err := adminStore.GetTask(context.Background(), id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if task.State != domain.TaskRunning {
			t.Errorf("task %s state = %s, want running", id, task.State)
		}
		if task.Attempts != 1 {
			t.Errorf("task %s attempts = %d, want 1", id, task.Attempts)
		}
		var n int
		if err := adminStore.pool.QueryRow(context.Background(),
			"SELECT count(*) FROM task_attempts WHERE task_id = $1", id).Scan(&n); err != nil {
			t.Fatalf("count attempts: %v", err)
		}
		if n != 1 {
			t.Errorf("task %s attempt rows = %d, want 1", id, n)
		}
	}
}

func contains(ids []uuid.UUID, want uuid.UUID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestClaimOrderIsFIFO(t *testing.T) {
	clearQueuedTasks(t)
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		ids = append(ids, mustCreateTask(t, adminStore, client.ID, policy.ID,
			"fifo-key-000"+string(rune('0'+i)), testHash(byte(i))).TaskID)
	}
	claimed := mustClaim(t, adminStore, uuid.New())
	if claimed.ID != ids[0] {
		t.Errorf("first claim = %s, want oldest %s", claimed.ID, ids[0])
	}
	claimed = mustClaim(t, adminStore, uuid.New())
	if claimed.ID != ids[1] {
		t.Errorf("second claim = %s, want %s", claimed.ID, ids[1])
	}
	claimed = mustClaim(t, adminStore, uuid.New())
	if claimed.ID != ids[2] {
		t.Errorf("third claim = %s, want %s", claimed.ID, ids[2])
	}
	if _, err := adminStore.ClaimNextQueued(ctx, uuid.New(), 30*time.Second); !errors.Is(err, ErrNoTaskAvailable) {
		t.Errorf("empty queue: want ErrNoTaskAvailable, got %v", err)
	}
}

func TestRenewTaskLease(t *testing.T) {
	client, policy := setupTaskEnv(t)
	ctx := context.Background()
	id := mustCreateTask(t, adminStore, client.ID, policy.ID, "lease-key-0001", testHash(8)).TaskID

	// Renewing a non-running task is rejected.
	if err := adminStore.RenewTaskLease(ctx, id, time.Minute); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("renew queued: want ErrInvalidTransition, got %v", err)
	}

	if err := adminStore.TransitionTask(ctx, id, domain.TaskQueued, domain.TaskRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	if err := adminStore.RenewTaskLease(ctx, id, 2*time.Minute); err != nil {
		t.Fatalf("renew running: %v", err)
	}
	if err := adminStore.RenewTaskLease(ctx, uuid.New(), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("renew unknown: want ErrNotFound, got %v", err)
	}
}

func TestListTasksByClient(t *testing.T) {
	ctx := context.Background()
	clientA := mustCreateClient(t, adminStore, "list-client-a")
	clientB := mustCreateClient(t, adminStore, "list-client-b")
	policy := mustUpsertPolicy(t, adminStore, "list-workload")
	for i := 0; i < 3; i++ {
		mustCreateTask(t, adminStore, clientA.ID, policy.ID, "list-a-0000"+string(rune('0'+i)), testHash(byte(i)))
	}
	mustCreateTask(t, adminStore, clientB.ID, policy.ID, "list-b-00000", testHash(9))

	tasks, err := adminStore.ListTasksByClient(ctx, clientA.ID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("client A has %d tasks, want 3", len(tasks))
	}
	for i := 1; i < len(tasks); i++ {
		if tasks[i].CreatedAt.After(tasks[i-1].CreatedAt) {
			t.Error("tasks not ordered by created_at DESC")
		}
	}
	tasks, err = adminStore.ListTasksByClient(ctx, clientB.ID, 10)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("client B has %d tasks, want 1", len(tasks))
	}
}
