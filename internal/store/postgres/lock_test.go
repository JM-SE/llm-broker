package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdvisoryLockExclusiveAcrossSessions(t *testing.T) {
	ctx := context.Background()
	release, err := adminStore.AcquireSessionAdvisoryLock(ctx, SessionAdvisoryLockKey)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A second session must not acquire while the first holds the lock.
	if _, err := adminStore.TryAcquireSessionAdvisoryLock(ctx, SessionAdvisoryLockKey); !errors.Is(err, ErrLockNotAcquired) {
		t.Errorf("second acquire: want ErrLockNotAcquired, got %v", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release a new session acquires.
	release2, err := adminStore.TryAcquireSessionAdvisoryLock(ctx, SessionAdvisoryLockKey)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release 2: %v", err)
	}
}

func TestAdvisoryLockOnlyOneSessionHolds(t *testing.T) {
	ctx := context.Background()

	// Session A acquires and HOLDS the lock.
	acquired := make(chan struct{})
	releaseHold := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		release, err := adminStore.AcquireSessionAdvisoryLock(ctx, SessionAdvisoryLockKey)
		if err != nil {
			holderErr <- err
			close(acquired)
			return
		}
		close(acquired)
		<-releaseHold
		holderErr <- release()
	}()
	select {
	case <-acquired:
	case err := <-holderErr:
		t.Fatalf("holder acquire failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("holder never acquired")
	}

	// Session B blocks; with a short deadline it must time out.
	waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := adminStore.AcquireSessionAdvisoryLock(waitCtx, SessionAdvisoryLockKey); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second session: want context deadline exceeded while held, got %v", err)
	}

	// After the holder releases, a session acquires normally.
	close(releaseHold)
	if err := <-holderErr; err != nil {
		t.Fatalf("holder release: %v", err)
	}
	release2, err := adminStore.AcquireSessionAdvisoryLock(ctx, SessionAdvisoryLockKey)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release 2: %v", err)
	}
}
