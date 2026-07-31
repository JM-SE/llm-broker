package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHeartbeatUpsertAndRead(t *testing.T) {
	ctx := context.Background()
	workerID := uuid.New()
	if err := adminStore.UpsertWorkerHeartbeat(ctx, workerID, "active"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, err := adminStore.GetWorkerHeartbeat(ctx, workerID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := adminStore.UpsertWorkerHeartbeat(ctx, workerID, "active"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	second, err := adminStore.GetWorkerHeartbeat(ctx, workerID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !second.LastHeartbeatAt.After(first.LastHeartbeatAt) {
		t.Error("heartbeat timestamp did not advance on upsert")
	}
	// Upsert must not create duplicate rows.
	workers, err := adminStore.ListActiveWorkers(ctx, time.Minute)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, w := range workers {
		if w.WorkerID == workerID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("worker appears %d times, want 1", n)
	}
}

func TestStaleWorkersFilteredAndPruned(t *testing.T) {
	ctx := context.Background()
	workerID := uuid.New()
	if err := adminStore.UpsertWorkerHeartbeat(ctx, workerID, "active"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Backdate the heartbeat so it is stale.
	if _, err := adminStore.pool.Exec(ctx,
		"UPDATE worker_heartbeats SET last_heartbeat_at = now() - interval '10 minutes' WHERE worker_id = $1",
		workerID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	workers, err := adminStore.ListActiveWorkers(ctx, time.Minute)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, w := range workers {
		if w.WorkerID == workerID {
			t.Error("stale worker listed as active")
		}
	}
	removed, err := adminStore.PruneStaleWorkers(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d workers, want 1", removed)
	}
	if _, err := adminStore.GetWorkerHeartbeat(ctx, workerID); err != ErrNotFound {
		t.Errorf("heartbeat still present after prune: %v", err)
	}
}
