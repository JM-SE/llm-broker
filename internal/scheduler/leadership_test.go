package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestOnlyOneLeaderAcquires(t *testing.T) {
	workerA, workerB := uuid.New(), uuid.New()
	leaderA := NewLeader(rtStore, workerA, 0, nil)
	leaderB := NewLeader(rtStore, workerB, 0, nil)

	ok, err := leaderA.TryAcquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("leader A acquire = %v, %v; want true", ok, err)
	}
	t.Cleanup(leaderA.Release)

	ok, err = leaderB.TryAcquire(context.Background())
	if err != nil {
		t.Fatalf("leader B acquire: %v", err)
	}
	if ok {
		t.Fatal("leader B acquired while leader A holds the lock")
	}
	if leaderB.Held() {
		t.Fatal("leader B reports held without the lock")
	}
}

func TestStandbyNeverClaims(t *testing.T) {
	leaderA := NewLeader(rtStore, uuid.New(), 0, nil)
	if ok, err := leaderA.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("leader A acquire = %v, %v", ok, err)
	}
	t.Cleanup(leaderA.Release)

	leaderB := NewLeader(rtStore, uuid.New(), 0, nil)
	if ok, _ := leaderB.TryAcquire(context.Background()); ok {
		t.Fatal("leader B acquired unexpectedly")
	}
	schedB := New(rtStore, DefaultPolicy(), leaderB)
	if _, err := schedB.ClaimNext(context.Background()); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("standby claim error = %v, want ErrNotLeader", err)
	}
}

func TestLockLossStopsClaims(t *testing.T) {
	leader := NewLeader(rtStore, uuid.New(), 0, nil)
	if ok, err := leader.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	sched := New(rtStore, DefaultPolicy(), leader)
	if !leader.Held() {
		t.Fatal("leader should report held after acquire")
	}

	// Graceful release simulates lock loss; claims must stop immediately.
	leader.Release()
	if leader.Held() {
		t.Fatal("leader reports held after release")
	}
	if _, err := sched.ClaimNext(context.Background()); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("claim after lock loss = %v, want ErrNotLeader", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	leaderA := NewLeader(rtStore, uuid.New(), 0, nil)
	leaderB := NewLeader(rtStore, uuid.New(), 0, nil)
	if ok, err := leaderA.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("A acquire = %v, %v", ok, err)
	}
	leaderA.Release()
	if ok, err := leaderB.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("B acquire after release = %v, %v", ok, err)
	}
	t.Cleanup(leaderB.Release)
}

func TestHeartbeatLifecycle(t *testing.T) {
	workerID := uuid.New()
	leader := NewLeader(rtStore, workerID, 0, nil)
	if ok, err := leader.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	hb, err := rtStore.GetWorkerHeartbeat(context.Background(), workerID)
	if err != nil {
		t.Fatalf("heartbeat after acquire: %v", err)
	}
	if hb.State != "active" {
		t.Errorf("heartbeat state = %q, want active", hb.State)
	}
	leader.Release()
	hb, err = rtStore.GetWorkerHeartbeat(context.Background(), workerID)
	if err != nil {
		t.Fatalf("heartbeat after release: %v", err)
	}
	if hb.State != "stopped" {
		t.Errorf("heartbeat state = %q, want stopped", hb.State)
	}
}
