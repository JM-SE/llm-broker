// Package worker implements the single active executor of the broker: it
// holds leadership, claims exactly one task at a time through the
// scheduler, invokes the configured backend adapter, persists terminal
// outcomes, and applies the retry/recovery policy. At most one worker
// process runs inference; every other instance stands by.
//
// Failure philosophy (product contract): definitely-unsent and
// proven-safe transient failures retry with persisted backoff; ambiguous
// outcomes (indeterminate) stop ALL new inference until an operator
// resolves the task through `llm-broker worker resolve`; a task whose
// lease expired is recovery work, never a license to re-invoke the
// adapter.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// Options wires the worker to its dependencies.
type Options struct {
	Store        *postgres.Store
	Scheduler    *scheduler.Scheduler
	Leader       *scheduler.Leader
	Registry     *backend.Registry
	Capabilities map[string]config.Capability
	Backends     map[string]config.BackendConfig
	Policy       scheduler.Policy
	Logger       *slog.Logger

	// Now is the clock seam for retry scheduling (defaults to time.Now).
	Now func() time.Time
	// Rand is the jitter source for bounded retry backoff (defaults to a
	// new source). Tests inject a deterministic one.
	Rand *rand.Rand
	// ClaimPoll is the idle pause when no task is eligible.
	ClaimPoll time.Duration
	// DrainTimeout bounds graceful shutdown while an inference completes.
	DrainTimeout time.Duration
}

// Worker owns the active execution loop.
type Worker struct {
	store        *postgres.Store
	sched        *scheduler.Scheduler
	leader       *scheduler.Leader
	registry     *backend.Registry
	capabilities map[string]config.Capability
	backends     map[string]config.BackendConfig
	policy       scheduler.Policy
	log          *slog.Logger
	now          func() time.Time
	rng          *rand.Rand
	claimPoll    time.Duration
	drainTimeout time.Duration
	wg           sync.WaitGroup // in-flight bookkeeping goroutines
}

// New builds a worker with defaults for zero values.
func New(opts Options) *Worker {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if opts.ClaimPoll <= 0 {
		opts.ClaimPoll = 500 * time.Millisecond
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 10 * time.Second
	}
	if opts.Policy.Lease <= 0 {
		opts.Policy = scheduler.DefaultPolicy()
	}
	return &Worker{
		store:        opts.Store,
		sched:        opts.Scheduler,
		leader:       opts.Leader,
		registry:     opts.Registry,
		capabilities: opts.Capabilities,
		backends:     opts.Backends,
		policy:       opts.Policy,
		log:          opts.Logger,
		now:          opts.Now,
		rng:          opts.Rand,
		claimPoll:    opts.ClaimPoll,
		drainTimeout: opts.DrainTimeout,
	}
}

// Run acquires leadership (standing by while another worker is active),
// then claims and executes tasks until ctx is canceled, leadership is
// lost, or a fatal error occurs. In-flight inference is drained on
// graceful shutdown: no new claims after ctx cancellation, and the
// current call finishes under its workload deadline.
func (w *Worker) Run(ctx context.Context) error {
	for {
		ok, err := w.leader.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquire leadership: %w", err)
		}
		if ok {
			break
		}
		w.log.Info("worker standing by: another worker holds the execution lock")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.leader.Interval()):
		}
	}
	w.log.Info("worker active", "worker_id", w.leader.WorkerID())
	defer w.leader.Release()
	return w.loop(ctx)
}

// loop is the active claim-execute cycle. Leadership is verified before
// every claim; losing it stops claims immediately.
func (w *Worker) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			w.log.Info("worker stopping: no new claims after shutdown")
			return w.drain(ctx)
		default:
		}
		if !w.leader.Held() {
			return errors.New("execution lock lost; stopping claims")
		}
		blocked, err := w.store.HasUnresolvedIndeterminate(ctx)
		if err != nil {
			return fmt.Errorf("check unresolved ambiguous execution: %w", err)
		}
		if blocked {
			w.log.Warn("worker blocked: an ambiguous execution requires operator action; run 'llm-broker worker status' and resolve it")
			if !w.sleep(ctx, w.leader.Interval()) {
				return w.drain(ctx)
			}
			continue
		}
		res, err := w.sched.ClaimNext(ctx)
		if err != nil {
			if errors.Is(err, scheduler.ErrNotLeader) {
				return errors.New("execution lock lost; stopping claims")
			}
			return fmt.Errorf("claim task: %w", err)
		}
		if res.NoTask {
			if !w.sleep(ctx, w.claimPoll) {
				return w.drain(ctx)
			}
			continue
		}
		if err := w.execute(ctx, res.Task); err != nil {
			return fmt.Errorf("execute task %s: %w", res.Task.ID, err)
		}
	}
}

// drain lets the in-flight inference finish under its own workload
// deadline, bounded by DrainTimeout.
func (w *Worker) drain(ctx context.Context) error {
	select {
	case <-ctx.Done():
		// Loop already observed cancellation; nothing new is claimed.
	default:
	}
	// The in-flight call runs on its own deadline (context.WithoutCancel
	// inside execute); wait for it or the drain bound.
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(w.drainTimeout):
		w.log.Warn("drain timed out; in-flight inference continues under its workload deadline")
		return nil
	}
}

// sleep waits for d or ctx cancellation. Returns false when ctx ended.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// workerID is the identity used for attempt rows.
func (w *Worker) workerID() uuid.UUID { return w.leader.WorkerID() }
