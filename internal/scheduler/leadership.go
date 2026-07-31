package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// Leader owns the global exclusive right to schedule: it holds the session
// advisory lock on a dedicated connection and refreshes the worker heartbeat
// while active. Every claim must go through a Leader; a standby (no lock)
// never claims. Lock loss — graceful release or heartbeat failure — stops
// new claims immediately.
type Leader struct {
	store    *postgres.Store
	key      int64
	workerID uuid.UUID
	interval time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	held    bool
	release func() error
	stop    chan struct{}
}

// NewLeader builds a leadership holder for one worker. interval <= 0 falls
// back to the default policy heartbeat interval.
func NewLeader(store *postgres.Store, workerID uuid.UUID, interval time.Duration, log *slog.Logger) *Leader {
	if interval <= 0 {
		interval = DefaultPolicy().HeartbeatInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Leader{
		store:    store,
		key:      postgres.SessionAdvisoryLockKey,
		workerID: workerID,
		interval: interval,
		log:      log,
	}
}

// TryAcquire attempts to become the active worker. It returns (false, nil)
// when another session holds the lock. On success it announces itself with
// an active heartbeat and starts the heartbeat refresh loop.
func (l *Leader) TryAcquire(ctx context.Context) (bool, error) {
	release, err := l.store.TryAcquireSessionAdvisoryLock(ctx, l.key)
	if errors.Is(err, postgres.ErrLockNotAcquired) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		// Already the active worker; drop the redundant lock connection.
		_ = release()
		return true, nil
	}
	l.release = release
	l.held = true
	l.stop = make(chan struct{})
	if err := l.store.UpsertWorkerHeartbeat(ctx, l.workerID, "active"); err != nil {
		l.held = false
		_ = release()
		return false, fmt.Errorf("announce worker heartbeat: %w", err)
	}
	go l.heartbeat(l.stop)
	return true, nil
}

// heartbeat refreshes the active heartbeat while the lock is held. A failed
// refresh treats leadership as lost: no new claims, lock connection closed.
func (l *Leader) heartbeat(stop chan struct{}) {
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := l.store.UpsertWorkerHeartbeat(ctx, l.workerID, "active")
			cancel()
			if err != nil {
				l.log.Error("leadership heartbeat failed; treating leadership as lost",
					"worker_id", l.workerID, "error", err)
				l.lost()
				return
			}
		}
	}
}

// lost transitions to standby and releases the lock connection.
func (l *Leader) lost() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return
	}
	l.held = false
	rel := l.release
	l.release = nil
	close(l.stop)
	if rel != nil {
		_ = rel()
	}
}

// Held reports whether this leader currently owns the exclusive lock.
func (l *Leader) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// WorkerID returns the identity of this leader's worker.
func (l *Leader) WorkerID() uuid.UUID { return l.workerID }

// Release gracefully steps down: announce stopped, stop the heartbeat loop,
// and drop the lock.
func (l *Leader) Release() {
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		return
	}
	l.held = false
	rel := l.release
	l.release = nil
	stop := l.stop
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = l.store.UpsertWorkerHeartbeat(ctx, l.workerID, "stopped")
	cancel()
	close(stop)
	if rel != nil {
		_ = rel()
	}
}
