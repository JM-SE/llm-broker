package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JM-SE/llm-broker/internal/metrics"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// ClaimResult is the outcome of one claim attempt.
type ClaimResult struct {
	Task     postgres.TaskWithPayload
	Decision Decision
	// NoTask reports that the queue held no eligible task.
	NoTask bool
}

// Scheduler composes policy, selection, and leadership into the single claim
// path the worker will use. There is exactly one claim path, and it refuses
// to run without current leadership. Only this type may move tasks from
// queued to running in the product.
type Scheduler struct {
	store   *postgres.Store
	policy  Policy
	leader  *Leader
	now     func() time.Time
	metrics *metrics.Metrics

	mu    sync.Mutex // serializes claim+batch state transitions
	batch AffinityState
}

// New builds a scheduler. policy zero-value falls back to DefaultPolicy.
func New(store *postgres.Store, policy Policy, leader *Leader) *Scheduler {
	if policy.Lease <= 0 {
		policy = DefaultPolicy()
	}
	return &Scheduler{store: store, policy: policy, leader: leader, now: time.Now}
}

// SetMetrics attaches the instrumentation surface (nil-safe).
func (s *Scheduler) SetMetrics(m *metrics.Metrics) { s.metrics = m }

// ClaimNext claims the single best eligible task under current leadership:
// highest effective priority (class base + bounded aging), then FIFO by
// creation time and task ID. Affinity may only reorder tasks inside the
// maximum effective-priority cohort. ClaimNext is
// safe for concurrent callers; the database guarantees each task is claimed
// once.
func (s *Scheduler) ClaimNext(ctx context.Context) (ClaimResult, error) {
	if !s.leader.Held() {
		return ClaimResult{}, ErrNotLeader
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	preferred, active := s.batch.PreferredCapability(now, s.policy.BatchSize, s.policy.BatchMaxAge)
	ct, err := s.store.ClaimNextByPolicy(ctx, s.leader.WorkerID(), s.policy.Lease, eligibleSelectSQL(), active, preferred)
	if errors.Is(err, postgres.ErrNoTaskAvailable) {
		s.batch.Close()
		return ClaimResult{NoTask: true, Decision: Decision{Reason: "no_eligible"}}, nil
	}
	if err != nil {
		return ClaimResult{}, err
	}
	dec := s.batch.Consider(ct.Capability, ct.EffectivePriority,
		s.policy.BatchSize, s.policy.BatchMaxAge, now)
	if s.metrics != nil {
		s.metrics.IncDecision(dec.Reason)
	}
	return ClaimResult{Task: ct.TaskWithPayload, Decision: dec}, nil
}
